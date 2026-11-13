# Spec: `bio-validation`

## Objective

Turn fetched, attributable biography candidates into one safe Russian biography without inventing facts.

The module must:

- determine the actual language from biography text rather than trusting a provider label;
- distinguish Russian from other Cyrillic languages and detect materially mixed text;
- preserve already-Russian source text unchanged apart from safe whitespace normalization;
- translate a real non-Russian source into Russian through the existing LLM chain only when necessary;
- constrain translation sentence-by-sentence so text cannot be summarized, expanded, or supplemented with new facts;
- reject uncertain, malformed, promotional, empty, or insufficiently grounded results rather than fabricate a fallback;
- score accepted candidates deterministically and atomically select the best automatic result;
- retain original text, Russian text, source identity, source URL, language evidence, and translation evidence.

This module does not fetch providers, resolve artist identity, alter Subsonic responses, or implement manual Studio overrides.

## Assumptions

1. A faithful Russian translation of a real sourced biography is acceptable; entirely generated or source-free prose is not.
2. Existing Groq/Gemini fallback ordering remains unchanged and is reused for ambiguous language classification and translation.
3. Obvious Russian text should avoid an AI call for latency and cost.
4. If every source and translation fails validation, the correct result is no automatic biography, not invented text.
5. Manual text remains optional and continues to override the automatic result through the existing store contract.

## Tech Stack

- Existing Go module and standard library Unicode support.
- Existing `ai.TextGenerator` and configured Groq/Gemini fallback.
- Existing `artistbio.Store`, candidate rows, automatic selection, and source provenance.
- Existing background processing path from `bio-ingestion`.
- No new language-model client, language-detection dependency, embedding service, database, or worker framework.

## Commands

```sh
gofmt -w internal/artistbio/*.go internal/ai/*.go cmd/proxy/main.go
go test -count=1 ./internal/artistbio ./internal/ai
go test -race -count=1 ./internal/artistbio ./internal/ai
go test -count=1 ./...
go vet ./...
go build -o /tmp/poisq-proxy ./cmd/proxy
git diff --check
```

## Project Structure

```text
internal/artistbio/language.go          Deterministic script/Russian signals and bounded result
internal/artistbio/language_test.go     Russian, Ukrainian, mixed, Latin, short, and adversarial cases
internal/ai/biography_translation.go    Strict language fallback, translation, and verification contracts
internal/ai/biography_translation_test.go
                                        Prompt isolation and malformed/hallucinated result rejection
internal/artistbio/validation.go        Candidate validation, scoring, persistence, and selection
internal/artistbio/validation_test.go   End-to-end candidate outcomes and ranking
internal/artistbio/store.go             Bounded candidate listing and atomic validation updates
cmd/proxy/main.go                        Reuse the existing LLM and background composition
```

## Code Style

Deterministic language and grounding checks run before any automatic selection:

```go
analysis := DetectBiographyLanguage(candidate.OriginalText)
if analysis.Language == "ru" && analysis.Confidence >= 0.90 && analysis.RussianShare >= 0.80 {
	return validator.AcceptOriginal(ctx, candidate, analysis)
}

translated, err := translator.Translate(ctx, candidate.OriginalText)
if err != nil || !translated.Grounded {
	return validator.Reject(ctx, candidate, "translation_not_grounded")
}
```

Rules:

- keep pure language/scoring functions separate from store writes;
- use compact stable evidence codes, not free-form AI explanations;
- pass untrusted biography text as JSON user data, never in the instruction role;
- cap data before serialization and validate every model field against supplied sentence IDs;
- reuse original candidate/source rows; do not create a second translation cache.

## Language Contract

`LanguageAnalysis` contains:

- normalized language code (`ru`, `uk`, `be`, `en`, another supported code, `mixed`, or `und`);
- confidence in `[0,1]`;
- Russian character/token share in `[0,1]`;
- bounded stable evidence codes;
- whether strict AI classification is required.

Deterministic detection uses:

- Unicode script proportions;
- Russian-, Ukrainian-, and Belarusian-specific letters;
- bounded high-frequency function-word sets;
- letter/token counts that exclude URLs, markup, punctuation, and digits;
- minimum evidence thresholds so very short text remains `und`.

AI classification is allowed only when deterministic evidence is ambiguous. It receives bounded JSON and may return only one allowlisted language code, confidence, and supplied evidence codes. It cannot rewrite biography text.

## Russian Acceptance Rules

- Direct acceptance requires language `ru`, confidence `>= 0.90`, Russian share `>= 0.80`, and at least 80 meaningful characters.
- Predominantly Russian text with small names/foreign phrases remains original.
- Ukrainian, Belarusian, mixed, Latin-script, or uncertain text is not labeled Russian merely because it uses Cyrillic.
- Provider language hints are supporting evidence only and cannot override detected text.
- Boilerplate-only text, link farms, repeated fragments, and text over `MaxBiographyBytes` are rejected.

## Grounded Translation Contract

- Split the source into at most 120 bounded paragraphs/sentences with stable numeric IDs.
- The model returns one JSON item for every supplied ID and no additional IDs.
- Every translated item must be non-empty and remain within a bounded length ratio to its source.
- Preserve all numbers, years, URLs, email addresses, catalog identifiers, and explicit quoted names found in the source.
- Reject Markdown fences, unknown fields, trailing data, missing/duplicate IDs, oversized output, and non-Russian translated text.
- Run a second strict grounding verdict through the same existing fallback only when deterministic preservation checks are insufficient.
- The verifier receives source and translation pairs and can return only `grounded`, `not_grounded`, or `uncertain` plus supplied evidence codes.
- Only `grounded` translation with confidence `>= 0.95` can become `accepted`.
- Translation may lower confidence but never raise the artist-identity confidence or add source evidence.
- Original text is never overwritten; Russian translation is stored separately.

## Candidate Scoring and Selection

Accepted candidates receive a deterministic score from:

1. artist match confidence;
2. direct Russian source versus verified translation;
3. actual language confidence/Russian share;
4. source reliability tier;
5. useful bounded text length and low boilerplate/repetition;
6. freshness as a tie-breaker only.

Source reliability is explicit and stable, not learned from current candidates. A longer text cannot beat a decisively better identity match. Manual override is not part of this ranking and remains higher priority at read time.

Selection runs in one transaction:

- validate/update candidate language, Russian text, status, confidence, and evidence;
- verify the candidate still belongs to the artist;
- choose the highest accepted candidate using deterministic ordering;
- update `artist_biographies.automatic_candidate_id` atomically.

## Testing Strategy

All AI tests use fake `TextGenerator`; no live model or provider is required.

Required tests:

- clear Russian is accepted without an AI call;
- Ukrainian and Belarusian text are not mislabeled Russian;
- mixed Cyrillic/Latin and very short text remain mixed/undetermined as appropriate;
- provider `ru` hint cannot override non-Russian text;
- prompt-injection text remains JSON data and cannot change the contract;
- sentence IDs cannot be missing, duplicated, reordered into a different source, or invented;
- new numbers, URLs, dates, names, paragraphs, or unsupported claims reject translation;
- malformed, fenced, trailing, oversized, non-Russian, and low-confidence model output is rejected;
- provider/AI outage leaves candidates retryable and never selects unvalidated text;
- direct Russian beats an otherwise equal translation; stronger identity beats provider preference;
- refreshing a selected candidate is atomic and manual override remains untouched;
- concurrent validation of one artist coalesces or remains race-free;
- errors and logs contain no biography bodies, prompts, API keys, or library paths;
- complete existing ingestion, identity, lyrics, proxy, and Studio tests remain green.

## Boundaries

- Always:
  - keep original source text and attribution;
  - detect actual language before accepting or translating;
  - perform deterministic preservation checks before AI grounding verification;
  - use the existing AI fallback and candidate store;
  - fail closed on uncertainty, malformed data, or insufficient grounding;
  - keep all work in the existing background path.
- Ask first:
  - adding a language-detection, translation, embedding, or NLP dependency;
  - changing global LLM ordering/models;
  - lowering Russian, grounding, or identity confidence thresholds;
  - permitting summarization instead of translation.
- Never:
  - generate a biography without real source text;
  - add facts, dates, names, achievements, genres, or opinions absent from the source;
  - accept provider language labels without inspecting text;
  - place biography text in an AI system instruction or log raw prompts/responses;
  - overwrite original text with a translation;
  - select `fetched` or `translation_pending` candidates as automatic output;
  - block a client request on detection, translation, verification, or selection.

## Success Criteria

- Every automatically selected biography is Russian by measured text, not provider assertion.
- Every translated biography maps one-to-one to a real stored source and passes strict grounding checks.
- No source-free or unverified AI prose can enter the effective biography.
- Clear Russian candidates require no model call and select quickly in the background.
- Provider/AI failures leave safe retryable state and never erase an earlier valid automatic result.
- Deterministic ranking selects the best accepted candidate atomically while preserving manual precedence.
- Focused tests, race tests, full tests, vet, build, and diff checks pass.

## Open Questions

None. This module intentionally prefers no automatic biography over an invented or insufficiently grounded one.
