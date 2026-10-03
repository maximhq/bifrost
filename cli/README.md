# Bifrost CLI

The Bifrost CLI combines the existing interactive coding-agent launcher with a
non-interactive gateway management client. Running `bifrost` without a command
continues to open the launcher.

## Build

From the repository root:

```bash
make build-cli
./tmp/bifrost version
./tmp/bifrost help
```

From this directory:

```bash
GOWORK=off go build -o ../tmp/bifrost .
```

## Configure a gateway context

```bash
bifrost context add local --base-url http://localhost:8080 --use
bifrost context list
bifrost context show
```

Non-loopback gateway URLs must use HTTPS. Plain HTTP is accepted only for
`localhost`, `127.0.0.1`, and `::1` development endpoints, preventing stored
credentials from being sent over a remote plaintext connection.

Contexts contain non-secret connection metadata. Credentials are stored in the
operating-system keyring and are deliberately separated by purpose:

```bash
printf '%s\n' "$BIFROST_TEST_VIRTUAL_KEY" | bifrost auth set-virtual-key --stdin
printf '%s\n' "$BIFROST_TEST_MANAGEMENT_KEY" | bifrost auth set-management-key --stdin
bifrost auth status
```

For an OSS gateway using dashboard password authentication:

```bash
printf '%s\n' "$BIFROST_TEST_ADMIN_PASSWORD" |
  bifrost auth login --username admin --password-stdin
bifrost auth logout
```

The following environment variables provide temporary or CI configuration.
Credential variables override keyring values when the command consumes that
credential:

- `BIFROST_BASE_URL`
- `BIFROST_CONTEXT`
- `BIFROST_VIRTUAL_KEY`
- `BIFROST_MANAGEMENT_KEY`
- `BIFROST_SESSION_TOKEN`
- `BIFROST_AGENT_TOKEN`
- `BIFROST_AGENT_VIRTUAL_KEY_ID`
- `BIFROST_OUTPUT`

`BIFROST_AGENT_TOKEN` supplies an access token for the current process only.
Automatic SSO refresh requires the access/refresh pair stored by
`bifrost auth login` in a named context; environment-only and ad-hoc
`--base-url` authentication does not rotate an expired agent token.

## Management examples

```bash
bifrost status --output json
bifrost doctor
bifrost models list
bifrost providers list --query limit=20
bifrost credentials list openai
bifrost virtual-keys list --query search=developer
bifrost teams list
bifrost users list                       # Enterprise
bifrost resources list                   # discover every first-class resource
bifrost resources describe access-profiles
```

The first-class registry covers OSS resources (providers and credentials,
virtual keys, teams, customers, routing, model configs, pricing, prompts,
plugins, skills, webhooks, MCP, logs, notifications, and gateway config) plus
Enterprise resources (users, projects, business units, access profiles, RBAC,
audit logs, prompt deployments, administrative keys, alerting, guardrails,
circuit breakers, network trust, cluster, branding, licensing, and edge/device
policy). `bifrost resources list --output json` is the authoritative list for
the installed binary.

JSON-backed first-class management mutations and `bifrost api call` accept
either inline JSON or a file. They support credential-free dry runs, and
destructive operations require explicit confirmation. The generic
`bifrost request` command does not provide these safeguards:

```bash
bifrost providers create --file provider.json --dry-run
bifrost providers create --file provider.json
bifrost virtual-keys delete "$VK_ID" --dry-run
bifrost virtual-keys delete "$VK_ID" --yes
```

Two convenience commands have specialized input modes:

- `bifrost virtual-keys generate` uses named flags such as `--name`,
  `--provider`, and `--allowed-model`; it does not accept `--body` or `--file`.
- `bifrost credentials create` accepts `--body`/`--file` for complete JSON, or
  uses its named flags when neither body option is supplied.

## Complete operation-level access

The generated operation catalog is derived from `docs/openapi/openapi.json`:

```bash
bifrost api list --search virtual --tag "Virtual Keys"
bifrost api describe getVirtualKey
bifrost api call getVirtualKey --param vk_id="$VK_ID"
bifrost api call updateVirtualKey \
  --param vk_id="$VK_ID" \
  --file virtual-key-update.json \
  --dry-run
```

For a runtime route that is not in the bundled catalog, use the raw gateway
transport. Paths must remain relative to the configured gateway, preventing a
credential-bearing request from being redirected to another host.

```bash
bifrost request GET /api/version --auth none
bifrost request GET /api/logs --query limit=10 --output json
bifrost request POST /v1/responses --auth inference --file request.json
bifrost request POST /v1/responses --auth inference --file request.json --stream
bifrost request GET /api/agent/usage-summary --auth agent --output json

# Structured spelling for scripts
bifrost http request GET /api/version --auth none
```

`--file` supplies a complete request body and is limited to 32 MiB. It does not
construct multipart uploads from a local file. For large or provider-specific
multipart File API uploads, use the provider SDK or an HTTP client until the
CLI exposes a dedicated streaming upload command; list, retrieve, download,
and delete operations remain available through the catalog.

`--timeout` covers the complete exchange for buffered requests. For `--stream`,
it limits response-header receipt only; the request context or user cancellation
controls the body lifetime after streaming begins.

## Inference and coding agents

```bash
bifrost chat --model openai/gpt-5 --message "Hello"
bifrost chat completions openai/gpt-5 \
  -m "system:Be concise" -m "user:Hello" \
  --temperature 0.2 --max-tokens 200
bifrost infer embeddings --file embedding-request.json
bifrost run claude --model anthropic/claude-sonnet-5 -- --verbose
bifrost run codex --model openai/gpt-5

# Direct aliases forward native arguments without requiring --
bifrost claude --resume
bifrost codex exec "summarize this repository" --json
```

Launcher sessions use temporary configuration where required. Persistent native
configuration is explicit and reversible:

```bash
bifrost configure claude --model anthropic/claude-sonnet-5
bifrost unconfigure claude
bifrost configure codex --model openai/gpt-5
bifrost unconfigure codex
```

Restoration receipts are owner-readable only. `unconfigure` refuses to overwrite
an agent file edited after Bifrost configured it unless `--force` is supplied.

## Test

Fast isolated CLI suite:

```bash
cd cli
GOWORK=off go test ./...
GOWORK=off go vet ./...
```

Repository-standard suite:

```bash
make test-cli
```

Verify that the generated API catalog is current:

```bash
cd cli
GOWORK=off go generate ./internal/operations
git diff --exit-code -- internal/operations/catalog.gen.go
```

Gateway smoke test, against a disposable local instance:

```bash
make dev
# In a second terminal:
make build-cli
./tmp/bifrost context add smoke --base-url http://localhost:8080 --use
./tmp/bifrost status --output json
./tmp/bifrost doctor
./tmp/bifrost models list --output json
./tmp/bifrost api call getVersion --auth none --output raw
```

Use disposable resources for mutation testing and delete only IDs created by
the test. Never run destructive smoke commands against a production context.

The paid coding-agent E2E suite must be scoped to control provider cost:

```bash
make run-cli-harness-test \
  CLI=codex \
  PROVIDER=openai \
  SCENARIO=simple-chat \
  PARALLEL=1
```

The generic HTTP operation layer covers JSON and arbitrary request bodies. Live
WebSocket/WebRTC interaction, model-deployment transactions, team
self-assignment, and resumable encryption migration require additional gateway
contracts and are not represented as completed capabilities here. Use
`api call` or `request` for existing Bifrost endpoints that do not yet have an
ergonomic wrapper.

## Enterprise browser SSO

Use browser SSO for user-scoped inference and to inspect the virtual keys
assigned to your Enterprise identity. The browser reuses the gateway's existing
identity provider session. The CLI receives opaque `ck-bf-agent-*` access and
refresh tokens from Bifrost; it never reads or stores the browser cookie, IdP
access token, or password.

```bash
bifrost context add production --base-url https://gateway.example.com --use
bifrost auth login
bifrost auth whoami --output json
bifrost auth status --output json
bifrost virtual-keys list --output table
bifrost virtual-keys assigned --output table
bifrost usage
bifrost usage budgets --output json
bifrost usage rate-limits --output yaml
bifrost auth select-virtual-key "$VIRTUAL_KEY_ID"
bifrost models list --output table
```

When browser SSO is the only configured credential, `virtual-keys list` calls
the user-scoped `/api/agent/virtual-keys` endpoint and returns only keys assigned
to that user. If a management API key or OSS dashboard session is also present,
the same command retains its administrative behavior and calls
`/api/governance/virtual-keys`. Use `virtual-keys assigned` to force the
user-scoped view when both credential types are present.

`bifrost usage` is the terminal equivalent of the Edge tray usage panel. It
shows every user-visible budget and rate-limit scope plus top model and app
usage. `summary`, `budgets`, `rate-limits`, `models`, and `apps` views support
table, JSON, YAML, and raw output. Token and request counters remain integers
through decoding and rendering, including values larger than JavaScript's safe
integer range.

Default table output is intentionally compact: list responses fit populated
fields to the terminal width and show at most eight columns, common identity
and status fields come first, and nested values are summarized for terminal
readability. The CLI prints a notice when it omits fields. Use `--output json`
or `--output yaml` for the complete response.

SSO inference mirrors the Edge agent's gateway mode. The CLI sends the opaque
agent access token as `Authorization: Bearer ...`, identifies itself as the
Bifrost CLI, and attributes usage to `bifrost-cli`. The gateway resolves the
user's applicable access profile or an assigned virtual key; the CLI never
downloads or stores a virtual-key secret value. To choose among assigned keys,
store only its non-secret row ID:

```bash
bifrost virtual-keys assigned --output table
bifrost auth select-virtual-key "$VIRTUAL_KEY_ID"
bifrost auth clear-selection
```

When both an SSO session and a legacy raw virtual key are configured, the CLI's
automatic inference authentication sends only the SSO token. The raw key
remains a fallback for contexts without a signed-in session. Explicit
`x-bf-vk` or `Authorization` headers supplied to `bifrost request` are treated
as intentional per-request overrides; gateway conflict policy matters only
when a manually constructed request carries both credentials.

Browser SSO does not authorize administrative management endpoints. Commands
such as `users list`, `providers create`, or `virtual-keys generate` still need
a scoped `bfst-*` management API key:

```bash
printf '%s\n' "$BIFROST_MANAGEMENT_KEY" |
  bifrost auth set-management-key --stdin
```

For a machine where the browser cannot be launched automatically, keep the CLI
running and open the printed URL yourself:

```bash
bifrost auth login --no-browser
```

The printed URL targets `/api/agent/auth/authorize`. When the browser does not
already have a gateway session, the gateway first shows its normal login page
and preserves the authorization URL in `return_to`; completing login resumes
the CLI flow.

The callback browser and CLI must run on the same machine because authentication
returns to an ephemeral `127.0.0.1` port. SSH-only and container sessions should
use a scoped management API key until the gateway exposes a device-code flow.
Access and rotating refresh tokens are stored separately in the operating-system
keyring. An agent-authenticated inference or agent self-service request whose
response identifies a rejected agent session refreshes once and retries once,
including streaming requests. Explicit agent-auth rejections and legacy bare
`401` responses can refresh; unmarked `403` policy failures and virtual-key
policy failures are returned without needlessly rotating a valid session.
Use `bifrost auth logout` to revoke the Enterprise agent session and clear its
local access, refresh, and cached identity values. The implementation uses the
existing `/api/agent/auth/*` contract and requires no gateway schema changes.

The interactive launcher exposes the same flow on its **Enterprise SSO** row.
Select **sign in** to open the PKCE browser flow, or select the signed-in row to
log out and press `y` in the confirmation prompt. After a successful token
exchange, the browser shows a Bifrost CLI completion page, and the launcher
returns to the same context. Editing the context's base URL revokes and clears
its existing SSO session before the new URL is saved, preventing credentials
issued by one gateway from being used with another.

The auth exchange includes a random opaque installation ID so Enterprise device
licensing behaves consistently across CLI contexts. That value is generated by
the CLI, stored in the operating-system keyring, retained across logout, and is
not derived from a hostname, MAC address, disk ID, or other machine identifier.

Persistent credentials require a named context. Ad-hoc `--base-url` requests
may use credential environment variables, but `auth login` and `auth set-*`
will not create a hidden keyring namespace, and an environment-provided agent
access token is not automatically refreshed. Command resolution loads only the
credential kinds needed for that operation.

Stable exit codes are `0` success, `1` general/usage, `3`
authentication/authorization, `4` not found, `5` conflict, and `6` for
timeouts, HTTP 5xx responses, and DNS or connection failures.
The open-source CLI depends only on the public Enterprise HTTP wire contract; it
does not import or publish Enterprise implementation code.

The SSO session also applies to coding agents launched through `run` or the
interactive launcher. Claude receives a temporary, session-scoped
`apiKeyHelper` that calls `bifrost auth print-token`; the helper validates the
current session and rotates rejected credentials before printing the agent
token. The temporary settings contain no token and are removed when Claude
exits. Codex receives the current agent token in its child-process environment.
Its isolated temporary `CODEX_HOME` starts from the user's `config.toml`, so
MCP servers, project trust entries, and preferences remain available while
authentication files and session history stay isolated. Changes made inside
that temporary home are discarded when Codex exits.
OpenCode receives the token in an owner-readable temporary provider config,
which is deleted when the process exits. Persistent `configure` remains
virtual-key-only so short-lived user credentials are never written to an
agent's long-lived config.

Harness authentication and refresh behavior is intentionally explicit:

| Harness | Enterprise agent token | Virtual key | Refresh caveat |
| --- | --- | --- | --- |
| Claude Code | Yes | Yes | `apiKeyHelper` can refresh during the session |
| Codex CLI | Yes | Yes | Token is fixed for the process; restart after expiry |
| OpenCode | Yes | Yes | Token is fixed for the process; restart after expiry |
| Gemini CLI | No | Yes | Requires a virtual key when inference auth is enabled |

Gemini API-key mode sends credentials as `x-goog-api-key`, while Enterprise
agent sessions authenticate through `Authorization: Bearer`. Do not place an
agent token in `GEMINI_API_KEY`; the current CLI fails with a clear virtual-key
instruction instead.

Codex's `/model` picker is backed by Codex's own catalog, not Bifrost's
`/v1/models` response. The launcher can pass an arbitrary provider-qualified
model at process startup, but an in-session `/model provider/model` attempt may
leave the previous Codex model selected. Use `/status` to verify it and relaunch
with `bifrost run codex --model provider/model` when necessary.

OpenCode renders its own conversation UI. Both `bifrost launch` and the direct
`bifrost opencode` alias give the harness native terminal passthrough. When
`~/.config/opencode/tui.json` has no theme, the launcher supplies OpenCode's
`system` theme; set an explicit theme in that file if the ANSI palette has low
contrast.

`bifrost launch` uses native terminal passthrough by default. The previous
multiplexer remains available with `bifrost launch --tabs`; its virtual
terminal can render some harness UI features differently.

`bifrost auth print-token` intentionally writes a credential to stdout for
agent helpers and scripts. Do not paste its output into logs or configuration
files. Refresh-token rotation is serialized per context so concurrent helper
processes cannot consume the same refresh token.

OSS gateways retain password login:

```bash
printf '%s\n' "$BIFROST_ADMIN_PASSWORD" |
  bifrost auth login --username admin --password-stdin
```

## Test matrix

Run the isolated unit, static-analysis, code-generation, and build checks:

```bash
cd cli
GOWORK=off go test ./...
GOWORK=off go vet ./...
GOWORK=off go generate ./internal/operations
git diff --exit-code -- internal/operations/catalog.gen.go
GOWORK=off go build -trimpath -o /tmp/bifrost-cli .
```

Exercise offline discovery without a running gateway:

```bash
/tmp/bifrost-cli help
/tmp/bifrost-cli resources list --output json
/tmp/bifrost-cli resources describe virtual-keys
/tmp/bifrost-cli api list --search virtual --output json
/tmp/bifrost-cli api describe createChatCompletion --output yaml
/tmp/bifrost-cli completion bash >/tmp/bifrost-completion.bash
```

For an integration run, start a disposable OSS gateway and use credentials
created only for the test:

```bash
bifrost context add smoke --base-url http://localhost:8080 --use
printf '%s\n' "$BIFROST_TEST_VIRTUAL_KEY" | bifrost auth set-virtual-key --stdin
bifrost status --output json
bifrost doctor
bifrost models list --output json
bifrost chat completions "$BIFROST_TEST_MODEL" -m 'user:reply with pong' --output json
bifrost providers create --file /tmp/provider.json --dry-run --output json
bifrost request GET /api/version --auth none --output raw
```

Against a disposable Enterprise instance, additionally smoke-test read paths
before mutations:

```bash
bifrost auth login
bifrost auth whoami --output json
bifrost virtual-keys assigned --output json
bifrost usage --output table
bifrost usage budgets --output json
bifrost usage rate-limits --output json
bifrost models list --output json

printf '%s\n' "$BIFROST_TEST_MANAGEMENT_KEY" | bifrost auth set-management-key --stdin
bifrost users list --limit 5 --output json
bifrost projects list --limit 5 --output json
bifrost access-profiles list --limit 5 --output json
bifrost roles list --output json
bifrost audit-logs list --limit 5 --output json
bifrost cluster health --output json
```

The coding-agent cloud harness does not replace the management API smoke test.
Run the block above with a scoped disposable management key, then exercise one
create/get/update/delete cycle for each high-risk resource family required by
the release. Run every mutation with `--dry-run` first and retain the returned
IDs so cleanup targets only resources created by the test.

For every mutation, first run the identical invocation with `--dry-run`. Use
unique test names, record each returned ID, and clean up only those IDs with
`--yes`. Persistent agent configuration should be tested in a disposable home
directory or user account, followed immediately by `unconfigure` and a byte-for-byte
comparison of the original files. Paid inference/agent tests should use one
small model, one short prompt, and serial execution.
