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

## 3. Repository Constitution

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

## 4. Core Invariants

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

## 5. State Authority

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

## 6. Responsibility Boundaries

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

## 7. Observation and Interpretation

Keep observed facts distinct from derived meaning whenever that distinction is architecturally relevant.

* Do not store interpretation as if it were raw observation.
* Do not let presentation concerns mutate authoritative state.
* Prefer explicit derivation boundaries over caches that blur fact and meaning.
* If derived state is reconstructable, document whether it is a cache, projection, index, or source of truth.

---

## 8. Documentation Impact Assessment

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

## 9. Go Development Workflow

Before considering work complete, run:

```shell
golangci-lint run ./...
go test ./...
```

Use the repository-owned `.golangci.yml`.

Do not enable additional linters without documenting the intended contract.

---

## 10. Testing

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

## 11. Error Handling

Ignoring an error is a design decision.

Do not use:

* `_ = ...`
* `//nolint:errcheck`

to silently discard operational failures.

Represent intentional non-fatal behavior through executable control flow.

Comments alone are not error handling.

---

## 12. Test Support Boundaries

Test-only behavior must remain outside production ownership boundaries.

* Shared test doubles belong in test-only packages.
* Production code must not import test-support packages.
* Test doubles should expose only the minimum required behavior.
* Do not add callbacks, flags, or hidden branches solely for testing.
* Prefer observing behavior externally through wrappers, decorators, fakes, or spies.

Reducing duplication is secondary to preserving ownership boundaries.

---

## 13. Review Principles

Prefer:

* explicit ownership;
* deterministic behavior;
* reconstructable derived state;
* small responsibility boundaries;
* correctness before convenience.

Avoid introducing coupling between Metadata Store and Object Store.

Question any implementation that weakens repository invariants for local convenience.

---

## 14. AI Agent Behavior

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
