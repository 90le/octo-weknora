# Octo integration: first implementation boundary

Status: source assessment, not implemented. 2026-09-14.

## Reuse confirmed in this checkout

- `internal/router/routes_knowledge.go`: hybrid search already uses Viewer and KBAccessRead gates. Knowledge mutation routes have their own gates.
- `internal/handler/knowledge_api_key_scope_test.go`: scoped API keys already limit accessible KBs and intersect Agent/API-key scopes.
- `internal/types/principal.go` and auth middleware: use existing principal concepts; do not invent a second generic RBAC system.
- Native KB IDs remain authoritative. Octo scope bindings reference them; names are presentation only.

## Smallest addition

1. An Octo connection belongs to a WeKnora workspace. Secrets use existing protected configuration patterns and are never sent to the model.
2. Scope identity is connection + group ID + optional subarea ID. An external ID alone is not authorization.
3. A binding selects an existing KB and query access. Asset maintenance is independently checked against existing KB authorization. A default draft target is only a preference among writable targets.
4. A service-authenticated OpenClaw adapter supplies trusted message context. The server validates scope membership and allowed KBs before calling existing search or mutation services.
5. Start with query and binding; never expose an unrestricted API key to a public Agent. Identity mapping and replay handling need explicit implementation before enabling writes.

## Screens

Extend the native KB detail with “Octo 使用范围”. Add an Octo connection/scope page under integrations. Reuse native document management. Do not ship the retired React console inside this Vue application.

## Acceptance

- Same KB can be queried from two authorized scopes.
- Scope without binding cannot query by guessing KB ID.
- Query-only binding cannot create a draft, including by setting it as default.
- Parent rollup cannot mutate subarea knowledge.
- Renaming does not alter identity or ownership.
- Bot identity cannot be replaced by user-supplied text.

The existing server does not yet expose these Octo-specific endpoints. These checks are implementation criteria, not passing test claims.
