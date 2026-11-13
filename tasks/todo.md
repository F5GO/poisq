# Tasks: `bio-store`

- [x] Task 1: Define bounded domain types and extend the existing SQLite schema.
  - Acceptance:
    - Stable enums and structs cover artist resolution, source candidate, selected automatic result, manual override, and effective result.
    - Validation rejects empty/oversized identifiers and text, malformed evidence JSON, invalid URL schemes, and non-finite/out-of-range confidence.
    - `OpenPoisqDatabase` creates the new normalized tables and required indexes without changing existing rows.
    - Opening the same database twice is idempotent.
  - Verify: `go test -count=1 ./internal/db ./internal/artistbio`
  - Files: `internal/db/poisq_db.go`, `internal/artistbio/types.go`, `internal/artistbio/store_test.go`

- [x] Task 2: Implement candidate upsert/read and atomic automatic selection.
  - Acceptance:
    - Candidate upsert is parameterized and unique by artist/provider/provider-artist identity.
    - Refreshing a candidate updates normalized metadata without creating duplicates.
    - Selection succeeds only for a candidate owned by the requested artist.
    - Selection and presentation state update atomically.
    - SQL metacharacters in all text fields are stored as ordinary data.
  - Verify: `go test -count=1 -run 'TestStore_(Candidate|Select)' ./internal/artistbio`
  - Files: `internal/artistbio/store.go`, `internal/artistbio/types.go`, `internal/artistbio/store_test.go`

- [x] Task 3: Implement optional manual override and indexed effective reads.
  - Acceptance:
    - Empty manual text never masks an automatic result.
    - Non-empty manual text takes precedence without deleting or modifying automatic data.
    - Clearing an override immediately reveals the latest automatic result.
    - Effective reads use one bounded local query and return no partial state under concurrent reads/writes.
    - The query plan uses the artist primary/unique key and does not scan candidate text rows.
  - Verify: `go test -count=1 -run 'TestStore_(Manual|Effective|Concurrent|QueryPlan)' ./internal/artistbio`
  - Files: `internal/artistbio/store.go`, `internal/artistbio/types.go`, `internal/artistbio/store_test.go`

- [x] Task 4: Integrate reset behavior and complete performance/security verification.
  - Acceptance:
    - Existing reset functionality clears biography state in dependency-safe order and preserves unrelated settings/API-key behavior.
    - Cancellation and database errors are propagated without leaking biography bodies or secrets.
    - Focused tests, complete tests, vet, and build pass.
    - Final diff contains no unrelated user-file modifications, unsafe SQL, unbounded external fields, or unnecessary abstractions.
  - Verify:
    - `gofmt -w internal/db/poisq_db.go internal/artistbio/*.go`
    - `go test -count=1 ./internal/db ./internal/artistbio`
    - `go test -count=1 ./...`
    - `go vet ./...`
    - `go build ./cmd/proxy`
  - Files: `internal/db/poisq_db.go`, `internal/db/db_test.go`, `internal/artistbio/store.go`, `internal/artistbio/store_test.go`
