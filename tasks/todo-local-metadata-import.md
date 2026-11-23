# Tasks: `local-metadata-import`

- [x] Task 1: Add media-info schema and repository.
  - Acceptance: fingerprint, technical facts, embedded flags, status/error, reset, and cascade behavior persist idempotently.
  - Verify: `go test -count=1 -run 'TestMediaInfo' ./internal/db`
  - Files: `internal/db/metadata_schema.go`, `internal/db/media_info.go`, `internal/db/media_info_test.go`, `internal/db/poisq_db.go`

- [x] Task 2: Implement bounded ffprobe execution and explicit decoding.
  - Acceptance: fixed executable/args, 10s timeout, 4 MiB stdout and 2 KiB stderr bounds, cancellation, missing executable, malformed and oversized output errors.
  - Verify: `go test -count=1 -run 'TestFFProbe' ./internal/metadata`
  - Files: `internal/metadata/audio_probe.go`, `internal/metadata/audio_probe_test.go`

- [x] Task 3: Map embedded tags into normalized metadata.
  - Acceptance: aliases, credits, genres, partial dates, track/disc totals, compilation and all typed IDs map deterministically without ambiguous splitting.
  - Verify: `go test -count=1 -run 'TestMapAudioTags' ./internal/metadata`
  - Files: `internal/metadata/audio_probe.go`, `internal/metadata/audio_probe_test.go`

- [x] Task 4: Implement fingerprint-aware local reconciliation.
  - Acceptance: unchanged files skip; changed files probe; non-empty local fields overlay unlocked fields; fallback and locked values remain; failure preserves last valid data.
  - Verify: `go test -count=1 -run 'TestLocalImporter' ./internal/metadata`
  - Files: `internal/metadata/local_importer.go`, `internal/metadata/local_importer_test.go`

- [x] Task 5: Integrate bounded import with library synchronization.
  - Acceptance: scanner imports changed primary tracks with at most configured workers; errors do not fail inventory sync; importer is optional and wired in production.
  - Verify: `go test -race -count=1 ./internal/metadata`
  - Files: `internal/metadata/folder_scanner.go`, `internal/metadata/local_importer.go`, `cmd/proxy/main.go`

- [x] Task 6: Complete regression and security verification.
  - Acceptance: focused/race/full/vet/build pass; no audio/Navidrome writes, new dependency, GPL code, or unrelated edits.
  - Verify: commands in the module spec.
  - Files: module files above
