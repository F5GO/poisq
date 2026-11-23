# Capability Map: Music Metadata Platform

## Assumptions

1. Poisq remains a Go service backed by SQLite, with Navidrome as the primary media server and poisq Studio as the mutation UI.
2. Audio files become the source of truth for embedded metadata; `navidrome.db` remains an inventory and compatibility source.
3. Every mutation of audio files or artwork continues to require a Studio Staging proposal and explicit approval.
4. Existing Studio and Subsonic behavior remains backward compatible while APIs and database rows gain new fields.
5. Database migrations preserve all current tracks, staging history, biographies, lyrics, jobs, and settings.
6. New implementations may reproduce Jellyfin's architectural ideas, but must not copy GPL-licensed source code.

## Modules

| Module id | Responsibility | Depends on |
|---|---|---|
| `metadata-core` | Define the normalized music metadata model, typed external IDs, multi-value credits, dates, provenance, confidence, locks, SQLite migrations, and compatibility adapters for current `TrackRecord`/`CanonicalTagSet` users. | — |
| `local-metadata-import` | Read embedded tags and technical metadata directly from supported audio files, reconcile them with Navidrome inventory, import embedded/external lyrics and artwork facts, and expose bounded incremental scanning. | `metadata-core` |
| `metadata-merge-policy` | Merge local, Navidrome, NFO, manual, and remote candidates per field using configurable provider order, refresh modes, provenance, confidence, and field locks. | `metadata-core`, `local-metadata-import` |
| `musicbrainz-enrichment` | Resolve artist, release, release group, recording, and release-track identities using album/track/disc/duration evidence; fix Picard-compatible tag mapping; paginate the entire library audit; preserve Folder-as-Album safeguards. | `metadata-core`, `metadata-merge-policy` |
| `staging-writeback` | Turn merged changes into reviewable proposals; implement crash-recoverable write transactions, post-write verification, reconciliation, and real backup/restore Undo for tags and artwork. | `metadata-core`, `metadata-merge-policy`, `musicbrainz-enrichment` |
| `nfo-interoperability` | Import and export `artist.nfo` and `album.nfo` using the normalized model without bypassing locks or Staging, for Jellyfin/Kodi interoperability. | `metadata-core`, `metadata-merge-policy` |
| `audio-analysis` | Import ReplayGain, calculate bounded track/album LUFS jobs, persist analysis provenance, display results, and optionally stage gain-tag writes. | `metadata-core`, `local-metadata-import`, `staging-writeback` |
| `media-enrichment` | Extend artwork to album/artist primary, disc, logo, banner, and backdrop roles; add quality-aware selection and optional ListenBrainz similar-artist references keyed by verified MBID. | `metadata-core`, `metadata-merge-policy`, `musicbrainz-enrichment`, `staging-writeback` |
| `studio-metadata-controls` | Add Studio views and APIs for provenance, provider ordering, refresh modes, field locks, expanded tag editing, audit pagination/progress, transaction recovery, NFO actions, LUFS/ReplayGain, extended artwork, and similar artists. | All provider modules above |
| `expanded-tag-editing` | Complete the normalized/tag-writer model with BPM, rating, all supported credit roles, and bounded custom tags; add previewable single-track and bulk patch editing in Studio without bypassing Staging. | `metadata-core`, `local-metadata-import`, `staging-writeback`, `studio-metadata-controls` |
| `field-change-history` | Persist immutable per-field before/after records for applied metadata operations and provide bounded history plus single/multi-field revert through the existing Staging/writeback path. | `metadata-core`, `staging-writeback`, `expanded-tag-editing` |

## Build Order

`metadata-core` → `local-metadata-import` → `metadata-merge-policy` → `musicbrainz-enrichment` → `staging-writeback` → (`nfo-interoperability`, `audio-analysis`, `media-enrichment`) → `studio-metadata-controls` → `expanded-tag-editing` → `field-change-history`

The three parenthesized modules may be specified and built independently after `staging-writeback`; the Studio module integrates their public contracts last.

## Initiative Completion

The initiative is complete only when every module has an approved `SPEC-<module-id>.md`, an approved implementation plan and task list, passing focused tests, and passing the complete applicable Go test suite plus Studio UI contract tests.
