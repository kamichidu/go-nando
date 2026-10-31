= Storage Delivery / Lease / GC Design Audit

== Authority and scope

`docs/design.ir.yaml` is the sole semantic design authority. This file is an audit record, source mapping, migration sequence, and test plan; it does not override or add requirements to the IR. No Go implementation was changed for this audit.

Audit baseline: `main` at `04f4e8991f0f7cd12c87a01fcd5a690558e5cf64`. The checkout has no `README.md` and no `docs/index.adoc`, although `GEMINI.md` names `docs/index.adoc` in its reading order. `docs/design-ir-migration.md` identifies `docs/design.ir.yaml` as the direct design authority. No replacement AsciiDoc index was created.

== Inspected implementation and current behavior

|===
| Area | Observed source and tests | Current behavior / gap against the accepted target

| Composition and CLI
| `internal/cli/root.go`, `internal/config/config.go`, `internal/app/init.go`, `internal/cli/commands/{put,get,cat,gc,doctor,init}.go`
| Composition always constructs SQLite Metadata and Local ObjectStore. User config selects repository path and verbosity only. There is no delivery-mode or bucket configuration. GCS is not contacted in Local operation because the GCS adapter is not selected; the GCS adapter is a stub.

| Put and staging
| `internal/repository/put.go`, `internal/objectstore/local/stage.go`, `internal/objectstore/local/store.go`; `internal/repository/repository_test.go`, `internal/objectstore/local/stage_test.go`
| Put creates an operation-ID-specific Local temp file before acquiring a SQLite Lease; then it copies/hashes, renews an operation Lease, and enters `ExclusiveWithLease` for parent resolution, Local no-replace Object publication, then Metadata Create/Update. No GCS upload or content-ID delivery receipt exists. The temp file is also used by failed/incomplete Put staging and current cleanup. A crash after Local publication but before Metadata commit leaves an orphan Object for GC.

| Lease and Coordinator
| `internal/repository/coordinator.go`, `internal/coordinator/sqlite/coordinator.go`; `internal/coordinator/sqlite/coordinator_test.go`
| `put-leases.db` has only `operation_id` and `expires_at`; it has no schema migration ledger. This is an operation-scoped Put staging Lease, not the target resource RWLock. Lease acquisition/renewal use a separate SQLite database so heartbeats do not wait behind the long `coordination.db` transaction. Commit validates the Lease while holding Coordinator exclusion. Expired Lease invalidation is coordinated, but active Lease enumeration and filesystem cleanup are separate operations. Resource-keyed RLock/WLock records do not exist. Coordinator tests cover the current Lease lifecycle, multiple instances/processes, cancellation, and renewal during a long exclusive callback.

| Local Object Store
| `internal/objectstore/store.go`, `internal/objectstore/local/store.go`, `internal/objectstore/local/stage.go`, `internal/objectstore/local/publish_*.go`, `internal/objectstore/local/publish_test.go`, `internal/objectstore/local/store_crossfs_linux_test.go`
| Local IDs are filenames under the configured objects directory. Stage uses an operation-ID-specific file under temp; Publish uses no-replace publication and validates an existing object before reuse. `List` returns parseable IDs; `ListRaw` returns raw filenames. `Delete` accepts only a valid content ID. There is no capacity-statistics API or safe deletion API for invalid raw names. Cross-filesystem atomic-publication failure is explicitly tested on Linux.

| GCS Object Store
| `internal/objectstore/gcs/store.go`
| All methods, including Stage, Publish, Open, List, Delete, and Verify, return `ErrNotImplemented`. There is no GCS conformance or integration test suite.

| Get and Cat
| `internal/repository/get.go`, `internal/repository/repository.go`; `internal/repository/phase4_test.go`, `internal/repository/repository_test.go`
| Both read only through the Local ObjectStore. Get creates an export temp file in the destination directory and atomically replaces the destination after full copy, attributes, sync, close, and reader close. Cat writes directly to the caller's writer. Get and the full Cat stream currently hold the Repository Coordinator exclusive operation through object I/O; a failing Cat writer can already have accepted a prefix. Neither path uses a content-ID Lease.

| GC and Doctor
| `internal/repository/repository.go`; `internal/repository/phase4_test.go`, `internal/repository/repository_test.go`
| GC first cleans staging, then under exclusive coordination compares Metadata references to `ObjectStore.List` and deletes every valid unreferenced Local object. It has no capacity target, replica-status check, GCS listing, GCS deletion, or invalid-raw-file deletion path. Doctor validates Metadata structure and all referenced Local objects, reports malformed raw Local filenames, and ignores valid unreferenced Objects. Doctor has no delivery-mode or remote-replica semantics.

| Metadata contract and SQLite schema
| `internal/metadata/store.go`, `internal/metadata/validating_store.go`, `internal/metadata/sqlite/schema.go`, `internal/metadata/sqlite/store.go`; `internal/metadata/sqlite/store_test.go`, `internal/metadata/validating_store_test.go`
| Public MetadataStore exposes semantic operations, not SQL transactions. SQLite schema migration v1 has `entries`, `repository_format`, and `schema_migrations`; `repository_format` stores format version and content-ID algorithm/format. Create, Update, Move, and Delete use SQLite transactions and operation-specific checks; revision CAS, sibling uniqueness, parent existence/kind, and cycle checks are backend-side. There is no Firestore adapter and no transfer-state schema. Metadata backup procedure is not present in the inspected CLI or app code.

| Existing compatibility tests
| `internal/repository/phase4_test.go`, `internal/repository/repository_test.go`, `internal/objectstore/local/*_test.go`, `internal/coordinator/sqlite/*_test.go`, `internal/metadata/sqlite/store_test.go`, `internal/cli/commands/commands_test.go`
| Current tests intentionally assert Local behavior, including GC waiting behind a full Cat/Get read, immediate deletion of unreferenced Local Objects, and the SQLite migration/format boundary. These assertions must be classified as Local-mode compatibility or revised for the new Lease contract; they must not simply be weakened. There are no GCS tests.
|===

The corresponding current-source facts and comparisons are recorded in `docs/design.ir.yaml` as `IMP-CURRENT-*` observations pinned to the audit baseline. They do not claim that the target design is implemented.

== Accepted target summarized from the IR

* Local mode has no GCS dependency; Local is the authoritative durable Object copy.
* GCS mode publishes a complete Local Object, then atomically commits the Entry and idempotent content-ID queue item in one MetadataStore operation. SQLite stores both in the existing Metadata DB and one transaction. A Firestore adapter must preserve this semantic atomicity without exposing backend transaction types. The Entry is immediately visible; transfer follows asynchronously. Before GCS confirmation, Local may be the only known-good physical copy, and physical loss may lose content.
* GCS uploads use resumable upload directly to final key `objects/{content_id}`, with no temporary Object or copy. A successful GCS API response is authoritative. Ambiguous results and existing-name/precondition responses converge through final-object lookup and API attributes. Persist generation and the API metadata needed for later conditional operations. Do not add independent full-object SHA-256 read-back; Local hash verification remains required, and arbitrary external GCS corruption is not guaranteed to be detected.
* A single Resource Lease model has resource ID, operation ID, RLock/WLock, expiry, and extensible information; one operation may protect multiple resources. RLocks are shared, WLocks exclusive. Local and GCS follow the same Lease transitions. The configurable defaults are a 30-second TTL and 5-second heartbeat; SQLite database time is authoritative for expiry. Heartbeat through operation completion and release afterward. Put acquires temp RLock before Stage creation, then content-ID RLock after hashing while retaining the temp lock through Local Publish and Metadata Commit. The Lease is the Put Commit validity check; there is no separate Put Operation Lease.
* Resource Lease records live in a dedicated area of the SQLite coordination database and are the only GC protection mechanism. GC obtains and renews WLock, then rechecks references, replica state, and deletion eligibility. Long I/O does not hold Repository-wide exclusive coordination.
* If renewal fails or a Lease expires, the operation stops and performs no later Publish, Metadata Commit, or queue confirmation. Already-sent I/O may finish late. This can make an Object unavailable: a read 404 fails locally and does not mutate Entry Metadata or propagate a repository-wide state change. No fencing token, Deleting state, Local/GCS-specific transition, or long Coordinator critical section is required. This is an accepted availability boundary, not an implementation blocker.
* One periodic Maintenance Job starts/schedules three independent subtasks: transfer queue processing, Capacity GC, and Orphan/Invalid GC. Scheduling is the only shared behavior; each subtask owns its failures, and transfer failure cannot stop either GC.
* Capacity GC uses Repository capacity settings and runs periodically, reclaiming eligible Local space only when needed. It preserves Local authority, untransferred GCS-mode content, and RLock-protected resources. Actual capacity exhaustion ends Put/Download with a capacity error; there is no required reservation or preflight space guarantee. The two GC classes remain distinct. Physical deletion rechecks eligibility under WLock and does not intentionally delete the last known-good copy. An already-sent delete may still finish late after expiry.
* Local/GCS mode, bucket, and capacity policy are Repository settings. No general settings framework is implied. Existing-repository compatibility and mode migration remain open.
* The persistent queue is content-ID keyed, idempotently enqueued in the same Metadata transaction as Put, rediscovered after restart, and retried periodically from complete Local content. GCS API-contract completion (including API-attribute reconciliation of ambiguous/existing-name results) deletes the queue row. It is not a Pending Entry or resumed Put; no retry state machine is needed. Metadata backups remain an operator responsibility.

== Remaining open decisions

These are the unresolved decisions retained as `OPEN-*` nodes in the IR; resolved Lease/fencing/read-back/session questions have been removed.

. **GCS bucket policy:** define behavior under versioning, soft delete, retention, external overwrite/delete, bucket or prefix changes, and object recreation. Generation retention and conditional operations are required; arbitrary external corruption detection is not.
. **Capacity policy values:** Repository settings own capacity policy, periodic GC consults it, and actual exhaustion returns a capacity error. Exact configurable thresholds and eviction priority are operational details not specified here; no reservation guarantee is required.
. **Doctor and Cat:** define GCS-mode Doctor findings for missing Local cache, pending transfer, or missing/corrupt GCS content. Cat can leave an already-written response prefix after downstream writer failure.
. **Mode migration:** current config has no mode or bucket. Decide persistent location, Local-to-GCS prefill, bucket changes, GCS-to-Local recovery, rollback, and safe behavior for existing repositories.
. **Firestore deployment:** current SQLite coordination is local-path/process scoped. A Firestore MetadataStore must define distributed coordination and consistent reference enumeration if deployed across hosts; adapter must retain atomic Entry-plus-queue semantics.
. **Backup/restore:** define the recoverable set and quiescence boundary for Metadata, Repository Format, delivery configuration, transfer queue, and Local objects.
. **Read revision policy:** decide whether a revision change during streaming returns the opened content or retries; this remains independent from Lease/GC safety.

The GCS JSON API supports resumable insert with `ifGenerationMatch` and generation-matched deletion preconditions. These mechanisms support no-replace and conditional-delete behavior; they do not promise detection of arbitrary external corruption: [Objects: insert](https://docs.cloud.google.com/storage/docs/json_api/v1/objects/insert), [request preconditions](https://docs.cloud.google.com/storage/docs/request-preconditions), [Objects: delete](https://docs.cloud.google.com/storage/docs/json_api/v1/objects/delete).

== Implementation readiness

The architecture decisions are settled for one Lease model and its default timing, Put ordering, GC WLock/re-evaluation, delayed in-flight I/O behavior, GCS API-confirmed transfer, atomic SQLite queue registration/deletion, Repository-owned delivery settings, capacity-error behavior, and the independent Maintenance subtasks. Physical GC and Phase 6 implementation are **not blocked on absolute fencing**. They must preserve the accepted availability boundary: a late request may yield a localized 404/read failure without Metadata mutation. Remaining Open Items concern GCS bucket policy, existing-repository mode migration, Doctor/Cat reporting, backups, and Firestore deployment; exact capacity-policy settings are not specified. Work can proceed in slices that respect those boundaries; unresolved operational choices must not be filled with stronger guarantees.

== Migration and implementation sequence

This sequence maps dependencies; it does not authorize implementation in this design task.

. **Preserve current Local behavior:** Local mode remains GCS-independent, retains no-replace publication and existing object hash verification, and should be covered by composition tests.
. **Add unified Resource Lease semantics:** replace current Put-specific lease target with one multi-resource Lease model. Acquire temp RLock before Stage creation; acquire content-ID RLock after hashing while keeping temp protected; heartbeat, validate before Publish/Commit/state confirmation, and release on completion. Implement identical transitions for Local/GCS.
. **Add the MetadataStore queue contract:** atomically mutate the Put Entry and enqueue its content ID. SQLite uses the existing Metadata DB transaction. Add migration and adapter-neutral semantic operations; Firestore conformance must use native transaction semantics.
. **Implement content-ID uploader:** retry unconfirmed queue IDs from complete Local objects; publish directly to final GCS key with resumable API and no-replace precondition. Treat success responses as authoritative and reconcile ambiguous/precondition responses through final-object API attributes. Persist generation/required API metadata.
. **Implement Local-first GCS reads:** hold content/temp RLocks, hydrate via shared temp, hash-verify bytes, and Local-publish without replacement before serving. Preserve Get atomic destination behavior. 404 remains a local operation failure; Cat may have emitted a prefix on downstream error.
. **Implement GC classes:** capacity eviction of confirmed Local cache only; invalid/orphan reconciliation of Local/GCS candidates. Acquire and renew WLock, recheck references/replica/deletion eligibility after acquisition, and honor the documented possibility of a sent delete completing late. Add invalid-raw Local deletion APIs only with explicit ownership checks.
. **Resolve remaining operation/config choices:** existing-repository mode migration, bucket policy, Doctor/Cat output, backups, and Firestore deployment/coordination.
. **Compatibility gate:** retain current Local CAS, migration, atomic Get, Doctor, CLI, and Metadata semantics tests where still normative. Rewrite full-operation lock timing assertions only to match the decided Lease semantics; add delayed-I/O/404 tests that assert no Metadata mutation.

== Existing implementation to target mapping

|===
| Current source | Target responsibility or required change (design only)

| `internal/cli/root.go`, `internal/config/config.go`, `internal/app/init.go`
| Repository-owned mode/bucket selection and composition; exact persisted location and initialization/migration remain open.

| `internal/repository/put.go`
| Preserve local spool/hash and Local publication before Metadata; add durable content-ID delivery discovery and async uploader only using the decided same-transaction Metadata queue contract.

| `internal/objectstore/store.go`, `internal/objectstore/local/stage.go`, `internal/objectstore/local/store.go`
| Keep Local no-replace CAS. Target interface needs complete verified hydrate/publish and separate capacity/orphan enumeration/deletion semantics; raw invalid deletion is currently absent.

| `internal/objectstore/gcs/store.go`
| Implement direct final-name resumable upload, Local-miss download, content verification, no-replace preconditions, object/generation listing and conditional deletion. Current adapter has no behavior to preserve.

| `internal/coordinator/sqlite/coordinator.go`, `internal/repository/coordinator.go`
| Replace current Put-specific Lease with the single multi-resource RLock/WLock model in the dedicated SQLite coordination area. Acquire temp RLock before Stage, add content-ID RLock after hashing, heartbeat through completion, validate before Publish/Commit, and release. GC WLock rechecks eligibility; a delayed sent delete may complete late as documented. No second GC protection mechanism. Local SQLite is not a multi-host Firestore coordinator.

| `internal/repository/repository.go` (Get/Cat/GC/Doctor)
| Replace full-read Coordinator hold with resource RLocks and WLocks; route local misses to hydration; split capacity and orphan GC; define mode-aware Doctor after Open Items are decided.

| `internal/metadata/store.go`, `internal/metadata/sqlite/schema.go`, `internal/metadata/sqlite/store.go`
| Keep backend-neutral atomic semantics. Add a content-ID transfer queue in the existing Metadata database migration. Put Entry mutation and queue insertion share one SQLite transaction. Expose backend-neutral atomic Put-and-enqueue and completion semantics; no SQLite transaction type enters the common interface.

| `internal/repository/phase4_test.go`, `internal/repository/repository_test.go`, `internal/objectstore/local/*_test.go`, `internal/coordinator/sqlite/*_test.go`, `internal/metadata/sqlite/store_test.go`, `internal/cli/commands/commands_test.go`
| Preserve Local behavior and final-state invariants; add cross-mode and remote failure/concurrency matrices below. Current tests asserting full-read exclusion must be rewritten only after the new Lease semantics are accepted.
|===

== Required test matrix before GCS implementation is considered complete

|===
| Area | Scenarios and final-state assertion

| Mode/configuration
| Local mode initializes/runs with no GCS client or credentials; GCS mode selects repository-owned bucket identity. Existing repositories remain Local unless explicitly migrated. Queue enqueue is atomic with Put Entry mutation through MetadataStore. Invalid/missing format/config fails before writes.

| Put and durable transfer state
| Same bytes under multiple logical names create one content-ID queue item; Put atomically commits Entry and queue item in one Metadata operation; restart re-discovers unconfirmed IDs; restart after upload-before-completion retries by verify/no-replace; duplicate enqueue is idempotent; failure before local publish leaves no metadata; metadata replacement preserves old entry until commit.

| Resumable GCS upload
| direct final key only; interrupted session; successful response; ambiguous response resolved by object lookup/API attributes; existing-name precondition handled idempotently; duplicate uploader; generation precondition conflict; required generation/API attributes persisted. Do not require full-object read-back SHA-256 or promise detection of arbitrary external GCS corruption.

| Lease/GC races
| temp RLock is acquired before Stage creation; content-ID RLock is added after hash while temp lock remains held; RLock sharing/WLock exclusivity; WLock rechecks eligibility; heartbeat/release; renewal failure blocks subsequent Publish/Commit/queue confirmation; request sent before expiry may complete late; subsequent read 404 fails locally with Metadata unchanged; Local and GCS follow same transitions.

| Local capacity
| Confirmed versus unconfirmed replica; active Put/Get/Cat/upload/download RLocks; insufficient space and shared-temp exhaustion return capacity errors; periodic Capacity GC consults Repository settings and reclaims eligible space only when needed; no preflight reservation guarantee; Local mode never evicts authority; last-known-good copy remains.

| Read-through/Get/Cat
| Local hit never calls GCS; Local miss complete download/hash mismatch/short read/cancel/retry; duplicate downloads; GC racing download/publication; no partial object becomes visible; Get destination remains old on fetch failure and atomically changes on success; read 404 is local and does not mutate Metadata; Cat writer failure may leave a response prefix.

| GC/Doctor
| Local invalid raw names and malformed files; referenced missing/corrupt objects; valid unreferenced Local object; GCS orphan; shared content ID referenced by several entries; active upload/download/read RLocks and GC WLock; GCS generation changed before delete; delete timeout/ambiguous result/retry; Doctor behavior per mode and pending delivery. GC is idempotently retryable.

| Metadata adapter conformance
| SQLite migration and golden compatibility; atomic expected-revision Create/Update/Move/Delete; sibling uniqueness; parent kind/existence; ancestry cycle; consistent ReferencedObjectIDs under GC; Firestore conformance and cross-host Coordinator test only if that deployment is approved.

| Backup/restore and CLI
| restore preserves format, delivery mode, content-ID queue/verification state and reference IDs; GCS/local missing replica diagnosis; delayed-delete 404 remains local and leaves Metadata unchanged; capacity error mapping; transfer visibility/output stability; no credentials or bucket secrets persisted to logs.
|===

== Validation during this design task

* `DESIGN_IR_REPO=/home/kusogaki/workspaces/design-ir ./tools/validate-design-ir.sh` — `VALID`.
* `go test ./...` — passed.
* `git diff --check` — passed. No tests or Go files were modified.
* GCS API references were checked against current primary Google Cloud documentation linked above; no library-specific implementation choice is assumed.
