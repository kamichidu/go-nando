#!/usr/bin/env python3
"""Keep an unfsynced FUSE writer open while the gate crashes its processes."""

import os
import signal
import sys
import time

mountpoint, ready_file = sys.argv[1:]
path = os.path.join(mountpoint, "crash-uncommitted")
fd = os.open(path, os.O_CREAT | os.O_RDWR | os.O_TRUNC, 0o600)
os.write(fd, b"must-be-recovered-after-crash")
with open(ready_file, "w", encoding="ascii") as marker:
    marker.write(f"{os.getpid()}\n")
    marker.flush()
    os.fsync(marker.fileno())
while True:
    signal.pause()
