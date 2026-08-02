# Semantic Cache E2E

End-to-end test suite for the `semantic_cache` plugin. See `PLAN.md` for the full case list.

## Prerequisites

The suite assumes a properly-provisioned test environment — it verifies but does not provision.

- **Bifrost running** at `BIFROST_URL` (default `http://localhost:8080`). Required endpoints: `/api/plugins/*`, `/api/cache/*`, `/api/providers`, `/api/logs/{id}`, `/v1/chat/completions`.
- **Vector store** configured in `config.json`, type **`weaviate`**, reachable from Bifrost. The plugin will create/use namespace `BifrostSemanticCachePluginE2E` by default (override via `SC_NAMESPACE`).
- **Providers configured with API keys**:
  - **OpenAI** — required. Must have a chat model (default `openai/gpt-4o-mini`), an alternate chat model used in cache-by-model cases (default `openai/gpt-4o`), and the embedding model `text-embedding-3-small` (used in Phase 2).
  - **Gemini** — optional. When absent, cross-provider cases are skipped with a `WARN` in `0.3_optional_providers`. Chat model: default `gemini/gemini-2.5-flash`.
  - **Anthropic** — optional. Same behavior as Gemini: absence skips cross-provider cases instead of aborting. Chat model: default `anthropic/claude-haiku-4-5`.
- **`semantic_cache` plugin must be ABSENT** at run start. Set `RUN_FORCE=1` to auto-delete a pre-existing row before the run.

## Running

```bash
# All phases (recommended)
GOWORK=off go test -v ./...

# Single phase
GOWORK=off go test -v -run TestPhase1_DirectOnly ./...

# Single case
GOWORK=off go test -v -run TestPhase1_DirectOnly/1.1_exact_match_chat ./...

# Auto-delete pre-existing plugin row
RUN_FORCE=1 GOWORK=off go test -v ./...

# Keep the plugin around for post-mortem
RUN_KEEP_PLUGIN=1 GOWORK=off go test -v ./...
```

`GOWORK=off` is required because this module isn't in the repo's `go.work` (test modules under `tests/*` follow the same pattern as `tests/governance` — standalone).

## Env vars

| var | default | purpose |
| --- | --- | --- |
| `BIFROST_URL` | `http://localhost:8080` | Bifrost base URL |
| `SC_CHAT_MODEL_OPENAI` | `openai/gpt-4o-mini` | OpenAI chat model used in cases |
| `SC_CHAT_MODEL_OPENAI_ALT` | `openai/gpt-4o` | second OpenAI chat model for cache-by-model cases |
| `SC_EMBED_MODEL_OPENAI` | `text-embedding-3-small` | embedding model for Phase 2 |
| `SC_CHAT_MODEL_GEMINI` | `gemini/gemini-2.5-flash` | Gemini chat model |
| `SC_CHAT_MODEL_ANTHROPIC` | `anthropic/claude-haiku-4-5` | Anthropic chat model |
| `SC_NAMESPACE` | `BifrostSemanticCachePluginE2E` | vector store namespace (isolates test data from prod) |
| `RUN_FORCE` | unset | `1` → delete pre-existing plugin row before run |
| `RUN_KEEP_PLUGIN` | unset | `1` → skip teardown DELETE on exit |
| `TRAIL_SESSION_ID` | unset | stamped onto every log line when running under `trail` |

## Trail integration

Start Bifrost under `trail`, capture the session id, export it, then run:

```bash
trail run --label semantic-cache-e2e -- ./bifrost-http -port 8080 -config config.json
# capture the printed session id, then in another shell:
export TRAIL_SESSION_ID=<uuid>
RUN_FORCE=1 GOWORK=off go test -v ./...
```

Every log line carries `trail_sid=<uuid>`, so a single `trail get_logs` call with that session id reconstructs both the test harness output and the Bifrost stdout for the run.

## Output

Each run writes to `reports/<UTC-timestamp>/`:
- `run.log` — one structured line per step (mirrors `t.Logf` output)
- `p<phase>-<case>-s<step>.req.json` / `.resp.json` — full request/response bodies for forensics
- `*.plugin_create.req.json` / `.plugin_update.req.json` — exact wire bodies sent to `/api/plugins` (for parity audit against the UI)

On any FAIL the matching `*.resp.json` and `run.log` line carry enough info to grep via `trail` (look for `bifrost_req_id=<id>` or `[SC-E2E] case=<name>`).

## What's implemented so far

Skeleton + Phase 0 preconditions + Phase 1 smallest viable loop (cases 1.1, 1.2, 1.3, 1.13). See `PLAN.md` §11 for the full implementation roadmap.
