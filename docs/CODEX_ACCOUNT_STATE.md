# Codex STATE plugin and historical source record

STATE now lives in `plugins/codex-state/` as **baiyu.codex-state 0.1.0**.
The host remains **0.2.7-baiyu**. See the plugin README, MAINTENANCE.md,
sources.lock.json and THIRD_PARTY_NOTICES.md for configuration, packaging,
reviewed upstream baselines and the selective update workflow.

## Current boundary

- The plugin owns harvesting, strict envelopes, active/ready lifecycle,
  complete-response observations and its management UI.
- The host provides generic scoped admission, streaming Forward, resources,
  actions, encrypted PostgreSQL state, CAS and fenced leases. It does not own
  ticket models, plans, TTLs or renewal policy.
- Business requests use the host-selected egress and existing concurrency
  slots. A proxy group shares one account/model ticket lifecycle.
- Explicit disable restores ordinary host requests. Crashes, restarts and
  maintenance preserve the committed scope and block only managed targets.
- Old account codex-ticket endpoints and gateway/account ticket forms have
  been retired. New installation is disabled. Old configuration and tickets
  are not migrated, enabled or deleted automatically.
- Legacy private accounts.extra fields remain server-managed, redacted in
  ordinary APIs/export/audit and stripped on create/import.

The Chinese long-term contract is in
`.wiki/03-模块指南/08-Codex-STATE票据.md`. Fork registrations are
`generic-plugin-runtime-v2`, `codex-state-plugin` and the historical/privacy-only
`openai-codex-state-tickets`.

## Preserved PR ancestry

- PR #7315 head: `3c2f05c957b4b93866318ec8695fc5a28fff70eb`.
- PR #7315 merge: `49a39b6dc1abed30fd227611e8af1108bc427610`.
- PR #7338 head: `09e112fb4997be666b10da48c3b5f65fb8d8b53b`.
- Fork merge of #7338: `a6be2645fffca32a2425d79f822902d7a5aedaff`.

#7315 entered through #7338 parent history; it was not merged twice. The
authors and parent history remain intact when runtime code moves to a plugin.
The original #7338 lifecycle reference was ccodex-sleep-state at
`26b22196bf68b372d0daad9381f686a3321068d4`.

## Reviewed design baselines

- ccodex-sleep-state v0.4.0:
  `b18fabf9ad8e9d7af7d9d0306b623ba6091a39d6`.
- sub2api-codex-turn-state v0.1.6:
  `c8e5483e06ab9de199c3848d9d46fccfc063f88a`.
- sub2api-state-kit release v0.3.4, separately reviewed source:
  `9f2d20ba7db0f76a558ce2c28eefb20128eae562`.

ccodex is a GPL design reference only: no implementation files or dependencies
are imported. Its local Codex configuration takeover, CCS/profile UI,
subscription/node management, memory-only storage, response replacement,
local relay and native upstream WebSocket STATE behavior are not adopted.

Future updates compare all three repositories' HEAD, releases and diffs against
the locked source commits before selectively adapting changes. A release label
does not prove that its binary was built from a reviewed source commit.
