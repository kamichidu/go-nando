#!/usr/bin/env python3
"""Run S3 integration tests against a uniquely scoped RustFS Compose project."""

import os
import socket
import subprocess
import sys
import time
import uuid


ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
COMPOSE = os.path.join(ROOT, "e2e", "s3-compose.yaml")
PROJECT = "nando-s3-test-" + uuid.uuid4().hex[:12]
BASE = ["docker", "compose", "-p", PROJECT, "-f", COMPOSE]
with socket.socket() as port_socket:
    port_socket.bind(("127.0.0.1", 0))
    HOST_PORT = str(port_socket.getsockname()[1])
COMPOSE_ENV = os.environ.copy()
COMPOSE_ENV["NANDO_S3_HOST_PORT"] = HOST_PORT


def run(args, *, capture=False):
    return subprocess.run(args, cwd=ROOT, env=COMPOSE_ENV, check=True, text=True, capture_output=capture)


def project_containers():
    result = run(["docker", "ps", "-aq", "--filter", f"label=com.docker.compose.project={PROJECT}"], capture=True)
    return [line for line in result.stdout.splitlines() if line]


def main():
    failed = False
    try:
        run(BASE + ["up", "-d", "--wait"])
        env = os.environ.copy()
        env.update({
            "NANDO_S3_INTEGRATION": "1",
            "NANDO_S3_ENDPOINT": f"http://127.0.0.1:{HOST_PORT}",
            "NANDO_S3_REGION": "us-east-1",
            "NANDO_S3_ACCESS_KEY_ID": "nando-test-access",
            "NANDO_S3_SECRET_ACCESS_KEY": "nando-test-secret-key",
            "NANDO_S3_BUCKET": "nando-integration",
        })
        deadline = time.monotonic() + 30
        while True:
            probe = subprocess.run(["curl", "--silent", "--fail", f"http://127.0.0.1:{HOST_PORT}/health"], cwd=ROOT, env=COMPOSE_ENV)
            if probe.returncode == 0:
                break
            if time.monotonic() >= deadline:
                raise RuntimeError("RustFS did not become reachable within 30 seconds")
            time.sleep(0.25)
        result = subprocess.run(["go", "test", "./internal/objectstore/s3", "./internal/repository", "-count=1", "-v"], cwd=ROOT, env=env)
        failed = result.returncode != 0
    except Exception as exc:  # Compose may have partially started before failing.
        print(f"S3 integration setup failed: {exc}", file=sys.stderr)
        failed = True
    finally:
        try:
            down = subprocess.run(BASE + ["down", "--volumes", "--remove-orphans"], cwd=ROOT, env=COMPOSE_ENV)
            if down.returncode != 0:
                print("docker compose down failed", file=sys.stderr)
                failed = True
            remaining = project_containers()
            if remaining:
                print("Project containers remain after cleanup: " + ", ".join(remaining), file=sys.stderr)
                failed = True
        except Exception as exc:
            print(f"Could not verify scoped Compose cleanup: {exc}", file=sys.stderr)
            failed = True
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
