# AGENTS.md

## graphify

This project has a knowledge graph at graphify-out/ with god nodes, community structure, and cross-file relationships.

When the user invokes `$graphify` (or `/graphify` in a host that supports slash commands), use the installed graphify skill or instructions before doing anything else.

Rules:
- For codebase questions, first run `graphify query "<question>"` when graphify-out/graph.json exists. Use `graphify path "<A>" "<B>"` for relationships and `graphify explain "<concept>"` for focused concepts. These return a scoped subgraph, usually much smaller than GRAPH_REPORT.md or raw grep output.
- Dirty graphify-out/ files are expected after hooks or incremental updates; dirty graph files are not a reason to skip graphify. Only skip graphify if the task is about stale or incorrect graph output, or the user explicitly says not to use it.
- If graphify-out/wiki/index.md exists, use it for broad navigation instead of raw source browsing.
- Read graphify-out/GRAPH_REPORT.md only for broad architecture review or when query/path/explain do not surface enough context.
- After modifying code, run `graphify update .` to keep the graph current (AST-only, no API cost).

## Local safety boundary

- Graphify is only a local code-architecture navigation index for this setup.
- For WeKnora, use code-only AST extraction/update; do not semantically ingest project documents, credentials, or media.
- Never push Graphify output to WeKnora's Neo4j, FalkorDB, MCP, or any production/runtime data store.
- Keep `graphify-out/` as a local derived artifact; do not commit it or treat graph edges as proof. Verify source code and tests before changing behavior.

## Octo product and development governance

- [OCTO-REQUIREMENTS.md](OCTO-REQUIREMENTS.md) is the authoritative Octo product requirements document; use [OCTO-STATUS.md](OCTO-STATUS.md) for recorded status and evidence. Keep requirements in one place and link to them from plans and implementation documentation.
- Use a common session-artifact and channel-delivery path; do not add format-specific tools or switches. Reuse existing knowledge-base business permissions and preserve their authorization boundaries.
- A Skill describes a workflow, not authorization. Sandbox execution, Skills, network and delivery must obey the applicable independently granted permissions; legacy Markdown configuration must not silently expand them.
- Do not remove existing tools until replacement coverage, compatibility, migration and rollback are evidenced. Report source checks, automated tests, real end-to-end acceptance and deployment separately; plans are not implemented or deployed features.
