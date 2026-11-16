#!/usr/bin/env python3
"""Run external compatibility suites and preserve their unmodified evidence."""

import argparse
import datetime as dt
import hashlib
import json
import os
import platform
import shlex
import subprocess
import sys
import time
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--suite", required=True, help="suite name, e.g. pjdfstest, fsx, litmus")
    parser.add_argument("--target", required=True, help="target interface and configuration")
    parser.add_argument("--cwd", type=Path, default=Path.cwd(), help="working directory for the suite")
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    command = args.command[1:] if args.command and args.command[0] == "--" else args.command
    if not command:
        parser.error("provide a command after --")

    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    start = dt.datetime.now(dt.timezone.utc)
    started = time.monotonic()
    working_directory = args.cwd.resolve()
    proc = subprocess.run(command, cwd=working_directory, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
    elapsed = time.monotonic() - started
    end = dt.datetime.now(dt.timezone.utc)
    (output / "stdout.log").write_bytes(proc.stdout)
    (output / "stderr.log").write_bytes(proc.stderr)
    try:
        sha = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    except (OSError, subprocess.CalledProcessError):
        sha = None
    manifest = {
        "schema_version": 1,
        "suite": args.suite,
        "target": args.target,
        "command": command,
        "command_display": shlex.join(command),
        "started_at": start.isoformat(),
        "finished_at": end.isoformat(),
        "duration_seconds": elapsed,
        "exit_code": proc.returncode,
        "status": "completed" if proc.returncode == 0 else "failed",
        "failure_classification": "unclassified",
        "repository_sha": sha,
        "environment": {
            "platform": platform.platform(),
            "python": platform.python_version(),
            "machine": platform.machine(),
            "kernel": platform.release(),
            "cwd": str(working_directory),
        },
        "logs": {
            "stdout": {"path": "stdout.log", "sha256": hashlib.sha256(proc.stdout).hexdigest()},
            "stderr": {"path": "stderr.log", "sha256": hashlib.sha256(proc.stderr).hexdigest()},
        },
    }
    (output / "result.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"result": str(output / "result.json"), "exit_code": proc.returncode}))
    return proc.returncode


if __name__ == "__main__":
    sys.exit(main())
