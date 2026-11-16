#!/usr/bin/env python3
"""Record an already-installed external test tool; this command never installs anything."""

import argparse
import datetime as dt
import hashlib
import json
import os
import platform
import shutil
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--tool-name", required=True)
    parser.add_argument("--version", required=True, help="explicit version or source revision of the installed suite")
    parser.add_argument("--executable", action="append", required=True,
                        help="installed executable used by the run command; repeat for wrappers and suite binaries")
    parser.add_argument("--installation-notes", required=True,
                        help="how this exact version was installed; installation is a separate manual phase")
    parser.add_argument("--source-revision", default="", help="release tag or source commit, when applicable")
    parser.add_argument("--precondition", action="append", default=[], metavar="KEY=VALUE")
    args = parser.parse_args()

    output = args.output.resolve()
    executables = []
    for name in args.executable:
        found = shutil.which(name)
        if not found:
            parser.error(f"required executable is not installed or not on PATH: {name}")
        path = str(Path(found).resolve())
        digest = hashlib.sha256(Path(path).read_bytes()).hexdigest()
        executables.append({"name": name, "path": path, "sha256": digest})
    preconditions = {}
    for item in args.precondition:
        if "=" not in item:
            parser.error(f"precondition must be KEY=VALUE: {item}")
        key, value = item.split("=", 1)
        preconditions[key] = value
    manifest = {
        "schema_version": 1,
        "tool_name": args.tool_name,
        "version": args.version,
        "source_revision": args.source_revision,
        "installation_notes": args.installation_notes,
        "prepared_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "preconditions": preconditions,
        "executables": executables,
        "environment": {"platform": platform.platform(), "kernel": platform.release(),
                        "machine": platform.machine(), "python": platform.python_version(), "cwd": os.getcwd()},
    }
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"tooling_manifest": str(output), "tool_name": args.tool_name, "version": args.version}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
