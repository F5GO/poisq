# Plan: `studio-metadata-controls`

1. Add a narrow metadata-control API facade over existing repositories/services.
2. Wire the already constructed metadata, policy, media-info, analysis, NFO, enrichment, and writeback dependencies into AdminServer.
3. Cover read, validation, staging, background-action, and failure-retention contracts with API tests.
4. Add a Metadata workspace to Studio using the current navigation, component language, and API helper.
5. Verify responsive behavior in a real browser, then run focused, race, full, vet, build, and diff gates.
