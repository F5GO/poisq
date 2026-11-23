# Spec: nfo-interoperability

Module: `nfo-interoperability`
Depends on: `metadata-core`, `metadata-merge-policy`, `staging-writeback`

## Objective

Import and export Jellyfin/Kodi-compatible `artist.nfo` and `album.nfo` without bypassing field locks, provider policy, Staging review, transactional writeback, or Undo.

## Requirements

- Parse bounded UTF-8 XML with explicit artist/album DTOs; reject DTD/entity directives, oversized files, path escapes, and unknown roots.
- Map names, sort names, genres, labels, dates, MusicBrainz artist/release/release-group IDs, and overview/comment into normalized candidates with `SourceNFO` provenance.
- Merge NFO candidates per track through the configured merge policy; locked/audio/manual winners remain untouched.
- Create reviewable `nfo_import` tag proposals rather than mutating audio directly.
- Export deterministic UTF-8 XML from effective normalized folder metadata.
- Create `nfo_export` proposals for `album.nfo` and `artist.nfo`; approved writes use the writeback journal, exact backup, verification, recovery, and Undo.
- Bound XML and proposal content to 1 MiB and never follow an out-of-root symlink.

## Commands

```sh
gofmt -w internal/metadata/nfo*.go internal/tagger/*.go internal/security/path_guard.go
go test -count=1 ./internal/metadata ./internal/tagger ./internal/security
go test -race -count=1 ./internal/metadata ./internal/tagger
go test -count=1 ./...
go vet ./...
go build ./cmd/proxy
```

## Success Criteria

1. Valid artist/album NFO round-trips deterministically.
2. Imports respect locks and configured source priority.
3. Import creates tag proposals; export creates file proposals.
4. NFO apply/Undo restores exact previous bytes or removes a newly created NFO.
5. Malformed, entity-bearing, oversized, and escaped paths are rejected.
