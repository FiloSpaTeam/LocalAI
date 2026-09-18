# opencode-plugin-localai: durable result recovery handoff

## Scope and source

This LocalAI change adds durable root job identity and status/result lookup for the embedded LocalAGI executor. OpenCode remains the main conversation and plan owner. There are no plugin implementation, deployment, UI, distributed executor, retry or cancellation changes in this repository. Cogito and LocalAGI pins remain unchanged.

Read `docs/content/features/agent-job-recovery.md` for the exact endpoint/JSON and operational contract. Implementation sources: `core/services/jobs/chat_store.go`, `core/services/agentpool/durable_chat.go`, `core/services/agentpool/agent_interactions.go`, `core/http/endpoints/localai/agent_job_lookup.go`, and `core/http/routes/agents.go`.

## Client procedure

1. Authenticate to LocalAI and use the public agent alias. Keep inference provider configuration separate from these agent operations. Subscribe to `/api/agents/:name/sse` before submitting approved work.
2. POST `/api/agents/:name/chat` with `{message, conversation_id}`. For a new embedded root the 202 receipt adds `job_id`, equal to `message_id`. Persist that receipt against the OpenCode session and delegated task BEFORE relying on later SSE delivery. Existing status/message_id/question_id fields remain compatible.
3. A free-text question answer or a live-loop message injection has NO new job_id. Do not register another delegated job; retain the original root ID. Child completions are not root completions. Events routed through the chat lifecycle carry root job_id; other existing stream callbacks may not, so retain conversation/message correlation too.
4. On reconnect, timeout waiting for a report, or a `persistence_unavailable` event, GET `/api/agents/:name/jobs/:job_id`. This is an authoritative retained result lookup, not SSE replay. Do not resubmit the original POST.
5. Interpret every response:
   - `accepted`: admission persisted; wait/poll conservatively.
   - `running`: execution has reported processing; no heartbeat guarantee.
   - `waiting_user`: fetch pending questions/plans and relay answers to the existing endpoints.
   - `waiting_agents`: delegates active; also fetch pending because children can need input.
   - `completed`: deliver the returned `result` once. Key delivery deduplication by job_id in your own session state; transport delivery itself is not exactly-once. Result can be an empty string. Root terminal persistence precedes ordinary terminal SSE.
   - `failed`: surface safe `error.code=execution_failed` and its message. Do not automatically retry; side effects may have occurred.
   - `interrupted`: surface `error.code=execution_interrupted`; startup found no durable terminal outcome. Do not infer failure or success, resume, or retry. Ask the user to inspect the task/repository before deciding next work.
   - 404: unknown, wrong owner/alias or purged record. Do not disclose a guess about another user or treat this as proof of non-execution.
   - 410: retained owner-scoped tombstone; report unavailable expired result. Do not retry the task.
   - 401/403: fix client authentication/permissions, not task execution.
   - 501: backend executor does not support durable lookup. Do not promise reconnect recovery in that mode.
   - 503: storage unavailable, unresolved outcome persistence failure, or pool initializing. Keep task outcome unknown and retry GET with bounded backoff/user visibility; never POST automatically.
   - 500/network/malformed response: outcome remains unknown. Retain identifiers and show the lookup failure.
6. Fetch `/pending?conversation_id=...` independently for interactions. It returns all pending questions but only the oldest plan. Refresh after decisions; absence of another plan in a nonempty plan snapshot does not expire that plan. These registries remain volatile and disappear on restart even when job results persist.

## Failure guarantees and boundaries

Before admission persistence fails: no Ask execution and no durable acceptance. The HTTP response is 503; ambiguous client transport failure still cannot prove whether acceptance occurred. After execution, failed terminal persistence suppresses normal terminal events, emits safe `json_error` code `persistence_unavailable`, and makes lookup503 in that process. Restart trusts only durable storage: unfinished records become interrupted; a terminal write that committed despite returning an error remains terminal.

Do not infer liveness from a nonterminal status alone. There is no heartbeat/lease or automatic retry. Reconciliation assumes one embedded executor owns the storage namespace; do not deploy multiple such executors against the same store.

## Persistence and retention

Existing auth DB: additive `agent_chat_jobs` GORM table with advisory migration lock. No auth DB: `<agent-state-dir>/chat-jobs/chat-jobs.json`, atomic synced writes, private permissions. Persist the volume/database. Startup reconciles unfinished jobs before serving execution; it does not migrate old in-memory jobs or transfer records between storage modes.

Terminal results retained for startup `AgentJobRetentionDays` (default30) from completion; interrupted retention starts at reconciliation. Then GET410 while the tombstone is retained for another interval; after that GET404. Cleanup startup/hourly, and lookup enforces logical expiry between sweeps. Active jobs do not age-expire. Increasing retention never restores scrubbed results. Store only the response and generic error metadata, not transcripts/tool traces/config secrets. Final report text may contain sensitive user data and must stay private in the plugin too.

## Explicit remaining gaps

- Lost initial submission receipt: no discovery/list-by-client-ID API; recovery requires retaining job_id.
- Idempotency: POST retries can execute twice.
- Event replay: absent; only latest retained state and terminal report are durable.
- Cancellation: no new per-job cancellation API.
- Pending questions/plans: still memory-only, not restartable.
- Automatic retries/resumption and distributed execution: not implemented.

These gaps must remain visible in plugin behavior and documentation. A completed lookup can recover a report missed while disconnected; it cannot reconstruct every intermediate event or safely resubmit uncertain work.

## Validation

Focused Go storage, lifecycle and HTTP suites passed, including real SQLite API-key authentication and owner isolation. The lifecycle suite also passed with the Go race detector. Routes compile with the auth build tag, Swagger is regenerated, and the change passed independent review. Exact commands and environment limitations are recorded in `2026-09-18-durable-agent-jobs.md`. No real inference, deployment or OpenCode/plugin changes were performed.
