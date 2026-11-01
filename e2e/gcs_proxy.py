#!/usr/bin/env python3
"""Host-only transparent proxy with one controllable post-commit response hold."""

from __future__ import annotations

import argparse
import http.client
import json
import socket
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit, urlunsplit


class ProxyState:
    def __init__(self) -> None:
        self.lock = threading.Lock()
        self.armed = False
        self.held_commits = 0
        self.upload_puts = 0
        self.object_attribute_gets = 0
        self.replay_preconditions = 0
        self.ambiguous_name = ""
        self.committed = threading.Event()
        self.release = threading.Event()

    def arm(self) -> None:
        with self.lock:
            self.armed = True
            self.held_commits = 0
            self.upload_puts = 0
            self.object_attribute_gets = 0
            self.replay_preconditions = 0
            self.ambiguous_name = ""
            self.committed.clear()
            self.release.clear()

    def snapshot(self) -> dict:
        with self.lock:
            return {
                "armed": self.armed,
                "held_commits": self.held_commits,
                "upload_puts": self.upload_puts,
                "object_attribute_gets": self.object_attribute_gets,
                "replay_preconditions": self.replay_preconditions,
                "ambiguous_name": self.ambiguous_name,
            }


def handler_type(state: ProxyState, upstream_host: str, upstream_port: int):
    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"
        server_version = "nando-e2e-gcs-proxy/1"

        def log_message(self, fmt: str, *args) -> None:
            print(fmt % args, flush=True)

        def respond(self, status: int, payload: dict) -> None:
            body = json.dumps(payload).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self) -> None:
            if self.path == "/__e2e/status":
                self.respond(200, state.snapshot())
                return
            self.forward()

        def do_POST(self) -> None:
            if self.path == "/__e2e/arm":
                state.arm()
                self.respond(200, {"armed": True})
                return
            if self.path == "/__e2e/release":
                state.release.set()
                self.respond(200, {"released": True})
                return
            self.forward()

        def do_PUT(self) -> None:
            self.forward()

        def do_DELETE(self) -> None:
            self.forward()

        def do_PATCH(self) -> None:
            self.forward()

        def do_OPTIONS(self) -> None:
            self.forward()

        def do_HEAD(self) -> None:
            self.forward()

        def forward(self) -> None:
            length = int(self.headers.get("Content-Length", "0"))
            body = self.rfile.read(length) if length else None
            headers = {
                key: value
                for key, value in self.headers.items()
                if key.lower() not in {"host", "connection", "content-length", "expect"}
            }
            query = parse_qs(urlsplit(self.path).query)
            if self.command == "GET" and "/o/" in urlsplit(self.path).path and "prefix" not in query:
                with state.lock:
                    state.object_attribute_gets += 1
            object_name = query.get("name", [""])[0]
            if (
                self.command == "POST"
                and "/upload/" in self.path
                and query.get("uploadType") == ["resumable"]
                and "upload_id" not in query
            ):
                with state.lock:
                    if state.armed:
                        state.ambiguous_name = object_name
                    elif state.held_commits == 1 and object_name and object_name == state.ambiguous_name:
                        state.upload_puts += 1
                        state.replay_preconditions += 1
                        payload = json.dumps({
                            "error": {
                                "code": 412,
                                "message": "Precondition Failed: ifGenerationMatch=0",
                                "status": "FAILED_PRECONDITION",
                            }
                        }).encode()
                        self.send_response(412, "Precondition Failed")
                        self.send_header("Content-Type", "application/json")
                        self.send_header("Content-Length", str(len(payload)))
                        self.send_header("Connection", "close")
                        self.end_headers()
                        self.wfile.write(payload)
                        self.close_connection = True
                        return
            connection = http.client.HTTPConnection(upstream_host, upstream_port, timeout=45)
            try:
                connection.request(self.command, self.path, body=body, headers=headers)
                response = connection.getresponse()
                response_body = response.read()
                response_headers = response.getheaders()
                upload_query = parse_qs(urlsplit(self.path).query)
                is_upload_request = (
                    self.command in ("POST", "PUT")
                    and "/upload/" in self.path
                    and ("uploadType" in upload_query or "upload_id" in upload_query)
                )
                completed_object_upload = False
                if self.command in ("POST", "PUT") and "upload_id" in upload_query and response.status in (200, 201):
                    try:
                        returned = json.loads(response_body)
                        completed_object_upload = (
                            returned.get("name", "").startswith("objects/")
                            and int(returned.get("generation", "0")) > 0
                        )
                    except (ValueError, TypeError, AttributeError):
                        pass
                held = False
                if is_upload_request:
                    with state.lock:
                        state.upload_puts += 1
                        if state.armed and completed_object_upload:
                            state.armed = False
                            state.held_commits += 1
                            held = True
                if held:
                    state.committed.set()
                    state.release.wait(timeout=30)
                self.send_response(response.status, response.reason)
                for key, value in response_headers:
                    lowered = key.lower()
                    if lowered in {
                        "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
                        "te", "trailers", "transfer-encoding", "upgrade",
                    }:
                        continue
                    if lowered == "location":
                        parsed = urlsplit(value)
                        if parsed.scheme:
                            value = urlunsplit((parsed.scheme, self.headers.get("Host", "127.0.0.1:4444"), parsed.path, parsed.query, parsed.fragment))
                        elif value.startswith("/"):
                            value = "http://127.0.0.1:4444" + value
                    self.send_header(key, value)
                self.send_header("Connection", "close")
                self.end_headers()
                if response_body:
                    self.wfile.write(response_body)
            except (BrokenPipeError, ConnectionResetError):
                pass
            except (OSError, http.client.HTTPException, socket.timeout) as exc:
                if not self.wfile.closed:
                    self.respond(502, {"error": f"fake-gcs upstream unavailable: {exc}"})
            finally:
                connection.close()
                self.close_connection = True

    return Handler


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--listen", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=4444)
    parser.add_argument("--upstream", default="127.0.0.1")
    parser.add_argument("--upstream-port", type=int, default=4443)
    args = parser.parse_args()
    state = ProxyState()
    server = ThreadingHTTPServer((args.listen, args.port), handler_type(state, args.upstream, args.upstream_port))
    server.daemon_threads = True
    print(f"READY {args.listen}:{args.port} -> {args.upstream}:{args.upstream_port}", flush=True)
    server.serve_forever(poll_interval=0.1)


if __name__ == "__main__":
    main()
