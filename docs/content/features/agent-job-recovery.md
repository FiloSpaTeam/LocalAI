---
title: "Durable Agent Job Recovery"
weight: 17
---

Embedded-agent chat submissions have durable root job records. An external client can recover a final report after losing its SSE connection. This applies to LocalAI's embedded LocalAGI pool, not the native distributed executor. It adds no event replay, automatic retries, cancellation, or browser interface.

## Submit and retain the receipt

`POST /api/agents/:name/chat` accepts the existing body:

```json
{"message":"Implement the approved task", "conversation_id":"client-conversation"}
```

A new root job returns HTTP `202`:

```json
{"status":"message_received", "message_id":"job-uuid", "job_id":"job-uuid"}
```

`job_id` equals the root `message_id`. The accepted record is persisted before the agent executes and before the acceptance response. Subscribe to `/api/agents/:name/sse` before submission to receive live progress; store the receipt locally for later recovery. Job callback events published through this chat lifecycle include `job_id`; use existing `conversation_id`/`message_id` as well. Agent-wide streaming callbacks may retain their existing payloads.

The new field is additive. A request that answers a pending free-text question still returns `{"status":"answer_received","question_id":"question-uuid"}` without a new job. A request injected into an existing live conversation still returns `status` and its new `message_id`, but no new `job_id`: continue tracking the original root job. This rule includes the second live-loop admission check inside the shared chat lifecycle. Distributed chat retains its previous receipt and has no durability guarantee from this feature.

## Retrieve status and result

`GET /api/agents/:name/jobs/:job_id` uses the same authentication and `agents` feature permission as chat. `:name` is the public agent alias, not an internal owner-prefixed pool key. The existing admin/agent-worker `user_id` override rules are unchanged; ordinary users cannot use the query parameter to impersonate another owner. Retained records remain readable by their owner after the agent configuration is deleted.

Example HTTP `200` response:

```json
{
  "job_id":"job-uuid",
  "agent":"coder",
  "message_id":"job-uuid",
  "conversation_id":"client-conversation",
  "status":"completed",
  "result":"Implemented the change and ran the focused tests.",
  "created_at":"2026-09-18T10:00:00Z",
  "updated_at":"2026-09-18T10:04:00Z",
  "completed_at":"2026-09-18T10:04:00Z",
  "expires_at":"2026-10-18T10:04:00Z"
}
```

`job_id`, `agent`, `message_id`, `conversation_id`, `status`, `created_at`, and `updated_at` are always present. An empty conversation ID means the original request used legacy stateless chat. `result` is present on completion, including an empty string if that is the final reply. `error` is present for failed/interrupted outcomes. Terminal records include `completed_at` and `expires_at`. All times are UTC RFC3339 timestamps (fractional seconds may be present).

| Status | Interpretation and client action |
|---|---|
| `accepted` | Admission persisted; execution has not yet reported running. Keep tracking. |
| `running` | Processing has been observed for the job. Keep tracking; this is not a liveness heartbeat. |
| `waiting_user` | The root or a delegated worker reported waiting for input. Fetch pending interactions and relay them. |
| `waiting_agents` | The root reported waiting for delegates. Fetch pending interactions too: a child may need input. |
| `completed` | Root execution returned successfully and the final response is durable. Use `result`. |
| `failed` | Root returned an error, nil result, or a recovered panic. `error.code` is `execution_failed`, with a safe generic message. Side effects may already have occurred. |
| `interrupted` | Startup found an unfinished record. `error.code` is `execution_interrupted`; the execution outcome is unknown. Investigate before deciding whether to submit new work. |

Only the return from the root invocation establishes its terminal result. Child completions and parked intermediate replies do not complete it. Status is the latest persisted nonterminal lifecycle observation correlated to the root, including delegated interaction waits; it is not the entire child activity tree. A terminal record is immutable except for retention cleanup.

Lookup errors use `{"error":{"code":503,"message":"agent job storage unavailable","type":"server_error"}}` (with the corresponding code/message/type below):

| HTTP | Meaning |
|---|---|
| `401` / `403` | Existing authentication/feature middleware denied access. |
| `404` | `type: not_found`, `message: agent job not found`: unknown job, different owner/alias, or tombstone already purged. These cases are intentionally indistinguishable. |
| `410` | `type: expired`, `message: agent job result expired`: this owner/alias's terminal result has expired; its tombstone remains. No result is returned. |
| `501` | `type: unsupported`, `message: durable agent jobs require the embedded executor`: durable lookup requires the embedded executor. |
| `503` | Storage is unavailable or this process could not persist the job's latest outcome; do not infer completion or absence. Pool-starting middleware also returns 503, with the existing startup error format. |
| `500` | `type: server_error`, `message: unable to retrieve agent job`: an unexpected lookup failure; outcome remains uncertain. |

Neither 404 nor 410 authorizes an automatic task retry. They mean the record is unavailable, not that execution never happened.

## Persistence and failure ordering

Final response decoration is applied before persistence so the retained report matches the final assistant reply. Only the final response and safe failure information are retained: no prompt, conversation transcript, tool payloads, runtime metadata, agent configuration, or remote API credentials are copied into the job record. Model-generated responses can themselves contain sensitive user data; protect the backing storage and credentials as you would other private application data. Raw execution error strings are replaced with generic messages.

Terminal persistence completes before the ordinary final reply/error and completion status are published. Persistence does not require an SSE subscriber. If admission persistence fails, HTTP returns `503 {"error":"agent job persistence is unavailable"}` and execution does not start. A write that reached storage but failed its final acknowledgement can leave a conservative unfinished record; no success is acknowledged.

If a status write fails after admission, lookup returns 503 for that job until a later lifecycle write succeeds. No automatic retry is scheduled. If the terminal write fails, normal terminal events are suppressed and a `json_error` event carries `code: persistence_unavailable` with root correlation. The same process returns 503 for lookup rather than presenting stale active state as current. A client that missed that error also sees 503 on lookup. Restart reconciliation then uses only what was durably stored: unfinished work becomes interrupted; a terminal write that actually committed remains terminal.

## Storage, migration, restart and retention

When LocalAI has an auth database, startup uses the existing GORM connection and advisory migration lock to add the `agent_chat_jobs` table. It does not modify existing task/job tables. Without that database, standalone storage is a private atomic JSON file at `<agent-state-dir>/chat-jobs/chat-jobs.json`; the directory is mode 0700 and file mode 0600. Writes sync the file, atomically rename it, then sync the directory. The agent state directory resolves from agent pool StateDir, then application DataPath, then DynamicConfigsDir, then `agents`.

Persist the chosen database or state directory across container recreation. Do not share a standalone file or the same embedded chat-job database namespace between concurrently running LocalAI executors. Startup reconciliation assumes exclusive ownership. Changing from file-backed to database-backed operation does not automatically migrate old records; keep the storage mode stable or preserve the old store separately for recovery.

Agent-pool startup fails (the application remains up and agent routes return 503) if initialization, migration, validation, reconciliation or initial cleanup fails. It does not silently fall back to volatile execution. Completed/failed records survive restart. All unfinished records are conservatively marked interrupted before the pool starts accepting work; execution is never resumed or retried. Pending questions/plans and conversation memory remain in-memory and do not survive restart.

Retention uses the existing `AgentJobRetentionDays` application setting, default 30 days (nonpositive values use the default here), sampled at pool startup. Terminal results expire that many days after `completed_at`; interrupted jobs measure from reconciliation time. Active work never expires solely due to age. Cleanup runs at startup and hourly. Expired payloads are removed on cleanup, leaving a private owner/alias tombstone for one further retention window. Lookup enforces the expiry boundaries even before cleanup; after the second window it returns 404. Restart with a changed retention setting reevaluates existing records against that setting; increasing retention never restores scrubbed results. Backups have their own retention policy.

## Recovery boundary

Durable lookup solves recovery when a client retained its job ID and missed terminal events. It does **not** solve a lost initial submission receipt. Task submission is not idempotent: retrying a POST after a timeout may duplicate work. It also supplies no event replay, job cancellation, automatic retries, pending-interaction durability, or distributed execution support.
