= Design IR Migration Coverage Record

== Canonical model

[design.ir.yaml](design.ir.yaml) is the only design authority and the direct machine- and agent-readable design artifact. This file is a one-time migration audit record, not an operating guide for maintaining document projections.

The model uses Design IR v1alpha1 schema repository revision `455ed318b7bc409d3e1da320620ef92d457042b9`, schema SHA-256 `9b109dbc7ac09b32d3dbe2e233eed7da1378bc912d0e84c479c3ed0bed9f7103`, and one document-scoped stable ID namespace. One file is sufficient for the current model; no multi-file linking convention is needed. Most nodes are `system_design`; `system_requirement` is used for explicit normative obligations. No stakeholder input or upstream authority is fabricated.

== Source coverage

|===
| Retired source | Covered meaning in the IR

| `architecture.adoc`
| Purpose/scope: `DES-REPOSITORY-BOUNDARIES`; component responsibilities: `DES-CLI-BOUNDARY`, `DES-APPLICATION-BOUNDARY`, `DES-DEPENDENCY-DIRECTION`, `DES-CONSTRUCTION`; store semantics: `DES-METADATA-MODEL`, `DES-OBJECT-MODEL`, `DES-OBJECT-BACKEND-NEUTRALITY`, `DES-OBJECT-CONSISTENCY`; operations and failure: `DES-READ-SEMANTICS`, `DES-FAILURE-BOUNDARIES`, `DES-DOCTOR`, `DES-ERROR-MODEL`; path contract: `DES-PATH-SEMANTICS`; scaffolding boundary: `DES-SCAFFOLDING-BOUNDARY`; normative obligations are linked requirement nodes.

| `invariants.adoc`
| Authority, committed-only state, Entry identity, namespace uniqueness, root and explicit directories, Object Commit/order, revisions, Put/GC and Read/GC exclusion, replacement, subtree mutation, no-resume, Lease limits, derived paths and timestamps: all represented by `REQ-*` and `DES-*` nodes, linked by `REL-*` relations.

| `repository-model.adoc`
| Store separation/SSOT, Entry and directory model, Put/replacement, Read/GC, Move/Remove: `DES-REPOSITORY-BOUNDARIES`, `DES-METADATA-MODEL`, `DES-OBJECT-MODEL`, `DES-EXPLICIT-DIRECTORY-MODEL`, `DES-READ-SEMANTICS`, `DES-FAILURE-BOUNDARIES` and the linked requirements.

| `metadata-model.adoc`
| EntryID/parent-child identity, namespace constraints, committed-only state, revision, Unix-nanosecond timestamp representation, path-cache authority: `REQ-ENTRY-ID`, `REQ-NAMESPACE-UNIQUENESS`, `REQ-META-COMMITTED-ONLY`, `REQ-REVISION`, `DES-METADATA-MODEL`, `DES-REVISION-SEMANTICS`, `DES-TIMESTAMP-REPRESENTATION`; cache strategy is `OPEN-PATH-CACHE-UPDATE`.

| `object-store.adoc`
| CAS authority, atomic Commit, post-Commit Open/Exists, backend-private layout, verification/enumeration/deletion, unified Resource Lease state transitions and local/GCS status: `REQ-OBJECT-SSOT`, `REQ-OBJECT-COMMIT`, `DES-OBJECT-MODEL`, `DES-OBJECT-BACKEND-NEUTRALITY`, `DES-OBJECT-CONSISTENCY`, `DES-LEASE`, `DES-IDENTITY-TARGET`, `DES-PHASE6-GCS`; current GCS state is `IMP-CURRENT-GCS-STUB` with `OBS-GCS-PLACEHOLDER`.

| `consistency-model.adoc`
| Read consistency versus availability versus GC safety, Put ordering/exclusion, replacement, interruption outcomes and backend conformance: `REQ-OBJECT-COMMIT`, `REQ-METADATA-ORDER`, `REQ-PUT-GC-EXCLUSION`, `REQ-READ-GC-EXCLUSION`, `REQ-REPLACEMENT`, `REQ-NO-RESUME`, `DES-READ-SEMANTICS`, `DES-FAILURE-BOUNDARIES`, `DES-OBJECT-CONSISTENCY`; Read behavior after revision change is `OPEN-READ-REVISION-CHANGE`.

| `state-machine.adoc`
| Committed Entry lifecycle/revisions, Object lifecycle, failure boundaries, replacement and unified Resource Lease lifecycle: corresponding `REQ-*` and `DES-*` nodes; interruption is not resumed.

| `roadmap.adoc`
| Phase 0–7 status/goals, phase ordering, exit criteria, cross-cutting validation and deferred optimizations: `DES-PHASE0-SCAFFOLDING`, `DES-PHASE1-FORMAT`, `DES-PHASE2-CAS`, `DES-PHASE3-EXCLUSION`, `DES-PHASE4-CLEANUP-EXPORT`, `DES-PHASE5-DIRECTORIES`, `DES-PHASE6-GCS`, `DES-PHASE7-SYNC`, `DES-CROSS-CUTTING-VALIDATION`, `DES-DEFERRED-OPTIMIZATIONS`. The stable directory contract is separately represented by `DES-EXPLICIT-DIRECTORY-MODEL`. Phase order is also represented by `depends_on` relations. Exit criteria, status, phase numbers and checklist values are retained in roadmap-node attributes.

| `glossary.adoc`
| Entry, Directory, Object, Commit, Revision, Timestamp, Lease, Canonical Path, Derived Data, both Stores, Repository, and prohibited Operation Record: represented by the corresponding semantic nodes and requirements; no runtime Operation Record is introduced.
|===

== Implementation observations

Observed implementation is separate from design and pinned to source revision `d3a1db2faf961a91908fae5e71620449bc947385`:

* `IMP-CURRENT-SHA1` / `OBS-SHA1-IMPLEMENTATION-GAP`: implementation uses unnamespaced SHA-1 while Phase 1 targets SHA-256 namespaced identity.
* `IMP-CURRENT-UNLOCKED-REPOSITORY` / `OBS-REPOSITORY-EXCLUSION-GAP`: inspected Put, Read, and GC lack process-wide exclusion.
* `IMP-CURRENT-GCS-STUB` / `OBS-GCS-PLACEHOLDER`: GCS methods are unimplemented.
* `IMP-CURRENT-GET-TRUNCATES` / `OBS-GET-ATOMIC-EXPORT-GAP`: Get truncates and writes the destination directly.

These are observations, not amendments to design authority. Implementation artifacts are pinned in the observation records. The prior local design-document edits are not treated as implementation evidence.

Implementation observations have no `realizes` or `satisfies` relations to target design: those relations would falsely imply success. `observations` and their `artifact_refs` record what was inspected and which design claims were compared.

== Relationship and node review

`depends_on` is retained only where the source design contract or roadmap phase semantically relies on the target authority or prerequisite. Administrative proximity and complete graph connectivity are not treated as reasons to add relations. The Phase 5 directory design contract (`DES-EXPLICIT-DIRECTORY-MODEL`) is separate from its roadmap goals and exit criteria (`DES-PHASE5-DIRECTORIES`); the latter depends on the former and on Phase 4.

== Open decisions

At the time of migration the IR retained four open items: `OPEN-SHA1-FORMAT-TRANSITION`, `OPEN-READ-REVISION-CHANGE`, `OPEN-PATH-CACHE-UPDATE`, and `OPEN-DELETE-RETENTION`. Later implementation decisions are recorded in the canonical IR; this migration record remains a snapshot of the original source coverage.

No representation gaps remain. AsciiDoc headings/navigation were presentation-only and are retired. Roadmap sequence and criteria are present in node attributes and phase dependency relations; validator enforcement of those attributes would be a separate tooling enhancement, not missing semantic representation.


== Later storage design audit

The content-ID-based Local/GCS delivery, single Resource Lease model, persistent transfer queue, capacity/orphan GC, metadata portability, current-source mapping, migration order, and test matrix are recorded in [storage-delivery-audit.md](storage-delivery-audit.md). The Design IR remains the sole semantic authority; the audit file is review evidence and a migration/test plan.
