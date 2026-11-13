# Implementation Plan: `bio-delivery`

Source of truth: [`SPEC-bio-delivery.md`](../SPEC-bio-delivery.md).

## Approach

Attach one optional biography response adapter to the existing `httputil.ReverseProxy.ModifyResponse` chain. A small request-capture helper recognizes an exact `getArtistInfo2` request, validates its artist ID, restores any inspected POST body, and carries only that bounded ID in request context. The response adapter performs one short local `EffectiveBiography` read and rewrites only the JSON/XML `biography` field.

Every enrichment failure is a transparent pass-through. Provider, identity, language, translation, and AI services remain unreachable from the proxy package.

## Dependency Graph

```text
exact request recognition + bounded ID capture
                    ↓
       effective-biography reader boundary
                    ↓
      bounded response body acquisition
             ┌──────┴──────┐
             ↓             ↓
      strict JSON edit  tokenized XML edit
             └──────┬──────┘
                    ↓
       ModifyResponse composition + runtime wiring
```

## Components

### 1. Exact request recognition and ID capture

- Add a helper that recognizes only `/rest/getArtistInfo2` and `/rest/getArtistInfo2.view`, allowing an optional leading Subsonic base path without substring matches.
- Support GET query parameters and bounded `application/x-www-form-urlencoded` POST bodies.
- Reject empty, duplicate, oversized, control-character, or malformed IDs for enrichment.
- Restore an inspected POST body byte-for-byte and preserve `Content-Length`.
- Store only the validated artist ID in request context; never log form/query values.
- Remove `Accept-Encoding` only after a valid artist-info request has been captured.

Checkpoint: request tests cover exact paths, false-positive paths, GET, POST restoration, duplicate IDs, method/content-type rejection, body bounds, cancellation, and credential preservation.

### 2. Narrow reader boundary and bounded response acquisition

- Define `effectiveBiographyReader` with the existing `EffectiveBiography` method.
- Add `Server.SetBiographyReader` for startup-time dependency injection without database or network work.
- On an eligible HTTP 200 uncompressed response, create a short child context from the request context and query the reader once.
- Revalidate artist ID and selected biography size/content before rewriting.
- Read at most 2 MiB plus one byte; if oversized, reconstruct the exact already-read prefix plus remaining original stream and pass through.
- Never return lookup/rewrite errors to the reverse proxy; retain the original body whenever a complete mutation is unavailable.

Checkpoint: fake-reader tests cover success, missing result, error, timeout, cancellation, oversize, compressed content, upstream failures, and exactly one local lookup.

### 3. Strict JSON biography rewriting

- Decode the root, `subsonic-response`, and `artistInfo2` as bounded `map[string]json.RawMessage` layers.
- Reject duplicate object keys at every level, trailing data, non-object shapes, failed Subsonic status, and malformed UTF-8.
- Marshal only the new biography string and rebuilt containing objects; retain unknown siblings as raw JSON.
- Add `biography` when absent and replace it when present.
- Return `(body, changed)` so caller can distinguish a safe no-op from mutation.

Checkpoint: fixtures preserve images, similar artists, unknown/OpenSubsonic fields, status, numeric precision, nulls, and escaped text; adversarial JSON fails closed.

### 4. Strict XML biography rewriting

- Use `encoding/xml.Decoder`/`Encoder` token streaming with a fixed maximum depth and token count.
- Reject directives/DTD, malformed XML, multiple `artistInfo2` elements, nested/conflicting biography structures, or unexpected depth.
- Track direct children of the single `artistInfo2` element.
- Replace an existing direct biography element's content while preserving its element name/attributes, or insert a new Subsonic-namespaced biography element before `artistInfo2` closes.
- Encode stored text as character data so markup remains inert.
- Preserve all unrelated elements, attributes, namespaces, comments, and processing instructions supported safely by `encoding/xml`.

Checkpoint: XML fixtures cover replace, insert, namespace/attributes, unknown elements, escaping, duplicate elements, DTD, depth/token limits, and malformed input.

### 5. Reverse-proxy composition and header correctness

- Reuse the current `ModifyResponse` closure and keep extension advertisement plus CORS behavior intact.
- Capture artist info before the reverse proxy call without affecting search/native routing.
- Run biography rewriting after the upstream response exists and independently of extension rewriting.
- On mutation, update `Content-Length` and remove only invalid content validators (`ETag`, `Content-MD5`, `Digest`, `Content-Digest`, `Repr-Digest`).
- Preserve cookies, CORS, cache policy, content type, status, and unrelated headers.
- Attach the existing `bioStore` in `cmd/proxy/main.go` before servers start.

Checkpoint: end-to-end proxy tests prove JSON/XML delivery, pass-through, header behavior, POST body forwarding, concurrent artist isolation, and no impact on search/extensions/cast.

## Implementation Order

1. Request recognition and bounded ID capture.
2. Reader boundary and bounded response acquisition.
3. JSON transformer.
4. XML transformer.
5. ModifyResponse composition and runtime wiring.
6. Full security, race, regression, and vulnerability audit.

The request contract comes first so no response is inspected without a validated key. JSON and XML transformations remain pure functions and are completed before touching the reverse-proxy lifecycle. Runtime wiring is last.

## Reuse Decisions

- Reuse `artistbio.Store.EffectiveBiography` and `artistbio.MaxProviderIDBytes`/`MaxBiographyBytes`.
- Reuse the existing `ReverseProxy.ModifyResponse` hook rather than adding a handler, Navidrome plugin, or second proxy.
- Reuse the current extension/CORS response chain and request context.
- Reuse standard-library JSON/XML encoders; add no parser dependency.
- Use one small shared strict-JSON duplicate-key checker if the current biography AI checker can be generalized safely; otherwise keep the proxy implementation package-local to avoid cross-layer coupling.
- Add no response cache until measurements show the local indexed SQLite read is material.

## Risks and Mitigations

| Risk | Mitigation |
|---|---|
| POST body inspection breaks Navidrome authentication | Strict 64 KiB bound and byte-for-byte body restoration tests |
| Substring route match intercepts unrelated endpoint | Exact normalized path-segment matcher |
| Malformed upstream body causes client failure | Rewrite into a separate buffer; original body remains the fallback |
| Oversized/chunked response is consumed during inspection | Read cap+1 and reconstruct prefix plus unread stream |
| JSON duplicate keys hide a conflicting biography | Duplicate-key token validation before decoding |
| XML entity/DTD or depth abuse | Reject directives, cap depth/token count, standard decoder only |
| Biography markup becomes executable XML/JSON | Marshal string/character data through encoders |
| Header validators describe old content | Remove only content validators after successful mutation |
| One artist receives another artist's text | Request-context ID, stateless transformer, concurrency/race tests |
| Request latency grows or blocks on external work | One short local DB read; no provider/AI interfaces in proxy |

## Verification Checkpoints

1. Request: focused path/form/body/bounds tests.
2. Reader/body: timeout, cancellation, oversized stream reconstruction, and pass-through tests.
3. Formats: pure JSON/XML fixtures and hostile inputs.
4. Integration: local reverse proxy with fake reader, headers, concurrent IDs, and existing route regressions.
5. Security audit: no credentials/body logging, unbounded reads, external calls, regex rewriting, or user-content errors.
6. Completion: focused tests, full tests, full race detector, vet, production build, `govulncheck`, formatting, and `git diff --check`.
7. Diff audit: unrelated iOS/image changes and completed ingestion/validation behavior remain untouched.

## Out of Scope

- Native Navidrome `/api/artist` response enrichment.
- Fetching, validating, translating, or ranking biographies.
- Studio override handlers/UI.
- Compressed response rewriting or a response cache.
- Changing any `artistInfo2` field other than `biography`.
