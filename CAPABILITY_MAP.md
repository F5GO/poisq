# Capability Map: Russian Artist Biographies

| Module id | Responsibility | Depends on |
|---|---|---|
| `bio-store` | Persistent candidates, selected biography, cache state, provenance, and optional manual override | — |
| `artist-identity` | Build artist candidates and resolve identity with deterministic evidence plus an AI arbiter | `bio-store` |
| `bio-ingestion` | Fetch biography candidates from Yandex Music, Deezer, Apple Music, Wikipedia, TheAudioDB, Last.fm, Discogs, and approved official sources | `artist-identity` |
| `bio-validation` | Detect actual language, verify grounded translation, score candidates, and select the best safe Russian result | `bio-ingestion` |
| `bio-delivery` | Replace only `biography` in JSON/XML `getArtistInfo2` responses while preserving upstream data | `bio-store`, `bio-validation` |
| `studio-overrides` | Optional manual biography override, source URL, lock state, and reset to the automatic result | `bio-store` |

Build order: `bio-store` → `artist-identity` → `bio-ingestion` → `bio-validation` → `bio-delivery`, `studio-overrides`.
