# Spec: Studio metadata controls

Module: `studio-metadata-controls`
Depends on: all music metadata capability modules

## Objective

Expose the normalized metadata pipeline in Poisq Studio so an operator can inspect provenance, lock fields, change provider policy, start refresh/analysis, stage NFO and ReplayGain changes, manage role-specific artwork, inspect similar artists, and see writeback recovery state without bypassing Staging.

## Requirements

- Return one bounded track detail payload containing the normalized document, media facts, loudness facts, and track projection.
- Lock or unlock only known metadata fields and persist the lock through the normalized repository.
- Read and atomically replace the validated provider policy.
- Expose explicit actions for track refresh, NFO import/export, loudness analysis, ReplayGain staging, role artwork staging, and ListenBrainz similar-artist refresh.
- Return persisted artwork and similar artists by explicit scope/MBID.
- Expose writeback recovery state without leaking backup contents or arbitrary paths.
- Preserve the existing same-origin/CSRF, request-size, path guard, timeout, and Staging approval boundaries.
- Add a responsive metadata workspace to the existing Studio: provenance/locks, extended fields, provider policy, technical audio facts, NFO/ReplayGain actions, artwork roles, and similar artists.
- UI actions must report progress/errors, remain keyboard accessible, and not hide the existing library/staging workflows.

## Commands

```sh
gofmt -w internal/admin/api_metadata.go internal/admin/server.go cmd/proxy/main.go
go test -count=1 ./internal/admin ./internal/db ./internal/metadata ./internal/tagger
go test -race -count=1 ./internal/admin ./internal/db ./internal/metadata ./internal/tagger
go test -count=1 ./...
go vet ./...
go build ./cmd/proxy
git diff --check
```

## Success Criteria

1. API contract tests cover detail, locks, policy validation, guarded actions, artwork, similar artists, and recovery state.
2. All mutations use existing validated services and create Staging proposals where media files or sidecars would change.
3. Studio exposes the controls on desktop and mobile with visible status and no horizontal overflow.
4. Full regression, race, vet, build, browser, and diff checks pass.
