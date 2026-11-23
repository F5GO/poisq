# Implementation Plan: `metadata-core`

Source of truth: [`SPEC-metadata-core.md`](../SPEC-metadata-core.md).

## Approach

Add a dependency-free `internal/musicmeta` domain package, then introduce a small versioned migration runner and an atomic metadata repository in `internal/db`. Keep the existing `tracks` table and APIs operational throughout the transition by dual-reading and dual-writing only the legacy-compatible subset.

No audio files, artwork, Navidrome rows, or HTTP contracts are mutated by this module.

## Dependency Graph

```text
domain enums + value types
            ↓
validation + legacy document adapter
            ↓
versioned additive schema migrations
            ↓
atomic normalized metadata repository
            ↓
TrackRepository compatibility integration
            ↓
tagger ID contract compatibility + full verification
```

## Components

### 1. Domain types and validation

- Define fields, sources, credit roles, partial dates, typed external IDs, field states, and the track document.
- Canonicalize inputs with explicit byte/count bounds and stable duplicate rules.
- Return defensive copies and deterministic ordering.
- Keep the package independent of `internal/db` and `internal/tagger` to avoid cycles.

Checkpoint: adversarial values fail before SQL; valid values canonicalize deterministically.

### 2. Loss-aware legacy adapters

- Convert legacy scalar track data into a normalized document with `legacy` field provenance.
- Derive display strings and legacy year/number fields from normalized values.
- Preserve normalized-only fields when a legacy caller updates the subset it understands.
- Keep adapters expressed through small interfaces/value structs so `musicmeta` does not import database or tagger packages.

Checkpoint: legacy round trips do not erase disc, date precision, credits, genres, IDs, provenance, or locks.

### 3. Versioned additive migrations

- Bootstrap the existing schema first, then create `schema_migrations` and apply numbered migrations transactionally.
- Add extension, collection, external-ID, and field-state tables with foreign keys and required indexes.
- Make migration state explicit and idempotent.
- Extend reset behavior in dependency-safe order.

Checkpoint: new, legacy-shaped, and reopened databases converge on one schema without data changes.

### 4. Atomic normalized repository

- Read legacy-only rows as synthesized normalized documents.
- Persist scalar extension data, ordered collections, IDs, states, and compatible legacy columns in one transaction.
- Implement merge-on-legacy-write so existing repository operations cannot delete richer data.
- Validate scope, IDs, fields, and collections before writes.

Checkpoint: readers observe either the old complete document or the new complete document, never a partial collection replacement.

### 5. Compatibility integration and verification

- Add normalized repository access beside `TrackRepository` without changing current JSON shapes.
- Extend `CanonicalTagSet` only with optional semantically distinct IDs needed for compatibility; retain existing fields.
- Add regression tests for current Studio/staging/tagger behavior.
- Run focused, race, vet, and full test/build commands.

Checkpoint: all current callers compile unchanged and the corrected typed-ID contract is available to later modules.

## Implementation Order

The five components are sequential because persistence consumes validated domain contracts and compatibility integration consumes the repository. Tests are written with each component before or alongside production behavior.

## Reuse Decisions

- Reuse the current SQLite driver, WAL configuration, timestamps, and repository construction style.
- Reuse the existing `tracks` table as the compatibility projection.
- Reuse `ResetDatabase`; extend its table list rather than adding another reset path.
- Keep current tagger JSON names accepted.
- Do not add reflection-based validators, ORM code, generic repository frameworks, or third-party dependencies.

## Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Legacy startup applies half a migration | One transaction per numbered migration; version row inserted last in the same transaction |
| Existing writes erase richer metadata | Repository-level legacy merge tests and update-only-known-fields behavior |
| Multi-value credits collapse or reorder | Explicit position columns and deterministic validation tests |
| Recording and release-track IDs alias | Closed ID enum, distinct rows, separate adapter fields, targeted regression tests |
| Invalid JSON/API data exhausts memory | Validate byte/count limits before allocation-heavy transforms or SQL |
| Foreign-key order breaks reset | Dependency-ordered deletes and reset integration tests |
| New package creates import cycles | `musicmeta` owns pure values; db/tagger depend on it, never the reverse |
| Concurrent collection replacement leaks partial state | Single transaction and concurrent reader tests under WAL/race detector |

## Verification Checkpoints

1. Domain: `go test -count=1 ./internal/musicmeta`.
2. Schema: `go test -count=1 -run 'TestMetadata(Migration|Schema|Reset)' ./internal/db`.
3. Repository: `go test -race -count=1 ./internal/musicmeta ./internal/db`.
4. Compatibility: `go test -count=1 ./internal/tagger ./internal/admin ./internal/metadata`.
5. Completion: `go test -count=1 ./...`, `go vet ./...`, `go build ./cmd/proxy`.
6. Diff audit: no unrelated user files, destructive migration, copied GPL code, secrets, or unbounded inputs.

## Out of Scope

- Reading tags from audio files.
- Provider ranking or remote calls.
- NFO parsing or writing.
- LUFS execution and gain-tag writes.
- Studio UI changes.
- Audio/artwork writeback or transaction recovery.
