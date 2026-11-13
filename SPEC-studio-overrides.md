# Spec: `studio-overrides`

## Objective

Add an optional manual artist-biography override to poisq Studio without making manual work part of the automatic pipeline.

The module must let an authorized Studio user:

- search the real Navidrome artist inventory by name;
- inspect the current effective biography, automatic candidate/source, and manual-override state for one artist;
- enter or replace a bounded Russian manual biography and optional provenance URL;
- see clearly that manual text has priority over automatic selection;
- delete the manual override and immediately return to the automatic result;
- leave every artist fully automatic when no manual override is set.

The module must reuse `artistbio.Store.SetManualOverride` and `EffectiveBiography`. It does not generate text, call AI/providers, change candidate ranking, or require the user to fill anything in.

## Assumptions

1. Manual override is an expert escape hatch, not a required coverage mechanism.
2. A non-empty manual value is the lock/priority state; no second lock column or state machine is needed.
3. Artist IDs come from Navidrome's `artist` table and match the IDs used by the biography store/delivery path.
4. Studio remains protected by the existing `AdminGuardMiddleware`, including its mutation-origin checks.
5. Manual text should still be measured as Russian before acceptance so the delivery invariant is not bypassed accidentally.
6. The optional source URL is attribution metadata only and is never fetched by this module.

## Tech Stack

- Existing Go admin server and `AdminGuardMiddleware`.
- Existing Navidrome database opener and schema-tolerant loader helpers.
- Existing `artistbio.Store`, deterministic language detector, and field bounds.
- Existing embedded Studio HTML/CSS/vanilla JavaScript and `fetchApi` helper.
- No frontend framework, editor dependency, provider call, AI client, or database migration.

## Commands

```sh
gofmt -w internal/db/loader.go internal/db/loader_test.go internal/admin/api_artist_biographies.go internal/admin/api_artist_biographies_test.go internal/admin/server.go cmd/proxy/main.go
go test -count=1 -run 'Test(SearchArtistInventory|ArtistBiograph)' ./internal/db ./internal/admin
go test -race -count=1 ./internal/admin ./internal/artistbio
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go build -trimpath -o /tmp/poisq-proxy ./cmd/proxy
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
git diff --check
```

## Project Structure

```text
internal/db/loader.go                         Bounded parameterized Navidrome artist search
internal/db/loader_test.go                    Schema, escaping, bounds, ordering, and cancellation tests
internal/admin/api_artist_biographies.go      Detail/save/delete API over the existing biography store
internal/admin/api_artist_biographies_test.go Method, validation, CSRF, leakage, and precedence tests
internal/admin/server.go                      Route registration and optional store attachment
internal/admin/ui/index.html                  Optional Biographies Studio tab/panel
internal/admin/ui/app.js                      Search, selection, edit/save/reset behavior using fetchApi
internal/admin/ui/css/forms.css               Minimal existing-system styling only if current classes are insufficient
cmd/proxy/main.go                              Attach the existing biography store to AdminServer
```

## API Contract

All endpoints live under the existing guarded admin server:

### `GET /api/v1/artist-biographies?query=...&limit=...`

- Search is optional and bounded to 100 UTF-8 bytes.
- Result limit defaults to 25 and is capped at 50.
- The Navidrome query is parameterized, escapes SQL `LIKE` metacharacters, and returns deterministic `{id,name}` rows only.
- An empty query returns the first deterministic inventory page by reusing `LoadArtistInventoryPage`.
- The response never exposes biography bodies for every row; it is a lightweight picker.

### `GET /api/v1/artist-biographies/detail?id=...`

- Validate the bounded artist ID, confirm it exists in Navidrome, and read local biography state.
- Return artist ID/name, effective biography/source/source URL, whether it is manual/translated, and the current manual values.
- Return automatic candidate metadata separately when available so the UI can show what reset will restore.
- Do not return original non-selected provider candidates, identity evidence, prompts, or internal errors.

### `PUT /api/v1/artist-biographies/detail`

Strict JSON body:

```json
{
  "artistId": "navidrome-id",
  "text": "Русское описание…",
  "sourceUrl": "https://optional.example/about"
}
```

- Cap the request before decoding and reject unknown/trailing/duplicate fields.
- Confirm the artist exists in Navidrome.
- Normalize only surrounding/line whitespace; never rewrite prose.
- Require valid UTF-8, existing store bounds, and deterministic Russian analysis with confidence `>= 0.90`, Russian share `>= 0.80`, and sufficient meaningful text.
- Validate an optional absolute HTTP(S) URL through the existing store contract; never request it.
- Save through `SetManualOverride` and return the resulting effective biography.

### `DELETE /api/v1/artist-biographies/detail?id=...`

- Confirm the artist ID and clear through `SetManualOverride(ManualOverride{ArtistID: id})`.
- Preserve automatic selection/candidates.
- Return the now-effective automatic result or an explicit `hasBiography: false`.

Unsupported methods return 405 with a stable generic error. Database/internal errors never include biography text, filesystem paths, SQL, request bodies, or source URLs.

## Studio UX

- Add one `Биографии` navigation tab using the existing tab/panel system.
- The left/search area lists matching real artists; results load only after a short debounce and never use `innerHTML` with untrusted values unless passed through the existing escaping helper.
- Selecting an artist loads one detail record.
- Show automatic/effective source and state badges without provider evidence internals.
- The textarea begins with the current manual text when manual mode is active; otherwise it is empty and the automatic description remains visible as a read-only preview.
- `Сохранить ручное описание` activates manual priority.
- `Вернуться к автоматическому` requires a confirmation and clears only the override.
- Disable buttons during requests, prevent double submits, retain unsaved text on recoverable errors, and show existing toast notifications.
- Display a short warning: manual text is optional, must be factual/Russian, and overrides automatic updates until reset.
- Do not add an AI writing/generation button.

## Testing Strategy

Backend tests use temporary SQLite databases and the real Admin guard; UI behavior is kept in small functions that can be inspected without a new JavaScript test framework.

Required tests:

- inventory search uses parameterized escaped matching, deterministic limits/order, schema checks, cancellation, and hostile query strings;
- list/detail reject oversized, duplicate, missing, unknown-field, malformed, and control-character inputs;
- GET remains allowed through the guard while PUT/DELETE require the existing trusted-origin mutation policy;
- an artist must exist in Navidrome before an override can be created;
- clear Russian text saves and immediately wins `EffectiveBiography`/`getArtistInfo2` delivery;
- English, Ukrainian, Belarusian, mixed, short, repetitive, markup-heavy, oversized, invalid-UTF-8, and unsafe-URL values are rejected;
- clearing restores the exact selected automatic candidate and does not delete candidates;
- manual save/reset is transactionally safe under concurrent reads;
- errors/logs do not contain biography text, URL, request body, DB path, SQL, or credentials;
- existing Studio endpoints, reset behavior, delivery, validation, and proxy tests remain green.

## Boundaries

- Always:
  - keep manual entry entirely optional;
  - reuse the existing store precedence and Admin guard;
  - validate artist existence, IDs, language, lengths, URL, methods, and strict JSON;
  - escape all untrusted UI content and use `textContent` where possible;
  - preserve automatic candidates when saving or clearing manual text;
  - perform no external request from list/detail/save/delete.
- Ask first:
  - accepting non-Russian manual text;
  - requiring a source URL;
  - exposing all rejected/provider candidates or evidence in Studio;
  - adding history/versioning or multi-user ownership;
  - adding rich text, Markdown, or AI-assisted writing.
- Never:
  - make manual completion required for an artist;
  - generate or suggest biography prose;
  - fetch a user-entered source URL;
  - bypass `AdminGuardMiddleware` or its mutation-origin policy;
  - accept HTML/script as executable content;
  - delete automatic candidates when clearing an override;
  - log manual biography bodies or request payloads.

## Success Criteria

- Artists continue receiving automatic Russian biographies without any Studio interaction.
- A Studio user can find a real artist, save a verified Russian manual override, and see it delivered immediately through `getArtistInfo2`.
- Reset removes only manual priority and restores the previously selected automatic result.
- Manual endpoints are bounded, guarded, injection-safe, fail closed, and make zero external/AI calls.
- The UI clearly distinguishes automatic and manual state and does not expose untrusted content unsafely.
- Focused tests, full tests, race detector, vet, production build, vulnerability scan, formatting, and diff checks pass.

## Open Questions

None. Manual override remains optional by design and is intentionally not a fallback requirement.
