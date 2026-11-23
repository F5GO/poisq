# Spec: Field change history

Module: `field-change-history`
Initiative index: `CAPABILITY-MAP-music-metadata.md`

## Objective

Add an immutable, queryable history entry for every metadata field changed by a successfully applied Staging/writeback operation. Operators must be able to inspect who or what changed a field, compare old and new values, and prepare a safe revert of one or several historical fields without losing unrelated later edits.

Existing file backups and whole-operation Undo remain the disaster-recovery mechanism. Field history is an additional semantic audit trail, not a replacement for byte backups.

## Tech Stack

- Go 1.26.11 and the existing SQLite layer.
- Existing normalized `musicmeta.Document`, Staging repository, writeback transaction state machine, and embedded Studio UI.
- No new runtime dependency.

## Commands

```sh
gofmt -w internal/musicmeta internal/db internal/tagger internal/admin
go test -count=1 ./internal/musicmeta ./internal/db ./internal/tagger ./internal/admin
go test -race -count=1 ./internal/db ./internal/tagger ./internal/admin
go test -count=1 ./...
go vet ./...
go build ./cmd/proxy
git diff --check
```

## Project Structure

```text
internal/musicmeta/diff.go            Stable field-level diff and canonical JSON values
internal/db/metadata_schema.go        Additive field-history migration
internal/db/metadata_history.go       Append/list/get repository operations
internal/tagger/rollback.go           Append after reconciliation; stage semantic reverts
internal/admin/api_metadata_history.go  Bounded history and revert APIs
internal/admin/ui/                    Timeline, filters, selection and revert preview
```

## Persistence and Revert Contract

Each immutable row stores:

```text
id, track_id, proposal_id, writeback_transaction_id,
field, old_value_json, new_value_json,
source, provider, actor, created_at, reverted_by_proposal_id
```

- `field` uses the registered `musicmeta.Field` values plus registered custom-tag paths `custom:<canonical-key>`.
- Values use canonical bounded JSON so scalar, date, credit, genre, external-ID, and custom-tag changes can be compared without lossy string conversion.
- Rows are appended only after the file is committed, verified, and the normalized projection reconciles successfully. A failed or rolled-back write produces no successful history rows.
- One operation inserts all of its field rows in one SQLite transaction. Duplicate insertion for the same proposal/field is prevented by a unique constraint so crash recovery is idempotent.
- History is returned newest-first with cursor pagination, maximum page size 200, optional track and field filters, and no arbitrary file or backup paths.
- A field revert reads the selected historical old value, loads the current document, and changes only selected fields. Unselected current fields remain untouched.
- Before creating the revert proposal, the API returns a three-way preview: historical old value, historical new value, and current value. Divergence is clearly marked but does not silently overwrite.
- Applying a revert requires explicit confirmation of the current document revision/fingerprint. A stale preview receives `409 Conflict` and must be regenerated.
- The revert is a normal manual Staging proposal and therefore creates its own history rows. Reverting a revert is supported.
- `actor` is currently a bounded origin label (`studio`, provider, recovery, or system); it is not a user identity until authentication/roles exist.

## Code Style

Field diffs are deterministic and typed:

```go
type FieldChange struct {
	Field musicmeta.Field
	Before json.RawMessage
	After  json.RawMessage
}
```

Do not derive diffs by comparing arbitrary serialized whole documents. Use a registered field accessor table so new fields must deliberately opt into history and revert handling.

## Testing Strategy

- Deterministic diff tests for scalars, partial dates, credits, genres, IDs, BPM, rating, and custom tags.
- Migration, pagination, filtering, idempotency, cascade, and reset tests.
- Writeback tests proving history appears only after verified/reconciled success and is not duplicated during recovery.
- Revert tests for one field, multiple fields, divergent current values, stale previews, locked fields, and revert-of-revert.
- API bounds, malformed cursor, unknown field, unknown JSON, and authorization-boundary tests.
- Studio contract tests for timeline rendering, keyboard selection, divergence warning, preview, and error reporting.

## Boundaries

### Always

- Keep history append-only except database reset/cascade with deleted tracks.
- Preserve unrelated current fields during semantic revert.
- Require preview plus revision/fingerprint confirmation before staging a revert.
- Keep whole-file backup Undo available.

### Ask first

- Adding authenticated user identity to `actor`.
- Introducing retention or automatic history deletion.
- Allowing history export to an external service.

### Never

- Restore an old whole document for a single-field revert.
- Record secrets, backup contents, arbitrary paths, or raw audio data.
- Mark failed writes as successful history.
- Mutate files directly from the history endpoint.

## Success Criteria

1. Every successfully applied metadata operation creates exactly one immutable history row per changed field.
2. Failed, rolled-back, and no-op operations create no successful field history.
3. History pagination and filters remain bounded and deterministic.
4. Studio displays before/after, source, actor label, time, and operation linkage.
5. One or several fields can be reverted through preview, stale-state protection, Staging, and normal writeback verification.
6. A semantic revert preserves every unrelated current field and creates new history entries.
7. Existing whole-operation Undo and crash recovery continue to work.
8. Focused, race, full, vet, build, and diff checks pass.

## Open Questions

None.
