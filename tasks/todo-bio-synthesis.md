# Tasks: `bio-synthesis`

- [x] Task 1: Add persistent on-demand lifecycle, summary selection, and legacy cleanup.
  - Acceptance:
    - Store represents `queued`, `running`, `ready`, `no_data`, and `retryable` with bounded timestamps/cooldown/error classes.
    - `ai_summary` is committed atomically with bounded source mapping; precedence is `manual → ai_summary → provider → none`.
    - Rebuild/failure preserves manual text and the last valid automatic result.
    - `local_library` candidates/selections are removed idempotently without touching external/manual data.
    - Startup recovers stale non-terminal state and performs zero inventory/provider/AI work.
  - Verify: focused `internal/artistbio` and `internal/db` store/restart/cleanup tests.
  - Files: `internal/db/poisq_db.go`, `internal/artistbio/types.go`, `internal/artistbio/store.go`, corresponding tests.

- [x] Task 2: Implement deterministic bounded evidence preparation and versioning.
  - Acceptance:
    - Only accepted identity-verified external candidates enter evidence; manual/local/prior-summary text is excluded.
    - At most four origins, 1,200 characters per extract, and 4 KiB total are retained with deterministic deduplication.
    - Stable SHA-256 versions change exactly when eligible evidence changes.
    - Catalog sufficiency is decided without AI and unchanged versions make zero search/model calls.
  - Verify: focused deterministic, multilingual, duplicate, boundary, and hash tests.
  - Files: `internal/artistbio/synthesis.go`, `internal/artistbio/synthesis_test.go`, existing language helpers only if necessary.

- [x] Task 3: Add bounded Tavily discovery and encrypted key configuration.
  - Acceptance:
    - Tavily Basic Search uses existing resilient HTTP/key-pool patterns with bounded requests/responses and no SDK/scraping.
    - Only cited source-native HTTPS result records survive; malformed/private/local/unsafe/low-quality/duplicate results fail closed.
    - Successful/empty discovery caches for 90/30 days; quota/transport failures honor cooldown and preserve prior output.
    - Studio/key APIs accept `tavily` without exposing keys in responses or logs.
  - Verify: offline HTTP, redirects/SSRF, size, quota, cache, key encryption/redaction, and catalog-only fallback tests.
  - Files: `internal/artistbio/web_search.go`, tests, `internal/keypool/manager.go`, existing admin key files/tests.

- [x] Task 4: Implement strict collector, editor, and verifier AI roles.
  - Acceptance:
    - Each role has a separate bounded prompt/JSON contract and uses the existing `TextGenerator` fallback.
    - Collector cites only supplied search result IDs; editor cites only supplied evidence; verifier checks only cited sentence evidence.
    - Russian 400–1200-character output, exact sentence/source union, response bounds, duplicate/trailing JSON rejection, and per-sentence confidence `>= 0.95` are enforced.
    - Unsupported, contradictory, invented, promotional, injected, or uncertain content rejects the whole new summary.
    - Errors/logs expose no prompts, evidence prose, summary text, paths, or secrets.
  - Verify: fake-generator contract, injection, attribution, multilingual grounding, malformed output, and outage tests.
  - Files: `internal/ai/biography_synthesis.go`, `internal/ai/biography_synthesis_test.go`.

- [x] Task 5: Build the detached keyed synthesis coordinator.
  - Acceptance:
    - Queue holds at most 100 distinct artists, runs one pipeline, and starts at most three artists/minute.
    - Concurrent proxy/Studio requests join one artist job; admission and state persist before external work.
    - Accepted jobs survive requester cancellation under a maximum 90-second service timeout.
    - Terminal state and cooldown prevent repeated costs; failures preserve old output and store bounded error metadata only.
    - Periodic/startup artist-wide biography processing is removed.
  - Verify: queue, singleflight, pacing, restart, cancellation, quota, no-data, atomic commit, and race tests.
  - Files: `internal/artistbio/background.go` or a smaller replacement coordinator file, tests, `cmd/proxy/main.go`.

- [x] Task 6: Trigger on demand from `getArtistInfo2` with a measured configurable wait.
  - Acceptance:
    - Only a valid authenticated request plus successful matching upstream response can enqueue a job.
    - Cache hits are immediate; misses enqueue/join once, wait once, re-read once, and otherwise pass through safely.
    - `BIO_FIRST_REQUEST_WAIT` accepts 5–30 seconds, uses 20 seconds provisionally, and invalid values fall back safely.
    - Request cancellation ends only the wait; external work never runs in the request goroutine and credentials/user data never enter jobs.
  - Verify: focused proxy miss/hit, 5/30/invalid configuration, timeout, cancellation, concurrency, unknown artist, auth, and pass-through tests.
  - Files: `internal/config/config.go`, `internal/proxy/artist_info.go`, `internal/proxy/proxy.go`, tests, `cmd/proxy/main.go`.

- [x] Task 7: Add safe per-artist rebuild to Studio.
  - Acceptance:
    - Selected artist exposes `Пересобрать описание` and bounded job status.
    - POST action is authenticated, CSRF-protected, method-restricted, and validates one bounded artist ID.
    - It invalidates only that artist, preserves manual/last valid automatic text, clears eligible cooldown/no-data state, and joins/enqueues one job.
    - Global reset invalidates automatic state/cache but enqueues no artists.
  - Verify: handler/UI authorization, CSRF, method, ID, scope, state, singleflight, and global-reset tests.
  - Files: existing `internal/admin/api_artist_biographies.go`, handler tests, existing Studio assets/templates.

- [ ] Task 8: Complete live endpoint, latency, security, and release verification.
  - Acceptance:
    - Manual Tavily and applicable catalog endpoint checks succeed or degrade exactly as specified under free quota.
    - At least five cold runs per representative class record content-free stage and total `p50/p90/p95`.
    - Release wait becomes `clamp(total p90 + 2s, 5s, 30s)` after supported-client timeout checks; the measured value is documented.
    - Full tests, race detector, vet, build, vulnerability scan, formatting, and diff checks pass.
    - Final review finds no SSRF, injection, unsafe redirects, secret/content logging, unbounded inputs, request storms, cross-artist mixing, or unrelated-file changes.
  - Verify:
    - `gofmt -w internal/artistbio/*.go internal/ai/*.go internal/proxy/*.go internal/admin/*.go internal/config/*.go cmd/proxy/main.go`
    - `go test -count=1 ./internal/artistbio ./internal/ai ./internal/proxy ./internal/admin`
    - `go test -race -count=1 ./internal/artistbio ./internal/ai ./internal/proxy`
    - `go test -count=1 ./...`
    - `go vet ./...`
    - `go build -trimpath -o /tmp/poisq-proxy ./cmd/proxy`
    - `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`
    - `git diff --check`
  - Files: only files required by Tasks 1–7 plus measured release-default/config documentation.
