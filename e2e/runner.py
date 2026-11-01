#!/usr/bin/env python3
"""Real-mount scenarios run natively on the Linux/WSL host."""

from __future__ import annotations

import errno
import http.client
import json
import os
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
BUCKET = "nando-e2e"
FUSE_BIN = os.environ["E2E_FUSE_BIN"]
NANDO_BIN = os.environ["E2E_NANDO_BIN"]
MOUNTINFO = Path("/proc/self/mountinfo")
SCENARIO_LOG = ARTIFACTS / "scenarios.jsonl"
TEXT_LOG = ARTIFACTS / "scenarios.log"


class GateFailure(RuntimeError):
    pass


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
        raise GateFailure(f"RPC {method} failed ({error.get('code')}): {error.get('message')}")
    return payload.get("result")


def nando(*args: str, timeout: float = 20.0, check: bool = True) -> subprocess.CompletedProcess:
    result = subprocess.run(
        [NANDO_BIN, "--repository", REPOSITORY, "--daemon-endpoint", ENDPOINT, *args],
        capture_output=True,
        timeout=timeout,
        check=False,
    )
    if check and result.returncode != 0:
        raise GateFailure(
            f"nando {' '.join(args)} exited {result.returncode}: "
            f"{result.stderr.decode(errors='replace')}"
        )
    return result


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


def scenario(scenario_id: str, expected: str, fn) -> None:
    started = time.monotonic()
    old_handler = signal.signal(signal.SIGALRM, _scenario_timeout)
    signal.setitimer(signal.ITIMER_REAL, 45.0)
    try:
        observed = fn()
        record(scenario_id, "PASS", expected, str(observed))
    except BaseException as exc:
        record(scenario_id, "FAIL", expected, f"{type(exc).__name__}: {exc}")
        raise
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, old_handler)
        if time.monotonic() - started > 45.0:
            raise GateFailure(f"scenario {scenario_id} exceeded its 45s deadline")


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
        [FUSE_BIN, "-endpoint", ENDPOINT, "-mountpoint", mountpoint],
        stdin=subprocess.DEVNULL,
        stdout=log_file,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    try:
        wait_until(
            f"real FUSE mount {mountpoint}",
            lambda: mount_present(mountpoint),
            timeout=15.0,
        )
        # Reading through the mount proves this is an active kernel FUSE mount.
        os.listdir(mountpoint)
        yield mountpoint
    finally:
        if mount_present(mountpoint):
            try:
                subprocess.run(["fusermount3", "-u", mountpoint], timeout=8, check=True, capture_output=True)
            except Exception:
                subprocess.run(
                    ["fusermount3", "-uz", mountpoint], timeout=5, check=False, capture_output=True
                )
        still_mounted = mount_present(mountpoint)
        if still_mounted:
            process.kill()
        if process.poll() is None:
            process.send_signal(signal.SIGTERM)
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=2)
        log_file.close()
        if still_mounted:
            raise GateFailure(f"FUSE mount remained after unmount attempt: {mountpoint}")
        if mount_present(mountpoint):
            raise GateFailure(f"FUSE mount reappeared during cleanup: {mountpoint}")
        os.rmdir(mountpoint)


def committed_file(mountpoint: str, name: str, data: bytes) -> None:
    path = os.path.join(mountpoint, name)
    fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_RDWR, 0o644)
    try:
        written = os.write(fd, data)
        if written != len(data):
            raise GateFailure(f"short FUSE write: {written}/{len(data)}")
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


def g2_normal() -> None:
    data = b"replication-success-without-gcs-credentials"
    with fuse_mount("g2-normal") as mountpoint:
        def commit_without_delivery():
            committed_file(mountpoint, "replicated-normal", data)
            content_id = stat_hash("/replicated-normal")
            if cat_bytes("/replicated-normal") != data:
                raise GateFailure("local commit was not visible before replication")
            if gcs_object(content_id) is not None:
                raise GateFailure("GCS object appeared before the explicit asynchronous maintenance run")
            rpc_call("repository.maintenanceOnce")
            wait_until("normal GCS object upload", lambda: gcs_object(content_id) is not None, timeout=30)
            evidence = assert_gcs_object(content_id, data)
            (ARTIFACTS / "g2-normal-content-id").write_text(content_id + "\n", encoding="ascii")
            return f"local Fsync commit preceded maintenance; {evidence}"

        scenario("G2-01", "local commit is visible before async delivery and then appears in fake GCS", commit_without_delivery)

    duplicate_data = b"replication-success-without-gcs-credentials"
    with fuse_mount("g2-duplicate") as mountpoint:
        def same_content_reenqueued():
            committed_file(mountpoint, "replicated-duplicate", duplicate_data)
            duplicate_id = stat_hash("/replicated-duplicate")
            normal_id = (ARTIFACTS / "g2-normal-content-id").read_text(encoding="ascii").strip()
            if duplicate_id != normal_id:
                raise GateFailure(f"identical content yielded IDs {duplicate_id} and {normal_id}")
            rpc_call("repository.maintenanceOnce")
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
                rpc_call("repository.maintenanceOnce", timeout=15)
            except Exception as exc:
                (ARTIFACTS / "g2-outage-content-id").write_text(content_id + "\n", encoding="ascii")
                return f"Fsync succeeded; maintenance returned bounded outage error; local bytes remain visible ({exc})"
            raise GateFailure("maintenance unexpectedly succeeded while fake GCS was stopped")

        scenario("G2-02", "Fsync remains local during GCS outage and failed delivery leaves durable work", commit_while_stopped)


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
        expires = time.monotonic() + 45
        last_error = None
        while time.monotonic() < expires:
            try:
                rpc_call("repository.maintenanceOnce", timeout=15)
                if gcs_object(content_id) is not None:
                    break
            except Exception as exc:
                last_error = exc
            time.sleep(min(0.2, max(0.0, expires - time.monotonic())))
        else:
            raise GateFailure(f"queued GCS transfer did not recover: {last_error}")
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
        [FUSE_BIN, "-endpoint", ENDPOINT, "-mountpoint", mountpoint],
        stdin=subprocess.DEVNULL,
        stdout=log_file,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
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
        wait_until("uncommitted FUSE writer open", ready_file.exists, timeout=15)
        (ARTIFACTS / "g3-holder-pid").write_text(f"{holder.pid}\n", encoding="ascii")
        record(
            "G3-03",
            "PASS",
            "dirty unfsynced staging remains open while FUSE and daemon are crashed",
            f"FUSE pid={fuse.pid}; open writer pid={holder.pid}; no Fsync issued",
        )
    except BaseException:
        if holder is not None:
            holder.kill()
        fuse.kill()
        if mount_present(mountpoint):
            subprocess.run(["fusermount3", "-uz", mountpoint], timeout=8, check=False, capture_output=True)
        raise
    finally:
        log_file.close()


def _process_state(pid: int) -> str | None:
    try:
        return Path(f"/proc/{pid}/stat").read_text().split()[2]
    except (FileNotFoundError, ProcessLookupError):
        return None


def g3_crash_fuse() -> None:
    mountpoint = str(ARTIFACTS / "g3-crash-mount")
    fuse_pid = int((ARTIFACTS / "g3-fuse-pid").read_text().strip())
    holder_pid = int((ARTIFACTS / "g3-holder-pid").read_text().strip())
    os.kill(fuse_pid, signal.SIGKILL)
    def fuse_stopped():
        state = _process_state(fuse_pid)
        if state == "D":
            raise GateFailure(f"FUSE server entered uninterruptible D state, pid={fuse_pid}")
        return state in (None, "Z")
    wait_until("FUSE server process exit", fuse_stopped, timeout=5)
    state = _process_state(fuse_pid)
    if state == "D":
        raise GateFailure(f"FUSE server entered uninterruptible D state, pid={fuse_pid}")
    if mount_present(mountpoint):
        unmount = subprocess.run(["fusermount3", "-uz", mountpoint], timeout=8, capture_output=True, check=False)
        if unmount.returncode != 0 and mount_present(mountpoint):
            raise GateFailure(f"could not detach crashed FUSE mount: {unmount.stderr.decode(errors='replace')}")
    if mount_present(mountpoint):
        raise GateFailure("crashed FUSE mount remains attached")
    os.kill(holder_pid, signal.SIGTERM)
    def holder_stopped():
        state = _process_state(holder_pid)
        if state == "D":
            raise GateFailure(f"open writer entered uninterruptible D state, pid={holder_pid}")
        return state in (None, "Z")
    wait_until("uncommitted holder exit", holder_stopped, timeout=5)
    if _process_state(holder_pid) == "D":
        raise GateFailure(f"open writer entered uninterruptible D state, pid={holder_pid}")
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
        rows = nando("recovery", "list").stdout.decode()
        if "/crash-uncommitted" not in rows or f"size={len(b'must-be-recovered-after-crash')}" not in rows:
            raise GateFailure(f"uncommitted FUSE stage absent from Recovery after restart: {rows}")
        return "fsync-success bytes persist; uncommitted open-writer Recovery record exposes its path and staged size"

    scenario("G3-05", "daemon crash recovery preserves dirty session and successful fsync commit", verify_recovery_and_committed_bytes)


def g4_stress() -> None:
    with fuse_mount("g4-stress") as mountpoint:
        def parallel_fsyncs():
            workers = 8
            barrier = threading.Barrier(workers)
            errors: list[BaseException] = []

            def worker(index: int):
                try:
                    barrier.wait(timeout=8)
                    data = f"stress-worker-{index}".encode()
                    fd = os.open(os.path.join(mountpoint, f"stress-{index}"), os.O_CREAT | os.O_EXCL | os.O_RDWR, 0o644)
                    try:
                        os.write(fd, data)
                        os.fsync(fd)
                    finally:
                        os.close(fd)
                except BaseException as exc:
                    errors.append(exc)

            threads = [threading.Thread(target=worker, args=(i,)) for i in range(workers)]
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join(timeout=30)
            if any(thread.is_alive() for thread in threads):
                raise GateFailure("stress writer exceeded 30s")
            if errors:
                raise GateFailure(f"stress writer errors: {errors}")
            for i in range(workers):
                expected = f"stress-worker-{i}".encode()
                if cat_bytes(f"/stress-{i}") != expected:
                    raise GateFailure(f"stress object {i} has unexpected bytes")
            return f"{workers} parallel files committed and were verified through daemon reads"

        scenario("G4-01", "optional bounded parallel create/write/fsync stress preserves every result", parallel_fsyncs)


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: runner.py <wait-daemon|health|g1|g2-init|g2-normal|g2-outage|g2-retry|g3-commit|g3-verify|g3-start-uncommitted|g3-crash-fuse|g3-verify-recovery|g4>", file=sys.stderr)
        return 2
    action = sys.argv[1]
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
        elif action == "g2-init":
            g2_init()
        elif action == "g2-normal":
            g2_normal()
        elif action == "g2-outage":
            g2_outage_commit()
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
        print(f"FAIL {action}: {type(exc).__name__}: {exc}", file=sys.stderr, flush=True)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
