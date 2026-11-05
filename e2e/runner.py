#!/usr/bin/env python3
"""Real-mount scenarios run natively on the Linux/WSL host."""

from __future__ import annotations

import errno
import hashlib
import http.client
import json
import os
import random
import re
import signal
import socket
import stat
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from contextlib import contextmanager
from pathlib import Path


ARTIFACTS = Path(os.environ["E2E_ARTIFACTS_DIR"])
REPOSITORY = os.environ["E2E_REPOSITORY"]
SOCKET = os.environ["E2E_SOCKET"]
ENDPOINT = f"unix://{SOCKET}"
FAKE_GCS = os.environ.get("E2E_FAKE_GCS", "http://127.0.0.1:4443")
GCS_PROXY = os.environ.get("E2E_GCS_PROXY", "http://127.0.0.1:4444")
BUCKET = "nando-e2e"
FUSE_BIN = os.environ["E2E_FUSE_BIN"]
NANDO_BIN = os.environ["E2E_NANDO_BIN"]
MOUNTINFO = Path("/proc/self/mountinfo")
SCENARIO_LOG = ARTIFACTS / "scenarios.jsonl"
TEXT_LOG = ARTIFACTS / "scenarios.log"


class GateFailure(RuntimeError):
    pass


class RPCFailure(GateFailure):
    def __init__(self, method: str, code: int, message: str):
        super().__init__(f"RPC {method} failed ({code}): {message}")
        self.method = method
        self.code = code
        self.message = message


ACTIVE_PROCESSES: dict[int, tuple[subprocess.Popen, str]] = {}
ACTIVE_MOUNTS: set[str] = set()
PROCESS_LOG = ARTIFACTS / "processes.jsonl"
ACTION_SCENARIOS = {
    "g1": ["G1-01", "G1-02", "G1-03", "G1-04", "G1-05", "G1-06"],
    "relative-repository": ["G1-07"],
    "g2-init": ["G2-00"],
    "g2-normal": ["G2-01", "G2-04"],
    "g2-outage": ["G2-02"],
    "g2-ack-crash": ["G2-05"],
    "g2-ack-replay": ["G2-06"],
    "g2-retry": ["G2-03"],
    "g3-commit": ["G3-01"],
    "g3-verify": ["G3-02"],
    "g3-start-uncommitted": ["G3-03"],
    "g3-crash-fuse": ["G3-04"],
    "g3-verify-recovery": ["G3-05"],
    "g4": ["G4-01"],
}
SCENARIO_EXPECTATIONS = {
    "G2-05": "GCS accepted the object but daemon died before receiving the upload response or completing its durable transfer obligation",
    "G2-06": "daemon replay completed the still-pending obligation without changing object generation or creating another name",
}


class UnixHTTPConnection(http.client.HTTPConnection):
    def __init__(self, path: str, timeout: float):
        super().__init__("nando", timeout=timeout)
        self.path = path

    def connect(self) -> None:
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def rpc_call(method: str, params: dict | None = None, timeout: float = 20.0):
    request = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params or {}})
    conn = UnixHTTPConnection(SOCKET, timeout)
    try:
        conn.request("POST", "/rpc", request, {"Content-Type": "application/json"})
        response = conn.getresponse()
        payload = json.loads(response.read())
    finally:
        conn.close()
    if response.status != 200:
        raise GateFailure(f"RPC HTTP status {response.status}: {payload}")
    if payload.get("error"):
        error = payload["error"]
        raise RPCFailure(method, int(error.get("code", 0)), str(error.get("message", "")))
    return payload.get("result")


def rpc_cli(args: list[str], *, timeout: float = 30.0, check: bool = True) -> subprocess.CompletedProcess:
    process = subprocess.Popen(
        [NANDO_BIN, "--repository", REPOSITORY, "--daemon-listen", ENDPOINT, *args],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    register_process(process, f"nando {' '.join(args)}")
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        terminate_process_group(process, f"nando {' '.join(args)}")
        raise GateFailure(f"nando {' '.join(args)} exceeded {timeout}s")
    finally:
        ACTIVE_PROCESSES.pop(process.pid, None)
    result = subprocess.CompletedProcess(process.args, process.returncode, stdout, stderr)
    if check and result.returncode != 0:
        raise GateFailure(f"nando {' '.join(args)} exited {result.returncode}: {stderr.decode(errors='replace')}")
    return result


def nando(*args: str, timeout: float = 20.0, check: bool = True) -> subprocess.CompletedProcess:
    argv = [NANDO_BIN, "--repository", REPOSITORY, "--daemon-listen", ENDPOINT, *args]
    process = subprocess.Popen(
        argv,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    register_process(process, f"nando {' '.join(args)}")
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        terminate_process_group(process, f"nando {' '.join(args)}")
        raise GateFailure(f"nando {' '.join(args)} exceeded {timeout}s")
    finally:
        ACTIVE_PROCESSES.pop(process.pid, None)
    result = subprocess.CompletedProcess(argv, process.returncode, stdout, stderr)
    if check and result.returncode != 0:
        raise GateFailure(
            f"nando {' '.join(args)} exited {result.returncode}: "
            f"{result.stderr.decode(errors='replace')}"
        )
    return result


def metadata_child(name: str):
    """Read authoritative SQLite metadata through the production metadata adapter."""
    source = r'''package main
import (
    "context"
    "fmt"
    "os"
    "github.com/kamichidu/go-nando/internal/metadata"
    metadatasqlite "github.com/kamichidu/go-nando/internal/metadata/sqlite"
)
func main() {
    store, err := metadatasqlite.NewStore(os.Args[1] + "/metadata.sqlite")
    if err != nil { panic(err) }
    defer store.Close()
    entry, err := store.GetChild(context.Background(), metadata.EntryID("root"), os.Args[2])
    if err != nil { if err == metadata.ErrEntryNotFound { fmt.Print("ABSENT"); return }; panic(err) }
    object := ""
    if entry.ObjectID != nil { object = entry.ObjectID.String() }
    fmt.Printf("%s\t%d\t%d\t%s", entry.ID, entry.Revision, entry.Size, object)
}
'''
    program = Path(__file__).resolve().parent / ".tmp" / f"metadata-observe-{os.getpid()}.go"
    program.parent.mkdir(parents=True, exist_ok=True)
    program.write_text(source, encoding="utf-8")
    result = subprocess.run(
        ["go", "run", str(program), REPOSITORY, name],
        cwd=Path(__file__).resolve().parents[1],
        text=True,
        capture_output=True,
        timeout=30,
    )
    program.unlink(missing_ok=True)
    if result.returncode != 0:
        raise GateFailure(f"metadata adapter observation failed for {name}: {result.stderr.strip()}")
    return None if result.stdout == "ABSENT" else result.stdout.split("\t")


def daemon_ready(deadline: float = 20.0) -> None:
    expires = time.monotonic() + deadline
    last_error: Exception | None = None
    while time.monotonic() < expires:
        try:
            rpc_call("daemon.ping", timeout=1.0)
            return
        except Exception as exc:  # bounded readiness polling
            last_error = exc
            time.sleep(min(0.1, max(0.0, expires - time.monotonic())))
    raise GateFailure(f"daemon did not become ready within {deadline}s: {last_error}")


def mount_present(path: str) -> bool:
    try:
        return any(line.split()[4] == path for line in MOUNTINFO.read_text().splitlines())
    except (OSError, IndexError):
        return False


def wait_until(description: str, predicate, timeout: float = 15.0) -> None:
    expires = time.monotonic() + timeout
    last_error = None
    while time.monotonic() < expires:
        try:
            if predicate():
                return
        except Exception as exc:  # expected while an external service is starting
            last_error = exc
        time.sleep(min(0.1, max(0.0, expires - time.monotonic())))
    raise GateFailure(f"timed out waiting for {description}; last error: {last_error}")


def register_process(process: subprocess.Popen, description: str) -> None:
    ACTIVE_PROCESSES[process.pid] = (process, description)
    item = {"pid": process.pid, "pgid": process.pid, "description": description}
    with PROCESS_LOG.open("a", encoding="utf-8") as output:
        output.write(json.dumps(item, sort_keys=True) + "\n")
        output.flush()
        os.fsync(output.fileno())


def process_state(pid: int) -> str | None:
    try:
        raw = Path(f"/proc/{pid}/stat").read_text()
        return raw[raw.rfind(")") + 2 :].split()[0]
    except (FileNotFoundError, ProcessLookupError, IndexError):
        return None


def process_group_members(pgid: int) -> list[tuple[int, str, str]]:
    members = []
    for item in Path("/proc").iterdir():
        if not item.name.isdigit():
            continue
        try:
            raw = (item / "stat").read_text()
            fields = raw[raw.rfind(")") + 2 :].split()
            if int(fields[2]) == pgid:
                members.append((int(item.name), fields[0], (item / "comm").read_text().strip()))
        except (OSError, IndexError, ValueError):
            continue
    return members


def terminate_process_group(process: subprocess.Popen, description: str, *, force: bool = False) -> None:
    pgid = process.pid
    try:
        os.killpg(pgid, signal.SIGKILL if force else signal.SIGTERM)
    except ProcessLookupError:
        pass
    deadline = time.monotonic() + (2.0 if force else 5.0)
    while time.monotonic() < deadline:
        members = [(pid, state, name) for pid, state, name in process_group_members(pgid) if state != "Z"]
        if not members:
            break
        time.sleep(min(0.05, max(0.0, deadline - time.monotonic())))
    members = [(pid, state, name) for pid, state, name in process_group_members(pgid) if state != "Z"]
    d_state = [(pid, name) for pid, state, name in members if state == "D"]
    if d_state:
        raise GateFailure(f"{description} remains in uninterruptible D state: {d_state}")
    if members and not force:
        try:
            os.killpg(pgid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        deadline = time.monotonic() + 2.0
        while time.monotonic() < deadline and any(
            state != "Z" for _, state, _ in process_group_members(pgid)
        ):
            time.sleep(0.05)
        members = [(pid, state, name) for pid, state, name in process_group_members(pgid) if state != "Z"]
    try:
        process.wait(timeout=0.2)
    except subprocess.TimeoutExpired:
        pass
    if members:
        raise GateFailure(f"{description} process group did not exit: {members}")
    ACTIVE_PROCESSES.pop(process.pid, None)


def unmount_path(mountpoint: str) -> None:
    if not mount_present(mountpoint):
        ACTIVE_MOUNTS.discard(mountpoint)
        return
    errors = []
    for args, timeout_s in ((["fusermount3", "-u", mountpoint], 8), (["fusermount3", "-uz", mountpoint], 5)):
        try:
            result = subprocess.run(args, timeout=timeout_s, capture_output=True, check=False)
            if result.returncode != 0:
                errors.append(result.stderr.decode(errors="replace"))
        except subprocess.TimeoutExpired:
            errors.append(f"{args[1]} timed out")
        if not mount_present(mountpoint):
            ACTIVE_MOUNTS.discard(mountpoint)
            return
    raise GateFailure(f"failed to unmount {mountpoint}: {'; '.join(errors)}")


def cleanup_active() -> list[str]:
    failures = []
    for mountpoint in list(ACTIVE_MOUNTS):
        try:
            unmount_path(mountpoint)
        except BaseException as exc:
            failures.append(str(exc))
    for process, description in list(ACTIVE_PROCESSES.values()):
        try:
            terminate_process_group(process, description)
        except BaseException as exc:
            failures.append(str(exc))
    for mountpoint in list(ACTIVE_MOUNTS):
        try:
            unmount_path(mountpoint)
        except BaseException as exc:
            failures.append(str(exc))
    return failures


def _terminate_on_signal(signum, _frame):
    failures = cleanup_active()
    name = signal.Signals(signum).name
    raise GateFailure(f"received {name}; cleanup: {failures or 'complete'}")


def action_records_since(offset: int) -> list[dict]:
    if not SCENARIO_LOG.exists():
        return []
    with SCENARIO_LOG.open("rb") as source:
        source.seek(offset)
        return [json.loads(line) for line in source if line.strip()]


def action_failures(action: str, detail: str) -> None:
    for scenario_id in ACTION_SCENARIOS.get(action, []):
        expected = SCENARIO_EXPECTATIONS.get(scenario_id, "scenario assertions complete without error")
        record(scenario_id, "FAIL", expected, detail)


def detach_intentionally_live_crash_processes() -> None:
    for pid in (int((ARTIFACTS / "g3-fuse-pid").read_text()), int((ARTIFACTS / "g3-holder-pid").read_text())):
        ACTIVE_PROCESSES.pop(pid, None)
    ACTIVE_MOUNTS.discard(str(ARTIFACTS / "g3-crash-mount"))


def record(scenario_id: str, status: str, expected: str, observed: str) -> None:
    item = {
        "scenario": scenario_id,
        "status": status,
        "expected": expected,
        "observed": observed,
        "timestamp_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
    with SCENARIO_LOG.open("a", encoding="utf-8") as output:
        output.write(json.dumps(item, sort_keys=True) + "\n")
        output.flush()
        os.fsync(output.fileno())
    with TEXT_LOG.open("a", encoding="utf-8") as output:
        output.write(f"{status} {scenario_id}: expected={expected}; observed={observed}\n")
        output.flush()
    print(f"{status} {scenario_id}: {observed}", flush=True)


def scenario(scenario_id: str, expected: str, fn, *, timeout: float = 45.0) -> None:
    started = time.monotonic()
    old_handler = signal.signal(signal.SIGALRM, _scenario_timeout)
    signal.setitimer(signal.ITIMER_REAL, timeout)
    try:
        observed = fn()
    except BaseException as exc:
        record(scenario_id, "FAIL", expected, f"{type(exc).__name__}: {exc}")
        raise
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, old_handler)
        if time.monotonic() - started > timeout:
            failure = f"scenario {scenario_id} exceeded its {timeout:g}s deadline"
            record(scenario_id, "FAIL", expected, failure)
            raise GateFailure(failure)
    record(scenario_id, "PASS", expected, str(observed))


def _scenario_timeout(_signum, _frame):
    raise TimeoutError("45s scenario deadline exceeded")


@contextmanager
def fuse_mount(label: str):
    if not stat.S_ISCHR(os.stat("/dev/fuse").st_mode) or not os.access("/dev/fuse", os.R_OK | os.W_OK):
        raise GateFailure("/dev/fuse is missing or inaccessible in the FUSE runner")
    daemon_ready()
    mountpoint = str(ARTIFACTS / f"mount-{label}-{os.getpid()}")
    os.makedirs(mountpoint, exist_ok=True)
    if mount_present(mountpoint):
        raise GateFailure(f"mountpoint already mounted: {mountpoint}")
    log_path = ARTIFACTS / f"nando-fuse-{label}.log"
    log_file = log_path.open("ab", buffering=0)
    process = subprocess.Popen(
        [FUSE_BIN, "--daemon-listen", ENDPOINT, "--fuse-mountpoint", mountpoint],
        stdin=subprocess.DEVNULL,
        stdout=log_file,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    register_process(process, f"FUSE mount {mountpoint}")
    ACTIVE_MOUNTS.add(mountpoint)
    try:
        wait_until(
            f"real FUSE mount {mountpoint}",
            lambda: _mount_or_process_failed(process, mountpoint),
            timeout=15.0,
        )
        # Reading through the mount proves this is an active kernel FUSE mount.
        os.listdir(mountpoint)
        yield mountpoint
    finally:
        cleanup_errors = []
        try:
            unmount_path(mountpoint)
        except BaseException as exc:
            cleanup_errors.append(str(exc))
        try:
            terminate_process_group(process, f"FUSE mount {mountpoint}")
        except BaseException as exc:
            cleanup_errors.append(str(exc))
        if mount_present(mountpoint):
            try:
                unmount_path(mountpoint)
            except BaseException as exc:
                cleanup_errors.append(str(exc))
        log_file.close()
        if mount_present(mountpoint):
            cleanup_errors.append(f"FUSE mount remained after cleanup: {mountpoint}")
        else:
            try:
                os.rmdir(mountpoint)
            except FileNotFoundError:
                pass
            except OSError as exc:
                cleanup_errors.append(f"could not remove mountpoint directory {mountpoint}: {exc}")
        if cleanup_errors:
            raise GateFailure("; ".join(cleanup_errors))


def _mount_or_process_failed(process: subprocess.Popen, mountpoint: str) -> bool:
    if process.poll() is not None and not mount_present(mountpoint):
        raise GateFailure(f"FUSE server exited {process.returncode} before mounting {mountpoint}")
    return mount_present(mountpoint)


def committed_file(mountpoint: str, name: str, data: bytes) -> None:
    path = os.path.join(mountpoint, name)
    fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_RDWR, 0o644)
    try:
        view = memoryview(data)
        total = 0
        while view:
            written = os.write(fd, view)
            if written <= 0:
                raise GateFailure(f"short FUSE write after {total}/{len(data)} bytes")
            total += written
            view = view[written:]
        os.fsync(fd)
    finally:
        os.close(fd)


def cat_bytes(path: str) -> bytes:
    return nando("cat", path).stdout


def stat_hash(path: str) -> str:
    output = nando("stat", path).stdout.decode()
    for line in output.splitlines():
        if line.startswith("Hash:"):
            return line.split(":", 1)[1].strip()
    raise GateFailure(f"nando stat returned no content hash for {path}: {output}")


def g1() -> None:
    with fuse_mount("g1") as mountpoint:
        def crud():
            committed_file(mountpoint, "crud.txt", b"crud-fsync-bytes\n")
            if cat_bytes("/crud.txt") != b"crud-fsync-bytes\n":
                raise GateFailure("committed bytes were not visible through the daemon")
            copied = os.path.join(mountpoint, "copy.txt")
            with open(os.path.join(mountpoint, "crud.txt"), "rb") as source, open(copied, "xb") as target:
                target.write(source.read())
                target.flush()
                os.fsync(target.fileno())
            os.rename(copied, os.path.join(mountpoint, "renamed.txt"))
            os.mkdir(os.path.join(mountpoint, "subdir"))
            os.rmdir(os.path.join(mountpoint, "subdir"))
            names = os.listdir(mountpoint)
            if "renamed.txt" not in names or "crud.txt" not in names:
                raise GateFailure(f"ls omitted expected entries: {names}")
            os.unlink(os.path.join(mountpoint, "renamed.txt"))
            if cat_bytes("/crud.txt") != b"crud-fsync-bytes\n":
                raise GateFailure("CRUD operations changed the original file")
            return f"write/fsync/cat/cp/mv/ls/mkdir/rmdir/rm observed; names={sorted(os.listdir(mountpoint))}"

        scenario("G1-01", "real mount CRUD operations preserve the committed file", crud)

        def final_close():
            path = os.path.join(mountpoint, "close-commit.txt")
            fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_RDWR, 0o644)
            os.write(fd, b"final-close-commit")
            os.close(fd)
            if cat_bytes("/close-commit.txt") != b"final-close-commit":
                raise GateFailure("final Release did not commit dirty staging")
            return "final Release committed dirty bytes; daemon cat confirmed content"

        scenario("G1-02", "Fsync and final close make complete bytes externally visible", final_close)

        def multiple_writers():
            path = os.path.join(mountpoint, "multi-writer")
            with open(path, "xb") as initial:
                initial.write(b"..")
                initial.flush()
                os.fsync(initial.fileno())
            first = os.open(path, os.O_RDWR)
            second = os.open(path, os.O_RDWR)
            barrier = threading.Barrier(2)
            failures: list[BaseException] = []

            def write(fd: int, data: bytes, offset: int):
                try:
                    barrier.wait(timeout=5)
                    if os.pwrite(fd, data, offset) != len(data):
                        raise GateFailure("short concurrent write")
                    os.fsync(fd)
                except BaseException as exc:
                    failures.append(exc)

            threads = [
                threading.Thread(target=write, args=(first, b"A", 0)),
                threading.Thread(target=write, args=(second, b"B", 1)),
            ]
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join(timeout=20)
            for fd in (first, second):
                os.close(fd)
            if any(thread.is_alive() for thread in threads):
                raise GateFailure("concurrent writer thread exceeded 20s")
            if failures:
                raise GateFailure(f"concurrent writer errors: {failures}")
            actual = cat_bytes("/multi-writer")
            if actual != b"AB":
                raise GateFailure(f"multiple writers committed {actual!r}, expected b'AB'")
            return "two open handles wrote concurrently; final daemon-visible bytes are AB"

        scenario("G1-03", "two writers share staging and concurrent writes/fsyncs commit without loss", multiple_writers)

        def open_rename():
            old = os.path.join(mountpoint, "rename-before")
            new = os.path.join(mountpoint, "rename-after")
            committed_file(mountpoint, "rename-before", b"old-data")
            fd = os.open(old, os.O_RDWR)
            os.rename(old, new)
            if os.pwrite(fd, b"new-data", 0) != len(b"new-data"):
                raise GateFailure("short write through renamed open handle")
            os.fsync(fd)
            os.close(fd)
            if cat_bytes("/rename-after") != b"new-data":
                raise GateFailure("open handle did not commit to renamed Entry")
            if nando("stat", "/rename-before", check=False).returncode == 0:
                raise GateFailure("old rename path still resolves")
            return "open handle followed stable Entry ID; only renamed path exposes new revision"

        scenario("G1-04", "open writer follows rename and commits through stable Entry ID", open_rename)

        def open_unlink():
            path = os.path.join(mountpoint, "unlink-open")
            committed_file(mountpoint, "unlink-open", b"public")
            fd = os.open(path, os.O_RDWR)
            os.unlink(path)
            os.pwrite(fd, b"private", 0)
            os.fsync(fd)
            if os.pread(fd, 7, 0) != b"private":
                raise GateFailure("unlinked handle could not access its private staging")
            if os.path.exists(path):
                raise GateFailure("unlink did not immediately remove the namespace name")
            os.close(fd)
            if os.path.exists(path):
                raise GateFailure("final close resurrected an unlinked name")
            if "/unlink-open" in nando("recovery", "list").stdout.decode():
                raise GateFailure("unlink discard tombstone leaked into ordinary Recovery")
            return "open private I/O/fsync worked; name remained absent after final close and no Recovery row appeared"

        scenario("G1-05", "unlink is immediate; open I/O remains private and is discarded at final close", open_unlink)

        def cas_conflict():
            path = os.path.join(mountpoint, "cas-conflict")
            committed_file(mountpoint, "cas-conflict", b"base-revision")
            fd = os.open(path, os.O_RDWR)
            if os.pwrite(fd, b"fuse-writer", 0) != len(b"fuse-writer"):
                raise GateFailure("short staged CAS writer update")
            external = ARTIFACTS / "cas-winner.bin"
            external.write_bytes(b"external-winner")
            nando("put", str(external), "/cas-conflict")
            try:
                os.fsync(fd)
            except OSError as exc:
                if exc.errno != errno.EAGAIN:
                    raise GateFailure(f"CAS fsync errno={exc.errno}, expected EAGAIN") from exc
            else:
                raise GateFailure("stale FUSE writer fsync unexpectedly succeeded")
            os.close(fd)
            current = cat_bytes("/cas-conflict")
            if current != b"external-winner":
                raise GateFailure(f"CAS winner was overwritten: {current!r}")
            recovery = nando("recovery", "list").stdout.decode()
            if "/cas-conflict" not in recovery:
                raise GateFailure("CAS-failed staging was not retained in Recovery")
            return "fsync returned EAGAIN; competing revision remains visible; failed staging remains in Recovery"

        scenario("G1-06", "CAS conflict is reported and retains original staging without overwriting winner", cas_conflict)


def _relative_repository_operation() -> str:
    source = Path(__file__).resolve().parents[1] / "README.adoc"
    expected = source.read_bytes()
    repo_parent = ARTIFACTS / "relative-repository"
    repo_parent.mkdir(parents=True, exist_ok=True)
    repository = repo_parent / "repository"
    socket = ARTIFACTS / "run" / "relative-repository.sock"
    init = subprocess.run([NANDO_BIN, "--repository", str(repository), "init"], capture_output=True, timeout=20)
    if init.returncode != 0:
        raise GateFailure(f"initialize relative-path E2E repository: {init.stderr.decode(errors='replace')}")

    global REPOSITORY, SOCKET, ENDPOINT
    previous = REPOSITORY, SOCKET, ENDPOINT
    REPOSITORY, SOCKET, ENDPOINT = str(repository), str(socket), f"unix://{socket}"
    daemon = subprocess.Popen(
        [NANDO_BIN, "--repository", "./repository", "--daemon-listen", ENDPOINT, "daemon"],
        cwd=repo_parent,
        stdout=(ARTIFACTS / "daemon-relative-repository.log").open("wb"),
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    register_process(daemon, "daemon with relative repository path")
    try:
        daemon_ready()
        source_copy = ARTIFACTS / "README.adoc"
        source_copy.write_bytes(expected)
        nando("put", str(source_copy), "/README.adoc")
        actual = cat_bytes("/README.adoc")
        if actual != expected:
            raise GateFailure("daemon cat bytes differ after put against relative repository")

        with fuse_mount("relative-repository") as mountpoint:
            with source.open("rb") as src, open(os.path.join(mountpoint, "README-copy.adoc"), "xb") as dst:
                dst.write(src.read())
                dst.flush()
                os.fsync(dst.fileno())
            actual = cat_bytes("/README-copy.adoc")
            if actual != expected:
                raise GateFailure("daemon cat bytes differ after FUSE copy against relative repository")
        return "put/cat bytes and FUSE copy/cat bytes match README.adoc"
    finally:
        if daemon.poll() is None:
            terminate_process_group(daemon, "daemon with relative repository path")
        ACTIVE_PROCESSES.pop(daemon.pid, None)
        REPOSITORY, SOCKET, ENDPOINT = previous


def relative_repository() -> None:
    scenario(
        "G1-07",
        "daemon started with relative repository returns accessible Resource URIs for put/cat and FUSE copy/cat",
        _relative_repository_operation,
    )


def request_json(url: str, method: str = "GET", body: dict | None = None, timeout: float = 3.0):
    data = None if body is None else json.dumps(body).encode()
    headers = {"Content-Type": "application/json"} if data is not None else {}
    request = urllib.request.Request(url, data=data, headers=headers, method=method)
    with urllib.request.urlopen(request, timeout=timeout) as response:
        raw = response.read()
        return response.status, json.loads(raw) if raw else None


def gcs_object(content_id: str):
    escaped = urllib.parse.quote(f"objects/{content_id}", safe="")
    url = f"{FAKE_GCS}/storage/v1/b/{BUCKET}/o/{escaped}"
    try:
        _, metadata = request_json(url)
        return metadata
    except urllib.error.HTTPError as exc:
        if exc.code == 404:
            return None
        raise


def proxy_control(path: str, *, method: str = "GET") -> dict:
    request = urllib.request.Request(f"{GCS_PROXY}/__e2e/{path}", method=method)
    with urllib.request.urlopen(request, timeout=3) as response:
        return json.loads(response.read())


def assert_gcs_object(content_id: str, expected: bytes) -> str:
    metadata = gcs_object(content_id)
    if metadata is None:
        raise GateFailure(f"GCS object objects/{content_id} is absent")
    actual_meta = metadata.get("metadata", {}).get("nando-content-id")
    if actual_meta != content_id:
        raise GateFailure(f"GCS nando-content-id metadata={actual_meta!r}, expected {content_id}")
    if int(metadata.get("size", -1)) != len(expected) or int(metadata.get("generation", "0")) <= 0:
        raise GateFailure(f"GCS object API metadata has invalid size/generation: {metadata}")
    escaped = urllib.parse.quote(f"objects/{content_id}", safe="")
    media_url = f"{FAKE_GCS}/storage/v1/b/{BUCKET}/o/{escaped}?alt=media"
    with urllib.request.urlopen(media_url, timeout=5.0) as response:
        body = response.read()
    if body != expected:
        raise GateFailure(f"GCS object bytes {body!r}, expected {expected!r}")
    list_url = f"{FAKE_GCS}/storage/v1/b/{BUCKET}/o?prefix=objects%2F{urllib.parse.quote(content_id)}"
    _, listing = request_json(list_url)
    names = [item.get("name") for item in listing.get("items", [])]
    expected_name = f"objects/{content_id}"
    if names.count(expected_name) != 1:
        raise GateFailure(f"GCS object listing has {names.count(expected_name)} copies: {names}")
    return f"REST metadata/generation and downloaded bytes verified; one object name {expected_name}"


def g2_init() -> None:
    def emulator_ready():
        try:
            request_json(f"{FAKE_GCS}/storage/v1/b?project=nando-e2e", timeout=1)
            return True
        except urllib.error.HTTPError as exc:
            return exc.code < 500

    wait_until("fake-gcs-server HTTP API", emulator_ready, timeout=30)
    try:
        request_json(
            f"{FAKE_GCS}/storage/v1/b?project=nando-e2e",
            method="POST",
            body={"name": BUCKET, "location": "US-CENTRAL1"},
        )
    except urllib.error.HTTPError as exc:
        if exc.code != 409:
            raise
    record("G2-00", "PASS", "fake-gcs bucket initialized through its JSON API", f"bucket {BUCKET} is available")


def g2_proxy_ready() -> None:
    wait_until("host GCS fault proxy", lambda: proxy_control("status").get("armed") is False, timeout=15)


def g2_normal() -> None:
    data = b"replication-success-without-gcs-credentials"
    with fuse_mount("g2-normal") as mountpoint:
        def commit_without_delivery():
            committed_file(mountpoint, "replicated-normal", data)
            content_id = stat_hash("/replicated-normal")
            if cat_bytes("/replicated-normal") != data:
                raise GateFailure("local commit was not visible before replication")
            wait_until("daemon-scheduled GCS object upload without maintenance RPC", lambda: gcs_object(content_id) is not None, timeout=30)
            evidence = assert_gcs_object(content_id, data)
            (ARTIFACTS / "g2-normal-content-id").write_text(content_id + "\n", encoding="ascii")
            return f"local Fsync commit preceded daemon scheduler delivery with no maintenance RPC; {evidence}"

        scenario("G2-01", "local commit is visible before async delivery and then appears in fake GCS", commit_without_delivery)

    duplicate_data = b"replication-success-without-gcs-credentials"
    with fuse_mount("g2-duplicate") as mountpoint:
        def same_content_reenqueued():
            committed_file(mountpoint, "replicated-duplicate", duplicate_data)
            duplicate_id = stat_hash("/replicated-duplicate")
            normal_id = (ARTIFACTS / "g2-normal-content-id").read_text(encoding="ascii").strip()
            if duplicate_id != normal_id:
                raise GateFailure(f"identical content yielded IDs {duplicate_id} and {normal_id}")
            wait_until("daemon reconciles re-enqueued content ID", lambda: gcs_object(duplicate_id) is not None, timeout=30)
            return assert_gcs_object(duplicate_id, duplicate_data)

        scenario("G2-04", "re-enqueued identical content reconciles an existing generation without duplicate objects", same_content_reenqueued)


def g2_outage_commit() -> None:
    data = b"local-commit-during-gcs-outage"
    with fuse_mount("g2-outage") as mountpoint:
        def commit_while_stopped():
            committed_file(mountpoint, "replicated-outage", data)
            content_id = stat_hash("/replicated-outage")
            if cat_bytes("/replicated-outage") != data:
                raise GateFailure("local content did not commit while GCS was stopped")
            try:
                request_json(f"{FAKE_GCS}/storage/v1/b/{BUCKET}", timeout=1)
            except urllib.error.URLError as exc:
                reason = exc.reason
                if isinstance(reason, (TimeoutError, socket.timeout)):
                    pass
                elif not isinstance(reason, OSError) or reason.errno not in (
                    errno.ECONNREFUSED,
                    errno.ECONNRESET,
                    errno.ETIMEDOUT,
                ):
                    raise GateFailure(f"direct fake-GCS probe failed for an unexpected reason: {reason!r}") from exc
            else:
                raise GateFailure("direct fake-GCS API remained reachable while outage was expected")
            local_object = Path(REPOSITORY) / "objects" / content_id
            outage_until = time.monotonic() + 2.5
            wait_until("scheduler remains unable to deliver during sustained GCS outage", lambda: time.monotonic() >= outage_until, timeout=3)
            if local_object.read_bytes() != data:
                raise GateFailure("unreferenced pending Local source disappeared or changed during scheduler retries")
            try:
                gcs_object(content_id)
            except urllib.error.URLError:
                pass
            else:
                raise GateFailure("fake GCS unexpectedly responded while its container was stopped")
            (ARTIFACTS / "g2-outage-content-id").write_text(content_id + "\n", encoding="ascii")
            return "direct fake-GCS probe confirmed connection refusal; Local commit remained byte-identical and readable through scheduler retries, then delivered after recovery"

        scenario("G2-02", "Fsync remains local during GCS outage and failed delivery leaves durable work", commit_while_stopped)


def g2_ack_crash() -> None:
    data = random.Random(0xACCA11).randbytes(512 * 1024)

    def commit_then_crash_before_ack():
        proxy_control("arm", method="POST")
        payload_path = ARTIFACTS / "ack-boundary.bin"
        payload_path.write_bytes(data)
        nando("put", str(payload_path), "/ack-boundary")
        content_id = stat_hash("/ack-boundary")
        if cat_bytes("/ack-boundary") != data:
            raise GateFailure("local content was not committed before remote delivery")
        if gcs_object(content_id) is not None:
            raise GateFailure("fault-injection object unexpectedly exists before scheduler eligibility")
        wait_until("upstream object committed behind held upload response", lambda: gcs_object(content_id) is not None, timeout=30)
        proxy = wait_proxy_committed()
        if proxy["held_commits"] != 1:
            raise GateFailure(f"proxy did not hold exactly one committed upload response: {proxy}")
        object_metadata = gcs_object(content_id)
        generation = int(object_metadata["generation"])
        assert_gcs_object(content_id, data)
        proxy_before_crash = proxy_control("status")

        daemon_pid = int(os.environ["E2E_DAEMON_PID"])
        try:
            os.kill(daemon_pid, signal.SIGKILL)
        except ProcessLookupError as exc:
            raise GateFailure(f"daemon exited before the held GCS response boundary: pid={daemon_pid}") from exc
        wait_until("daemon SIGKILL before delivery acknowledgement", lambda: process_state(daemon_pid) in (None, "Z"), timeout=5)
        proxy_control("release", method="POST")
        (ARTIFACTS / "g2-ack-boundary.json").write_text(
            json.dumps({"content_id": content_id, "generation": generation, "sha256": hashlib.sha256(data).hexdigest(), "object_attribute_gets": proxy_before_crash["object_attribute_gets"], "upload_puts": proxy_before_crash["upload_puts"]}, sort_keys=True) + "\n",
            encoding="utf-8",
        )
        return f"daemon scheduler committed fake GCS generation={generation}; upload response was held; daemon SIGKILL preceded transfer acknowledgement"

    scenario("G2-05", SCENARIO_EXPECTATIONS["G2-05"], commit_then_crash_before_ack)


def wait_proxy_committed() -> dict:
    latest: dict = {}

    def committed() -> bool:
        nonlocal latest
        latest = proxy_control("status")
        return latest.get("held_commits", 0) == 1

    wait_until("proxy confirmation of held post-commit response", committed, timeout=30)
    return latest


def g2_ack_replay() -> None:
    expected_state = json.loads((ARTIFACTS / "g2-ack-boundary.json").read_text(encoding="utf-8"))
    content_id = expected_state["content_id"]
    data = random.Random(0xACCA11).randbytes(512 * 1024)

    def retry_ambiguous_delivery():
        before = proxy_control("status")
        wait_until(
            "scheduler reconciles existing GCS object after daemon restart",
            lambda: proxy_control("status")["object_attribute_gets"] > expected_state["object_attribute_gets"],
            timeout=45,
        )
        after = proxy_control("status")
        # A resumed upload session may be rejected at its initial request, or
        # the storage client may reconcile the committed object by attributes
        # before starting a new upload. Both paths must avoid replacing the
        # immutable object. fake-gcs-server can also accept an old session's
        # final chunk, so requiring a specific 412 from that emulator is not a
        # reliable statement of the repository contract.
        if after["upload_puts"] != expected_state["upload_puts"]:
            raise GateFailure(f"scheduler resent payload after the committed object was found: before={expected_state}, after={after}")
        metadata = gcs_object(content_id)
        if metadata is None or int(metadata["generation"]) != expected_state["generation"]:
            raise GateFailure(f"idempotent replay changed or lost the committed generation: {metadata}")
        evidence = assert_gcs_object(content_id, data)
        return f"pending transfer was retried after restart; generation stayed {expected_state['generation']}; {evidence}"

    scenario("G2-06", SCENARIO_EXPECTATIONS["G2-06"], retry_ambiguous_delivery, timeout=60)


def g2_retry() -> None:
    data = b"local-commit-during-gcs-outage"
    content_id = (ARTIFACTS / "g2-outage-content-id").read_text(encoding="ascii").strip()

    def resend_after_restart():
        def available():
            try:
                request_json(f"{FAKE_GCS}/storage/v1/b/{BUCKET}", timeout=1)
                return True
            except Exception:
                return False

        wait_until("restarted fake-gcs-server", available, timeout=30)
        if gcs_object(content_id) is not None:
            raise GateFailure("outage commit was already delivered before the queued retry")
        wait_until("daemon-scheduled queued GCS delivery after emulator restart", lambda: gcs_object(content_id) is not None, timeout=45)
        evidence = assert_gcs_object(content_id, data)
        return f"retry after emulator restart delivered the previously queued object; {evidence}"

    scenario("G2-03", "stopped-emulator delivery retries after restart without losing local commit", resend_after_restart)

def g3_commit() -> None:
    data = b"fsync-success-survives-daemon-crash"
    with fuse_mount("g3-commit") as mountpoint:
        def fsync_committed():
            committed_file(mountpoint, "crash-persist", data)
            if cat_bytes("/crash-persist") != data:
                raise GateFailure("fsync-success bytes were not externally visible")
            return "FUSE fsync returned success; daemon cat confirms committed bytes"

        scenario("G3-01", "successful fsync bytes are visible before daemon crash", fsync_committed)


def g3_verify_committed() -> None:
    data = b"fsync-success-survives-daemon-crash"
    daemon_ready()

    def verify_after_daemon_crash():
        if cat_bytes("/crash-persist") != data:
            raise GateFailure("successful fsync bytes were lost after daemon crash/restart")
        with fuse_mount("g3-remount") as mountpoint:
            if Path(mountpoint, "crash-persist").read_bytes() != data:
                raise GateFailure("remounted FUSE view did not show committed bytes")
        return "daemon restart and a fresh real mount both expose fsync-committed content"

    scenario("G3-02", "committed data survives daemon crash and remount", verify_after_daemon_crash)


def g3_start_uncommitted() -> None:
    daemon_ready()
    mountpoint = str(ARTIFACTS / "g3-crash-mount")
    os.makedirs(mountpoint, exist_ok=True)
    ready_file = ARTIFACTS / "g3-holder-ready"
    fuse_pid_file = ARTIFACTS / "g3-fuse-pid"
    if ready_file.exists():
        ready_file.unlink()
    if mount_present(mountpoint):
        raise GateFailure("crash-test mountpoint was already mounted")
    log_file = (ARTIFACTS / "nando-fuse-g3-crash.log").open("ab", buffering=0)
    fuse = subprocess.Popen(
        [FUSE_BIN, "--daemon-listen", ENDPOINT, "--fuse-mountpoint", mountpoint],
        stdin=subprocess.DEVNULL,
        stdout=log_file,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    register_process(fuse, f"crash-test FUSE mount {mountpoint}")
    ACTIVE_MOUNTS.add(mountpoint)
    holder = None
    try:
        wait_until("crash-test FUSE mount", lambda: mount_present(mountpoint), timeout=15)
        fuse_pid_file.write_text(f"{fuse.pid}\n", encoding="ascii")
        holder = subprocess.Popen(
            [sys.executable, str(Path(__file__).with_name("holder.py")), mountpoint, str(ready_file)],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=(ARTIFACTS / "g3-holder.log").open("ab", buffering=0),
            start_new_session=True,
        )
        register_process(holder, "uncommitted FUSE holder")
        wait_until("uncommitted FUSE writer open", ready_file.exists, timeout=15)
        (ARTIFACTS / "g3-holder-pid").write_text(f"{holder.pid}\n", encoding="ascii")
        record(
            "G3-03",
            "PASS",
            "dirty unfsynced staging remains open while FUSE and daemon are crashed",
            f"FUSE pid={fuse.pid}; open writer pid={holder.pid}; no Fsync issued",
        )
        detach_intentionally_live_crash_processes()
    except BaseException:
        if holder is not None:
            try:
                terminate_process_group(holder, "uncommitted FUSE holder", force=True)
            except BaseException as exc:
                print(f"cleanup FAIL holder: {exc}", file=sys.stderr, flush=True)
        try:
            unmount_path(mountpoint)
        except BaseException as exc:
            print(f"cleanup FAIL mount: {exc}", file=sys.stderr, flush=True)
        try:
            terminate_process_group(fuse, f"crash-test FUSE mount {mountpoint}", force=True)
        except BaseException as exc:
            print(f"cleanup FAIL FUSE: {exc}", file=sys.stderr, flush=True)
        raise
    finally:
        log_file.close()


def _process_state(pid: int) -> str | None:
    return process_state(pid)


def g3_crash_fuse() -> None:
    mountpoint = str(ARTIFACTS / "g3-crash-mount")
    fuse_pid = int((ARTIFACTS / "g3-fuse-pid").read_text().strip())
    holder_pid = int((ARTIFACTS / "g3-holder-pid").read_text().strip())
    try:
        os.killpg(fuse_pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    def fuse_stopped():
        state = _process_state(fuse_pid)
        if state == "D":
            raise GateFailure(f"FUSE server entered uninterruptible D state, pid={fuse_pid}")
        return state in (None, "Z")
    wait_until("FUSE server process exit", fuse_stopped, timeout=5)
    state = _process_state(fuse_pid)
    if state == "D":
        raise GateFailure(f"FUSE server entered uninterruptible D state, pid={fuse_pid}")
    ACTIVE_PROCESSES.pop(fuse_pid, None)
    unmount_path(mountpoint)
    try:
        os.killpg(holder_pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    def holder_stopped():
        state = _process_state(holder_pid)
        if state == "D":
            raise GateFailure(f"open writer entered uninterruptible D state, pid={holder_pid}")
        return state in (None, "Z")
    wait_until("uncommitted holder exit", holder_stopped, timeout=5)
    if _process_state(holder_pid) == "D":
        raise GateFailure(f"open writer entered uninterruptible D state, pid={holder_pid}")
    ACTIVE_PROCESSES.pop(holder_pid, None)
    record(
        "G3-04",
        "PASS",
        "FUSE server crash detaches the mount without committing open dirty bytes",
        "FUSE process died; mountinfo no longer contains crash mount; holder exited",
    )


def g3_verify_recovery() -> None:
    daemon_ready()

    def verify_recovery_and_committed_bytes():
        data = cat_bytes("/crash-persist")
        if data != b"fsync-success-survives-daemon-crash":
            raise GateFailure(f"committed file content after restart is {data!r}")
        rows = nando("recovery", "list").stdout.decode().splitlines()
        matches = [row.split("\t") for row in rows if "/crash-uncommitted" in row]
        expected = b"must-be-recovered-after-crash"
        if len(matches) != 1 or len(matches[0]) < 3:
            raise GateFailure(f"expected one Recovery record for /crash-uncommitted, got {rows}")
        session_id = matches[0][0]
        if f"size={len(expected)}" not in matches[0][2]:
            raise GateFailure(f"Recovery staged size did not match expected payload length: {matches[0]}")
        manifest_path = Path(REPOSITORY) / "temp" / "rpc-sessions" / f"{session_id}.json"
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        if manifest.get("session_id") != session_id or manifest.get("repository_path") != "/crash-uncommitted":
            raise GateFailure(f"Recovery manifest identity/path mismatch: {manifest}")
        if manifest.get("committed") is not False:
            raise GateFailure(f"Recovery manifest unexpectedly marks the interrupted write committed: {manifest}")
        expected_relative_stage = Path("temp") / "rpc-sessions" / f"{session_id}.stage"
        if manifest.get("version") != 2 or manifest.get("staging_path") != expected_relative_stage.as_posix():
            raise GateFailure(
                f"Recovery manifest staging path is not the canonical repository-relative path: {manifest}"
            )
        stage_path = Path(REPOSITORY) / expected_relative_stage
        stage_root = (Path(REPOSITORY) / "temp" / "rpc-sessions").resolve()
        if stage_path.parent.resolve() != stage_root or not stat.S_ISREG(stage_path.lstat().st_mode):
            raise GateFailure(f"Recovery manifest points outside the repository staging directory: {stage_path}")
        recovered = stage_path.read_bytes()
        if recovered != expected:
            raise GateFailure(
                f"durable Recovery stage content mismatch: sha256={hashlib.sha256(recovered).hexdigest()}, "
                f"expected={hashlib.sha256(expected).hexdigest()}, length={len(recovered)}"
            )
        before = metadata_child("crash-uncommitted")
        if before is None or int(before[1]) != 1 or int(before[2]) != 0 or not before[3]:
            raise GateFailure(f"initially created empty Entry state before retry is inconsistent: {before}")
        expectation = manifest["expectation"]
        observed = manifest["observed_entry"]
        if expectation["MustBeAbsent"] or expectation["EntryID"] != before[0] or expectation["Revision"] != 1:
            raise GateFailure(f"Recovery manifest lost the initial Entry CAS expectation: {expectation}, Entry={before}")
        if manifest.get("observed_object_id") != before[3] or observed.get("ObjectID") != {}:
            raise GateFailure(
                f"Recovery manifest did not durably encode canonical observed Object ID: "
                f"observed_object_id={manifest.get('observed_object_id')!r}, database={before[3]!r}, "
                f"legacy-object-field={observed.get('ObjectID')!r}"
            )
        # Retry is the product operation under test; exercise it through the CLI/daemon boundary.
        rpc_cli(["recovery", "retry", session_id])
        actual = cat_bytes("/crash-uncommitted")
        if actual != expected:
            raise GateFailure(
                f"recovered committed bytes mismatch: sha256={hashlib.sha256(actual).hexdigest()}, "
                f"expected={hashlib.sha256(expected).hexdigest()}"
            )
        remaining = nando("recovery", "list").stdout.decode()
        if session_id in remaining:
            raise GateFailure(f"successful recovery retry left Recovery record: {remaining}")
        after = metadata_child("crash-uncommitted")
        if after is None or int(after[2]) != len(expected) or not after[3]:
            raise GateFailure(f"persistent Entry after retry is missing or wrong: {after}")
        winner = b"concurrent-winner-must-survive"
        conflict_path = ARTIFACTS / "recovery-winner.bin"
        conflict_path.write_bytes(winner)
        put_winner = subprocess.run(
            [NANDO_BIN, "--repository", REPOSITORY, "--daemon-listen", ENDPOINT, "put", str(conflict_path), "/recovery-conflict"],
            text=True,
            capture_output=True,
            timeout=30,
        )
        if put_winner.returncode != 0:
            raise GateFailure(f"could not establish true recovery CAS conflict: {put_winner.stderr.strip()}")
        conflict = rpc_call(
            "resource.beginWrite",
            {"path": "/recovery-conflict", "resource_schemes": ["file"]},
        )
        if not conflict.get("session_id") or not conflict.get("resource_uri"):
            raise GateFailure(f"BeginWrite did not return a concrete stale-CAS session: {conflict}")
        conflict_manifest_path = Path(REPOSITORY) / "temp" / "rpc-sessions" / f"{conflict['session_id']}.json"
        conflict_manifest = json.loads(conflict_manifest_path.read_text(encoding="utf-8"))
        if conflict_manifest["expectation"] != {"MustBeAbsent": False, "EntryID": conflict_manifest["expectation"]["EntryID"], "Revision": 1}:
            raise GateFailure(f"true-conflict test manifest lacks the winner's original revision CAS: {conflict_manifest['expectation']}")
        conflict_stage = Path(urllib.parse.urlparse(conflict["resource_uri"]).path)
        conflict_payload = b"stale-recovery-must-not-overwrite"
        conflict_stage.write_bytes(conflict_payload)
        rpc_call("resource.release", {"session_id": conflict["session_id"]})
        records_before_retry = nando("recovery", "list").stdout.decode()
        if conflict["session_id"] not in records_before_retry:
            raise GateFailure(f"Release did not create durable Recovery before retry: {records_before_retry}")
        winner = b"newer-concurrent-winner-must-survive"
        conflict_path.write_bytes(winner)
        advance_winner = nando("put", str(conflict_path), "/recovery-conflict", check=False)
        if advance_winner.returncode != 0:
            raise GateFailure(f"could not advance competing Entry revision: {advance_winner.stderr.decode(errors='replace')}")
        conflict_retry = rpc_cli(["recovery", "retry", conflict["session_id"]], check=False)
        conflict_output = (conflict_retry.stdout + conflict_retry.stderr).decode(errors="replace")
        if conflict_retry.returncode == 0:
            raise GateFailure(f"true stale Recovery CAS unexpectedly succeeded: {conflict_output}")
        if "conflict" not in conflict_output.lower() and "entry changed" not in conflict_output.lower():
            raise GateFailure(f"true stale Recovery CAS did not return an explicit conflict: {conflict_output}")
        records_after_conflict = nando("recovery", "list").stdout.decode()
        if conflict["session_id"] not in records_after_conflict or "revision=1" not in records_after_conflict or "conflict" not in records_after_conflict.lower():
            raise GateFailure(f"true conflict did not retain original expectation and conflict state: {records_after_conflict}")
        if conflict_stage.read_bytes() != conflict_payload:
            raise GateFailure("true conflict retry changed or removed the original Recovery staging bytes")
        if cat_bytes("/recovery-conflict") != winner:
            raise GateFailure("true Recovery conflict overwrote the winning Entry bytes")
        return (
            f"manifest CAS and canonical observed Object ID matched SQLite; retry committed exact bytes and removed {session_id}; "
            f"true stale revision conflict retained original Recovery bytes/expectation and preserved winner; "
            f"SHA-256={hashlib.sha256(actual).hexdigest()}"
        )

    scenario("G3-05", "daemon crash recovery preserves dirty session and successful fsync commit", verify_recovery_and_committed_bytes)


def g4_stress() -> None:
    with fuse_mount("g4-stress") as mountpoint:
        def parallel_fsyncs():
            workers = 8
            path = os.path.join(mountpoint, "stress-shared.bin")
            generator = random.Random(0x4E414E444F)
            initial = generator.randbytes(2 * 1024 * 1024)
            expected = bytearray()
            fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_RDWR, 0o644)
            try:
                for offset in range(0, len(initial), 64 * 1024):
                    chunk = initial[offset : offset + 64 * 1024]
                    if os.write(fd, chunk) != len(chunk):
                        raise GateFailure("short write while creating 2 MiB binary stress input")
                os.fsync(fd)
            finally:
                os.close(fd)
            observed = cat_bytes("/stress-shared.bin")
            if hashlib.sha256(observed).digest() != hashlib.sha256(initial).digest():
                raise GateFailure("initial large binary content SHA-256 mismatch")

            truncated_size = 1024 * 1024 + 317
            fd = os.open(path, os.O_RDWR)
            try:
                os.ftruncate(fd, truncated_size)
                os.fsync(fd)
            finally:
                os.close(fd)
            expected = bytearray(initial[:truncated_size])
            truncated = cat_bytes("/stress-shared.bin")
            if len(truncated) != truncated_size or hashlib.sha256(truncated).digest() != hashlib.sha256(expected).digest():
                raise GateFailure("truncate did not persist the exact expected binary prefix")

            # Each worker owns distinct 8 KiB regions but uses random offsets
            # inside them, so the final byte array has an independent oracle.
            regions = generator.sample(range(truncated_size // 8192), workers * 2)
            patches = []
            for index, region in enumerate(regions):
                region_start = region * 8192
                max_start = min(region_start + 4096, truncated_size - 2048)
                offset = generator.randint(region_start, max_start)
                patch_bytes = generator.randbytes(2048)
                patches.append((index, offset, patch_bytes))
                expected[offset : offset + len(patch_bytes)] = patch_bytes

            barrier = threading.Barrier(workers)
            errors: list[BaseException] = []
            descriptors = [os.open(path, os.O_RDWR) for _ in range(workers)]

            def worker(index: int):
                fd = descriptors[index]
                try:
                    barrier.wait(timeout=8)
                    for patch_index in (index * 2, index * 2 + 1):
                        _, offset, patch_bytes = patches[patch_index]
                        written = os.pwrite(fd, patch_bytes, offset)
                        if written != len(patch_bytes):
                            raise GateFailure(f"short random-offset write: {written}/{len(patch_bytes)}")
                    os.fsync(fd)
                except BaseException as exc:
                    errors.append(exc)

            threads = [threading.Thread(target=worker, args=(i,)) for i in range(workers)]
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join(timeout=30)
            for fd in descriptors:
                os.close(fd)
            if any(thread.is_alive() for thread in threads):
                raise GateFailure("stress writer exceeded 30s")
            if errors:
                raise GateFailure(f"stress writer errors: {errors}")
            actual = cat_bytes("/stress-shared.bin")
            expected_hash = hashlib.sha256(expected).hexdigest()
            actual_hash = hashlib.sha256(actual).hexdigest()
            if len(actual) != len(expected) or actual_hash != expected_hash:
                raise GateFailure(
                    f"same-file concurrent result mismatch: length={len(actual)}/{len(expected)} "
                    f"sha256={actual_hash}/{expected_hash}"
                )
            return (
                f"2 MiB binary write, truncate to {truncated_size}, and {workers} concurrent handles "
                f"performed randomized-offset writes; final SHA-256={actual_hash}"
            )

        scenario("G4-01", "optional bounded parallel create/write/fsync stress preserves every result", parallel_fsyncs)


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: runner.py <wait-daemon|health|g1|g2-init|g2-proxy-ready|g2-normal|g2-outage|g2-ack-crash|g2-ack-replay|g2-retry|g3-commit|g3-verify|g3-start-uncommitted|g3-crash-fuse|g3-verify-recovery|g4>", file=sys.stderr)
        return 2
    action = sys.argv[1]
    signal.signal(signal.SIGTERM, _terminate_on_signal)
    signal.signal(signal.SIGINT, _terminate_on_signal)
    log_offset = SCENARIO_LOG.stat().st_size if SCENARIO_LOG.exists() else 0
    try:
        if action == "wait-daemon":
            daemon_ready(deadline=30.0)
            print("PASS daemon readiness: ping succeeded")
        elif action == "health":
            if not stat.S_ISCHR(os.stat("/dev/fuse").st_mode):
                raise GateFailure("/dev/fuse is not a character device")
            daemon_ready()
            print("runner ready; daemon ping passed; /dev/fuse accessible")
        elif action == "g1":
            g1()
        elif action == "relative-repository":
            relative_repository()
        elif action == "g2-init":
            g2_init()
        elif action == "g2-proxy-ready":
            g2_proxy_ready()
        elif action == "g2-normal":
            g2_normal()
        elif action == "g2-outage":
            g2_outage_commit()
        elif action == "g2-ack-crash":
            g2_ack_crash()
        elif action == "g2-ack-replay":
            g2_ack_replay()
        elif action == "g2-retry":
            g2_retry()
        elif action == "g3-commit":
            g3_commit()
        elif action == "g3-verify":
            g3_verify_committed()
        elif action == "g3-start-uncommitted":
            g3_start_uncommitted()
        elif action == "g3-crash-fuse":
            g3_crash_fuse()
        elif action == "g3-verify-recovery":
            g3_verify_recovery()
        elif action == "g4":
            g4_stress()
        else:
            raise GateFailure(f"unknown action {action!r}")
    except BaseException as exc:
        cleanup_errors = cleanup_active()
        detail = f"action {action} failed: {type(exc).__name__}: {exc}"
        if cleanup_errors:
            detail += f"; cleanup failures: {cleanup_errors}"
        action_failures(action, detail)
        print(f"FAIL {action}: {type(exc).__name__}: {exc}", file=sys.stderr, flush=True)
        if cleanup_errors:
            print(f"CLEANUP FAIL {action}: {cleanup_errors}", file=sys.stderr, flush=True)
        return 1
    if action == "g3-start-uncommitted":
        detach_intentionally_live_crash_processes()
    else:
        cleanup_errors = cleanup_active()
        if cleanup_errors:
            detail = f"action {action} cleanup failed: {cleanup_errors}"
            action_failures(action, detail)
            print(f"CLEANUP FAIL {action}: {cleanup_errors}", file=sys.stderr, flush=True)
            return 1
    observed = action_records_since(log_offset)
    observed_by_id = {item["scenario"]: item["status"] for item in observed}
    missing_or_failed = [scenario_id for scenario_id in ACTION_SCENARIOS.get(action, []) if observed_by_id.get(scenario_id) != "PASS"]
    if missing_or_failed:
        detail = f"action {action} did not produce PASS for required scenarios: {missing_or_failed}"
        for scenario_id in missing_or_failed:
            record(scenario_id, "FAIL", SCENARIO_EXPECTATIONS.get(scenario_id, "scenario assertions complete without error"), detail)
        print(f"FAIL {action}: {detail}", file=sys.stderr, flush=True)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
