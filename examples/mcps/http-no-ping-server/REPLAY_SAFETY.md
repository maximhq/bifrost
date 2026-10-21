# Local MCP replay-safety harness

The `mcp-replay-safety` provider-harness folder exercises the real Chat/Responses
tool-execution endpoints without calling an LLM or an external mutation service.
It is `[PREVIEW]` and serial: normal harness sweeps do not run it.

## Fixture and expected behavior

Start this example with `MCP_REPLAY_SAFETY_FIXTURE=1` to add five disposable tools:

| Tool | Contract | Expected committed effects after response loss |
|---|---|---:|
| `replay_append` | No annotations, additive mutation | 1; no automatic replay |
| `replay_additive` | Destructive=false, idempotent=false | 1; no automatic replay |
| `replay_destructive` | Destructive=true, idempotent=false | 1; no automatic replay |
| `replay_read` | Actually read-only | 0 across two invocations |
| `replay_upsert` | Actually idempotent within a scenario | 1 across two invocations |

The harness seeds `POST /_replay/scenarios` with an identifier, tool name and fault
(`503`, `401` or `reset`). The actual MCP handler completes its local mutation
before the first response fails. `GET /_replay/scenarios/<id>` reports wire calls,
handler invocations and committed effects independently. Each scenario is deleted
after its assertions; the run-unique client is unregistered and the login is ended.

Retained state is bounded at 256 scenarios, keyed by an identifier of at most 96
characters, with only tool/fault names and three integer counters per entry. It is
freed on scenario deletion or process exit. Fixture MCP request bodies are capped
at 64KiB. The default example behavior and dependencies are unchanged when the
environment flag is absent. Keep this disposable, unauthenticated fixture on
loopback; do not expose its management endpoints or use it with customer data.

Unsafe calls use Bifrost's existing HTTP error status (400) and explicitly report
that execution may have succeeded and automatic replay was suppressed. Do not
manually rerun an unsafe operation just because its response was lost.

## Setup

Use an isolated auth-enabled profile: private-address MCP registration deliberately
requires genuine dashboard authentication. These are disposable fixture credentials,
not production credentials. The `openai` label in request names is only a harness
filter keyword.

From the checkout root, prepare the profile and environment (requires `jq`):

```bash
mkdir -p tmp
MCP_REPLAY_APP_DIR=$(mktemp -d "$PWD/tmp/mcp-replay-app.XXXXXX")
jq --arg app "$MCP_REPLAY_APP_DIR" '
  .providers = {} |
  .mcp.client_configs = [] |
  .governance.auth_config = {
    "is_enabled": true,
    "admin_username": "mcp_replay_harness",
    "admin_password": "local-fixture-only-password"
  } |
  .config_store.config.path = ($app + "/config.db") |
  .logs_store.config.path = ($app + "/logs.db") |
  .client.oauth2_server_config.issuer_url = "http://127.0.0.1:3038" |
  del(.vector_store)
' tests/integrations/python/config.json > "$MCP_REPLAY_APP_DIR/config.json"
printf '%s\n' "$MCP_REPLAY_APP_DIR" > tmp/mcp-replay-app-dir
cat > tmp/mcp-replay-harness.env.json <<'JSON'
{"name":"Local MCP replay safety","values":[{"key":"mcpReplayUsername","value":"mcp_replay_harness","enabled":true},{"key":"mcpReplayPassword","value":"local-fixture-only-password","enabled":true},{"key":"mcpReplayFixtureUrl","value":"http://127.0.0.1:3037","enabled":true}]}
JSON
lsof -nP -iTCP:3037 -sTCP:LISTEN
lsof -nP -iTCP:3038 -sTCP:LISTEN
```

Both port checks must be empty, or show only the intended servers from this checkout.
This profile contains no providers or vector store, so it does not need backing
vector-store services. The SQLite files are isolated from the standard test profile.

Start the fixture in one terminal:

```bash
(cd examples/mcps/http-no-ping-server && MCP_REPLAY_SAFETY_FIXTURE=1 MCP_SERVER_PORT=3037 GOWORK=off GOTOOLCHAIN=go1.27.3 go run .)
```

Start Bifrost in another:

```bash
make dev PORT=3038 APP_DIR=$(cat tmp/mcp-replay-app-dir)
```

## Run the harness yourself

The Makefile performs an authenticated configuration preflight before the collection's
own login. Bootstrap a genuine disposable session, then pass its header explicitly.
No token is printed. Wait for `/health` first.

```bash
until curl -sf http://127.0.0.1:3038/health >/dev/null; do sleep 1; done
MCP_REPLAY_COOKIE_JAR=$(mktemp "$PWD/tmp/mcp-replay-auth.XXXXXX")
curl -fsS -c "$MCP_REPLAY_COOKIE_JAR" -H 'Content-Type: application/json' --data '{"username":"mcp_replay_harness","password":"local-fixture-only-password"}' http://127.0.0.1:3038/api/session/login >/dev/null
MCP_REPLAY_BOOTSTRAP_TOKEN=$(awk '$6 == "token" {print $7}' "$MCP_REPLAY_COOKIE_JAR")
test -n "$MCP_REPLAY_BOOTSTRAP_TOKEN"
export BIFROST_E2E_AUTH_HEADER="Bearer $MCP_REPLAY_BOOTSTRAP_TOKEN"
MCP_REPLAY_EXIT=0
make run-provider-harness-test PROVIDER=openai FEATURE="mcp-replay-safety" INCLUDE_PREVIEW=1 PARALLEL=0 DB_VERIFY=0 SKIP_STREAM_CANCEL=1 ENV_FILE=tmp/mcp-replay-harness.env.json BASE_URL=http://127.0.0.1:3038 || MCP_REPLAY_EXIT=$?
curl -fsS -X POST -H "Authorization: $BIFROST_E2E_AUTH_HEADER" http://127.0.0.1:3038/api/session/logout >/dev/null
unset BIFROST_E2E_AUTH_HEADER MCP_REPLAY_BOOTSTRAP_TOKEN
test "$MCP_REPLAY_EXIT" -eq 0
```

Stop the two foreground servers afterwards. The collection performs its own login,
uses only run-owned scenario/client identifiers, and removes them on a successful run.
If a run aborts, restart the fixture to clear its bounded ledger and remove the recorded
run-owned MCP client from the isolated profile. Never clean up unrelated clients.

The local fixture tests can be run without a gateway or any provider credentials:

```bash
(cd examples/mcps/http-no-ping-server && GOWORK=off GOTOOLCHAIN=go1.27.3 go test -race -count=3 ./...)
```
