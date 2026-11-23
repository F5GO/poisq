# Tasks: `metadata-core`

- [x] Task 1: Define normalized music metadata value types and validation.
  - Acceptance:
    - Documents cover scalar fields, ordered multi-role credits, genres, partial dates, typed IDs, provenance, confidence, and locks.
    - Validation enforces every bound and registry in the spec, rejects malformed dates/IDs/UTF-8/control characters/non-finite confidence, and canonicalizes duplicates deterministically.
    - Returned documents cannot be mutated through aliased slices or maps.
  - Verify: `go test -count=1 ./internal/musicmeta`
  - Files: `internal/musicmeta/types.go`, `internal/musicmeta/validate.go`, `internal/musicmeta/types_test.go`, `internal/musicmeta/validate_test.go`

- [x] Task 2: Add loss-aware legacy adapters.
  - Acceptance:
    - Legacy artist/album-artist strings become ordered credits with legacy provenance.
    - Normalized display strings and integer year remain compatible with current TrackRecord behavior.
    - Applying a legacy subset preserves normalized-only fields and locks.
    - Recording and release-track IDs remain separate in every conversion.
  - Verify: `go test -count=1 -run 'TestLegacy' ./internal/musicmeta`
  - Files: `internal/musicmeta/legacy.go`, `internal/musicmeta/legacy_test.go`

- [x] Task 3: Introduce transactional versioned metadata migrations.
  - Acceptance:
    - New and production-shaped legacy databases receive `schema_migrations` and all normalized tables/indexes.
    - Each numbered migration is atomic and idempotent; a failing migration does not advance the version.
    - No existing row or column value changes during migration.
    - Reset deletes normalized rows in dependency-safe order and retains current API-key behavior.
  - Verify: `go test -count=1 -run 'TestMetadata(Migration|Schema|Reset)' ./internal/db`
  - Files: `internal/db/metadata_schema.go`, `internal/db/metadata_schema_test.go`, `internal/db/poisq_db.go`

- [x] Task 4: Implement atomic normalized metadata persistence.
  - Acceptance:
    - Full documents round-trip all scalars, ordered collections, IDs, and states.
    - Legacy-only rows synthesize valid normalized documents without eager rewrite.
    - Full replacement is one transaction; invalid input and injected SQL failure expose no partial update.
    - Deletes cascade; queries are parameterized and context-aware.
  - Verify: `go test -race -count=1 -run 'TestMetadataRepository' ./internal/db`
  - Files: `internal/db/metadata_repository.go`, `internal/db/metadata_repository_test.go`

- [x] Task 5: Integrate legacy TrackRepository updates without metadata loss.
  - Acceptance:
    - Existing upsert and metadata-update paths continue to populate current columns and JSON responses.
    - Legacy updates preserve normalized dates, disc data, credits beyond display artist roles, genres, external IDs, provenance, and locks.
    - Track deletion and deduplication do not leave normalized orphans.
  - Verify: `go test -race -count=1 ./internal/db`
  - Files: `internal/db/repo_tracks.go`, `internal/db/repo_tracks_test.go`, `internal/db/metadata_repository.go`

- [x] Task 6: Correct the tagger compatibility contract for typed MusicBrainz IDs.
  - Acceptance:
    - Existing CanonicalTagSet fields and JSON remain accepted.
    - Recording maps only to `MUSICBRAINZ_TRACKID`; release track maps only to `MUSICBRAINZ_RELEASETRACKID`; release group has its own tag.
    - Undo clears only IDs introduced by the applied proposal.
    - No audio file is rewritten as part of metadata-core tests.
  - Verify: `go test -count=1 ./internal/tagger ./internal/musicmeta`
  - Files: `internal/tagger/picard_mapper.go`, `internal/tagger/tagger_test.go`, `internal/musicmeta/legacy.go`

- [x] Task 7: Complete compatibility, security, and regression verification.
  - Acceptance:
    - Current admin/staging JSON contracts remain unchanged.
    - Focused tests, race tests, full tests, vet, and proxy build pass.
    - Final diff contains no copied GPL source, secrets, destructive migration, unrelated user changes, or new runtime dependencies.
  - Verify:
    - `gofmt -w internal/musicmeta/*.go internal/db/metadata_*.go internal/db/poisq_db.go internal/db/repo_tracks.go internal/tagger/*.go`
    - `go test -count=1 ./internal/musicmeta ./internal/db ./internal/tagger ./internal/admin ./internal/metadata`
    - `go test -race -count=1 ./internal/musicmeta ./internal/db`
    - `go test -count=1 ./...`
    - `go vet ./...`
    - `go build ./cmd/proxy`
  - Files: all metadata-core files above; tests only where regression coverage is missing
