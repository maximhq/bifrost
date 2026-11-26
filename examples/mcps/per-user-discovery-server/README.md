# per-user-discovery-server

A test fixture for Bifrost's per-user discovery: MCP clients with `per_user_discovery` on, which keep a separate discovery (tools, instructions, resources, resource templates and prompts) for every caller, made with that caller's own credential.

One process serves the same caller profiles through two doors:

| Door | Path | Bifrost auth type | Who the caller is |
| --- | --- | --- | --- |
| OAuth | `/oauth/mcp` | `per_user_oauth` | The username typed on the sign-in page. A mock OAuth 2.1 server on the same port handles RFC 9728 and RFC 8414 discovery, RFC 7591 registration, the code grant with S256 PKCE, and rotating refresh tokens. |
| Headers | `/headers/mcp` | `per_user_headers` | `X-User-Key: key-<name>`. `X-Workspace: beta` adds a `beta_search` tool; any other value is the main workspace. A key of another shape gets a 401. |

The name picks the caller's profile. A name no profile has gets alice's content under its own identity, so any number of ad-hoc callers share one discovery.

## Run

```bash
cd examples/mcps/per-user-discovery-server
GOWORK=off go run .                    # http://localhost:3005
GOWORK=off go run . -access-ttl 30s    # make OAuth refresh happen quickly
GOWORK=off go test ./...               # the fixture's own tests
```

`GOWORK=off` is needed when a `go.work` at the repository root does not list this module.

| Flag | Default | |
| --- | --- | --- |
| `-addr` | `localhost:3005` | Listen address |
| `-issuer` | `http://<addr>` | OAuth issuer and public base URL, when Bifrost reaches the fixture by another host name |
| `-access-ttl` | `10m` | OAuth access-token lifetime |
| `-refresh-ttl` | `24h` | OAuth refresh-token lifetime, kept across rotations |
| `-slow-delay` | `12s` | How long the `slow` profile takes to list its tools |

State is in memory. A restart forgets OAuth registrations and tokens, but a `client_id` the fixture does not know is still accepted at `/authorize`, so a Bifrost that registered before the restart signs in again without re-creating the client.

## Bifrost clients

```json
{
  "name": "pud_oauth",
  "connection_type": "http",
  "connection_string": "http://localhost:3005/oauth/mcp",
  "auth_type": "per_user_oauth",
  "oauth_config": { "scopes": ["notes"] },
  "per_user_discovery": true,
  "tools_to_execute": ["*"]
}
```

```json
{
  "name": "pud_headers",
  "connection_type": "http",
  "connection_string": "http://localhost:3005/headers/mcp",
  "auth_type": "per_user_headers",
  "per_user_header_keys": ["X-User-Key", "X-Workspace"],
  "per_user_discovery": true,
  "tools_to_execute": ["*"]
}
```

Verifying either client as an admin signs in as `admin` (OAuth) or submits `key-admin` (headers): that profile's `admin_audit` tool must not show up for anyone, since verification of a per-user-discovery client keeps nothing.

## Profiles

| Profile | Discovers | Drives |
| --- | --- | --- |
| `alice` | `whoami`, `search_notes`, `create_note`; instructions; resource `notes://handbook`; template `notes://note/{id}`; prompt `summarize_notes` | The baseline |
| `bob` | Exactly alice's discovery | Two callers sharing one cached copy |
| `carol` | Alice's plus `delete_note` (destructive annotation) | A different tool set |
| `dave` | Alice's tools and instructions, resource `notes://archive`, prompt `weekly_review` | Discoveries differing only in resources and prompts |
| `erin` | Alice's tools with titles, icons, annotations, an output schema and `_meta` | Tool details kept per caller |
| `frank` | Nothing: no tools, instructions, resources or prompts; no resource or prompt capability | An empty discovery |
| `grace` | `whoami` and an upstream tool named `connect` | The name Bifrost gives its own `<client>-connect` tool |
| `heidi` | `fetch-item`, `item-count` | Hyphenated names, which Bifrost maps to underscores |
| `ivan` | 150 tools of eight required fields each, listed 50 a page | A large, paginated discovery |
| `legacy` | Alice's discovery from a server that answers `server/discover` with method-not-found | A pre-2026-07-28 upstream, reached through `initialize` |
| `faulty` | `tools/list` always answers a JSON-RPC error | A discovery that fails (sign-in redirects with `tools=pending`) |
| `slow` | `tools/list` waits `-slow-delay` (12 s) | Discovery past the 10 s sign-in bound and the 5 s connect bound |
| `partial` | Tools list fine; resources, templates and prompts answer errors | A discovery whose resources and prompts lists fail |
| `admin` | `whoami`, `admin_audit` | The admin sign-in that verifies a client |
| any other name | Alice's discovery | Many callers sharing one copy, each with its own identity |

`whoami` answers `door=<oauth|headers> identity=<name> profile=<profile> workspace=<main|beta>`. Every other tool echoes its name, the caller and its arguments, so a test can tell whose credential a call reached the upstream with.

## Control API

No authentication; the fixture is for local test runs.

| Request | Does |
| --- | --- |
| `GET /admin/state` | Profiles with their current tools, runtime faults, revoked identities, live OAuth tokens, and the headers each headers caller last sent |
| `GET /admin/calls[?identity=alice]` | Requests per identity and JSON-RPC method; `tools/call` is counted per tool as `tools/call:<tool>` |
| `POST /admin/reset` | Clears runtime tools, faults, revocations and counters; OAuth sign-ins stay |
| `POST /admin/profiles/{profile}/tools` `{"name": "export_notes", "description": "..."}` | Adds a tool to a profile |
| `DELETE /admin/profiles/{profile}/tools/{tool}` | Removes a tool from a profile |
| `POST /admin/faults` `{"profile", "identity", "method", "tool", "kind", "delay_ms"}` | Adds a fault. Empty fields match everything. `kind` is `error`, `method_not_found`, `http500`, `http401` or `delay` |
| `DELETE /admin/faults` | Clears the runtime faults (each profile's own stay) |
| `POST /admin/revoke` `{"identity": "alice"}` | Both doors answer the identity with a 401, and its OAuth refresh tokens are refused |
| `POST /admin/restore` `{"identity": "alice"}` | Lifts a revoke |
| `POST /admin/expire` `{"identity": "alice"}` | Ends the identity's OAuth access tokens now, so the next request needs a refresh |

```bash
curl -s localhost:3005/admin/state | jq '.profiles[] | {name, tools}'
curl -s -X POST localhost:3005/admin/faults -d '{"identity":"bob","method":"tools/call","tool":"search_notes","kind":"error"}'
curl -s localhost:3005/admin/calls?identity=alice
```

## What it can test

| Part of per-user discovery | How to drive it |
| --- | --- |
| A caller with no discovery is offered `<client>-connect` | Any profile, before signing in or submitting |
| Connect without a credential returns the sign-in or submit link | Call connect before signing in |
| OAuth sign-in discovers before redirecting | Sign in as `alice` |
| A headers submit discovers by verifying | Submit `key-alice` |
| A submit whose values the upstream refuses fails | Submit `alice` (no `key-` prefix) |
| Discovery fails or times out at sign-in, then connect retries | Sign in as `faulty` or `slow`; or as `alice` with a runtime fault on `tools/list`, then clear it and call connect |
| Resources and prompts failing leaves those lists empty | `partial` |
| Callers see their own tools, instructions, resources and prompts | `alice` against `carol`, `dave`, `erin`, `frank` |
| Callers with equal discoveries share one copy | `alice`, `bob` and ad-hoc names discover byte-identical content |
| Tool details are kept | `erin` |
| An upstream tool named `connect` | `grace` |
| Hyphenated names and their mapping | `heidi` |
| A large, paginated discovery | `ivan` |
| A legacy upstream | `legacy` |
| A call reaches the upstream with its caller's credential | `whoami` or any tool's echo |
| Re-submitting headers replaces the discovery | Submit `X-Workspace: main`, then `beta` |
| A refresh keeps the discovery | Run with `-access-ttl 30s`, or `POST /admin/expire` |
| A credential that can no longer refresh drops the discovery | `POST /admin/revoke`, then expire |
| The upstream refusing a credential Bifrost still holds | `POST /admin/revoke`, or an `http401` fault on `tools/call` |
| The upstream's tools change after discovery | `POST` or `DELETE /admin/profiles/{profile}/tools`; a caller keeps its discovery until it connects or signs in again, since per-call connections hear no `list_changed` |
| Admin verification keeps nothing | Verify as `admin`; nobody sees `admin_audit` |
