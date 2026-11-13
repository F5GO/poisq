# Tasks: `bio-ingestion`

- [x] Task 1: Define the bounded provider contract and ingestion orchestrator.
  - Acceptance:
    - `BiographySource` and its result type validate provider, source identity, HTTPS provenance, text, language hint, confidence, evidence, and timestamps before persistence.
    - One artist runs at most four source calls concurrently and all artists share a bounded global semaphore.
    - One provider's failure or empty result does not suppress successful candidates from other providers.
    - Concurrent ingestion for one artist is coalesced; cancellation always clears the in-flight entry.
    - Candidates are upserted as `fetched`; this module does not translate or select a winner.
  - Verify: `go test -race -count=1 -run 'TestIngestion' ./internal/artistbio`
  - Files: `internal/artistbio/ingestion.go`, `internal/artistbio/ingestion_test.go`, `internal/artistbio/types.go`, `internal/artistbio/store.go`

- [x] Task 2: Add the minimal shared HTTP safety hooks required by provider adapters.
  - Acceptance:
    - Existing resilient-client callers remain behaviorally compatible.
    - Provider clients can enforce HTTPS and same-host redirects without exposing the internal `http.Client`.
    - Cancellation, response limits, retry behavior, and circuit breaking remain intact.
    - Redirect and transport errors do not include authorization values, response bodies, or credential-bearing URLs.
  - Verify: `go test -count=1 ./internal/httpclient`
  - Files: `internal/httpclient/resilient_client.go`, `internal/httpclient/resilient_client_test.go`

- [x] Task 3: Implement exact-MBID Wikipedia, TheAudioDB, and Last.fm sources.
  - Acceptance:
    - MusicBrainz relationship lookup is bounded and supplies only exact Wikidata/Wikipedia links for the resolved MBID.
    - Wikipedia fetches a Russian API summary only through an exact linked page/entity, never a name-only result.
    - TheAudioDB queries by MBID and prefers non-empty `strBiographyRU` over other real biography fields.
    - Last.fm calls `artist.getInfo` by MBID with `lang=ru` only when a key is available.
    - Missing links, missing keys, and empty biographies return no candidate without fabricated fallback.
  - Verify: `go test -count=1 -run 'Test(MusicBrainzArtistLinks|WikipediaSource|AudioDBSource|LastFMSource)' ./internal/metadata ./internal/artistbio`
  - Files: `internal/metadata/musicbrainz_client.go`, `internal/metadata/musicbrainz_client_test.go`, `internal/artistbio/source_exact.go`, `internal/artistbio/source_exact_test.go`, `internal/artistbio/provider_http.go`

- [x] Task 4: Implement the Yandex Music identity and biography adapter.
  - Acceptance:
    - Artist search and direct-album evidence are bounded and use existing Yandex request conventions.
    - A Yandex biography is fetched only after exact linkage or decisive shared identity scoring; name-only rank cannot pass.
    - `brief-info` reads only the matched artist's `description.text` and returns no candidate when it is empty.
    - Public unauthenticated requests are preferred; an existing configured token is reused only when required.
    - Provider bodies, tokens, and biography text cannot leak through errors or logs.
  - Verify: `go test -count=1 -run 'TestYandexBiographySource' ./internal/artistbio`
  - Files: `internal/artistbio/source_yandex.go`, `internal/artistbio/source_yandex_test.go`, `internal/artistbio/provider_http.go`, `internal/lyrics/provider_yandex.go`

- [x] Task 5: Implement the Deezer catalog matcher and approved Russian-page extractor.
  - Acceptance:
    - Public JSON search/artist/albums calls provide stable IDs and bounded release evidence.
    - Only a decisively matched numeric artist ID can reach `https://www.deezer.com/ru/artist/{id}`.
    - The extractor requires the approved biography container and semantic description span, decodes entities, converts presentation breaks, and strips remaining tags.
    - Missing/reordered markers, scripts, oversized sections, cross-host redirects, and empty text fail closed.
    - Captured КИНО/Земфира-shaped fixtures and hostile HTML pass without a new parser dependency.
  - Verify: `go test -count=1 -run 'TestDeezer(BiographySource|DescriptionExtractor)' ./internal/artistbio`
  - Files: `internal/artistbio/source_deezer.go`, `internal/artistbio/source_deezer_test.go`, `internal/artistbio/provider_http.go`

- [x] Task 6: Implement optional Apple Music and Discogs sources.
  - Acceptance:
    - Each source is disabled cleanly when its key/token is absent from the existing encrypted key pool.
    - Apple Music uses a Russian storefront, bounded search/release evidence, and documented editorial notes only.
    - Discogs uses bounded artist/release evidence and stores only the selected real artist profile.
    - Same-name candidates without decisive evidence are rejected.
    - 401/403, 429, and transient failures update existing key-pool state without persisting secrets or raw bodies.
  - Verify: `go test -count=1 -run 'Test(AppleMusic|Discogs)BiographySource' ./internal/artistbio`
  - Files: `internal/artistbio/source_catalogs.go`, `internal/artistbio/source_catalogs_test.go`, `internal/artistbio/provider_http.go`, `internal/keypool/manager.go`

- [x] Task 7: Add bounded artist inventory processing and runtime wiring.
  - Acceptance:
    - The Navidrome reader returns deterministic bounded pages containing only validated artist ID and name.
    - Existing local context, resolved identities, and fresh candidates are reused so completed artists skip redundant provider work.
    - The existing background synchronization loop runs a bounded batch after catalog sync and honors shutdown cancellation.
    - `main` composes the existing database, track repository, store, MusicBrainz client, shared AI judge, key pool, and sources without performing network work during construction.
    - No provider or identity request is added to the synchronous Subsonic proxy path.
  - Verify: `go test -count=1 -run 'Test(LoadArtistInventory|BiographyBackgroundBatch)' ./internal/db ./internal/artistbio ./cmd/proxy && go test -count=1 ./internal/proxy`
  - Files: `internal/db/loader.go`, `internal/db/loader_test.go`, `internal/artistbio/background.go`, `internal/artistbio/background_test.go`, `cmd/proxy/main.go`

- [x] Task 8: Complete security, regression, race, and manual endpoint verification.
  - Acceptance:
    - Oversized fields, malformed JSON/HTML, unsafe redirects, same-name ambiguity, cancellation, concurrency, and secret-marker tests pass.
    - Errors and logs contain no tokens, raw provider bodies, biography text, prompts, library paths, or full artist context.
    - No new runtime dependency, provider SDK, AI client, cache framework, database, worker framework, or request-path network call is introduced.
    - Yandex and Deezer manual smoke checks log only HTTP status, artist ID, detected field presence, and text length.
    - Focused tests, race tests, full tests, vet, build, and diff checks pass without touching unrelated user changes.
  - Verify:
    - `gofmt -w internal/artistbio/*.go internal/metadata/musicbrainz_client.go internal/httpclient/*.go internal/db/loader*.go cmd/proxy/main.go`
    - `go test -race -count=1 ./internal/artistbio ./internal/httpclient`
    - `go test -count=1 ./...`
    - `go vet ./...`
    - `go build -o /tmp/poisq-proxy ./cmd/proxy`
    - `git diff --check`
  - Files: only files already touched by Tasks 1–7 plus this checklist
