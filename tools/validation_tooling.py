#!/usr/bin/env python3
"""Record an already-installed external test tool; this command never installs anything."""

import argparse
import datetime as dt
import hashlib
import json
import os
import platform
import shlex
import shutil
import subprocess
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--tool-name", required=True)
    parser.add_argument("--version-command", required=True,
                        help="quoted command that reports the installed version or pinned source revision")
    parser.add_argument("--executable", action="append", required=True,
                        help="installed executable used by the run command; repeat for wrappers and suite binaries")
    parser.add_argument("--installation-notes", required=True,
                        help="how this exact version was installed; installation is a separate manual phase")
    parser.add_argument("--source-revision", default="", help="release tag or source commit, when applicable")
    parser.add_argument("--source-tree", action="append", default=[],
                        help="test/source tree whose contents must remain unchanged; repeat as needed")
    parser.add_argument("--precondition", action="append", default=[], metavar="KEY=VALUE")
    args = parser.parse_args()

    output = args.output.resolve()
    version_command = shlex.split(args.version_command)
    if not version_command:
        parser.error("version-command must not be empty")
    executable_names = list(args.executable) + [version_command[0]]
    executables = []
    seen_paths = set()
    for name in executable_names:
        found = shutil.which(name)
        if not found:
            parser.error(f"required executable is not installed or not on PATH: {name}")
        path = str(Path(found).resolve())
        if path in seen_paths:
            continue
        seen_paths.add(path)
        digest = hashlib.sha256(Path(path).read_bytes()).hexdigest()
        executables.append({"name": name, "path": path, "sha256": digest})
    version_result = subprocess.run(version_command, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                    check=False, timeout=30)
    if version_result.returncode != 0:
        parser.error(f"version command exited with status {version_result.returncode}")
    source_trees = []
    for tree in args.source_tree:
        path, digest = hash_tree(tree)
        source_trees.append({"path": path, "sha256": digest})
    preconditions = {}
    for item in args.precondition:
        if "=" not in item:
            parser.error(f"precondition must be KEY=VALUE: {item}")
        key, value = item.split("=", 1)
        preconditions[key] = value
    manifest = {
        "schema_version": 1,
        "tool_name": args.tool_name,
        "version": version_result.stdout.decode("utf-8", errors="replace").strip(),
        "version_check": {"command": version_command, "cwd": os.getcwd(),
                          "exit_code": version_result.returncode,
                          "stdout": version_result.stdout.decode("utf-8", errors="replace"),
                          "stdout_sha256": hashlib.sha256(version_result.stdout).hexdigest(),
                          "stderr": version_result.stderr.decode("utf-8", errors="replace"),
                          "stderr_sha256": hashlib.sha256(version_result.stderr).hexdigest()},
        "source_revision": args.source_revision,
        "installation_notes": args.installation_notes,
        "prepared_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "preconditions": preconditions,
        "executables": executables,
        "source_trees": source_trees,
        "environment": {"platform": platform.platform(), "kernel": platform.release(),
                        "machine": platform.machine(), "python": platform.python_version(), "cwd": os.getcwd()},
    }
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"tooling_manifest": str(output), "tool_name": args.tool_name,
                      "version": manifest["version"]}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
