# Semantic Cache E2E

End-to-end test suite for the `semantic_cache` plugin. See `PLAN.md` for the full case list.

## Quick start

A ready-to-run Bifrost config ships with the suite as `config.json` (OpenAI +
Anthropic + Gemini providers via env keys, Weaviate vector store on the
docker-compose port). From the repo root:

```bash
# 1. Vector store (Weaviate on localhost:9000)
docker compose -f tests/docker-compose.yml up -d weaviate

# 2. Provider keys — ALL THREE are required (missing providers FAIL the run, they don't skip)
export OPENAI_API_KEY=sk-...
export ANTHROPIC_API_KEY=sk-ant-...
export GEMINI_API_KEY=...

# 3. Bifrost from the local working tree, using this directory as the app dir
make dev APP_DIR=$(pwd)/tests/semanticcache
# wait for http://localhost:8080 to answer

# 4. Run
make test-semantic-cache                      # all phases
make test-semantic-cache CACHE_TYPE=direct    # direct-only scope (no embedding calls)
```

The suite installs, reconfigures, and deletes the `semantic_cache` plugin row
on the target gateway (`RUN_FORCE=1` is the Makefile default), so point it at
a dev gateway — never one whose cache config you care about. Bifrost writes
its runtime `*.db` files next to `config.json`; they're gitignored.

## Prerequisites

The suite assumes a properly-provisioned test environment — it verifies but does not provision. `config.json` in this directory satisfies the config-side requirements; the list below is what it maps to.

- **Bifrost running** at `BIFROST_URL` (default `http://localhost:8080`). Required endpoints: `/api/plugins/*`, `/api/cache/*`, `/api/providers`, `/api/logs/{id}`, `/v1/chat/completions`.
- **Vector store** configured in the gateway's `config.json`, type **`weaviate`**, reachable from Bifrost. This directory's `config.json` points at `localhost:9000` — the host port `tests/docker-compose.yml` publishes for its Weaviate service. The plugin will create/use namespace `BifrostSemanticCachePluginE2E` by default (override via `SC_NAMESPACE`).
- **Providers configured with API keys — all REQUIRED.** `TestPreconditions` (0.2) fails if any is missing, and the cross-provider cases call `requireProvider` and fail (not skip) at case level too:
  - **OpenAI** — chat model (default `openai/gpt-4o-mini`), an alternate chat model for cache-by-model cases (default `openai/gpt-4o`), and the embedding model `text-embedding-3-small` (used by the semantic phases).
  - **Anthropic** — cross-provider cache-key cases. Chat model: default `anthropic/claude-haiku-4-5`.
  - **Gemini** — chat-provider ≠ embedding-provider cases. Chat model: default `gemini/gemini-2.5-flash`.
- **`semantic_cache` plugin must be ABSENT** at run start. Set `RUN_FORCE=1` to auto-delete a pre-existing row before the run (the Makefile targets do this by default).

## Running

Via the Makefile (from the repo root — wraps the run in `trail` when available):

```bash
make test-semantic-cache                      # all phases
make test-semantic-cache CACHE_TYPE=direct    # preconditions + direct + lifecycle (no embeddings needed)
make test-semantic-cache CACHE_TYPE=semantic  # preconditions + fixtures + semantic + lifecycle
make test-semantic-cache-complete             # plugin unit tests + this suite
```

Or directly with `go test` (from this directory):

```bash
# All phases
RUN_FORCE=1 GOWORK=off go test -v ./...

# Single phase
RUN_FORCE=1 GOWORK=off go test -v -run '^(TestPreconditions|TestDirect)$' ./...

# Single case
RUN_FORCE=1 GOWORK=off go test -v -run 'TestDirect/1.1_exact_match_chat' ./...

# Keep the plugin around for post-mortem
RUN_KEEP_PLUGIN=1 RUN_FORCE=1 GOWORK=off go test -v ./...
```

Phases: `TestPreconditions`, `TestDirect`, `TestParaphraseFixtures`, `TestSemantic`, `TestLifecycle`.

`GOWORK=off` is required because this module isn't in the repo's `go.work` (test modules under `tests/*` follow the same pattern as `tests/governance` — standalone).

## Env vars

| var | default | purpose |
| --- | --- | --- |
| `BIFROST_URL` | `http://localhost:8080` | Bifrost base URL |
| `SC_CHAT_MODEL_OPENAI` | `openai/gpt-4o-mini` | OpenAI chat model used in cases |
| `SC_CHAT_MODEL_OPENAI_ALT` | `openai/gpt-4o` | second OpenAI chat model for cache-by-model cases |
| `SC_EMBED_MODEL_OPENAI` | `text-embedding-3-small` | embedding model for the semantic phases |
| `SC_CHAT_MODEL_GEMINI` | `gemini/gemini-2.5-flash` | Gemini chat model |
| `SC_CHAT_MODEL_ANTHROPIC` | `anthropic/claude-haiku-4-5` | Anthropic chat model |
| `SC_NAMESPACE` | `BifrostSemanticCachePluginE2E` | vector store namespace (isolates test data from prod) |
| `RUN_FORCE` | unset | `1` → delete pre-existing plugin row before run |
| `RUN_KEEP_PLUGIN` | unset | `1` → skip teardown DELETE on exit |
| `TRAIL_SESSION_ID` | unset | stamped onto every log line when running under `trail` |

## Trail integration

Start Bifrost under `trail`, capture the session id, export it, then run:

```bash
trail run --label semantic-cache-e2e -- ./bifrost-http -port 8080 -app-dir tests/semanticcache
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
