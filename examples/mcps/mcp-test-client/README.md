# mcp-test-client

A tiny interactive MCP client for exercising a running Bifrost `/mcp` endpoint
under any inbound-auth configuration. It speaks streamable HTTP via
[`mark3labs/mcp-go`](https://github.com/mark3labs/mcp-go) and supports both
credential styles the Bifrost MCP server accepts:

- **header credentials** — a virtual key (`x-bf-vk`, `Authorization: Bearer`, or
  `x-api-key`) or a session id (`x-bf-mcp-session-id`). Use with server auth
  mode `headers` or `both`.
- **OAuth** — full discovery (RFC 9728/8414) + dynamic client registration +
  PKCE authorization-code flow, with a local browser-callback. Use with server
  auth mode `both` or `oauth`.

Toggle the server-side knobs on your running instance (`mcp_server_auth_mode`,
`enforce_auth_on_inference`, `disable_vk_identity`, virtual-key active state,
...), then `reconnect` and `list` / `call` tools to see the effect. The OAuth
token is held in memory for the session, so `reconnect` after a knob flip does
not re-prompt unless the token is actually rejected.

## Build / run

```bash
cd examples/mcps/mcp-test-client
GOWORK=off go run . [flags]
```

## Examples

Virtual key over `x-bf-vk` (headers / both mode):

```bash
GOWORK=off go run . -url http://localhost:8080/mcp -auth headers -vk sk-bf-xxxxx
```

Same VK as a bearer, or as `x-api-key`:

```bash
GOWORK=off go run . -auth headers -bearer sk-bf-xxxxx
GOWORK=off go run . -auth headers -api-key sk-bf-xxxxx
```

Session id (only accepted while `enforce_auth_on_inference=false`):

```bash
GOWORK=off go run . -auth headers -session <session-id>
```

OAuth (both / oauth mode) — opens a browser for consent, registers dynamically:

```bash
GOWORK=off go run . -auth oauth -scope mcp
```

Anonymous (no creds), or one-shot non-interactive:

```bash
GOWORK=off go run . -auth headers                       # anonymous
GOWORK=off go run . -auth headers -vk sk-bf-xxx -once list
GOWORK=off go run . -auth headers -vk sk-bf-xxx -once 'call echo {"text":"hi"}'
```

## REPL commands

```
list | tools            list tools visible to the current credential
desc <tool>             show a tool's description + input schema
call <tool> [json]      call a tool, e.g. call echo {"text":"hi"}
set <header> <value>    change/add a header (then 'reconnect' to apply)
unset <header>          remove a header (then 'reconnect' to apply)
reconnect               redo start+initialize (use after toggling server knobs)
info                    show current url / auth / headers
help                    this text
quit | exit             leave
```

## Notes

- This module is intentionally outside the repo `go.work`; run it with
  `GOWORK=off` (or build a binary) so it resolves against its own `go.mod`.