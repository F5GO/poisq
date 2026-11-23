# Plan: `expanded-tag-editing`

1. Extend `musicmeta.Document`, validation, SQLite schema and repository with BPM, rating and ordered custom tags.
2. Extend FFprobe import, canonical tag mapping and post-write verification for added fields and all credit roles.
3. Introduce a typed metadata patch/diff layer shared by single and bulk editing.
4. Add bounded preview/apply APIs; bulk apply creates pending Staging proposals and reports per-track results.
5. Add Studio selection, bulk editor and per-track extended fields with diff preview.
6. Run focused, race, full, vet, build and diff verification.
