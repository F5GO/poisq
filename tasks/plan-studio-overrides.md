# Implementation Plan: `studio-overrides`

Source of truth: [`SPEC-studio-overrides.md`](../SPEC-studio-overrides.md).

## Approach

Add one guarded Studio resource around the completed biography store. The backend reuses the Navidrome database opener for a bounded artist picker and the existing `artistbio.Store` for detail/save/reset. The UI adds one small tab using the current HTML/CSS/vanilla-JS conventions and `fetchApi`; no editor framework or new state system is introduced.

A non-empty manual value is the priority state. Clearing it restores the already selected automatic candidate without modifying ingestion, validation, or delivery.

## Dependency Graph

```text
bounded Navidrome artist lookup
              ↓
AdminServer biography-store attachment
              ↓
guarded list/detail/save/reset handlers
              ↓
Studio artist picker + optional editor
              ↓
automatic/manual delivery integration tests
```

## Components

### 1. Bounded artist inventory lookup

- Reuse `LibraryArtist`, `LoadArtistInventoryPage`, schema inspection, and existing field bounds.
- Add a parameterized `SearchArtistInventory` only for non-empty queries.
- Escape `%`, `_`, and the escape character for literal `LIKE` matching.
- Normalize surrounding query whitespace, require valid UTF-8, cap query bytes, and cap results at 50.
- Order deterministically by normalized name then ID and omit empty/oversized rows.
- Add an exact bounded artist-by-ID lookup for detail/save/reset existence checks rather than loading pages in application code.

Checkpoint: temporary Navidrome schemas test paging reuse, search escaping, hostile values, bounds, missing columns, deterministic order, cancellation, and exact ID lookup.

### 2. AdminServer store attachment and strict API models

- Add an optional `*artistbio.Store` field plus `SetBiographyStore`, called before serving.
- Register list and detail routes inside the existing guarded mux.
- Define response DTOs containing only artist identity, effective/manual/automatic display state, source metadata, and timestamps.
- Keep candidate bodies/evidence private except for the selected automatic text needed as a reset preview.
- Add one package-local strict JSON decoder with body cap, unknown-field rejection, duplicate-key rejection, single-value enforcement, and generic errors.

Checkpoint: construction and method tests prove nil-store fail-closed behavior, route guarding, strict decoding, and no body/error leakage.

### 3. List and detail reads

- `GET /artist-biographies` opens Navidrome read-only for the duration of the request and calls paging or bounded search.
- `GET /artist-biographies/detail` validates one ID, confirms the real artist, then reads `EffectiveBiography` and `AutomaticCandidate` from the local store.
- Detail distinguishes manual, automatic translated, automatic original, and no-biography states.
- Source URLs are returned only for the effective/manual and selected automatic records.
- Database errors map to generic HTTP responses; paths, SQL, and biography bodies are not logged.

Checkpoint: API tests cover empty/search lists, automatic/manual/no-result detail, missing artist/store/DB, bounds, methods, and cancellation.

### 4. Manual save and reset

- `PUT` accepts only artist ID, text, and optional source URL.
- Confirm artist existence before mutation.
- Normalize surrounding whitespace and blank-line spacing without altering prose content.
- Reuse `DetectBiographyLanguage` and require the approved Russian thresholds before calling `SetManualOverride`.
- Reuse `ManualOverride.Validate` for text/URL bounds and absolute HTTP(S) URL policy.
- `DELETE` confirms artist existence and clears through an empty `ManualOverride`.
- Return freshly read effective state after save/reset so UI never guesses precedence.

Checkpoint: tests cover Russian acceptance; English/Ukrainian/Belarusian/mixed/short/repeated/markup/invalid UTF-8/oversize rejection; URL validation; reset preservation; CSRF guard; concurrent effective reads; and generic errors.

### 5. Studio tab and state flow

- Add a `Биографии` tab/panel using existing navigation and surface/form classes.
- Extend the global state only with artist results, selected biography detail, query, loading/saving flags, and debounce timer.
- Search after a short debounce; render names through DOM `textContent` or the existing `escapeHtml` helper.
- Selecting an artist loads detail; automatic text remains a read-only preview.
- Manual textarea is populated only from current manual text, preventing an accidental automatic-to-manual copy on save.
- Save and reset buttons use `fetchApi`, disable during mutation, confirm reset, retain unsaved text after failure, and show existing toasts.
- Add minimal CSS only where current form/surface classes cannot express the two-column picker/editor layout.

Checkpoint: static DOM/JS audit plus backend integration fixtures verify IDs, endpoint methods, escaping, loading states, reset confirmation, and no AI/generation control.

### 6. Runtime wiring and complete audit

- Attach the existing `bioStore` to `AdminServer` next to its other pre-start dependencies.
- Prove manual save immediately changes `EffectiveBiography` and delivery; reset restores the same automatic candidate.
- Run focused tests, full regression/race, vet, production build, vulnerability scan, formatting, and diff audit.
- Confirm no external request, new dependency, schema migration, raw body logging, or unrelated iOS/image edit.

Checkpoint: all project verification commands pass and every checklist item is complete.

## Implementation Order

1. Artist inventory search/exact lookup.
2. Admin store attachment and strict request/response contracts.
3. Read endpoints.
4. Save/reset endpoints.
5. Studio tab/UI behavior.
6. Runtime wiring and full audit.

The backend contract is completed and tested before UI work. Runtime wiring is last so partially implemented handlers cannot mutate production state.

## Reuse Decisions

- Reuse `db.LibraryArtist`, table-column inspection, Navidrome opener, and inventory paging.
- Reuse `artistbio.Store`, `ManualOverride`, `EffectiveBiography`, `AutomaticCandidate`, and deterministic language analysis.
- Reuse `AdminGuardMiddleware`, `writeJSON`, `fetchApi`, toast helpers, tab switching, CSS tokens, and form controls.
- Treat non-empty manual text as the lock; add no lock column or migration.
- Add no rich-text editor, Markdown parser, frontend framework, API client, AI helper, history table, or cache.

## Risks and Mitigations

| Risk | Mitigation |
|---|---|
| SQL wildcard/injection changes search scope | Parameterized query plus literal `LIKE ... ESCAPE` handling |
| Override created for a forged ID | Exact Navidrome existence check before every detail mutation |
| Manual entry bypasses Russian invariant | Same deterministic language/quality thresholds as automatic direct acceptance |
| CSRF changes biographies | Existing Admin guard mutation-origin policy and integration tests |
| UI inserts artist/biography as HTML | `textContent` or existing escaping helper only |
| Reset deletes automatic work | Empty manual override only; candidates and selection remain untouched |
| Double click or stale UI overwrites state | Disabled mutation buttons and detail reload from server response |
| Error leaks text/path/SQL | Stable generic API errors and leakage tests |
| Added interface becomes mandatory work | Empty state remains automatic; no prompts/badges implying required completion |

## Verification Checkpoints

1. DB lookup: parameterization, escaping, schema, bounds, deterministic order, cancellation.
2. API read: methods, guard, list/detail states, nil/error pass-through.
3. API mutation: strict JSON, language/URL/ID validation, CSRF, save/reset precedence.
4. UI: tab navigation, debounce, escaped rendering, unsaved-text retention, button state, optional wording.
5. Integration: manual delivery immediately wins and reset restores automatic candidate.
6. Completion: focused/full/race tests, vet, build, `govulncheck`, formatting, and `git diff --check`.
7. Diff audit: no new dependency/migration/external call/log leak or unrelated user-file modification.

## Out of Scope

- AI-assisted or suggested manual biography writing.
- Requiring manual text or a source URL.
- Rich text, Markdown, revision history, approvals, or multi-user ownership.
- Viewing/editing all provider candidates or identity evidence.
- Changing automatic ingestion, validation, ranking, or delivery.
