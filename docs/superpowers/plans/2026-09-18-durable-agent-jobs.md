# Durable embedded-agent jobs: design and implementation plan

Approved scope: durable root chat identity/status/result lookup only. No retry, cancellation, replay, distributed execution, deployment or UI changes.

## Design

Reuse existing storage conventions: GORM/auth database if available (additive table migration using existing advisory migration lock); otherwise private atomic JSON files under the agent pool state directory. Neither path stores request prompts, tools, agent configs or raw exception text. Persist final assistant response only; safe fixed error codes/messages describe failure. Responses can contain user data and remain owner scoped.

Each root uses its existing message UUID as job_id. Admission happens inside the shared chat lifecycle immediately before Ask, after its second live-loop injection check. Persist accepted before execution and before HTTP acknowledgement. Buffer pre-admission events until persistence succeeds. Question answers and successful live injections return their legacy receipts without job_id. No durable child records are created.

Statuses: accepted, running, waiting_user, waiting_agents, completed, failed, interrupted. Terminal status/result is written synchronously before final SSE publication; root Ask return defines termination, never child events. Persistence failures before admission prevent Ask and return 503. Failures during execution cause lookup to return 503 while unresolved; a successful terminal write can restore durable truth. Failed terminal writes suppress ordinary terminal publication and emit a safe persistence error; no automatic execution or persistence retries. On reconstruction, unfinished durable records become interrupted; interrupted means execution outcome is unknown, not that side effects did not occur.

GET /api/agents/:name/jobs/:job_id uses the existing agents feature gate and effective user identity. Match owner and public alias in storage, even if the agent was deleted. Unknown and other-owner IDs return identical 404. Terminal results retained for AgentJobRetentionDays (default 30) from completion, then become expired tombstones (410) for another retention window, then 404. Active work never expires by age. Cleanup at startup and periodically; lookup also enforces expiry. Storage failure returns 503, never absence. Embedded single-process ownership of a state directory/database namespace is required; shared embedded executors are not supported.

Existing receipts retain status/message_id/question_id; new roots add job_id. job_id equals the root message_id, so existing correlation remains usable. Result response includes public agent name, conversation_id/message_id, state and UTC timestamps; no owner IDs or internal pool keys. pending remains memory-only. Lost initial receipts and idempotent POST are explicitly unsolved.

## Implementation tasks

1. Storage (core/services/jobs/chat_store.go and tests): define record/store interfaces; DB migration; atomic synced private file backend; authoritative scoped reads; conditional updates; conservative startup reconciliation; retention/tombstones; tests for reconstruction/isolation/failures/expiry.
2. Lifecycle (core/services/agentpool/durable_chat.go and tests; agent_interactions.go; agent_pool.go): initialize store before starting pool, stop cleanup loop, admit after final injection check, persist transitions/terminal result independently of subscribers, safe 503 lookup on failed writes. Deterministic fake-worker tests cover ordering, root/child events, nil/errors, injected messages and failure paths.
3. HTTP (agents.go/routes/agents.go and tests): additive receipt/lookup schema, 404/410/501/503 behavior, effective-user isolation, agent feature middleware, swagger and instructions discovery. No new model capability or admin MCP tool: this is existing user agents capability.
4. Docs/handoff: exact code-derived contracts and client recovery actions, operational paths/migration/retention, limits. Run focused Go/storage/HTTP tests and route compilation; regenerate Swagger and diff-check. Independent review, fixes, local commit only.

## Validation ledger

Implementation and independent review complete. Review identified and fixed missing-file failure handling, child interaction status propagation, and expired-result behavior after increasing retention; regression tests cover all three.

Validation (2026-09-18):

```sh
go test -p 2 -tags auth ./core/services/jobs ./core/services/agentpool ./core/http/endpoints/localai \
  -ginkgo.focus='Durable|Interactive|interactive agent|Agent interaction endpoints' -ginkgo.no-color -count=1
go test -race -p 2 -tags auth ./core/services/agentpool \
  -ginkgo.focus='Durable chat lifecycle' -ginkgo.no-color -count=1
go test -p 2 -tags auth ./core/http/routes -run '^$'
swag init -g core/http/app.go --output swagger
git diff --check
```

All passed. Tests use deterministic fake workers and real SQLite authentication/storage, without model inference. Focused suites also passed without the auth build tag. Go was `/tmp/go/bin/go`; SQLite/race checks used `CGO_ENABLED=1 CC=/tmp/localai-compiler/bin/cc`, `GOMAXPROCS=2`, `GOCACHE=/tmp/localai-go-build` and `GOMODCACHE=/tmp/localai-go-mod`. Swagger was generated using `/tmp/localai-go-tools/swag`. Changed Go files are gofmt-clean. Docker and golangci-lint are unavailable in this environment, so full Docker-backed integration suites and the lint/coverage CI jobs were not run; these focused checks are not a claim of full CI coverage.

Local commit only. No push, PR, publication or deployment authorized.
