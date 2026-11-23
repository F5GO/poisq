# Implementation Plan: `local-metadata-import`

Source of truth: [`SPEC-local-metadata-import.md`](../SPEC-local-metadata-import.md).

## Approach

Add a small `AudioProber` boundary with a bounded ffprobe implementation, persist file fingerprints/technical facts in a versioned table, then attach a reconciliation importer to the existing Navidrome inventory scanner. Local tags overlay only populated, unlocked fields; failures retain the last valid document.

## Dependency Graph

```text
media-info schema/repository
          ↓
bounded ffprobe DTO + mapper
          ↓
document overlay + lock handling
          ↓
fingerprint-aware importer
          ↓
bounded scanner integration + wiring
          ↓
full regression verification
```

## Components

1. Add `track_media_info`, repository operations, reset/cascade behavior, and migration tests.
2. Implement fixed-argument ffprobe execution with timeout/output/error bounds and explicit JSON DTOs.
3. Map tag aliases, multi-values, dates/numbers/credits, and typed IDs into `musicmeta.Document`.
4. Reconcile local values over the stored document while preserving missing values and locks.
5. Skip current fingerprints, isolate failures, and run changed tracks with bounded concurrency.
6. Attach the importer to `LibraryScanner` and proxy construction without changing existing sync responses.

## Risks and Mitigations

| Risk | Mitigation |
|---|---|
| ffprobe hangs or floods output | context timeout and bounded writers |
| malformed one-file tags abort sync | per-track result/error isolation |
| artist punctuation splits incorrectly | only NUL/unit-separator/semicolon split |
| locked/manual data overwritten | overlay checks persisted field state |
| crash marks incomplete import current | ready fingerprint written only after document success |
| large library process storm | fixed worker pool, fingerprint skip |

## Verification

1. `go test -count=1 ./internal/metadata ./internal/db ./internal/musicmeta`
2. `go test -race -count=1 ./internal/metadata ./internal/db`
3. `go test -count=1 ./...`
4. `go vet ./...`
5. `go build ./cmd/proxy`
