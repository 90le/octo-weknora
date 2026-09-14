# Existing deployment patch provenance

The draft visibility patch was recovered from the previous deployment based on upstream `5db13a1`. It prevents stale index hits for disabled or unpublished manual documents from appearing in search results, including parent/neighbor enrichment. Document state is checked before the primary result cap.

`draft-visibility.patch` preserves the original diff. The corresponding implementation is applied under `internal/application/service`; the original test is retained as `.go.txt`, with the formatted runnable test in the service package.

This is not a new Octo integration feature. Applying a patch and passing formatting checks does not prove compatibility with the current upstream. Run the focused service tests before merging or deploying.

Focused command:

```sh
go test ./internal/application/service -run 'TestDraftKnowledgeVisibility|TestDraftExcludedFromPrimaryAndEnrichmentResults' -count=1
```
