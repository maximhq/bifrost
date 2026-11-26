---
name: mcp-harness
description: Use when a change touches MCP behaviour (MCP clients and their upstream auth, connection types, the /mcp and /mcp/<slug> endpoints, tool execution, MCP tools in inference and code mode, tool filtering, virtual keys' MCP grants and Virtual MCPs, per-user discovery and sessions, MCP logs and pricing, the MCP library, the Bifrost OAuth server, the Bifrost MCP on /v1/mcp) or when asked to add MCP tests. Lists the cases each kind of MCP change needs, where they go in the MCP harness, how to write them and how to run them. MCP cases go in the MCP harness, never in the provider harness.
---

# MCP harness

Every end-to-end MCP case lives in the MCP harness: `tests/e2e/api/runners/build-mcp-harness.mjs`, which generates the Postman collection at the start of each run (the collection is not committed; edit the builder). `make run-mcp-harness-test` boots its own Bifrost and an mcpchaos fleet and calls nothing outside the machine: no API keys, no paid calls. Run it yourself, scoped to your cases, and report the result.

Read AGENTS.md "Every MCP change ships with MCP harness cases" and "MCP Tool Filtering is 4 Levels Deep" first. Cite names, not line numbers: the helpers below are names in `tests/e2e/api/runners/lib/mcp-harness.mjs` on `dev`.

## 1. What goes where

| What changed | Test it in |
|---|---|
| MCP behaviour a client can observe: the admin API, `/mcp`, a slug, tool execution, MCP tools in inference, sessions, MCP logs | the MCP harness (this skill), plus Go unit tests |
| A function's logic, a race, an error path no request can reach | Go unit tests beside the code; `make test-mcp` for `core/internal/mcptests` |
| How a provider converts a tool definition or a tool call | the provider harness (`.claude/skills/harness-test-writer/SKILL.md`) |
| A dashboard page | Playwright (`tests/e2e/features/mcp-*`) |
| Upstream behaviour the fleet cannot produce yet | extend chaosmock (`tests/cmd/mcpconsole/chaosmock`, with its own Go test), then use it from the harness |

A change that reaches both a provider's conversion and MCP behaviour needs both harnesses.

## 2. The axes a case is placed on

Every MCP change sits on some of these axes. Section 3 says which ones a kind of change must cover; cover every value the change can reach, not only the one named in the issue.

- **Connection type:** `http`, `sse`, `stdio` (a STDIO client spawns `{{chaos_bin}}`, which bridges to the fleet), `openapi`; the in-process clients only through the Bifrost MCP and Warp.
- **Upstream auth type:** `none`, `headers`, `oauth`, `per_user_oauth`, `per_user_headers`, `token_exchange`. Helpers: `mockAuth`, `asServer`, `createOAuth`, `consentAs`, `followCallback`, `completeOAuth`, `puConfig`, `puConnect`, `flowStart`, `puFinish`.
- **Inbound auth mode on `/mcp`:** `headers`, `both`, `oauth` (`mcp_server_auth_mode`; `withMode`, `MODE_NAMES`, `modePrefix`), with `enforce_auth_on_inference` on and off (`setEnforce`).
- **Caller credential:** none, a virtual key (`x-bf-vk`; `vkCreate`, `vkExec`), a token from Bifrost's OAuth server, a temp token (`TT`); a key with and without MCP grants; two credentials at once.
- **Surface:** `/mcp` (`initialize`, `tools/list`, `tools/call`, prompts, resources, notifications), `/mcp/<slug>`, `/v1/mcp/tool/execute`, `/v1/chat/completions` and `/v1/responses` with MCP tools (manual and auto-execute, streaming and not), code mode, the Bifrost MCP on `/v1/mcp`.
- **Filter level:** global, client (`tools_to_execute`, `tools_to_auto_execute`), tool, per request (`x-bf-mcp-include-clients`, `x-bf-mcp-include-tools`), the virtual key's `mcp_configs`, Virtual MCPs.
- **Where the config comes from:** the admin API, `config.json` at boot (a client that boots pending verification can only come from `config.json`), the MCP library.
- **Client state:** healthy, unstable (with `last_failure`), disabled, pending verification, reconnected, deleted while it dials.
- **Upstream behaviour (fleet):** faults, hangs, slow tools, `tools/list_changed` pushes, auth challenges, `input_required` mid-call, MCP Apps `ui://` resources.
- **Network path:** direct, through an outbound proxy, `no_proxy`, TLS inspection, idle cuts.

## 3. The cases each kind of change needs

Each block lists what the cases must prove and the sections they go in. For every kind of change, also cover: turning it on and back off, a client that boots with it from `config.json` reading back the same as one created through the API, and a change taking effect without a restart.

**A new or changed upstream auth type**, or a change to how a credential is stored, refreshed or sent (AUTH, PUD, OAS, LOG):
- Every connection type that can carry it; created through the API and from `config.json`.
- The credential reaches the upstream on `tools/list`, `tools/call`, the health check, reconnect and tool sync (`identityCallsAre`, `mockCalls`), and no other credential does (`noIdentity`).
- Expiry and refresh: before, at and after expiry; a refresh the upstream refuses; the upstream rejecting the credential mid-run, with the client state and message that follow.
- Shared and per-user: each caller gets their own credential; one caller's credential never reaches another caller's call.
- Update to another auth type and delete: credentials, discoveries and session rows go with it.
- Each inbound mode (`headers`, `both`, `oauth`) on `/mcp`, `/v1/mcp/tool/execute` and inference with auto-execute.
- No answer, error or log echoes the secret or configuration values (URLs, hosts, header values).

**A new or changed connection type or transport** (CONN, FAIL, NET, SYNC):
- Every auth type that applies; create, update, delete, reconnect.
- Protocol version negotiation, streamed responses, notifications, long calls and `tool_execution_timeout`.
- The upstream down at create (the client stays unstable with its `last_failure`), dropped mid-call, recovering on its own.
- Tool sync on the interval, on `tools/list_changed` and on refresh-tools.
- Through a proxy, with `no_proxy`, and with idle cuts.

**Inbound auth on `/mcp`**: a new caller credential, auth mode or header, or a change to how a caller is identified (EP, OAS, GOV):
- Each mode, with enforce on and off; every caller credential alone and each pair.
- Every surface: `/mcp`, `/mcp/<slug>`, `/v1/mcp/tool/execute`, inference.
- The refusal: exact status and message, `WWW-Authenticate` and protected resource metadata where the mode advertises OAuth, none where it does not.
- The identity reaches governance and logs: the right virtual key is checked and charged.

**Tool filtering, virtual key MCP grants or Virtual MCPs** (GOV, SET, EP, INF):
- Each level alone and combined: a tool is exposed only when every level agrees.
- Every surface answers the same: `tools/list`, a refused `tools/call`, `/v1/mcp/tool/execute`, the tools injected into an inference request, code mode's listing, slug routes.
- Wildcards, empty lists, a renamed or deleted client, a tool removed upstream.
- An update to the key or Virtual MCP applies to the next call.

**Tool execution and results** (EP, INF, FAIL, LOG):
- Every surface; every result shape the change touches (text, structured content, images, `isError`, resource links, `ui://` resources); `input_required` mid-call; JSON-RPC errors versus tool errors; timeouts and upstream faults.
- What the upstream received: arguments, headers, identity (`mockCalls`).
- The call's log row.

**MCP tools in inference, agent mode or code mode** (INF, and `make run-mcp-codemode-test` for the code mode collection):
- `/v1/chat/completions` and `/v1/responses`, streaming and not; manual and auto-execute; max depth; a tool failing mid-loop.
- The fleet's `llm` server scripts the model's replies and records what it was sent: assert the tools the model was offered and the tool results it got back.
- Code mode per client and per slug.

**The client admin API or a client setting** (ADM, SET):
- Each invalid field gets its exact 400 message; create, read, update and delete round trip; list filters and paging; secrets redacted in every answer.
- The same setting from `config.json` reads back identically.

**Client lifecycle**: health checks, reconnect, sync, boot, disabled clients (ADM, FAIL, SYNC, CONN):
- The state and `last_failure` after each transition; a delete while the client dials; a disabled client is neither served nor dialled; a boot-time client pending verification.

**Per-user discovery, per-user credentials or the sessions API** (PUD):
- Every per-user auth type; the `<client>-connect` tool offered until the caller connects; discovery at sign-in; a discovery dropped with its credential; per-caller serving on `/mcp`; callers never see or reach each other's tools; refreshing one caller's discovery; the sessions list and revoke; races between connect, refresh and delete.

**MCP logs, pricing or telemetry** (LOG):
- One log row per call on every surface, with the client, tool, caller, status and latency; `tool_pricing` priced into the cost; failed calls logged; metrics.

**The MCP library** (LIB): the fleet serves the catalog; a test that changes it resets the library server.

**Bifrost's OAuth server** used by MCP clients (OAS): discovery documents, client ID metadata documents, consent, issue, refresh and revoke, across modes.

**The Bifrost MCP on `/v1/mcp` and Warp** (BMC, WMG): what is served and annotated, who may call it, tool calls, settings, and that no read hands out a virtual key's value.

**Network paths** (NET): proxies, `no_proxy`, a proxy that is down or asks for credentials, firewalls and TLS inspection.

## 4. Writing a case

- **Section and ID:** pick the section from section 3. The ID is the next free number in it (`grep -oE 'folder\("ADM-[0-9]+' tests/e2e/api/runners/build-mcp-harness.mjs | sort -V | tail -1`). Add the constant to that section's list in the `collection` at the bottom of the builder.
- **Shape:** `folder("<ID> <what it proves>", "Setup: <the starting state in plain words>.", [...requests])`. Every request is `request("<ID>", "<what this step checks>", ...)`, so its title starts with the ID. Steps that only prepare start with `setup:`, steps that clean up with `teardown:`.
- **Independent tests:** re-add the fleet servers you use (`freshServer`, `serveTools`), and restore everything you change (`restoreHeaders`, `restoreConfig`, `setEnforce`, `deleteClient`, `vkDelete`). Never rely on another test's state or order. A client that must boot from `config.json` is declared in the runner's boot config (`runners/individual/run-newman-mcp-harness-tests.sh`), named after its test, and points at a fleet server of the same name that the test sets up.
- **Assert facts, exactly:** the status (`A.status`), the message (`A.msgEquals`), the tool names (`toolNamesAre`, `toolNamesInList`), what the upstream received (`mockCalls`, `identityCallsAre`), the client state (`clientState`, with `waitForClient` for anything asynchronous). Never return early on an error status, and never assert a 500 as the expected answer.
- **A known bug:** assert the intended answer wrapped in `A.skip(...)`. It is reported as skipped until the fix, which unwraps it in the same PR.
- **Red before green:** run the case on the code before the change and confirm it fails on the asserted fact, not on setup.
- **The fleet:** `tests/cmd/mcpconsole/chaosmock/README.md` lists its control API (tools, prompts and resources, faults, gates, counters, auth, push notifications, the `llm` server). Extend it with its own test when a case needs upstream behaviour it does not have.

## 5. Running

```bash
# Go first: the package and the mock-based MCP tests
go test ./core/mcp/...
make test-mcp TESTCASE=<TestName>

# the builder alone: fails on an unknown ID or a broken folder
node tests/e2e/api/runners/build-mcp-harness.mjs --out tmp/mcp-harness.json

# only your cases (hermetic: run it yourself)
TESTS=<ID>,<ID> make run-mcp-harness-test

# a whole section, for a change that reaches many of its cases
make run-mcp-harness-test FOLDER="<section name, e.g. PUD Per-user discovery>"
```

Pass `HARNESS_PORT`, `CHAOS_PORT`, `INPUT_PORT` or `APPS_PORT` when the defaults are taken. Report the IDs you ran: new or changed cases red before the change and green after, the existing tests the change reaches green before and after. A change that reaches several sections (an auth or filtering rule, a transport) runs every section it reaches before merge.

## 6. Adding a new value to an axis

A new upstream auth type, connection type, discovery or credential kind, inbound auth mode, caller credential, filter level, surface, result shape or protocol version is a new value on one of the axes in section 2. Its own new tests are not enough:

1. **Add it to the matrix of every existing test.** Find each test that enumerates that axis and add the new value to it, so it gets the same coverage as the values already there. The lists are inline in the builder: the modes a test loops over (`["H", "B"].flatMap(...)`, `withMode`, `MODE_NAMES`), the connection types or auth types it repeats a step for, the caller credentials it tries, and the full lists some tests assert verbatim (the `auth_type` and `connection_type` validation messages in ADM-05). Grep for an existing value of the axis (`grep -n '"per_user_headers"' tests/e2e/api/runners/build-mcp-harness.mjs`) to find them. A test that deliberately leaves the new value out says why in its `Setup:` description.
2. **Update this skill.** Add the value to its axis in section 2 and to every block in section 3 that names that axis. A new kind of change gets its own block in section 3. Keep the AGENTS.md rule and the review configs (`.coderabbit.yaml`, `.greptile/`) in step when the rule itself changes.
3. **Extend the fleet when it cannot produce the behaviour.** If no fleet server can play the new upstream side (a new auth scheme, discovery mechanism, transport or protocol feature), add it to chaosmock (`tests/cmd/mcpconsole/chaosmock`): the server behaviour, its control API route, a Go test beside it, and its section in the chaosmock README. Then drive it from the harness through the control API like the existing ones. A behaviour only a real SDK server shows goes under `examples/mcps/` and is started by the runner, as `input-required-server` and `apps-server` are.
