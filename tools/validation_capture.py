#!/usr/bin/env python3
"""Run an already-prepared external suite and preserve its evidence."""

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


def hash_tree(root):
    root = Path(root).resolve(strict=True)
    if not root.is_dir():
        raise ValueError(f"source tree is not a directory: {root}")
    digest = hashlib.sha256()
    for path in sorted(root.rglob("*")):
        relative = path.relative_to(root).as_posix().encode()
        digest.update(relative + b"\0")
        if path.is_symlink():
            digest.update(b"L\0" + os.readlink(path).encode())
        elif path.is_file():
            digest.update(b"F\0" + path.read_bytes())
        elif path.is_dir():
            digest.update(b"D\0")
        else:
            digest.update(b"O\0")
    return str(root), digest.hexdigest()


def executable_digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def verify_preparation(tooling, conditions, command, evidence):
    if not isinstance(tooling, dict) or tooling.get("schema_version") != 1:
        raise ValueError("tooling manifest has an unsupported schema")
    evidence["checks"]["tooling_manifest"] = "passed"

    if not isinstance(conditions, dict):
        raise ValueError("conditions JSON must contain an object")
    evidence["checks"]["conditions"] = "passed"

    approved = {}
    for item in tooling["executables"]:
        path = str(Path(item["path"]).resolve(strict=True))
        actual = executable_digest(path)
        if actual != item["sha256"]:
            raise ValueError(f"prepared executable content changed: {path}")
        approved[path] = item["sha256"]
    executable = shutil.which(command[0])
    executable_path = str(Path(executable).resolve(strict=True)) if executable else None
    if executable_path not in approved:
        raise ValueError(f"run executable {command[0]!r} is not in the prepared manifest")
    evidence["checks"]["executables"] = "passed"

    checked_trees = []
    for item in tooling.get("source_trees", []):
        path, actual = hash_tree(item["path"])
        checked_trees.append({"path": path, "expected_sha256": item["sha256"], "actual_sha256": actual})
        if actual != item["sha256"]:
            raise ValueError(f"prepared test/source tree content changed: {path}")
    evidence["checks"]["source_trees"] = checked_trees

    version_check = tooling["version_check"]
    version_command = version_check["command"]
    version_executable = shutil.which(version_command[0])
    version_path = str(Path(version_executable).resolve(strict=True)) if version_executable else None
    if version_path not in approved:
        raise ValueError("version-check executable is not in the prepared manifest")
    version_proc = subprocess.run(version_command, cwd=version_check["cwd"], stdout=subprocess.PIPE,
                                  stderr=subprocess.PIPE, check=False, timeout=30)
    version_evidence = {
        "command": version_command,
        "exit_code": version_proc.returncode,
        "stdout": version_proc.stdout.decode("utf-8", errors="replace"),
        "stdout_sha256": hashlib.sha256(version_proc.stdout).hexdigest(),
        "stderr": version_proc.stderr.decode("utf-8", errors="replace"),
        "stderr_sha256": hashlib.sha256(version_proc.stderr).hexdigest(),
    }
    evidence["version_check"] = version_evidence
    if version_proc.returncode != version_check["exit_code"]:
        raise ValueError(f"version-check exit code changed: {version_proc.returncode}")
    if version_evidence["stdout_sha256"] != version_check["stdout_sha256"]:
        raise ValueError("installed suite version/source revision differs from preparation")
    if version_evidence["stderr_sha256"] != version_check["stderr_sha256"]:
        raise ValueError("version-check stderr differs from preparation")
    evidence["checks"]["version_check"] = "passed"
    return evidence


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
    started_at = dt.datetime.now(dt.timezone.utc)
    started = time.monotonic()
    run_stdout = run_stderr = b""
    tooling = conditions = None
    tooling_bytes = conditions_bytes = None
    manifest_sha = conditions_sha = None
    preflight = {"status": "running", "checks": {}}
    preflight_error = None
    try:
        tooling_bytes = args.tooling_manifest.read_bytes()
        manifest_sha = hashlib.sha256(tooling_bytes).hexdigest()
        preflight["checks"]["tooling_manifest_sha256"] = manifest_sha
        tooling = json.loads(tooling_bytes)
        conditions_bytes = args.conditions_json.read_bytes()
        conditions_sha = hashlib.sha256(conditions_bytes).hexdigest()
        conditions = json.loads(conditions_bytes)
        verify_preparation(tooling, conditions, command, preflight)
        preflight["status"] = "passed"
    except Exception as exc:
        preflight_error = {"type": type(exc).__name__, "message": str(exc),
                           "errno": getattr(exc, "errno", None)}
        preflight["status"] = "failed"
        preflight["error"] = preflight_error

    proc = None
    launch_error = None
    if preflight_error is None:
        try:
            proc = subprocess.run(command, cwd=args.cwd.resolve(), stdout=subprocess.PIPE,
                                  stderr=subprocess.PIPE, check=False)
            run_stdout, run_stderr = proc.stdout, proc.stderr
        except OSError as exc:
            launch_error = {"type": type(exc).__name__, "message": str(exc), "errno": exc.errno}
    duration = time.monotonic() - started
    finished_at = dt.datetime.now(dt.timezone.utc)

    (output / "stdout.log").write_bytes(run_stdout)
    (output / "stderr.log").write_bytes(run_stderr)
    preflight_bytes = (json.dumps(preflight, indent=2) + "\n").encode("utf-8")
    (output / "preflight.log").write_bytes(preflight_bytes)
    if tooling_bytes is not None:
        (output / "tooling-manifest.input.json").write_bytes(tooling_bytes)
    if conditions_bytes is not None:
        (output / "conditions.input.json").write_bytes(conditions_bytes)
    if preflight.get("version_check") is not None:
        check = preflight["version_check"]
        (output / "version-check.stdout.log").write_text(check["stdout"], encoding="utf-8")
        (output / "version-check.stderr.log").write_text(check["stderr"], encoding="utf-8")

    try:
        repository_sha = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    except (OSError, subprocess.CalledProcessError):
        repository_sha = None
    if preflight_error:
        status, failure_kind, failure_classification, exit_code = "preflight_failed", "preflight_error", "environment_issue", None
    elif launch_error:
        status, failure_kind, failure_classification, exit_code = "launch_failed", "environment_launch_error", "environment_issue", None
    elif proc.returncode != 0:
        status, failure_kind, failure_classification, exit_code = "command_failed", "exit_code", "unclassified", proc.returncode
    else:
        status, failure_kind, failure_classification, exit_code = "completed", None, "unclassified", 0

    manifest = {
        "schema_version": 1,
        "suite": args.suite,
        "tool": ({"name": tooling.get("tool_name"), "version": tooling.get("version"),
                  "source_revision": tooling.get("source_revision"),
                  "installation_notes": tooling.get("installation_notes"),
                  "manifest": str(args.tooling_manifest.resolve()),
                  "manifest_sha256": manifest_sha,
                  "preconditions": tooling.get("preconditions"), "executables": tooling.get("executables"),
                  "source_trees": tooling.get("source_trees"), "version_check": tooling.get("version_check")}
                 if isinstance(tooling, dict) and tooling_bytes is not None else None),
        "target": args.target,
        "command": command,
        "command_display": shlex.join(command),
        "started_at": started_at.isoformat(),
        "finished_at": finished_at.isoformat(),
        "duration_seconds": duration,
        "status": status,
        "failure_kind": failure_kind,
        "failure_classification": failure_classification,
        "preflight": preflight,
        "launch_error": launch_error,
        "exit_code": exit_code,
        "conditions": conditions,
        "conditions_sha256": conditions_sha,
        "tooling_manifest_sha256": manifest_sha,
        "repository_sha": repository_sha,
        "environment": {"platform": platform.platform(), "python": platform.python_version(),
                        "machine": platform.machine(), "kernel": platform.release(),
                        "cwd": str(args.cwd.resolve())},
        "logs": {
            "stdout": {"path": "stdout.log", "sha256": hashlib.sha256(run_stdout).hexdigest()},
            "stderr": {"path": "stderr.log", "sha256": hashlib.sha256(run_stderr).hexdigest()},
            "preflight": {"path": "preflight.log", "sha256": hashlib.sha256(preflight_bytes).hexdigest()},
            "tooling_manifest_input": {"path": "tooling-manifest.input.json" if tooling_bytes is not None else None,
                                       "sha256": manifest_sha},
            "conditions_input": {"path": "conditions.input.json" if conditions_bytes is not None else None,
                                 "sha256": conditions_sha},
            "version_check_stdout": {"path": "version-check.stdout.log" if preflight.get("version_check") else None,
                                     "sha256": preflight.get("version_check", {}).get("stdout_sha256")},
            "version_check_stderr": {"path": "version-check.stderr.log" if preflight.get("version_check") else None,
                                     "sha256": preflight.get("version_check", {}).get("stderr_sha256")},
        },
    }
    (output / "result.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    returned = exit_code if exit_code is not None else (125 if preflight_error else 127)
    print(json.dumps({"result": str(output / "result.json"), "status": status, "exit_code": returned}))
    return returned


if __name__ == "__main__":
    sys.exit(main())
