# OpenCode plugin / LocalAI API handoff

## Approved goal and ownership

Repository: `opencode-plugin-localai`, an independent OpenCode integration (not an official OpenCode project).

The user chats with OpenCode, asks questions, reviews and approves an overall plan, and delegates approved tasks to configured LocalAGI agents through LocalAI. The user can keep chatting while work runs; worker questions, approvals, progress and results return to the same OpenCode session. LocalAI serves inference from DeepSeek V4 Flash on a GB10 with 128 GB unified memory. Start with one delegated worker at a time; do not assume concurrent model inference.

OpenCode owns the main conversation and overall plan. LocalAGI owns delegated tasks. Keep Cogito/LocalAGI's questions, approval, background work and conversation primitives. LocalAI owns authenticated, user-scoped API access and model serving. Its browser agent chat is restored to its previous behavior; its agent configuration editor remains available. Do not rebuild an interactive LocalAI chat UI.

## Source baselines

- LocalAI fork: `https://github.com/FiloSpaTeam/LocalAI`, branch `master`. Initial API implementation: `1d904c115e7ca2068abf20f0db7580a25c175644`. Read the latest master for the UI rollback and this document.
- LocalAGI fork: `a88ddd8242f856e4c4a52481cb635804d2541d89`.
- Cogito fork: `8f2ce74a8d14d138a1bde69f379d1258f0a4489c`.
- Upstream Go import paths are preserved through fork replacements.
- Deployment automation: `https://git.unitoo.it/claudiomaradonna/castrum`, role `roles/ai-stack`. It runs separate LocalAI, LocalAGI and LocalRecall containers. No deployment changes have been performed here.

V1 targets LocalAI's EMBEDDED LocalAGI pool. The separate LocalAGI container is not automatically part of that pool. Remote `/v1/responses` delegation returns a final result but does not relay remote questions/plan approvals. Native distributed executor interaction parity is out of scope.

## Verified API contract to inspect before implementation

Authoritative LocalAI files: `core/http/routes/agents.go`, `core/http/endpoints/localai/agents.go`, `core/services/agentpool/agent_interactions.go`, `core/services/agentpool/agent_chat.go`. LocalAGI files: `core/chat`, `core/interactions/registry.go`, `core/agent/delegation.go`, `core/state/delegation.go`.

- `GET /api/agents`: discover agents accessible to the authenticated user; inspect actual response rather than inventing a discovery schema.
- `POST /api/agents/:name/chat`: `{message, conversation_id}`. Async receipt with status and optional message_id/question_id. Free-text replies to a pending question can return `answer_received` without a new job.
- `GET /api/agents/:name/sse`: job events. Connect before submitting work. Correlate by conversation_id/message_id, not the currently visible OpenCode session. Auth must also apply to SSE.
- `GET /api/agents/:name/pending?conversation_id=...`: `{questions: [...], plan: object|null}`. Questions are exhaustive; plan is ONLY the oldest pending plan. Refresh after decisions and reconnect. Do not expire sibling plans merely because another plan was returned.
- `POST /api/agents/:name/answer`: `{question_id, selected?: string[], text?: string}`.
- `POST /api/agents/:name/plan`: `{plan_id, approved: boolean, subtasks?: string[], feedback?: string}`. False plus nonempty feedback requests replanning; final rejection must send empty feedback. Omitted subtasks and an empty edited list differ.
- SSE names: json_message, json_message_status, stream_event, json_error, question, plan, sub_agent. Statuses include processing, waiting_user, waiting_agents, completed. Inspect actual payloads in the pinned source, including nested child identity and root message correlation.
- Questions/plans raised by embedded child agents belong to the parent registry and event stream.
- Chat during waiting_agents is injected into the live loop; it is not a new job. Chat against a question that forbids free text returns 409 with pending_question_id.
- Unknown/resolved interaction: 404. Invalid answer/decision: 400. Unsupported interaction endpoints in native distributed mode: 501. Pool starting: 503.
- LocalAI enforces owner isolation and public agent aliases. Do not infer ownership or let model-provided fields select another tenant. Existing admin user_id override is not a general impersonation mechanism.
- Agent settings: enable_user_questions, enable_planning + require_plan_approval, enable_sub_agents, sub_agents, remote_agents, last_message_duration.

## Known limits: do not promise more than the API provides

Update: the local durable-job implementation is described in [the recovery handoff](2026-09-18-durable-agent-jobs-plugin-handoff.md). The following paragraph records the original pre-durability baseline; use the new contract when running a build that includes it.

Pending recovery is NOT event replay or a durable result store. If a worker completes while disconnected, pending can be empty even though its result was missed. Conversation trackers and interaction registries are in memory and expire/restart. The initial async receipt is not a durable task handle. Do not blindly retry a task POST after an ambiguous timeout: there is no verified idempotency guarantee. There is no verified per-job cancellation API; agent-wide pause is not equivalent. Verify these gaps in source and report any necessary LocalAI API additions for a follow-up in the LocalAI session.

Using the same worker agent for concurrent tasks may interact with cancellation policies. V1 should serialize work per worker until behavior is verified. LocalAI's model API and agent API are different protocols: pointing an OpenCode model provider at `/api/agents` is not an integration.

## Implementation sequence for the dedicated session

1. Read the new repository's instructions and inspect its contents. Verify the CURRENT OpenCode plugin/SDK APIs from official source. Pin a tested version. Prove custom tools, session correlation, async message injection, user questions/approvals and persistent plugin state using minimal experiments. Do not invent plugin hooks. If an essential interaction requires OpenCode changes, document the exact limitation before choosing a fork.
2. Save a self-contained design and implementation plan in that repository. Use this document as the accepted scope; only unresolved technical choices need exploration. Include an API compatibility matrix and explicit reconnect/result-loss behavior.
3. Add configuration for LocalAI URL, credentials and agent selection, an authenticated HTTP/SSE client, and contract tests using fixtures derived from pinned source. Never log tokens or remote agent config secrets.
4. Implement discovery and one foreground delegated task. Keep inference provider configuration separate from delegation configuration. Include a LocalAI OpenAI-compatible provider example with placeholder URL/model, not deployment-specific addresses.
5. Implement background delegation, correlation, progress/result delivery, question/plan relay, reconnect and session isolation. Preserve zero additional LLM calls solely to relay a human answer or approval. Test duplicate/out-of-order events, stale snapshots, sibling plans, expired IDs, connection loss and ambiguous task submission failures.
6. Connect approved overall plans to tasks, with sequential dependency-aware dispatch first. OpenCode should not approve child work silently or dispatch before the user's approval. Skills execute in the environment of their owning agent; do not assume OpenCode's local skills are automatically installed in LocalAGI workers.
7. Run mocked integration tests and then a live small-repository acceptance scenario: dry-run flag request, clarification, edited approved plan, delegated implementation, continued chat, child question and final report. Verify repo/MCP access before expecting actual edits. A configured local model/endpoint is required for live validation; request it when that step is reached.

## Completed validation before this handoff

The original LocalAI backend passed focused Go service/config/HTTP tests and route compilation. The former browser interaction suite plus retained config editor suite passed all 16 tests before removal of the interaction UI. Browser crashes were traced to missing fonts/Fontconfig in the temporary runtime, not application behavior. The real-model coding scenario has NOT been run. Deployment/build of a GB10 image has NOT been performed.

## LocalAI scope-change checklist

- [x] Restore AgentChat, useAgentChat and its JS API wrapper to their previous behavior.
- [x] Remove interaction cards, sub-agent strip styles and dedicated browser interaction tests; keep the existing spinner CSS extraction so the inline-style baseline does not increase.
- [x] Keep all Go APIs, tests, agent configuration editor, delegation scope/client policy and dependency pins.
- [x] Update user docs to describe API clients and the built-in UI limitation.
- [x] Validate the retained editor, restored chat, UI build/lint and focused Go tests. Four targeted browser tests passed; build, scoped ESLint, inline-style gate (511), focused Go tests, and independent review passed.
- [ ] Commit and push the follow-up to the user's fork master, preserving existing history.
