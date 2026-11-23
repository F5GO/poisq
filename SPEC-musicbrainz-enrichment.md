# Spec: musicbrainz-enrichment

Module: `musicbrainz-enrichment`
Depends on: `metadata-core`, `metadata-merge-policy`

## Objective

Resolve the complete Picard identity chain for audited music: artist, release, release group, recording, and release-track, using folder and track evidence while retaining review-before-write behavior.

## Requirements

- Fetch exact release details after release search, including media/disc positions, track positions, durations, recording IDs, release-track IDs, and release-group ID.
- Match a local track deterministically using disc/track position, normalized title, and bounded duration difference; reject weak/ambiguous matches.
- Include all five MusicBrainz ID types in staging proposals without conflating recording and release-track IDs.
- Preserve Folder-as-Album Lock and existing confidence gates.
- Read the complete library in stable bounded pages instead of truncating the audit to 500 tracks.
- Bound MusicBrainz inputs, response size, candidate counts, request rate, and errors; never expose provider response bodies.
- Keep Picard-compatible mappings and Undo clearing distinct for release, release-group, recording, release-track, and artist IDs.

## Matching

Exact disc/track position is strongest. Title comparison is Unicode-normalized and punctuation/space tolerant but not fuzzy enough to join unrelated tracks. Duration within five seconds contributes evidence. A position match plus either title or duration is accepted; without position, both exact normalized title and duration are required. Ties are rejected.

## Commands

```sh
gofmt -w internal/metadata/musicbrainz*.go internal/metadata/folder_scanner.go internal/db/repo_tracks.go
go test -count=1 ./internal/metadata ./internal/db ./internal/tagger
go test -race -count=1 ./internal/metadata ./internal/db ./internal/tagger
go test -count=1 ./...
go vet ./...
go build ./cmd/proxy
```

## Success Criteria

1. Release details decode all identity levels with bounded arrays.
2. Matcher accepts strong evidence and rejects ambiguous/weak candidates.
3. Proposals carry distinct Picard IDs.
4. Audits include tracks beyond the former 500-item boundary.
5. Existing Folder-as-Album and tagger tests remain green.
