#!/usr/bin/env python3
"""Independent FUSE truncate/metadata probe; run as an ordinary mount user."""
import json
import os
import sys

if len(sys.argv) != 2:
    raise SystemExit(f"usage: {sys.argv[0]} MOUNTPOINT")
path = os.path.join(sys.argv[1], "truncate-probe.bin")
fd = os.open(path, os.O_CREAT | os.O_TRUNC | os.O_RDWR, 0o600)
observations = []

def observe(name, operation):
    try:
        value = operation()
        if isinstance(value, bytes):
            value = {"hex": value.hex(), "length": len(value)}
        observations.append({"step": name, "result": value})
    except OSError as error:
        observations.append({"step": name, "errno": error.errno, "error": str(error)})

observe("write_initial", lambda: os.write(fd, b"prefix-data"))
observe("fsync_initial", lambda: os.fsync(fd))
base = os.fstat(fd).st_size
observe("truncate_to_current_size", lambda: os.ftruncate(fd, base))
extended = base + 100000
observe("truncate_extend", lambda: os.ftruncate(fd, extended))
observe("fstat_after_extend", lambda: os.fstat(fd).st_size)
observe("read_hole_after_extend", lambda: os.pread(fd, 32, base))
observe("truncate_shrink", lambda: os.ftruncate(fd, 4))
observe("truncate_reextend", lambda: os.ftruncate(fd, 100))
observe("fstat_after_reextend", lambda: os.fstat(fd).st_size)
observe("read_preserved_prefix", lambda: os.pread(fd, 4, 0))
observe("read_reextended_hole", lambda: os.pread(fd, 96, 4))
observe("write_across_end_boundary", lambda: os.pwrite(fd, b"XYZ", 98))
observe("fstat_after_boundary_write", lambda: os.fstat(fd).st_size)
observe("read_across_end_boundary", lambda: os.pread(fd, 5, 96))
observe("fsync_after_boundary_write", lambda: os.fsync(fd))
observe("path_stat_after_commit", lambda: os.stat(path).st_size)
os.close(fd)
print(json.dumps({"path": path, "observations": observations}, indent=2))
