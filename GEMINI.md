# go-nando: Workspace & Engineering Conventions (`GEMINI.md`)

This document defines the repository-specific engineering conventions for `go-nando`.

Architectural contracts belong under `docs/`.

This document defines:

* implementation workflow;
* engineering conventions;
* repository ownership boundaries;
* testing expectations;
* AI-agent operational guidance.

---

## 1. Reading Order

Before making any non-trivial change, read in the following order:

1. `GEMINI.md`
2. `docs/index.adoc`
3. Relevant documents under `docs/`
4. Source code

Do not infer repository architecture solely from the current implementation.

When implementation and documentation disagree, report the discrepancy rather than silently treating implementation as the new design.

---

## 2. Documentation Authority

`docs/` defines architectural contracts.

The Go implementation defines mechanisms.

When documentation and implementation disagree:

1. Describe the observed discrepancy.
2. Determine whether it appears to be:
   * an implementation defect;
   * incomplete documentation;
   * stale documentation;
   * or an unresolved design decision.
3. Do not silently rewrite the architecture to match the code.
4. Propose the smallest concrete resolution.
5. Preserve the human decision point when authority is ambiguous.

Design documents describe:

* ownership;
* invariants;
* compatibility;
* observable semantics.

Implementation describes:

* algorithms;
* package organization;
* runtime mechanics.

---

## 3. Core Engineering Principles

This section defines the core architectural and implementation contracts that govern the Repository and Metadata Store (`MetadataStore`) subsystem.

### 3.1. Correctness before Convenience

Do not add process-local mutexes or caches to hide persistence-layer concurrency issues. All correctness guarantees must remain valid across:
* multiple Store instances;
* multiple processes;
* and alternative database backend implementations.

Use backend-native transactional and conditional-write mechanisms, including physical unique constraints where available and appropriate.

### 3.2. Backend-Independent Store Semantics

The metadata store contract must not depend on SQLite-specific behaviors. SQLite transactions and UNIQUE constraints are implementation details, not API semantics. Design every operation so that an equivalent Firestore implementation remains completely possible under the same interface.

### 3.3. Validation Responsibility Boundaries

* **`ValidatingStore`:** Handles request-local, state-independent validations such as non-empty IDs, valid input enum values, revision lower bounds, and required field invariants.
* **Backend Store (e.g., SQLite `Store`):** Handles state-dependent validations (such as parent existence, parent directory kind, and namespace/ancestry cycle verification).
* **Mutation Atomicity:** State-dependent validation must execute within the exact same transaction block as the corresponding mutation to eliminate TOCTOU (Time-of-Check to Time-of-Use) race conditions. Do not move state-dependent checks outside of the transaction boundary.

### 3.4. Operation-Specific Invariant Validation

Implement dedicated, operation-specific private validation functions rather than building generic, reusable validation engines that blur invariant boundaries. Use explicit, named validation helpers:
* `validateCreateInvariants`
* `validateUpdateInvariants`
* `validateMoveInvariants`
* `validateDeleteInvariants` (if required)

### 3.5. Optimistic Concurrency & CAS Semantics

`Entry.Revision` represents the revision observed by the caller. Mutating operations (such as `Update` and `Move`) must:
1. Compare the requested revision against the persisted state.
2. Employ backend-native conditional writes using the requested revision as the expected version.
3. Return `ErrEntryConflict` without applying the mutation if a revision mismatch is detected.
4. Produce one new revision on success.

Do not silently reread the latest revision from the database to apply a stale caller request.

### 3.6. Entry-Based Mutation Contracts

Mutating APIs such as `Create`, `Update`, and `Move` use `Entry` values rather than introducing operation-specific mutation DTOs without a clear semantic benefit.

For partial operations such as `Move`:

* Explicitly document which fields serve as inputs (`ID`, `ParentID`, `Name`, and `Revision`).
* Ignore unrelated input fields completely.
* Use the persisted `Entry` fetched inside the transaction as the base of the returned value.
* Apply only the fields owned by the operation, preserving unrelated persisted fields such as `Kind`, `Size`, `ObjectID`, and `CreatedAt`.

Never let unrelated stale fields on the input argument overwrite persisted state or appear in the returned value.

### 3.7. Invariant-Focused Final-State Testing

Concurrency and transactional testing must focus on verifying end-state invariants rather than over-specifying transient SQLite locking error types. Assertions must verify:
* total count of successful mutations (e.g., exactly 1 success out of concurrent operations);
* final persisted state values and revision progression;
* parent/name namespace uniqueness;
* and the absence of hierarchical directory cycles.

### 3.8. Minimal Conceptual Overhead

Avoid introducing new structs, abstractions, helpers, or synchronization layers unless they measurably reduce semantic complexity. Prefer explicit, straightforward operation code over reusable abstractions that hide critical invariants.

### 3.9. Common Rejected Patterns

The following patterns are rejected by default and require explicit architectural approval:
* Store-local mutexes used to simulate persistent consistency.
* State-dependent validation executing outside the mutation transaction boundary.
* Generic invariant engines or shared validation pipelines that obscure Metadata Store mutation boundaries.
* Treating all database `UNIQUE` constraint errors as the same generic conflict.
* Returning caller-supplied stale fields after a partial mutation.
* Relying on SQLite-specific foreign keys as repository semantics.

---

## 4. Repository Constitution

The Repository is the sole owner of cross-store semantics.

The Repository coordinates:

* Metadata Store;
* Object Store.

Neither storage subsystem owns repository-wide correctness.

Repository responsibilities include:

* operation ordering;
* metadata visibility;
* cross-store consistency;
* recovery boundaries;
* repository semantics.

Whenever a responsibility could reasonably belong to either the Repository or an individual storage subsystem, prefer placing it in the Repository unless ownership clearly belongs to that subsystem.

Storage implementations must remain independently replaceable without changing observable repository behavior.

---

## 5. Core Invariants

Implementation must preserve the invariants defined under `docs/`.

In particular:

* Metadata Store owns the logical namespace.
* Object Store owns immutable content.
* Content-addressable objects are immutable after commit.
* Metadata never becomes visible before its referenced object is durable and verified.
* Repository operations preserve equivalent observable semantics regardless of storage backend.
* Failures must leave either:
  * a recoverable intermediate state; or
  * a previously valid state.
* Derived state must remain reconstructable from authoritative state.

Correctness takes priority over optimization.

---

## 6. State Authority

Every persistent or cached state must explicitly identify its role.

Typical roles include:

* source of truth;
* durable replica;
* cache;
* projection;
* index;
* journal;
* migration artifact;
* temporary working state.

Do not allow a store to ambiguously serve multiple authority roles.

If state is reconstructable, document the reconstruction source.

If state is authoritative, document its durability and compatibility contract.

---

## 7. Responsibility Boundaries

### CLI

**Owns:**

* argument parsing;
* dependency construction;
* process exit mapping;
* stream wiring.

**Must not:**

* execute repository logic;
* execute SQL directly;
* derive content identifiers;
* implement recovery policy.

### Application

**Owns:**

* use-case orchestration;
* repository operation policy;
* operation input/output models.

### Repository

**Owns:**

* ordering;
* consistency;
* coordination between stores;
* repository semantics.

### Metadata Store

**Owns:**

* metadata persistence;
* logical hierarchy;
* metadata revisions;
* metadata-specific derived state.

Metadata Store must not own object durability.

### Object Store

**Owns:**

* immutable content objects;
* object persistence;
* object verification;
* object enumeration.

Object Store must not own logical namespace behavior.

Responsibilities must not leak across these boundaries.

---

## 8. Observation and Interpretation

Keep observed facts distinct from derived meaning whenever that distinction is architecturally relevant.

* Do not store interpretation as if it were raw observation.
* Do not let presentation concerns mutate authoritative state.
* Prefer explicit derivation boundaries over caches that blur fact and meaning.
* If derived state is reconstructable, document whether it is a cache, projection, index, or source of truth.

---

## 9. Documentation Impact Assessment

Every non-trivial implementation change must be evaluated for documentation impact.

Update `docs/` when changing:

* architectural contracts;
* compatibility guarantees;
* repository semantics;
* data models;
* lifecycle rules;
* observable behavior.

Update `GEMINI.md` when changing:

* implementation workflow;
* testing conventions;
* review rules;
* AI-agent operational guidance.

Do not silently redefine repository architecture.

---

## 10. Go Development Workflow

Before considering work complete, run:

```shell
golangci-lint run ./...
go test ./...
```

Use the repository-owned `.golangci.yml`.

Do not enable additional linters without documenting the intended contract.

---

## 11. Testing

Repositories may classify tests by contract purpose:

* Ordinary Tests
* Golden Tests
* Migration Tests
* Semantics Tests

Golden tests protect compatibility contracts.

Golden tests must:
* use independently declared expected values;
* fail when compatibility changes;
* be updated only after compatibility impact has been reviewed.

Do not:
* derive golden expectations from production code;
* weaken compatibility tests simply because implementation changed;
* skip golden tests to make CI pass.

When changing persistent formats, compatibility boundaries, or externally observable semantics:
* determine whether versioning is required;
* add or update migration tests;
* document intentional compatibility breaks.

---

## 12. Error Handling

Ignoring an error is a design decision.

Do not use:

* `_ = ...`
* `//nolint:errcheck`

to silently discard operational failures.

Represent intentional non-fatal behavior through executable control flow.

Comments alone are not error handling.

---

## 13. Test Support Boundaries

Test-only behavior must remain outside production ownership boundaries.

* Shared test doubles belong in test-only packages.
* Production code must not import test-support packages.
* Test doubles should expose only the minimum required behavior.
* Do not add callbacks, flags, or hidden branches solely for testing.
* Prefer observing behavior externally through wrappers, decorators, fakes, or spies.

Reducing duplication is secondary to preserving ownership boundaries.

---

## 14. Review Principles

Prefer:

* explicit ownership;
* deterministic behavior;
* reconstructable derived state;
* small responsibility boundaries;
* correctness before convenience.

Avoid introducing coupling between Metadata Store and Object Store.

Question any implementation that weakens repository invariants for local convenience.

---

## 15. AI Agent Behavior

Do not:

* redesign repository architecture without approval;
* redefine repository invariants;
* weaken documented contracts;
* move responsibilities across architectural boundaries without justification.

Always preserve:

* repository invariants;
* ownership boundaries;
* observable semantics.

When uncertain:

1. report the ambiguity;
2. propose the smallest safe change;
3. preserve existing contracts until a human decision is made.

Once a design decision has been explicitly accepted, stop proposing alternatives.

Execute the agreed work first.

Further improvements may be proposed only after reporting completion.
