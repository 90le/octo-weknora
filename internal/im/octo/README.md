# Octo native IM adapter — implementation checkpoint

This package targets WeKnora's `im.Adapter` / `im.FileDownloader` contracts. It
does not embed OpenClaw or call a second conversational Agent. It is not yet
registered in the production IM factory catalog.

## Implemented

- WuKongIM v4 binary framing, per-connection X25519 handshake and AES-CBC payload
  decoding; packet size limits, bounded decoding, full decimal message IDs.
- WSS connection deadlines, heartbeat, cancellation and inbound acknowledgments.
  An accept callback must acknowledge only deliberately ignored or accepted input.
- Bot registration, reconnect backoff, identity consistency and server-kick stop.
- Native DM, group and subarea mapping, UID mentions, quoted text/file metadata,
  full reply with original quote snapshot, UTF-16 mention positions.
- Native attachment download with size limits, approved CDN origin, guarded
  network access and no credential forwarding or redirects.
- Unsigned HTTP callbacks rejected. Structured events and self messages do not
  become Agent questions. No automatic file-to-KB publishing here.
- Scoped GROUP.md / THREAD.md reads using official APIs, with explicit origin,
  version and parent/subarea labels. No global files or cross-account caches;
  deletion is visible on the next fetch, failed child reads do not silently use
  only the parent as complete context. Call only after scope authorization.
- Bundled `octo-bot-api` and `octo-card-message` instruction files, adapted for
  WeKnora and parsed through its native SkillSource interface. They carry no
  executable directory, credentials or automatic registration/greeting behavior.
  They are not yet attached to live Agent turns or installed globally.

## Required before activation

The native IM execution path now has an `ExecutionAuthorizer` hook: Octo fails
closed without it, authorizes before queue admission, rechecks the scope revision
before QA/attachments, uses an isolated read-only Agent copy with explicit KBs,
and does not auto-ingest channel attachments. The actual Octo membership/binding
authorizer, session revocation/isolation and factory/UI wiring remain pending.

The existing IM service falls back to Agent-configured KBs when no explicit KBs
are supplied. It also supports channel-level automatic attachment ingestion.
Neither behavior is an authorization mechanism for public Octo groups.

1. Add a trusted execution scope hook to native IM. Resolve tenant + configured
   Bot identity + parent group + subarea + verified native sender before executing
   QA or downloading/ingesting attachments. Recheck at execution and tool access;
   empty bindings must not select the Agent's entire knowledge collection.
2. Wire `octo_scopes` through a dedicated runtime authorizer, not its admin HTTP
   diagnostics. Include membership, Bot policy, inherited read bindings, removal
   and stale identity handling. Keep public IM principals read-only by default.
3. Namespace dedup by channel/Bot and maintain per-user sessions partitioned by
   the full native group/subarea channel key. A subarea is not a quote thread.
   Native `resolveUserSession` currently keys by platform/user/chat/tenant/agent
   without IMChannelID in its lookup. Thus two Bots sharing an Agent can collide,
   including DMs with empty ChatID. Add an explicit channel/session namespace;
   do not change the real outgoing channel ID to disguise this missing dimension.
   Revoke/reset stale session knowledge access when bindings change.
4. Persist inbound acceptance and downstream failure state. The current runner
   delegates durable queuing to its `accept` callback; it does not promise
   exactly-once Agent execution or durable delivery on its own.
5. Add native factory registration, credentials/identity handling, frontend IM
   selection and readiness reporting only with the above enforcement. Never
   present a socket upgrade as authenticated/usable before CONNACK.
6. Live isolated Bot acceptance: mention and no-mention policy, interleaved
   people, subarea isolation, proper user-card mentions, quote preview, file
   download/delivery, reconnect and unchanged private OpenClaw Bot behavior.

## Explicitly pending

No production switch, no real Bot registration or messaging was performed by the
unit tests. Real protocol parity and deployment remain unverified. Outbound file
upload, rich card/stream lifecycle and card callbacks are not yet implemented.
Skills, CLI and MCP remain native Agent tool capabilities requiring their own
authorization; the transport never grants them. Attaching channel skills and scope
documents to authorized native Agent turns remains part of runtime integration.
Mention preferences and member display-name hydration are still pending.
The initial host policy accepts only official `im.deepminer.com.cn` WSS and
`cdn.deepminer.com.cn` attachments; enterprise/custom endpoints need explicit
administrator policy rather than trusting a server-returned arbitrary URL.

## Sources and tests

Protocol and API reference: Apache-2.0
[Mininglamp-OSS/openclaw-channel-octo](https://github.com/Mininglamp-OSS/openclaw-channel-octo/tree/6b5b3f14457df72ab95d2159ad50214faed793e4),
especially `src/socket.ts`, `src/types.ts`, `src/api-fetch.ts`, `src/group-md.ts`.
The Go implementation is local to this package; upstream protocol-derived
constants and key derivation are kept for interoperability, not proposed as a
new cryptographic design.

The bundled instructions are locally authored adaptations of the upstream skill
design, not verbatim copies of its OpenClaw installation/credential instructions.
GROUP.md and THREAD.md are channel-authored context, not system policy. Thread
guidance can refine group conversation conventions but never grant wider access.

Run `go test ./internal/im/octo/wire` for transport fixtures and
`go test ./internal/im/octo/...` for adapter contracts. Adapter tests require the
same Linux/CGO dependencies as native WeKnora IM. No real tokens in test fixtures.
