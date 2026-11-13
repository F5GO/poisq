# Spec: `bio-delivery`

## Objective

Deliver the already selected Russian artist biography to every Subsonic client through the existing poisq reverse proxy.

The module must:

- recognize only `getArtistInfo2` and its `.view` form under the Subsonic REST path;
- obtain the Navidrome artist ID from a bounded GET query or form-encoded POST body;
- read `artistbio.Store.EffectiveBiography`, which already applies manual-over-automatic precedence;
- replace or add only the `biography` field inside a successful JSON or XML `artistInfo2` response;
- preserve every other upstream field, attribute, status, and response header whose value remains valid;
- leave the exact upstream response unchanged when no effective biography exists or safe rewriting is impossible;
- perform only a bounded local SQLite read in the request path, with no provider, identity, language, translation, or AI call;
- fail open to the original Navidrome response on lookup timeout, malformed response, unsupported encoding, or rewrite error.

This module does not fetch, validate, translate, rank, or edit biographies and does not add a new Navidrome plugin.

## Assumptions

1. The `id` supplied to `getArtistInfo2` is the same Navidrome artist ID stored by the background inventory pipeline.
2. JSON responses use `subsonic-response.artistInfo2`; XML responses use an `artistInfo2` element in the Subsonic namespace.
3. A selected manual override or accepted automatic candidate is safe to deliver exactly as stored.
4. If the selected text is unavailable, the correct behavior is transparent pass-through, not an empty or generated biography.
5. Local SQLite lookup latency is acceptable only under a short request-scoped timeout; upstream delivery must not depend on background services.

## Tech Stack

- Existing Go standard library HTTP reverse proxy, `encoding/json`, and `encoding/xml`.
- Existing `proxy.Server`, `httputil.ReverseProxy.ModifyResponse`, and response adaptation conventions.
- Existing `artistbio.Store.EffectiveBiography` contract.
- No plugin SDK, provider SDK, XML/JSON dependency, network cache, or additional service.

## Commands

```sh
gofmt -w internal/proxy/artist_info.go internal/proxy/artist_info_test.go internal/proxy/proxy.go cmd/proxy/main.go
go test -count=1 -run 'TestArtistInfo' ./internal/proxy
go test -race -count=1 ./internal/proxy
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go build -trimpath -o /tmp/poisq-proxy ./cmd/proxy
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
git diff --check
```

## Project Structure

```text
internal/proxy/artist_info.go       Bounded request capture and JSON/XML biography response rewriting
internal/proxy/artist_info_test.go  Pass-through, preservation, bounds, malformed, POST, timeout, and concurrency tests
internal/proxy/proxy.go             Compose the response modifier with the existing extension/CORS modifier
cmd/proxy/main.go                   Attach the existing artistbio store before serving requests
```

## Interface Contract

The proxy depends on the narrow read interface rather than the concrete store:

```go
type effectiveBiographyReader interface {
    EffectiveBiography(ctx context.Context, artistID string) (artistbio.EffectiveBiography, bool, error)
}

server.SetBiographyReader(bioStore)
```

`SetBiographyReader` is called during startup before either HTTP server begins accepting traffic. It performs no database read or external work.

## Request Handling

- Match path segments exactly after case normalization; strings that merely contain `getArtistInfo2` are not intercepted.
- Accept `GET` and form-encoded `POST` only; all other methods or content types pass through.
- Limit a POST form body to 64 KiB, restore it byte-for-byte after reading, and reject malformed or duplicate `id` values for enrichment only.
- Validate a non-empty artist ID against `artistbio.MaxProviderIDBytes` before attaching it to request context.
- Never log the request query/form because it can contain Subsonic credentials.
- Remove `Accept-Encoding` only for a valid artist-info request so the response can be safely inspected.

## Response Rewriting

- Rewrite only HTTP 200 responses with no `Content-Encoding` and a captured valid artist ID.
- Run the local effective-biography read with a short child timeout and the request cancellation context.
- Cap inspected response data at 2 MiB. If the response exceeds the cap, reconstruct the original body stream and pass it through unchanged.
- Treat `Content-Type` containing JSON as JSON; treat XML media types and standard Subsonic XML responses as XML; never guess from arbitrary binary data.
- Validate the stored biography again for non-empty content and `artistbio.MaxBiographyBytes` before insertion.

JSON behavior:

- decode only the outer objects into `map[string]json.RawMessage`;
- require successful `subsonic-response` and an object-valued `artistInfo2`;
- replace/add `artistInfo2.biography` using `json.Marshal`;
- preserve every unknown sibling field as raw JSON data;
- malformed, duplicate-key, non-object, or trailing JSON passes through unchanged.

XML behavior:

- use a bounded token stream, not string replacement;
- locate `artistInfo2` by local element name and preserve namespaces, attributes, and unknown child elements;
- replace character data within an existing direct `biography` child, or insert one immediately before `artistInfo2` closes;
- escape biography text through `encoding/xml`; markup in stored text is data, never executable XML;
- reject DTD/entity directives, excessive token depth, malformed XML, or multiple conflicting `artistInfo2`/`biography` elements by passing through unchanged.

After a successful mutation:

- replace the body and exact `Content-Length`;
- remove validators invalidated by mutation (`ETag`, `Content-MD5`, and strong digest headers);
- retain status, content type, CORS, cache policy, cookies, and unrelated headers;
- never include biography text in errors or logs.

## Testing Strategy

Tests use fake biography readers and local reverse-proxy transports; no provider, AI, or real Navidrome is required.

Required tests:

- JSON biography replacement preserves artist image URL, similar artists, external fields, OpenSubsonic fields, status, and headers;
- JSON adds a missing biography and escapes quotes/control characters correctly;
- XML replaces and inserts biography while preserving namespace, attributes, unknown elements, and non-biography text;
- manual effective biography is delivered identically to an automatic effective biography;
- no result, rejected/unselected candidate, reader error, timeout, cancellation, malformed input, unsupported method/content type, compressed response, oversized body, duplicate JSON keys, DTD, deep XML, or upstream failure returns the original response;
- query and form POST artist IDs work and POST bodies reach Navidrome unchanged;
- empty, duplicate, oversized, or injection-shaped IDs do not reach the biography reader;
- successful rewriting updates length and removes invalid validators without changing cookies/CORS/cache headers;
- concurrent requests are race-free and one client's response cannot receive another artist's biography;
- tests prove no provider/AI call is reachable from the proxy package.

## Boundaries

- Always:
  - preserve the upstream response unless a bounded local result and a complete safe rewrite both succeed;
  - change only `artistInfo2.biography`;
  - restore request bodies after bounded inspection;
  - use request cancellation and a short local lookup timeout;
  - treat stored biography text and upstream bodies as untrusted data;
  - keep response rewriting independent of background ingestion/validation.
- Ask first:
  - intercepting native Navidrome `/api/artist` responses in addition to Subsonic;
  - changing response cache policy or adding a biography response cache;
  - supporting compressed upstream response rewriting;
  - changing any field besides `biography`.
- Never:
  - call a provider, MusicBrainz, language detector, translator, or LLM from a client request;
  - return a fetched/rejected/unselected candidate;
  - synthesize text when no effective biography exists;
  - log biography bodies, Subsonic form/query values, credentials, or full responses;
  - use regex/string replacement for JSON or XML;
  - turn a safe pass-through condition into a client-visible proxy error.

## Success Criteria

- A JSON or XML `getArtistInfo2` request for an artist with an effective biography receives that exact Russian text in `biography`.
- Every other upstream response value is preserved semantically.
- An unavailable, invalid, unsafe, timed-out, or oversized enrichment path produces the original Navidrome response.
- The request path performs one bounded local read and zero external/AI operations.
- JSON/XML injection, credential leakage, cross-artist mixing, decompression abuse, and unbounded body reads are prevented by tests.
- Focused tests, full tests, race detector, vet, production build, vulnerability scan, formatting, and diff checks pass.

## Open Questions

None. Native Navidrome API enrichment remains outside this module until explicitly requested.
