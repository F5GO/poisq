# Spec: media-enrichment

Module: `media-enrichment`
Depends on: `metadata-core`, `metadata-merge-policy`, `musicbrainz-enrichment`, `staging-writeback`

## Objective

Support role-aware album/artist artwork and optional ListenBrainz similar-artist references keyed exclusively by verified MusicBrainz artist IDs.

## Requirements

- Model artwork roles: album primary, artist primary, disc, logo, banner, and backdrop.
- Rank candidates by role-specific aspect ratio, minimum useful resolution, provider preference, and provider confidence; reject invalid dimensions/URLs and deterministic ties.
- Persist selected artwork facts by scope/id/role.
- Stage artwork downloads with role-specific safe filenames; approved writes use exact backup, verification, recovery, and Undo.
- Fetch similar artists from the official ListenBrainz `GET /1/lb-radio/artist/{artist_mbid}` endpoint with bounded query parameters, timeout, response size, candidate count, deduplication, and required User-Agent.
- Accept only strict MusicBrainz UUIDs and exclude the seed artist.
- Persist similar artist MBID, display name, score/listen evidence, and fetch timestamp; remote failure must retain prior valid results.

## Commands

```sh
gofmt -w internal/metadata/media_enrichment*.go internal/db/media_enrichment*.go internal/metadata/cover_fetcher.go internal/tagger/rollback.go
go test -count=1 ./internal/metadata ./internal/db ./internal/tagger
go test -race -count=1 ./internal/metadata ./internal/db ./internal/tagger
go test -count=1 ./...
go vet ./...
go build ./cmd/proxy
```

## Success Criteria

1. All roles rank deterministically with role-aware geometry tests.
2. Artwork proposals write the correct safe filename and exact Undo works.
3. ListenBrainz requests/decoding are bounded and strict-MBID keyed.
4. Similar results round-trip and failed refreshes preserve previous rows.
5. Full regression, race, vet, and build checks pass.
