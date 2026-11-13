# Implementation Plan: `bio-store`

Source of truth: [`SPEC-bio-store.md`](../SPEC-bio-store.md).

## Approach

Extend the existing `poisq.db` schema and add one focused `internal/artistbio` package. The package owns validation and persistence operations; database opening, WAL configuration, and lifecycle remain in `internal/db`.

The request-path contract is a single indexed `EffectiveBiography` lookup. Candidate ingestion and selection writes are off the request path and may use short transactions where an invariant spans multiple rows.

## Dependency Graph

```text
schema + domain types
        ↓
candidate persistence
        ↓
automatic selection
        ↓
manual override + effective read
        ↓
reset integration + full verification
```

## Components

### 1. Schema and bounded domain types

- Extend `initPoisqSchema` rather than introducing a migration framework.
- Add normalized tables for artist resolution state, biography candidates, and selected presentation state.
- Add only indexes required by provider upsert, refresh scans, and the effective read.
- Define validated enums and bounded fields in `internal/artistbio/types.go`.
- Keep large biography text in candidate/presentation rows only where required; do not store raw HTTP responses.

Checkpoint: opening a new or existing database is idempotent, and invalid domain values fail before SQL execution.

### 2. Candidate persistence and automatic selection

- Add a concrete `Store` backed by the existing `*sql.DB`.
- Upsert candidates through parameterized SQL using the stable provider identity.
- Read candidates without exposing mutable shared state.
- Select an automatic candidate atomically and retain its provenance.
- Reject selection of a candidate belonging to another artist.

Checkpoint: candidate refreshes are idempotent and automatic selection cannot cross artist boundaries.

### 3. Optional manual override and effective read

- Add bounded set/clear operations for manual text and optional source URL.
- Keep manual and automatic values independent.
- Implement one indexed effective read with precedence `manual non-empty → automatic → none`.
- Ensure clearing manual content immediately reveals the current automatic result.

Checkpoint: no external I/O occurs, and the effective result is complete or absent—never partially updated.

### 4. Reset, performance, and security verification

- Extend the existing reset table list; do not duplicate reset machinery.
- Add tests for SQL metacharacters, invalid URL schemes, oversized payloads, non-finite confidence values, cancellation, concurrent readers, and atomic selection.
- Verify query plans for request-path reads and add a benchmark only if application-side processing becomes non-trivial.
- Run formatting, focused tests, full tests, vet, and build.

Checkpoint: all verification commands in the spec pass and unrelated dirty files remain untouched.

## Implementation Order

The four components are sequential because each consumes the contract of the previous component. Test cases may be prepared alongside their corresponding implementation, but no production operation is added without its focused tests.

## Reuse Decisions

- Reuse `internal/db.OpenPoisqDatabase` and `initPoisqSchema`.
- Reuse the existing SQLite driver, WAL settings, timestamps, and error-wrapping style.
- Reuse `ResetDatabase` by extending its explicit table list.
- Do not introduce a repository interface, migration framework, cache layer, or generic validation framework.

## Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Schema expansion slows startup | Use idempotent DDL and only necessary indexes; no table rewrites |
| External text exhausts storage or memory | Enforce byte limits before persistence and avoid raw responses |
| Manual text is overwritten by refresh | Separate manual columns/state from automatic selection operations |
| Wrong candidate selected across artists | Validate ownership inside the selection transaction |
| SQL injection | Fixed statements with parameters only; no external SQL identifiers |
| Partial state under concurrency | Short transactions for multi-row invariants and WAL-backed indexed reads |
| Reset leaves orphaned rows | Foreign keys plus explicit reset tests and dependency-safe deletion order |
| New abstraction duplicates existing code | Keep database lifecycle in `internal/db` and add only domain-specific operations |

## Verification Checkpoints

1. Schema/types: `go test -count=1 ./internal/db ./internal/artistbio`.
2. Store operations: focused tests including failure and concurrency cases.
3. Query path: `EXPLAIN QUERY PLAN` assertion or equivalent indexed-plan check.
4. Module completion: `go test -count=1 ./...`, `go vet ./...`, `go build ./cmd/proxy`.
5. Handoff audit: review diff for secrets, unsafe SQL, unbounded inputs, races, error leakage, and unrelated file changes.

## Out of Scope

- Network providers and HTTP response storage.
- AI identity resolution.
- Language detection and translation.
- Proxy interception and response rewriting.
- Studio HTTP handlers and authorization.
