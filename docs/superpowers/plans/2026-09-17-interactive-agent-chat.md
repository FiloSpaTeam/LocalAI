# Interactive agent chat implementation plan

> **For agentic workers:** Use superpowers:subagent-driven-development to implement and review the independent UI task alongside the service integration.

**Goal:** Deliver the approved LocalAI embedded-pool integration for interactive agent chat.

**Architecture:** Keep upstream module imports with replacements for the supplied FiloSpaTeam commits. Extend the existing LocalAI chat lifecycle to preserve metadata, citations and metrics while using LocalAGI conversation history, interaction registries and parked-loop injection. Scope every operation through the existing user agent key.

**Tech Stack:** Go, Echo, LocalAGI, Cogito, React, Playwright.

**Spec:** The approved 2026-09-16 design supplied in this session, sections 6.1–6.2, with the 2026-09-17 dependency update. Native distributed executor parity is a separate follow-up (section 6.3).

## Global constraints

- LocalAGI commit: `6154d76b48471ff9e010e4f06da98b5b23a0dfdf`.
- Cogito commit: `8f2ce74a8d14d138a1bde69f379d1258f0a4489c`.
- Preserve upstream import paths, existing auth and user scope, existing stateless callers, and model chat.
- Interactive settings default off. Keep remote API keys out of events and logs.
- Update `docs/content/` with behavior changes. Never lower coverage gates.
- Ginkgo/Gomega for Go tests; Playwright for UI behavior.

## Task 1: Dependencies and embedded service lifecycle

Files: `go.mod`, `go.sum`, `core/services/agentpool/agent_pool.go`, a new `agent_interactions.go` and associated Ginkgo specs.

- [x] Add both fork replacements and resolve checksums.
- [x] Test user-scoped registry lookup, missing agent, unsupported distributed operations, and conversation event correlation.
- [x] Add optional conversation identity to the existing chat service without breaking existing callers.
- [x] Before enqueueing: use `Interactions().AnswerText(conversationID, message)`; preserve its pending question ID for HTTP 409. Then use `InjectChat(conversationID, message, messageID)`.
- [x] Supply `WithUUID`, `WithEventCallback`, `WithConversationHistory` and `MetadataKeyConversationID` to jobs; persist history unless `ConversationSaved` is already true. Correlate all job events. Preserve metrics, citations and artifact metadata.
- [x] Expose user-scoped pending, answer and plan service methods using `Registry.Pending`, `Registry.Answer` and `Registry.Decide`.

## Task 2: HTTP and configuration

Files: `core/http/endpoints/localai/agents.go`, `core/http/routes/agents.go`, API discovery registry, `core/services/agents/config.go`, `configmeta.go`, related tests and agent documentation.

- [x] Test and add `conversation_id` forwarding and three endpoints: POST answer, POST plan, GET pending.
- [x] Map unknown interactions to 404, invalid answers/decisions to 400, forbidden free-text answers to 409; apply existing agent group middleware and effective user identity.
- [x] Expose opt-in question, plan approval, pool delegation and remote delegation settings, preserving them through config conversion.
- [x] Register endpoint discovery consistently and document configuration, expiry, restart behavior and standalone scope.

## Task 3: React interactions

Files: `core/http/react-ui/src/pages/AgentChat.jsx`, `hooks/useAgentChat.js`, `utils/api.js`, focused interaction components, CSS and Playwright specs.

Consumes: POST chat `{message, conversation_id}`; POST answer `{question_id, selected, text}`; POST plan `{plan_id, approved, subtasks?, feedback?}`; GET pending with conversation_id. All paths under `/api/agents/:name` retain user_id targeting.

- [x] Add behavioral specs for question selection/free text, plan editing/rejection, conversation routing and reconnect reconciliation.
- [x] Route events by conversation_id first, retaining message mapping fallback; maintain conversation-specific statuses and stream state.
- [x] Persist inline question/plan cards and resolution state, deduplicate pending recovery, disable stale/resolved cards, surface request errors.
- [x] Render sub-agent activity with elapsed time and expandable summaries; keep composer enabled while waiting_agents.
- [x] Recover pending cards on load, conversation changes and every SSE open/reconnect.
- [x] Run targeted browser tests, lint and build where available.

## Task 4: Review and validation

- [ ] Review user isolation, conversation races, secrets, optional defaults and config round trips.
- [ ] Run focused Go and React checks; report unavailable prerequisites explicitly. Ask before long project builds, per repository policy.
- [ ] Review the complete diff against the approved acceptance scenario and fix findings.

## Decisions

- Use a feature branch in the existing clean workspace; no shared branch writes or external publication.
- Do not silently run interactive requests through the native distributed executor; report its unsupported operations until the parity follow-up.
- Go was absent from PATH; use an isolated toolchain under `/tmp` for dependency resolution and focused validation.

## Validation status

Focused Go service, config, HTTP handler and discovery specs pass with the auth build tag. UI compilation, scoped ESLint and inline-style checks pass. All 16 mocked browser interaction and delegation-editor tests pass. Full database-backed suites require Docker, unavailable here. The live coding scenario with a real model and delegate has not been run. Backend and React review findings have been addressed, including conversation routing, status races, pending recovery, transcript order, and delegation scope.

## Dependency integration update (2026-09-17)

LocalAGI now pins fork commit `a88ddd8242f856e4c4a52481cb635804d2541d89`; cogito stays at `8f2ce74a8d14d138a1bde69f379d1258f0a4489c`. Upstream module import paths are unchanged.

- The embedded pool installs a resolver before starting agents. Public aliases resolve only within the actual parent pool key's owner namespace, including user IDs containing colons. Job metadata cannot override ownership.
- Remote delegation receives LocalAI's hardened HTTP client, with redirects refused and no fixed response deadline.
- LocalAGI forwards child interactions to the parent registry and event sink. The UI preserves sibling plans when pending returns only the oldest plan and refreshes after a decision to recover the next one.
- Focused Go service/config/HTTP tests pass against the new pin, including namespace regression cases.

## Browser validation update

The Chromium crash was caused by missing fonts and Fontconfig configuration in the temporary browser runtime. A standalone blank input reproduced `FATAL: SkFontMgr_FontConfigInterface.cpp:163`; supplying DejaVu fonts and a Fontconfig file resolved it without application changes.

After fixing the runtime, tests exposed ambiguous partial-label selectors, a nonexistent model-input ID, and an attempt to click the toggle's hidden checkbox. Tests now target exact textboxes, the actual model control, and the visible toggle. Assertions remain intact, with an added check of the resulting toggle state.

All 16 tests in `agent-chat-interactions.spec.js` and `agent-config-delegation.spec.js` passed together in 19.4 seconds, including plan editing/rejection, edited approval, sibling recovery, and configuration round trips. Scoped ESLint and `git diff --check` also passed. These tests mock API/SSE responses; live model/delegate acceptance remains a separate check.

For this temporary runtime, start Vite on port 8089 and use:

```sh
FONTCONFIG_FILE=/tmp/localai-browser-fonts.conf \
LD_LIBRARY_PATH=/tmp/localai-browser-root/usr/lib/x86_64-linux-gnu \
PLAYWRIGHT_BROWSERS_PATH=/tmp/localai-pw \
PLAYWRIGHT_EXTERNAL_SERVER=1 PW_WORKERS=1 \
npx playwright test e2e/agent-chat-interactions.spec.js e2e/agent-config-delegation.spec.js
```

The Fontconfig file points to `/tmp/localai-browser-root/usr/share/fonts` with a writable cache under `/tmp/localai-font-cache`. Standard CI environments should install Playwright's normal browser dependencies instead of using these temporary paths.

No changes were committed or published.
