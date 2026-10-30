= Storage Delivery / Lease / GC Design Audit

== Authority and scope

`docs/design.ir.yaml` is the sole semantic design authority. This file is an audit record, source mapping, migration sequence, and test plan; it does not override or add requirements to the IR. No Go implementation was changed for this audit.

Audit baseline: `main` at `b4dba45ae202d995c973af01154c08108e98ce1c`. The checkout has no `README.md` and no `docs/index.adoc`, although `GEMINI.md` names `docs/index.adoc` in its reading order. `docs/design-ir-migration.md` identifies `docs/design.ir.yaml` as the direct design authority. No replacement AsciiDoc index was created.

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
* GCS mode commits a complete Local Object, then atomically commits the Entry and a content-ID transfer-queue item through MetadataStore. SQLite stores the queue in the existing Metadata database and shares the Entry transaction; a Firestore candidate must provide the same atomic semantic API. The Entry is immediately visible, and transfer follows asynchronously. Before confirmation Local may be the only known-good physical copy; physical loss at that time may lose the content.
* GCS uploads use the final key `objects/{content_id}` through resumable upload after hashing. No temporary GCS Object or copy is introduced. Local and GCS publication are no-replace; existing content must be checked before it counts as idempotent success.
* The persistent SQLite transfer queue is content-ID keyed. Restart rediscovery/retry and verified completion are required; queue state is neither an Entry Pending state nor a resumed Put operation. Queue field/state/retry policy details remain open.
* Get/Cat prefer Local. On a miss, hold RLocks, download to shared repository temp, validate the full content ID, then publish Local without replacement before consuming the Object. Direct GCS-to-Get streaming is unnecessary; progressive streaming remains a future option. Get keeps its existing atomic export destination behavior.
* A dedicated resource-lock area in the SQLite coordination database is the only GC protection mechanism. Resource-scoped expiring RWLocks protect the same content IDs and temporary IDs: Put/Get/upload/download hold RLock; GC physical deletion holds WLock. SQLite transactions atomically manage lock acquisition/update/release. Long I/O does not hold Repository-wide exclusive coordination. Fencing of a stale WLock deletion after expiry is still open. The current operation-scoped Put staging Lease remains a Commit/cleanup fence, not a second GC protection mechanism.
* Capacity GC is distinct from invalid/orphan reconciliation. Capacity GC cannot evict Local authority in Local mode, unconfirmed GCS-mode content, or leased resources; it may evict confirmed Local cache content. Failed safe reclamation returns a capacity error. The Repository does not delete the last known-good physical copy. Orphan reconciliation also covers GCS and uses generation-conditional deletion.
* MetadataStore remains the semantic API; SQLite remains the current implementation. Firestore is a candidate only if it can meet the same atomic operation contracts without SQL/SQLite leakage. Metadata backups are an operator responsibility.

== Findings that block implementation decisions

The following are not silently resolved by the target model; each appears as an `OPEN-*` item in the IR. The user-selected RWLock protocol and SQLite-backed MetadataStore transfer queue are treated as decisions, not as open architecture choices.

. **Expired WLock fencing:** RLock/WLock resolves acquisition-versus-GC eligibility: normal accesses hold RLock and physical deletion requires WLock for the same resource ID. The remaining question is how an expired GC WLock holder is prevented from completing a delayed filesystem/GCS delete after another lock/access is granted. GCS generation preconditions protect object generations but do not alone fence a stale repository worker; local unlink has no native fencing token.
. **RWLock and Put staging order:** current Put creates the temp path before acquiring its operation-scoped Lease. The target resource RLock must protect that temporary ID, then bind protection to the content ID after hashing without a gap. The operation-scoped Lease continues to fence Commit/staging cleanup only; resource RWLocks alone protect from GC. The current API/schema do not model protected-resource references.
. **Lock expiry while I/O is in flight:** renewal failure must stop work, but an already accepted GCS resumable request may finish remotely. Specify whether a worker that lost its RLock may record a verified queue completion, how partial download cleanup works, and how stale operations are fenced. RLock/WLock TTL, heartbeat interval, and clock are not chosen.
. **Transfer queue details:** owner and atomicity are decided: the MetadataStore semantic contract owns a content-ID queue; SQLite stores it in the existing Metadata database and Put Entry mutation plus queue insertion use one SQLite transaction. A Firestore adapter must provide the equivalent atomic observable result through its backend-native transaction. Queue row fields, state transitions, retry/backoff, idempotent enqueue for shared content IDs, and verified completion update semantics remain to be specified. A crash after upload but before completion update must rediscover and safely retry/verify.
. **Remote verification:** GCS upload completion and a transport checksum do not by themselves define the required SHA-256 identity proof. Pre-existing objects and ambiguous upload completion need a selected verification rule before Local capacity GC may trust a receipt.
. **GCS generations and bucket policy:** no-replace upload and generation-conditional deletion are required. Bucket versioning, soft delete, retention, external overwrite/delete, bucket/prefix changes, and recreation of an object name affect whether a receipt proves a usable replica and are not specified.
. **Capacity admission:** Put spool and GCS download staging share repository temp. No capacity ceiling, free-space reserve, concurrent-work reservation, upload/download limit, eviction priority, or capacity error mapping exists. Concurrent operations can otherwise all pass a free-space check and exhaust the volume.
. **Doctor and response semantics:** current Doctor validates Local references and ignores valid orphans. In GCS mode it is not decided whether a missing Local cache with valid GCS is healthy, whether pending replication is a finding, or how remote corruption is reported. Cat cannot retract bytes already written if a Lease expires or the downstream writer fails.
. **Mode configuration and repository migration:** current config has no mode or bucket. It is undecided whether mode/bucket identity belongs in Repository Format or separate repository-owned config and how existing Local repositories are prefetched, rolled back, moved between buckets, or returned to Local mode.
. **Firestore and resource coordination:** current SQLite Coordinator and the accepted resource-lock store are local-path/process coordination. A Firestore Metadata adapter alone does not coordinate writers on other hosts. The deployment boundary, resource-lock authority, and consistent reference enumeration / GC contract for alternate Metadata backends must be decided. The queue API must remain backend-neutral and its atomic Entry-plus-enqueue contract must be provided by each adapter.
. **Backup/restore:** operator responsibility is accepted, but no procedure defines how Metadata, Repository Format, delivery mode, and the transfer queue form a recoverable backup set.

The GCS JSON API documents resumable insert with `ifGenerationMatch`, including `0` for no live destination object, and generation-matched deletion preconditions. These API mechanisms support the required no-replace/conditional-delete shape but do not decide this repository's verification, versioning, or external-mutation policy: [Objects: insert](https://docs.cloud.google.com/storage/docs/json_api/v1/objects/insert), [request preconditions](https://docs.cloud.google.com/storage/docs/request-preconditions), [Objects: delete](https://docs.cloud.google.com/storage/docs/json_api/v1/objects/delete).

== Implementation readiness

The architecture-level decisions requested for this revision are settled: GC protection uses only the resource-scoped expiring RWLock (RLock for Put/Get/upload/download, WLock for physical deletion), stored in a dedicated area of the SQLite coordination database; GCS transfer obligations use a content-ID queue owned by MetadataStore semantics and stored in the existing SQLite Metadata database. Put Entry mutation and queue enqueue share one SQLite transaction, while a future Firestore adapter must provide an equivalent backend-neutral atomic operation. The current operation-scoped Put staging Lease remains only a Commit/staging fence and is not a second GC protection mechanism.

Implementation is **not ready for physical GC deletion or end-to-end Phase 6** until the Open Items below specify expired-WLock fencing, Put temporary-ID to content-ID lock transition, queue transitions/retry policy, remote verification, and capacity/mode/diagnostic boundaries. Work on the MetadataStore conformance/API shape and queue schema migration can proceed as a separately reviewable design/implementation slice, but must not claim full GCS delivery or safe GC until those remaining contracts are resolved.

== Migration and implementation sequence

This is a design/migration order, not authorization to modify implementation in this task.

. **Resolve remaining contract details:** specify RWLock expiry/fencing and Put temp-to-content resource binding; queue fields/transitions/retry policy; remote verification; capacity admission; mode/bucket migration; Doctor semantics; and Firestore deployment/coordination boundary.
. **Specify backend-neutral Metadata conformance:** define atomic Put-and-enqueue and transfer-completion semantic operations, in addition to existing Entry semantics and consistent reference enumeration. SQLite implements them using the existing Metadata database and a shared transaction; do not expose SQL or transaction types. Define conformance tests before a Firestore adapter.
. **Add repository-owned delivery configuration and migrations:** preserve current Local-only repositories as Local mode unless an explicit migration is requested. Select Repository Format versus separate config. Define migration/rollback and backup before adding the queue schema.
. **Preserve and isolate Local mode:** retain current Local no-replace CAS and existing CLI behavior; add a test proving Local mode never constructs/calls GCS. Do not change local SHA-256 format in this workstream.
. **Implement durable GCS delivery obligation:** after Local publication, atomically commit the Entry and content-ID queue item in MetadataStore. Implement final-name resumable no-replace upload and verification; mark the queue item complete only after the chosen verification boundary. Retry precondition collisions by verifying the existing object.
. **Implement Lease/Coordinator resource protocol:** protect temporary paths and content IDs, bind Put spool to the known content ID without a gap, coordinate Lease acquisition with GC eligibility, and stop on renewal failure. Keep all long copy/network/response I/O outside Coordinator critical sections.
. **Implement Local-first GCS reads:** RLock cache/download resources, hydrate through shared temp, hash/verify before Local no-replace publication, then serve. Preserve Get destination atomicity; document Cat prefix-on-error behavior.
. **Split GC:** capacity eviction (confirmed GCS replicas only) and invalid/orphan reconciliation (Local invalid/unreferenced and GCS unreferenced). Add backend APIs for raw-invalid cleanup and remote generation-conditional deletion only after ownership contracts are approved.
. **Define Doctor/CLI/operations:** mode-aware diagnostics, capacity errors, transfer visibility, backup/restore procedures, and stable CLI exit/output behavior.
. **Compatibility gate:** run existing Local CAS, migration, atomic Get, Doctor, CLI and Metadata semantics tests unchanged where still normative; revise only tests whose prior whole-operation Coordinator timing is explicitly superseded by approved Lease semantics.

== Existing implementation to target mapping

|===
| Current source | Target responsibility or required change (design only)

| `internal/cli/root.go`, `internal/config/config.go`, `internal/app/init.go`
| Repository-owned mode/bucket selection and composition; exact persisted location and initialization/migration still open.

| `internal/repository/put.go`
| Preserve local spool/hash and Local publication before Metadata; add durable content-ID delivery discovery and async uploader only using the decided same-transaction Metadata queue contract.

| `internal/objectstore/store.go`, `internal/objectstore/local/stage.go`, `internal/objectstore/local/store.go`
| Keep Local no-replace CAS. Target interface needs complete verified hydrate/publish and separate capacity/orphan enumeration/deletion semantics; raw invalid deletion is currently absent.

| `internal/objectstore/gcs/store.go`
| Implement direct final-name resumable upload, Local-miss download, content verification, no-replace preconditions, object/generation listing and conditional deletion. Current adapter has no behavior to preserve.

| `internal/coordinator/sqlite/coordinator.go`, `internal/repository/coordinator.go`
| Retain current Put staging Lease as a Commit/cleanup fence. Implement resource RWLock R/W modes in a dedicated area of the SQLite coordination database, with SQLite-transaction atomicity, shared content/temp IDs, and stale-WLock fencing without long network I/O; do not add another GC protection mechanism. Local SQLite is not a multi-host Firestore coordinator.

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
| direct final key only; interrupted session; ambiguous completion; existing correct object; existing wrong/corrupt object; duplicate concurrent uploader; generation precondition conflict; checksum mismatch. No partial/final invalid object is accepted as confirmed.

| Lease/GC races
| RLock first excludes GC WLock; WLock first makes a new operation wait/recheck; Put temp-to-content binding has no gap; RLock/WLock expiry/renewal; renewal failure; expired WLock fencing against delayed unlink/GCS delete; multiple processes and lock modes.

| Local capacity
| Confirmed versus unconfirmed replica; active Put/Get/Cat/upload/download RLocks; insufficient space; shared temp exhaustion; simultaneous reservations; Local mode never evicts authority; last-known-good copy remains; capacity failure is classified and observable.

| Read-through/Get/Cat
| Local hit never calls GCS; Local miss complete download/hash mismatch/short read/cancel/retry; duplicate downloads; GC racing download/publication; no partial object becomes visible; Get destination remains old on fetch failure and atomically changes on success; Cat writer failure/lease expiry returns error with documented possible prefix.

| GC/Doctor
| Local invalid raw names and malformed files; referenced missing/corrupt objects; valid unreferenced Local object; GCS orphan; shared content ID referenced by several entries; active upload/download/read RLocks and GC WLock; GCS generation changed before delete; delete timeout/ambiguous result/retry; Doctor behavior per mode and pending delivery. GC is idempotently retryable.

| Metadata adapter conformance
| SQLite migration and golden compatibility; atomic expected-revision Create/Update/Move/Delete; sibling uniqueness; parent kind/existence; ancestry cycle; consistent ReferencedObjectIDs under GC; Firestore conformance and cross-host Coordinator test only if that deployment is approved.

| Backup/restore and CLI
| restore preserves format, delivery mode, content-ID queue/verification state and reference IDs; GCS/local missing replica diagnosis; capacity error mapping; transfer visibility/output stability; no credentials or bucket secrets persisted to logs.
|===

== Validation during this design task

* `DESIGN_IR_REPO=/home/kusogaki/workspaces/design-ir ./tools/validate-design-ir.sh` — `VALID` (0.625 s).
* `go test -count=1 ./...` — passed (2.787 s wall clock).
* `git diff --check` — passed. No tests or Go files were modified.
* GCS API references were checked against current primary Google Cloud documentation linked above; no library-specific implementation choice is assumed.
