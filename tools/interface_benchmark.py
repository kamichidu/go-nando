#!/usr/bin/env python3
"""Record a small common-operation benchmark across RPC, mounted FUSE, and WebDAV."""

import argparse
import datetime as dt
import http.client
import math
import json
import os
import platform
import socket
import statistics
import subprocess
import time
import urllib.parse
import urllib.request
import urllib.error
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
        try:
            conn.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            conn.sock.connect(self.socket)
            conn.request("POST", "/rpc", body, {"Content-Type": "application/json"})
            response = conn.getresponse()
            payload = json.loads(response.read())
        finally:
            conn.close()
        if response.status != 200 or "error" in payload:
            error = payload.get("error", {})
            raise RPCError(error.get("code"), f"RPC {method}: {error or response.status}")
        return payload.get("result")

    def remove_if_present(self, path):
        try:
            self.call("repository.stat", {"path": path})
        except RPCError as exc:
            if exc.code == -32004:
                return "already_absent"
            raise
        self.call("repository.remove", {"path": path})
        return "removed"

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


class RPCError(RuntimeError):
    def __init__(self, code, message):
        super().__init__(message)
        self.code = code


def write_fsynced(path, data):
    with path.open("wb") as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())


def percentile(values, fraction):
    ordered = sorted(values)
    return ordered[max(0, math.ceil(fraction * len(ordered)) - 1)]


def summarize_series(values):
    if not values:
        return None
    return {"samples": len(values), "median_ns": statistics.median(values),
            "p95_ns": percentile(values, .95), "p99_ns": percentile(values, .99),
            "min_ns": min(values), "max_ns": max(values)}


def summarize_records(records, operation_names):
    successful_summary, failed_partial_summary = {}, {}
    for name in operation_names:
        successful = [r["operations"][name] for r in records if r["status"] == "ok" and name in r["operations"]]
        partial = [r["operations"][name] for r in records if r["status"] == "failed" and name in r["operations"]]
        summary = summarize_series(successful)
        failed_summary = summarize_series(partial)
        if summary:
            successful_summary[name] = summary
        if failed_summary:
            failed_partial_summary[name] = failed_summary
    return successful_summary, failed_partial_summary


def cleanup_iteration(interface, rpc, root, base, old, new):
    results = []
    for path in (old, new):
        try:
            if interface == "rpc":
                status = rpc.remove_if_present(path)
            elif interface == "fuse":
                local_path = root / path.lstrip("/")
                if not local_path.exists() and not local_path.is_symlink():
                    status = "already_absent"
                else:
                    local_path.unlink()
                    status = "removed"
            else:
                try:
                    http_call(base, "DELETE", path)
                    status = "removed"
                except urllib.error.HTTPError as exc:
                    if exc.code == 404:
                        status = "already_absent"
                    else:
                        raise
            results.append({"path": path, "status": status})
        except Exception as exc:
            results.append({"path": path, "status": "failed",
                            "error": f"{type(exc).__name__}: {exc}"})
    return {"attempted": True, "status": "completed" if all(x["status"] != "failed" for x in results) else "failed",
            "results": results}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--interface", choices=("rpc", "fuse", "webdav"), required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--iterations", type=int, default=100)
    parser.add_argument("--payload-bytes", type=int, default=4096)
    parser.add_argument("--rpc-endpoint")
    parser.add_argument("--mountpoint", type=Path)
    parser.add_argument("--webdav-url")
    parser.add_argument("--run-conditions", type=Path, required=True,
                        help="JSON object with host-specific load, storage, and configuration details")
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
    additional_conditions = json.loads(args.run_conditions.read_text(encoding="utf-8")) if args.run_conditions else {}
    if not isinstance(additional_conditions, dict):
        parser.error("run-conditions must contain a JSON object")
    required_conditions = {"cache_state", "storage", "other_workload"}
    missing_conditions = sorted(required_conditions - additional_conditions.keys())
    if missing_conditions:
        parser.error("run-conditions must include: " + ", ".join(missing_conditions))
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
            failure = {"iteration": i, "error": f"{type(exc).__name__}: {exc}"}
            record = {"iteration": i, "payload_bytes": len(payload), "operations": timings, "status": "failed"}
            record["cleanup"] = cleanup_iteration(args.interface, rpc, root, base, old, new)
            if record["cleanup"]["status"] != "completed":
                failure["cleanup_error"] = record["cleanup"]
            errors.append(failure)
            records.append(record)

    operation_names = ("create_write_ns", "read_ns", "stat_ns", "list_ns", "rename_ns", "delete_ns")
    operation_summary, failed_observation_summary = summarize_records(records, operation_names)
    try:
        sha = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    except (OSError, subprocess.CalledProcessError):
        sha = None
    try:
        go_version = subprocess.check_output(["go", "version"], text=True, stderr=subprocess.STDOUT).strip()
    except (OSError, subprocess.CalledProcessError):
        go_version = None
    result = {"schema_version": 1, "kind": "interface_benchmark", "interface": args.interface,
              "started_at": started, "finished_at": dt.datetime.now(dt.timezone.utc).isoformat(),
              "repository_sha": sha, "tool": {"name": "go-nando", "version": sha[:12] if sha else None},
              "measurement_scope": "end_to_end",
              "adapter_cost_isolation": "not_measured; cross-interface latency differences are not attributable to adapter-only cost",
              "comparison_policy": "Do not interpret simple RPC/FUSE/WebDAV latency deltas as adapter thinness or overhead.",
              "interface_conditions": {
                  "rpc": {"connection": "new Unix HTTP connection for each JSON-RPC request; no connection reuse",
                          "cache": "daemon/resource behavior included; harness adds no cache",
                          "write_commit_boundary": "beginWrite + local Resource write/fsync + commit + release",
                          "read_boundary": "beginRead + local Resource read + release"},
                  "fuse": {"connection": "kernel FUSE requests; adapter RPC behavior included",
                           "cache": "adapter sets zero entry/attribute/negative lookup TTL and uses FOPEN_DIRECT_IO",
                           "write_commit_boundary": "write + fsync; adapter commit performed by fsync",
                           "read_boundary": "open/read/close through FUSE adapter"},
                  "webdav": {"connection": "one urllib HTTP request per operation; no explicit persistent session reuse",
                             "cache": "HTTP client and adapter behavior included; harness adds no cache",
                             "write_commit_boundary": "PUT response completion",
                             "read_boundary": "GET response body consumed"}},
              "configuration": {"iterations": args.iterations, "payload_bytes": len(payload),
              "rpc_endpoint": args.rpc_endpoint, "mountpoint": str(root) if root else None, "webdav_url": base},
              "environment": {"platform": platform.platform(), "kernel": platform.release(),
              "machine": platform.machine(), "python": platform.python_version(), "go_toolchain": go_version,
              "cwd": os.getcwd()},
              "completed": sum(r["status"] == "ok" for r in records),
              "failed": sum(r["status"] == "failed" for r in records),
              "cleanup_failed": sum(r.get("cleanup", {}).get("status") == "failed" for r in records),
              "status": "completed" if not errors else "failed",
              "run_conditions": additional_conditions, "percentile_method": "nearest-rank: sorted sample at ceil(p*n)-1 (1-indexed)",
              "classification": "unclassified", "summary_successful_iterations": operation_summary,
              "summary_failed_partial_observations": failed_observation_summary,
              "iterations": records, "errors": errors}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"result": str(args.output), "completed": result["completed"], "failed": result["failed"]}))
    return 1 if errors else 0


if __name__ == "__main__":
    raise SystemExit(main())
