# Spec: staging-writeback

Module: `staging-writeback`
Depends on: `metadata-core`, `metadata-merge-policy`, `musicbrainz-enrichment`

## Objective

Make every approved tag or artwork mutation crash-recoverable, verifiable, and exactly undoable.

## Requirements

- Create a durable byte-for-byte backup before changing an existing audio or cover file.
- Journal `prepared`, `written`, `verified`, `committed`, `rolled_back`, and `failed` transaction states in SQLite.
- Write through same-directory temporary files and atomic rename; retain existing bit-perfect checks.
- Verify requested tags after write by bounded `ffprobe`, including distinct MusicBrainz ID tags.
- Commit transaction state and staging status atomically after verification.
- Reconcile the compatibility DB projection after commit.
- On any pre-commit failure or startup recovery of an incomplete transaction, restore the exact backup (or remove a newly created file).
- Undo restores the exact original bytes and checksum, including unknown tags/artwork content, then reconciles DB state.
- Cover changes use the same journal/backup rules.
- Backup paths are generated internally beside the target, never accepted from API input.

## Transaction Order

`backup → prepared → write → written → verify → verified → atomic DB commit → reconcile projection`.

The durable commit is the boundary: incomplete transactions roll back; committed transactions reconcile forward. Backups remain available for explicit Undo.

## Commands

```sh
gofmt -w internal/tagger/*.go internal/db/writeback*.go internal/db/metadata_schema.go cmd/proxy/main.go
go test -count=1 ./internal/tagger ./internal/db ./internal/admin
go test -race -count=1 ./internal/tagger ./internal/db ./internal/admin
go test -count=1 ./...
go vet ./...
go build ./cmd/proxy
```

## Success Criteria

1. Backups are byte-identical and hash-verified on restore.
2. Post-write verification detects missing or wrong requested tags.
3. Incomplete transactions recover backward; committed transactions reconcile forward.
4. Undo restores original bytes rather than reconstructing a subset of tags.
5. Cover creation/replacement is undoable.
6. Existing Staging APIs remain compatible and all regressions pass.
