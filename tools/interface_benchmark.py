#!/usr/bin/env python3
"""Record a small common-operation benchmark across RPC, mounted FUSE, and WebDAV."""

import argparse
import datetime as dt
import http.client
import json
import os
import platform
import socket
import statistics
import time
import urllib.parse
import urllib.request
import uuid
from pathlib import Path


class RPC:
    def __init__(self, endpoint):
        parsed = urllib.parse.urlparse(endpoint)
        if parsed.scheme != "unix":
            raise ValueError("benchmark currently supports local unix:// RPC endpoints")
        self.socket = parsed.path or parsed.netloc
        self.serial = 0

    def call(self, method, params):
        self.serial += 1
        body = json.dumps({"jsonrpc": "2.0", "id": self.serial, "method": method, "params": params}).encode()
        conn = http.client.HTTPConnection("localhost")
        conn.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        conn.sock.connect(self.socket)
        conn.request("POST", "/rpc", body, {"Content-Type": "application/json"})
        response = conn.getresponse()
        payload = json.loads(response.read())
        conn.close()
        if response.status != 200 or "error" in payload:
            raise RuntimeError(f"RPC {method}: {payload.get('error', response.status)}")
        return payload.get("result")

    def write(self, path, data):
        session = self.call("resource.beginWrite", {"path": path, "resource_schemes": ["file"]})
        try:
            uri = urllib.parse.urlparse(session["resource_uri"])
            with open(urllib.request.url2pathname(uri.path), "wb") as stream:
                stream.write(data)
                stream.flush()
                os.fsync(stream.fileno())
            self.call("resource.commit", {"session_id": session["session_id"]})
        finally:
            self.call("resource.release", {"session_id": session["session_id"]})

    def read(self, path):
        session = self.call("resource.beginRead", {"path": path, "resource_schemes": ["file"]})
        try:
            uri = urllib.parse.urlparse(session["resource_uri"])
            with open(urllib.request.url2pathname(uri.path), "rb") as stream:
                stream.read()
        finally:
            self.call("resource.release", {"session_id": session["session_id"]})


def http_call(base, method, path, data=None, headers=None):
    request = urllib.request.Request(base.rstrip("/") + urllib.parse.quote(path), data=data,
                                     headers=headers or {}, method=method)
    with urllib.request.urlopen(request) as response:
        response.read()
        return response.status


def write_fsynced(path, data):
    with path.open("wb") as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())


def percentile(values, fraction):
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, int((len(ordered) - 1) * fraction))]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--interface", choices=("rpc", "fuse", "webdav"), required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--iterations", type=int, default=100)
    parser.add_argument("--payload-bytes", type=int, default=4096)
    parser.add_argument("--rpc-endpoint")
    parser.add_argument("--mountpoint", type=Path)
    parser.add_argument("--webdav-url")
    args = parser.parse_args()
    if args.iterations < 1 or args.payload_bytes < 0:
        parser.error("iterations must be positive and payload-bytes non-negative")
    if args.interface == "rpc" and not args.rpc_endpoint:
        parser.error("--rpc-endpoint is required for rpc")
    if args.interface == "fuse" and not args.mountpoint:
        parser.error("--mountpoint is required for fuse")
    if args.interface == "webdav" and not args.webdav_url:
        parser.error("--webdav-url is required for webdav")

    rpc = RPC(args.rpc_endpoint) if args.interface == "rpc" else None
    root = args.mountpoint.resolve() if args.mountpoint else None
    base = args.webdav_url
    payload = bytes((i % 251 for i in range(args.payload_bytes)))
    records, errors = [], []
    started = dt.datetime.now(dt.timezone.utc).isoformat()
    for i in range(args.iterations):
        name = f"nando-bench-{uuid.uuid4().hex}"
        old, new = "/" + name, "/" + name + "-renamed"
        local_old, local_new = root / name if root else None, root / (name + "-renamed") if root else None
        timings = {}
        try:
            if args.interface == "rpc":
                action = lambda: rpc.write(old, payload)
            elif args.interface == "fuse":
                action = lambda: write_fsynced(local_old, payload)
            else:
                action = lambda: http_call(base, "PUT", old, payload)
            t = time.perf_counter_ns(); action(); timings["create_write_ns"] = time.perf_counter_ns() - t

            if args.interface == "rpc":
                action = lambda: rpc.read(old)
            elif args.interface == "fuse":
                action = lambda: local_old.read_bytes()
            else:
                action = lambda: http_call(base, "GET", old)
            t = time.perf_counter_ns(); action(); timings["read_ns"] = time.perf_counter_ns() - t

            if args.interface == "rpc":
                action = lambda: rpc.call("repository.stat", {"path": old})
            elif args.interface == "fuse":
                action = lambda: local_old.stat()
            else:
                action = lambda: http_call(base, "PROPFIND", old, headers={"Depth": "0"})
            t = time.perf_counter_ns(); action(); timings["stat_ns"] = time.perf_counter_ns() - t

            if args.interface == "rpc":
                action = lambda: rpc.call("repository.list", {"path": "/"})
            elif args.interface == "fuse":
                action = lambda: list(root.iterdir())
            else:
                action = lambda: http_call(base, "PROPFIND", "/", headers={"Depth": "1"})
            t = time.perf_counter_ns(); action(); timings["list_ns"] = time.perf_counter_ns() - t

            if args.interface == "rpc":
                action = lambda: rpc.call("repository.move", {"source": old, "destination": new})
            elif args.interface == "fuse":
                action = lambda: local_old.rename(local_new)
            else:
                action = lambda: http_call(base, "MOVE", old, headers={"Destination": base.rstrip("/") + new})
            t = time.perf_counter_ns(); action(); timings["rename_ns"] = time.perf_counter_ns() - t

            if args.interface == "rpc":
                action = lambda: rpc.call("repository.remove", {"path": new})
            elif args.interface == "fuse":
                action = lambda: local_new.unlink()
            else:
                action = lambda: http_call(base, "DELETE", new)
            t = time.perf_counter_ns(); action(); timings["delete_ns"] = time.perf_counter_ns() - t
            records.append({"iteration": i, "payload_bytes": len(payload), "operations": timings, "status": "ok"})
        except Exception as exc:  # Preserve compatibility and environment failures as observations.
            errors.append({"iteration": i, "error": f"{type(exc).__name__}: {exc}"})
            records.append({"iteration": i, "payload_bytes": len(payload), "operations": timings, "status": "failed"})

    operation_summary = {}
    for key in ("create_write_ns", "read_ns", "stat_ns", "list_ns", "rename_ns", "delete_ns"):
        values = [r["operations"][key] for r in records if key in r["operations"]]
        if values:
            operation_summary[key] = {"samples": len(values), "median_ns": statistics.median(values),
                                      "p95_ns": percentile(values, .95), "min_ns": min(values), "max_ns": max(values)}
    try:
        import subprocess
        sha = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    except (OSError, subprocess.CalledProcessError):
        sha = None
    result = {"schema_version": 1, "kind": "interface_benchmark", "interface": args.interface,
              "started_at": started, "finished_at": dt.datetime.now(dt.timezone.utc).isoformat(),
              "repository_sha": sha, "configuration": {"iterations": args.iterations, "payload_bytes": len(payload),
              "rpc_endpoint": args.rpc_endpoint, "mountpoint": str(root) if root else None, "webdav_url": base},
              "environment": {"platform": platform.platform(), "kernel": platform.release(),
              "machine": platform.machine(), "python": platform.python_version(), "cwd": os.getcwd()},
              "completed": len(records) - len(errors), "failed": len(errors), "status": "completed" if not errors else "failed",
              "classification": "unclassified", "summary": operation_summary, "iterations": records, "errors": errors}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"result": str(args.output), "completed": result["completed"], "failed": result["failed"]}))
    return 1 if errors else 0


if __name__ == "__main__":
    raise SystemExit(main())
