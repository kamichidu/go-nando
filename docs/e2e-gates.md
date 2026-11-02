= Reproducible E2E gates

`e2e/run.sh` is the host one-command entry point for G0-G3 plus WebDAV G5. Docker Compose owns only the pinned `fake-gcs-server` external dependency. Go checks, `nando`, `nando-fuse`, `nando-webdav`, and the scenario runner execute directly on the Linux/WSL host. The gate never mounts FUSE inside a container, requests `privileged`, or grants a container mount capabilities.

Run the required gates:

[source,bash]
----
e2e/run.sh
----

Run the optional bounded writer stress profile with `e2e/run.sh --stress`.

== Requirements and lifecycle

The host needs Go 1.25.1, Python 3, curl, Docker Compose v2, `golangci-lint`, `fusermount3`, and a readable/writable `/dev/fuse` that permits a real host mount. The validator checkout must be at commit `455ed318b7bc409d3e1da320620ef92d457042b9`; set `DESIGN_IR_REPO` to its path if it is not `../design-ir`.

Every run creates an isolated temporary evidence and repository directory and a unique Compose project. The command starts fake GCS, then G0 runs root and FUSE-module unit and race tests, the repository lint configuration and lint checks for both modules, native binary builds, and the pinned Design IR validator. G1 uses only a Local-mode repository and makes no GCS requests. G2 uses a project-scoped named volume and loopback-only host port to test fake GCS delivery, outage, restart, and retry. A host-only HTTP fault proxy holds the successful upload response after fake GCS has durably accepted one object; the gate kills the daemon before the response can acknowledge transfer completion, restarts it, and verifies an actual replay keeps the same generation and single object name. The proxy is test infrastructure only; it adds no production hook. Native binaries use the isolated repositories and Unix sockets. Exit cleanup discovers only processes whose executable arguments refer to that run's artifact directory, terminates their process groups or recorded proxy PID with deadlines, unmounts only mounts under that directory, reports D-state processes, captures Compose status and logs, and runs `docker compose down --volumes --remove-orphans`. Any remaining process, mount, or cleanup failure makes the command fail. Evidence remains under the printed `/tmp/go-nando-e2e.*` path. No existing repository state or volume is reused.

Missing FUSE prerequisites produce G1-G3 `SKIP` and a nonzero overall result. A failed or unavailable real mount never becomes PASS. Each scenario has a 45-second deadline; readiness and eventual conditions poll against monotonic deadlines, and each gate has an outer timeout. Logs include `gates.tsv`, per-gate command output, `scenarios.jsonl`, `scenarios.log`, native daemon/FUSE logs, and Compose logs. If a kernel operation becomes uninterruptible, the gate fails, attempts unmount and process cleanup, and leaves the evidence directory for diagnosis.

== Scenario catalog

|===
|Gate / ID |Scenario |External observation

|G0 `G0-static`
|Native static and module checks
|Root, FUSE, and WebDAV module tests and race tests, lint configuration and checks, native binary builds, and pinned Design IR validation all succeed.

|G1 `G1-01`
|CRUD and namespace
|Actual host FUSE mount supports create/write/fsync/cat/copy/rename/list/mkdir/rmdir/unlink.

|G1 `G1-02`
|Final-close durability
|Daemon `cat` sees dirty bytes after final close without an explicit fsync.

|G1 `G1-03`
|Multiple writers
|Two open handles concurrently write and fsync; daemon `cat` sees both bytes.

|G1 `G1-04`
|Rename with open writer
|Open handle follows stable Entry identity; only the renamed path resolves committed bytes.

|G1 `G1-05`
|Unlink with open writer
|Name disappears immediately; open handle can access private staging; close does not recreate namespace or expose ordinary Recovery.

|G1 `G1-06`
|CAS conflict
|Competing daemon put wins; stale FUSE fsync returns EAGAIN and failed staging remains in Recovery.

|G2 `G2-00`
|Bucket setup
|Bucket is created through fake GCS JSON API.

|G2 `G2-01`
|Asynchronous delivery
|Local fsync and daemon read succeed before maintenance; fake GCS then exposes matching metadata, generation, content ID and downloaded bytes.

|G2 `G2-02`
|Emulator outage
|A direct fake-GCS API probe confirms connection refusal; local fsync/read still succeed; maintenance returns a repository transport error with an accepted upstream cause or hits its explicit 12-second RPC deadline.

|G2 `G2-03`
|Retry after restart
|After emulator restart, maintenance delivers queued bytes and object metadata through the external JSON API.

|G2 `G2-04`
|Duplicate content idempotency
|A second Entry with identical bytes reconciles the same object ID; listing has exactly one object name.

|G2 `G2-05`
|Lost acknowledgement after remote commit
|The host proxy observes fake GCS accept and persist the upload, withholds the success response, and the gate SIGKILLs the daemon before transfer completion can be acknowledged.

|G2 `G2-06`
|Replay after ambiguous commit
|After restart the pending transfer causes another upload PUT; content, generation, and the single external object name remain unchanged.

|G3 `G3-01` / `G3-02`
|Fsync and daemon restart
|Successful fsync bytes remain visible through daemon and a fresh real mount after daemon SIGKILL/restart.

|G3 `G3-03` / `G3-04`
|Dirty writer and FUSE crash
|Open unfsynced staging exists while the native FUSE process is killed; mount detaches and holder exits.

|G3 `G3-05`
|Recovery
|After daemon/FUSE crash, the manifest's original Entry revision and canonical observed Object ID are compared with SQLite; explicit retry commits exact bytes and removes that Recovery record. A second session captures an existing Entry revision, then a competing commit wins; retry must report conflict, retain the original staging and revision expectation, and preserve the winner's bytes.

|G4 `G4-01` (optional)
|Bounded stress
|A 2 MiB binary file is fsynced, truncated, then modified through eight simultaneous handles at seeded random offsets; final length and SHA-256 must match an independent byte-array oracle.
|G5 `G5-01`
|WebDAV HTTP interoperability smoke check
|Host curl verifies OPTIONS, GET/HEAD and ETag, PUT with If-Match/If-None-Match, MKCOL/PROPFIND, MOVE, file COPY to an absent path, DELETE, and explicit 501 responses for LOCK/PROPPATCH.
|===

== Emulator scope

The daemon uses the normal production Cloud Storage client with `STORAGE_EMULATOR_HOST=http://127.0.0.1:4443`; no source-level test override is added. The gate pins `fsouza/fake-gcs-server:1.52.3` and exercises JSON API bucket/object calls, metadata, generations, downloads, and duplicate-content reconciliation. It does not establish production IAM/ADC, ACL, billing, lifecycle, audit, throttling, or complete GCS API behavior. A pass is evidence only for the repository operations exercised against this emulator version, not cloud acceptance.

== Design reference note

The supplied repository workflow names `docs/index.adoc`, but it was absent when this gate was added. `docs/design.ir.yaml` remains the semantic authority already named by `README.adoc`; no architecture was inferred from the implementation. The scenario contract has been added there as `REQ-REPRODUCIBLE-E2E-GATES`.

== WebDAV G5

`e2e/run.sh` also builds and lint-checks the separate `adapters/webdav/` module at G0 and runs G5 on the Linux/WSL host. G5 starts the daemon and WebDAV executable natively and uses curl to check OPTIONS, GET/HEAD, PUT conditional validators, MKCOL/PROPFIND, MOVE, file COPY to an absent destination, DELETE, and explicit 501 responses for LOCK and PROPPATCH. It does not claim full DAV compliance; unsupported LOCK/dead-property/replacement semantics are documented in `webdav-phase-a.adoc`.
