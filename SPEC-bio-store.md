# Spec: `bio-store`

## Objective

Add a compact persistence layer for artist identity and biography enrichment to the existing `poisq.db`.

The store must:

- keep source candidates without presenting them as verified facts;
- keep one selected automatic Russian biography per Navidrome artist;
- keep an optional manual override separately, with manual text taking precedence only when present;
- retain provenance, detected language, confidence, timestamps, and refresh state;
- support fast reads on the `getArtistInfo2` request path without external I/O;
- avoid storing secrets and avoid unbounded or unsafe external payloads.

This module does not fetch sources, call AI services, translate text, expose Studio handlers, or modify proxy responses.

## Tech Stack

- Go 1.26 module `github.com/F5GO/poisq`.
- `database/sql` with the existing `modernc.org/sqlite` driver.
- Existing WAL-enabled `poisq.db` opened by `internal/db.OpenPoisqDatabase`.
- Existing repository conventions; no new runtime dependency for this module.

## Commands

```sh
# Format touched Go files
gofmt -w internal/db/poisq_db.go internal/artistbio/*.go

# Focused persistence tests
go test -count=1 ./internal/db ./internal/artistbio

# Complete Go test suite
go test -count=1 ./...

# Static analysis
go vet ./...

# Build the service
go build ./cmd/proxy
```

## Project Structure

```text
internal/db/poisq_db.go       Existing schema initialization; extended in place
internal/artistbio/store.go   Store API and SQLite implementation
internal/artistbio/types.go   Bounded persistence-domain types and status constants
internal/artistbio/store_test.go
                              In-memory/file-temporary SQLite tests
```

No duplicate database-opening layer or generic repository framework is introduced.

## Code Style

Use explicit domain operations and parameterized SQL. Normalize and validate before the write transaction.

```go
func (s *Store) PutCandidate(ctx context.Context, candidate Candidate) error {
	if err := candidate.Validate(); err != nil {
		return fmt.Errorf("invalid biography candidate: %w", err)
	}
	_, err := s.db.ExecContext(ctx, upsertCandidateSQL,
		candidate.ArtistID,
		candidate.Source,
		candidate.SourceArtistID,
		candidate.OriginalText,
		candidate.FetchedAt.Unix(),
	)
	return err
}
```

Rules:

- follow surrounding Go naming and error-wrapping conventions;
- do not create interfaces until a second implementation or an existing consumer requires one;
- use transactions only for multi-row invariants;
- use integer Unix timestamps consistently with the current schema;
- use stable string enums validated at the package boundary;
- keep request-path reads to one indexed query whenever possible.

## Testing Strategy

Unit and integration tests use a temporary SQLite database opened through the production initialization path.

Required coverage:

- schema initializes on a new database and remains idempotent;
- candidate upsert is unique by artist, source, and source artist identity;
- selected automatic biography can be read in one indexed lookup;
- an empty manual value does not override the automatic result;
- a non-empty manual value takes precedence and can be cleared without deleting the automatic result;
- oversized text, invalid source/status values, empty artist IDs, invalid URLs, and invalid confidence values are rejected;
- SQL metacharacters remain ordinary data;
- concurrent readers do not observe a partial selection update;
- reset behavior is explicit and covered so existing reset operations cannot leave inconsistent biography rows.

Performance check: the selected-biography lookup must not scan candidate text rows; verify its query uses the primary/unique artist key and keep a microbenchmark if the repository implementation adds non-trivial processing.

## Boundaries

- Always:
  - reuse `OpenPoisqDatabase` and the existing SQLite connection;
  - use parameterized SQL and bounded text/metadata lengths;
  - store source attribution and timestamps with automatic content;
  - preserve automatic data when a manual override is added or removed;
  - return copies/values that callers cannot mutate concurrently;
  - update reset behavior and tests when adding tables.
- Ask first:
  - destructive migration of an existing table;
  - storing full external HTTP responses instead of normalized fields;
  - adding a database or runtime dependency;
  - encrypting or storing credentials in biography tables.
- Never:
  - treat a fetched candidate as verified merely because it exists;
  - build SQL identifiers or clauses from external input;
  - log biography bodies, credentials, or raw AI prompts at normal log levels;
  - perform network or AI calls from store methods;
  - make manual input mandatory.

## Data Contract

The schema will represent three concepts without duplicating large text unnecessarily:

1. Artist resolution state keyed by the stable Navidrome artist ID.
2. Source candidates keyed by artist, provider, and provider artist ID, with normalized text, language metadata, match confidence, and provenance.
3. Selected presentation state keyed by artist, containing the chosen automatic candidate reference plus optional bounded manual text and source URL.

Text limits are enforced in Go before SQLite writes:

- biography text: 64 KiB maximum per candidate or manual override;
- source/provider identifiers: 128 bytes;
- external artist identifiers: 512 bytes;
- source URLs: 4 KiB and only `http` or `https`;
- JSON evidence: 32 KiB maximum and valid JSON;
- confidence: finite value in `[0, 1]`.

Deleting or clearing a manual override reveals the latest selected automatic result immediately. Candidate refreshes never overwrite manual fields.

## Success Criteria

- Opening an existing `poisq.db` adds the new schema without altering existing data.
- Reopening the database is idempotent.
- Callers can upsert candidates and atomically select one automatic biography.
- Callers can set and clear an optional manual override independently.
- The effective biography read is a single bounded, indexed local operation with no network access.
- Invalid and oversized external data is rejected before persistence.
- Reset behavior is defined and tested.
- Focused tests, full Go tests, `go vet`, and the service build pass.

## Open Questions

None. Provider-specific payloads, AI evidence generation, language detection, translation, delivery formatting, and Studio authorization belong to their respective modules.
