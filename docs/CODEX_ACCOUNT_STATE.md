# Account-level Codex STATE tickets

This document records both the imported PR baseline and the long-term fork contract for Codex `x-codex-turn-state` tickets. It is intentionally explicit about provenance: the merged baseline and the fork-owned reliability work have different maintenance ownership.

## Provenance and license boundary

- [Sub2API PR #7315](https://github.com/Wei-Shaw/sub2api/pull/7315), head `3c2f05c957b4b93866318ec8695fc5a28fff70eb` and merge commit `49a39b6dc1abed30fd227611e8af1108bc427610`, introduced background STATE harvesting, persistence, injection and model gating on top of the existing turn-state relay and provenance guard.
- [Sub2API PR #7338](https://github.com/Wei-Shaw/sub2api/pull/7338), head `09e112fb4997be666b10da48c3b5f65fb8d8b53b`, added account-level controls, dynamic-proxy acquisition, fixed-business-proxy verification, persistence and response watchdog behavior. Fork merge commit `a6be2645fffca32a2425d79f822902d7a5aedaff` preserves that PR's parent history and author attribution, including #7315; #7315 was not merged a second time.
- PR #7338 referenced [`ccodex-sleep-state@26b22196`](https://github.com/gylive/ccodex-sleep-state/tree/26b22196bf68b372d0daad9381f686a3321068d4) for lifecycle and status-presentation ideas.
- The fork reviewed [`ccodex-sleep-state@b18fabf9`](https://github.com/gylive/ccodex-sleep-state/commit/b18fabf9ad8e9d7af7d9d0306b623ba6091a39d6) on 2026-09-19 for strict envelope parsing, immutable snapshots and active/ready lifecycle ideas.

`ccodex-sleep-state` is GPL-3.0 while this repository is LGPL-3.0-or-later. The fork does **not** copy that project's source files, import its implementation dependencies or claim code provenance from it. The mechanisms described below are independently implemented for Sub2API's existing service, repository and scheduler boundaries. Links and commit IDs are retained so future maintainers can compare design evolution without silently importing GPL code.

The following reference-project behaviors were explicitly reviewed but are **not adopted** by this fork:

- Local Codex configuration takeover, CCS/profile management and the reference project's local web panel.
- Subscription import, proxy-node pool ownership, node lifecycle and per-node random-exit management.
- Memory-only STATE storage; Sub2API persists private server-managed snapshots in `accounts.extra`.
- Replacing or blocking business response bodies on a mismatch; Sub2API's watchdog observes complete responses without changing bytes.
- The reference project's `on_demand`/`standby` user refresh policy, local API-key relay and single-user service model.
- Native upstream WebSocket STATE management, account pause/restore orchestration and local configuration restoration.

## Imported #7338 baseline

The merged PR baseline provides:

- A global gateway switch and shared dynamic harvest proxy.
- Explicit opt-in for each non-shadow OpenAI OAuth/setup-token account.
- Manual Pro/Team plan selection and a configured target model.
- Candidate acquisition through the dynamic proxy followed by verification through the account's fixed business proxy.
- Persistence in private `accounts.extra` state before publication to process memory.
- A one-hour local lifetime, renewal ten minutes before expiry, at most eight attempts per round and a five-minute failure cooldown.
- A response watchdog that observes completed responses without changing response bytes or replaying the business request.
- Strict account/model scheduling when an opted-in target has no usable ticket.

The merged baseline management API is:

```text
GET  /api/v1/admin/accounts/:id/codex-ticket
PUT  /api/v1/admin/accounts/:id/codex-ticket
POST /api/v1/admin/accounts/:id/codex-ticket/harvest
```

Account reads, exports and audit logs must not expose ticket material or proxy credentials. New, imported and copied accounts remain disabled by default. No database migration is required because private state is stored in `accounts.extra`.

## Configure and operate

1. In **Settings → Gateway → Codex**, enable STATE tickets and configure the shared dynamic harvest proxy.
2. Save a fixed business proxy for the account, then reopen **Accounts → Edit**.
3. Enable only the required model rows and select Pro or Team for each model. This is an explicit operator setting; it does not infer or modify the upstream subscription.
4. Wait for an active ticket or start a manual harvest for one model. Management views show status, remaining lifetime, renewal/cooldown, strikes and redacted errors, never the STATE blob.

A proxy username containing `{sid}` receives a new random SID on each attempt. Existing 1024proxy `-sid-...-t-N` usernames remain supported, but a different SID does not guarantee a different egress address. If the harvest proxy itself requires an outer HTTP CONNECT proxy, use the deployment-only `gateway.openai_codex_ticket.harvest_dial_proxy_url`; it is not a persisted admin setting and does not alter business traffic.

Legacy deployment fields such as `target_length`, `models`, `fail_closed`, `ttl_seconds` and `refresh_before_seconds` may remain parseable for configuration compatibility. Account/model policy, strict behavior and the lifecycle rules below are authoritative for the fork implementation.

## Fork reliability contract

The fork enhancement is model-scoped. Each account may independently configure:

```text
gpt-6-astra
gpt-5.6-sol
gpt-5.6-terra
```

State is isolated by account, canonical outbound model, account configuration revision and fixed-proxy fingerprint. A ticket captured for one model, account or proxy binding must never satisfy another one. Legacy single-model `{enabled, model, ticket_plan}` data remains readable and is upgraded to the model map on the next explicit save.

Each model owns an `active` and optional `ready` ticket:

- `active` is the immutable snapshot that may be attached to new requests.
- `ready` is a verified renewal candidate and must not replace a healthy active ticket immediately.
- Promotion occurs only when active expires, becomes unusable or reaches the configured consecutive anomaly threshold.
- A delayed response may only report against the exact ticket version used by that request; it cannot invalidate or replace a newer version.
- Renewal failure keeps a still-valid active ticket until its original expiry and never extends its lifetime.

Tickets are parsed as strict Base64 URL-safe envelopes. Validation includes the envelope version, internal issue time, encrypted block count and time window. Pro uses the expected 10-block envelope and Team uses the expected 12-block envelope. Lengths such as 292 or 332 remain observations for operators, not the sole validity test.

The validity contract is one hour from the internal issue time. Future issue times tolerate at most 30 seconds of clock skew, and the final 30 seconds are not assigned to new requests. Renewal starts ten minutes before expiry.

## Client STATE precedence

The gateway's existing client STATE provenance guard and background ticket injection form one ordered contract:

1. A client-provided STATE known to originate from the selected account is preserved.
2. A client-provided STATE known to originate from another account is removed before forwarding.
3. A background active ticket is injected only when no usable client STATE remains.
4. If the selected account/model is explicitly enabled but has no usable active ticket, strict scheduling excludes only that account/model combination.

Strict exclusion must not disable the whole account, alter global health or affect other models. TTFT fail-open and other scheduler recovery paths must still respect this hard ticket gate.

## Acquisition, locking and watchdog

Harvest work is coordinated across replicas using the existing Redis leader-lock and PostgreSQL advisory-lock infrastructure. Lock identity includes account ID, canonical outbound model, account configuration revision and fixed-proxy fingerprint. In a multi-instance deployment, inability to establish either lock skips that acquisition round rather than running duplicate uncoordinated harvesters.

The watchdog only turns complete successful responses into observations. Model mismatch or a valid anomalous STATE increments the strike count for the exact active version. A normal matching response clears the count. Promotion or invalidation requires two consecutive anomalies; malformed, incomplete, oversized or stale-version observations do not directly replace active state.

Acquisition and watchdog processing never replay a production request. HTTP 401, 403 or 429 during acquisition stops the current round. Ticket and proxy values remain redacted from management responses, logs and audit records.

## Transport boundary

HTTP Responses, compact/compat response paths and the existing WebSocket-to-HTTP bridge may reuse the same ticket lifecycle only where the request is still handled as an HTTP upstream exchange.

Native upstream WebSocket ticket isolation is **not supported** by this fork contract. An established native WebSocket remains bound to its connection-level account and cannot be safely redispatched or upgraded to a different account/model ticket mid-connection. Future native WebSocket support requires a separate connection-lifecycle design and must not be inferred from HTTP-bridge tests.

## Operations and compatibility

- The global master switch and every account/model switch default to off.
- Initial rollout must enable one test account/model at a time after synthetic and VM validation.
- Dynamic harvest proxy changes prevent stale in-flight work from publishing; they do not silently extend an existing ticket.
- General account edits preserve server-managed ticket fields. Import, copy and create flows do not accept or duplicate them.
- Database backups may contain private `accounts.extra` material and must be protected as secrets.
- Reverting application code does not remove persisted private fields.

The machine-readable fork contract is registered as `openai-codex-state-tickets` in the fork extension audit catalog. The Chinese long-term design and maintenance notes are in `.wiki/03-模块指南/08-Codex-STATE票据.md`.
