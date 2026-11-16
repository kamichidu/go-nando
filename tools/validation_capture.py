#!/usr/bin/env python3
"""Run external compatibility suites and preserve their unmodified evidence."""

import argparse
import datetime as dt
import hashlib
import json
import os
import platform
import shlex
import shutil
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
    parser.add_argument("--tooling-manifest", required=True, type=Path,
                        help="manifest produced explicitly by validation_tooling.py")
    parser.add_argument("--conditions-json", required=True, type=Path,
                        help="JSON object describing workload and interface conditions")
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
    tooling_bytes = args.tooling_manifest.read_bytes()
    conditions_bytes = args.conditions_json.read_bytes()
    tooling = json.loads(tooling_bytes)
    conditions = json.loads(conditions_bytes)
    if not isinstance(conditions, dict):
        parser.error("conditions-json must contain a JSON object")
    executable = shutil.which(command[0])
    approved = {item["path"]: item["sha256"] for item in tooling["executables"]}
    executable_path = str(Path(executable).resolve()) if executable else None
    preflight_error = None
    if executable_path not in approved:
        preflight_error = f"run executable {command[0]!r} is absent from the prepared tooling manifest"
    elif hashlib.sha256(Path(executable_path).read_bytes()).hexdigest() != approved[executable_path]:
        preflight_error = f"run executable {executable_path!r} differs from the prepared tooling manifest"
    launch_error = None
    proc = None
    try:
        if preflight_error:
            raise OSError(preflight_error)
        proc = subprocess.run(command, cwd=working_directory, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
    except OSError as exc:
        launch_error = {"type": type(exc).__name__, "message": str(exc), "errno": exc.errno}
    elapsed = time.monotonic() - started
    end = dt.datetime.now(dt.timezone.utc)
    stdout = proc.stdout if proc else b""
    stderr = proc.stderr if proc else b""
    (output / "stdout.log").write_bytes(stdout)
    (output / "stderr.log").write_bytes(stderr)
    try:
        sha = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    except (OSError, subprocess.CalledProcessError):
        sha = None
    manifest = {
        "schema_version": 1,
        "suite": args.suite,
        "tool": {"name": tooling["tool_name"], "version": tooling["version"],
                 "installation_notes": tooling["installation_notes"],
                 "source_revision": tooling["source_revision"], "manifest": str(args.tooling_manifest.resolve()),
                 "manifest_sha256": hashlib.sha256(tooling_bytes).hexdigest(),
                 "preconditions": tooling["preconditions"], "executables": tooling["executables"]},
        "target": args.target,
        "command": command,
        "command_display": shlex.join(command),
        "started_at": start.isoformat(),
        "finished_at": end.isoformat(),
        "duration_seconds": elapsed,
        "exit_code": proc.returncode if proc else None,
        "status": "launch_failed" if launch_error else ("completed" if proc.returncode == 0 else "command_failed"),
        "failure_kind": "environment_launch_error" if launch_error else ("exit_code" if proc.returncode else None),
        "failure_classification": "environment_issue" if launch_error else "unclassified",
        "launch_error": launch_error,
        "conditions": conditions,
        "conditions_sha256": hashlib.sha256(conditions_bytes).hexdigest(),
        "repository_sha": sha,
        "environment": {
            "platform": platform.platform(),
            "python": platform.python_version(),
            "machine": platform.machine(),
            "kernel": platform.release(),
            "cwd": str(working_directory),
        },
        "logs": {
            "stdout": {"path": "stdout.log", "sha256": hashlib.sha256(stdout).hexdigest()},
            "stderr": {"path": "stderr.log", "sha256": hashlib.sha256(stderr).hexdigest()},
        },
    }
    (output / "result.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    exit_code = proc.returncode if proc else 127
    print(json.dumps({"result": str(output / "result.json"), "status": manifest["status"], "exit_code": exit_code}))
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
