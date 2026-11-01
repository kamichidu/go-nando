#!/usr/bin/env python3
"""Bounded cleanup scoped to one E2E artifact directory."""

from __future__ import annotations

import os
import signal
import subprocess
import sys
import time
from pathlib import Path


def proc_stat(pid: int) -> tuple[str, int] | None:
    try:
        raw = Path(f"/proc/{pid}/stat").read_text()
        fields = raw[raw.rfind(")") + 2 :].split()
        return fields[0], int(fields[2])
    except (OSError, IndexError, ValueError):
        return None


def processes(artifact: Path, repo: Path) -> list[tuple[int, list[str]]]:
    allowed_bins = {str((artifact / "bin/nando").resolve()), str((artifact / "bin/nando-fuse").resolve())}
    holder = str((repo / "e2e/holder.py").resolve())
    found = []
    for item in Path("/proc").iterdir():
        if not item.name.isdigit():
            continue
        pid = int(item.name)
        try:
            args = [part.decode(errors="replace") for part in (item / "cmdline").read_bytes().split(b"\0") if part]
        except OSError:
            continue
        if not args:
            continue
        if args[0] in allowed_bins or holder in args:
            found.append((pid, args))
    return found


def unmounts(artifact: Path) -> list[str]:
    paths = []
    prefix = str(artifact) + "/"
    for line in Path("/proc/self/mountinfo").read_text().splitlines():
        fields = line.split()
        if len(fields) < 6:
            continue
        mountpoint = fields[4].replace("\\040", " ").replace("\\011", "\t").replace("\\134", "\\")
        if mountpoint == str(artifact) or mountpoint.startswith(prefix):
            paths.append(mountpoint)
    return paths


def terminate_scoped(artifact: Path, repo: Path) -> list[str]:
    failures = []
    targets = processes(artifact, repo)
    for pid, _ in targets:
        try:
            os.kill(pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        remaining = [(pid, proc_stat(pid)) for pid, _ in targets]
        remaining = [(pid, state) for pid, state in remaining if state and state[0] not in ("Z",)]
        if not remaining:
            break
        time.sleep(0.05)
    for pid, state in [(pid, proc_stat(pid)) for pid, _ in targets]:
        if state and state[0] == "D":
            failures.append(f"pid {pid} remains in uninterruptible D state")
        elif state and state[0] != "Z":
            try:
                os.kill(pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
    deadline = time.monotonic() + 3
    while time.monotonic() < deadline:
        remaining = [(pid, proc_stat(pid)) for pid, _ in targets]
        remaining = [(pid, state) for pid, state in remaining if state and state[0] not in ("Z",)]
        if not remaining:
            break
        time.sleep(0.05)
    for pid, _ in targets:
        state = proc_stat(pid)
        if state and state[0] == "D":
            failures.append(f"pid {pid} remains in uninterruptible D state after SIGKILL")
        elif state and state[0] != "Z":
            failures.append(f"pid {pid} survived scoped process cleanup in state {state[0]}")

    pid_file = artifact / "host-pids.tsv"
    if pid_file.exists():
        proxy_script = str((repo / "e2e/gcs_proxy.py").resolve())
        for line in pid_file.read_text().splitlines():
            try:
                pid_text, kind = line.split("\t", 1)
                pid = int(pid_text)
            except ValueError:
                failures.append(f"malformed host process record: {line!r}")
                continue
            try:
                argv = [p.decode(errors="replace") for p in Path(f"/proc/{pid}/cmdline").read_bytes().split(b"\0") if p]
            except OSError:
                continue
            if kind != "gcs-proxy" or proxy_script not in argv:
                failures.append(f"refusing to signal unverified recorded pid {pid} ({kind})")
                continue
            try:
                os.kill(pid, signal.SIGTERM)
            except ProcessLookupError:
                continue
            expires = time.monotonic() + 3
            while time.monotonic() < expires:
                state = proc_stat(pid)
                if state is None or state[0] == "Z":
                    break
                time.sleep(0.05)
            state = proc_stat(pid)
            if state and state[0] == "D":
                failures.append(f"GCS proxy pid {pid} remains in uninterruptible D state")
            elif state and state[0] != "Z":
                try:
                    os.kill(pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass

    for mountpoint in unmounts(artifact):
        for option, timeout_s in (("-u", 8), ("-uz", 5)):
            try:
                result = subprocess.run(["fusermount3", option, mountpoint], timeout=timeout_s, capture_output=True)
            except subprocess.TimeoutExpired:
                failures.append(f"fusermount3 {option} timed out for {mountpoint}")
                continue
            if result.returncode != 0 and mountpoint in unmounts(artifact):
                failures.append(
                    f"fusermount3 {option} failed for {mountpoint}: {result.stderr.decode(errors='replace')}"
                )
            if mountpoint not in unmounts(artifact):
                break
    remaining_mounts = unmounts(artifact)
    if remaining_mounts:
        failures.append(f"FUSE mounts remain under artifacts: {remaining_mounts}")
    return failures


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: cleanup.py ARTIFACTS REPOSITORY", file=sys.stderr)
        return 2
    artifact = Path(sys.argv[1]).resolve()
    repo = Path(sys.argv[2]).resolve()
    failures = terminate_scoped(artifact, repo)
    if failures:
        for failure in failures:
            print(f"CLEANUP FAIL: {failure}", file=sys.stderr)
        return 1
    print("CLEANUP PASS: no E2E daemon/FUSE/holder processes or mounts remain")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
