# Token Exchange (On-Behalf-Of) Demo MCP Server

A plain MCP resource server for exercising Bifrost's `token_exchange` MCP auth
type against a **real identity provider** — Okta, Microsoft Entra, Keycloak,
Auth0, or any generic OIDC provider.

## What is This?

`token_exchange` moves the entire delegation dance into Bifrost and your
identity provider: on every tool call, Bifrost exchanges the caller's own
identity-provider token for one scoped to this server's audience and sends
only the exchanged token upstream. This server has no OAuth server logic of
its own — its only job is what a real internal MCP server behind token
exchange would actually do: **validate the bearer token it receives** and
prove whose identity arrived.

Validation is full OIDC discovery + JWKS signature verification + issuer +
audience checks via [`coreos/go-oidc`](https://github.com/coreos/go-oidc).
Nothing here trusts an unverified token; a missing, malformed, wrong-issuer,
or wrong-audience token is rejected with HTTP 401 before the MCP server ever
sees it.

## Prerequisites

At your identity provider, register a dedicated application authorized to
perform token exchange (or the on-behalf-of grant) for an audience. See
[`docs/mcp/auth/token-exchange.mdx`](../../../docs/mcp/auth/token-exchange.mdx)
for provider-specific setup notes (Okta custom authorization servers, Entra's
`jwt-bearer` grant, etc.) — the exact steps vary by vendor.

You'll need:

- Your identity provider's **issuer URL** (its OIDC discovery document must
  be reachable at `<issuer>/.well-known/openid-configuration`)
- The **audience** you registered the exchange application against
- The exchange application's **client ID**, for the Bifrost side
- The exchange application's **client secret**, for the Bifrost side —
  optional, depending on your identity provider and whether the application
  is registered as a public or confidential client

## Running the Server

### Prerequisites

```bash
go 1.26.5+
```

### Start the Server

```bash
# From this directory
TOKEN_EXCHANGE_ISSUER_URL="https://your-domain.okta.com/oauth2/your-auth-server-id" \
TOKEN_EXCHANGE_AUDIENCE="api://your-mcp-server" \
go run main.go
```

`TOKEN_EXCHANGE_ISSUER_URL` and `TOKEN_EXCHANGE_AUDIENCE` are both required —
the server fails fast at startup if either is missing, or if OIDC discovery
against the issuer fails.

`MCP_SERVER_PORT` optionally overrides the default port (`3004`).

Output:
```text
token-exchange-demo-server listening on http://localhost:3004/
Validating bearer tokens against issuer=https://your-domain.okta.com/oauth2/your-auth-server-id audience=api://your-mcp-server

Bifrost config:

{
  "name": "token_exchange_demo",
  "connection_type": "http",
  "connection_string": "http://localhost:3004/",
  "auth_type": "token_exchange",
  "token_exchange": {
    "audience": "api://your-mcp-server",
    "client_id": "<your exchange application's client_id>",
    "client_secret": "<your exchange application's client_secret>"
  },
  "tools_to_execute": ["*"]
}
```

## Connecting via Bifrost

Use the printed config above, filling in your exchange application's
`client_id` / `client_secret`. `auth_type: "token_exchange"` requires your
Bifrost deployment's identity-provider integration to be configured and
pointed at the same identity provider — see
[Token Exchange auth](../../../docs/mcp/auth/token-exchange.mdx#prerequisites).

## Available Tools

### 1. whoami

Returns the identity claims from the caller's exchanged token — the main
point of this server. A successful call proves the caller's own identity
reached this upstream server, not a shared credential.

```json
{
  "name": "whoami",
  "arguments": {}
}
```

Response:
```json
{
  "sub": "00u1a2b3c4d5e6f7g8h9",
  "email": "alice@example.com",
  "name": "Alice Smith",
  "aud": "api://your-mcp-server",
  "iss": "https://your-domain.okta.com/oauth2/your-auth-server-id",
  "exp": 1735689600
}
```

### 2. echo

Echoes back the input message, tagged with the caller's identity — useful
for seeing the delegated identity attached to an ordinary tool call, not just
`whoami`.

```json
{
  "name": "echo",
  "arguments": {
    "message": "Hello, World!"
  }
}
```

## Testing With Two Different Callers

The most useful check: call `whoami` as two different signed-in users (or
with two different bearer tokens) and confirm the response names a different
identity each time — that's the delegation working end-to-end, as opposed to
everyone hitting this server under one shared credential.

A caller with **no identity token at all** (e.g. a virtual-key-only request)
should fail before ever reaching this server — `token_exchange` has no
service-account fallback.

## Implementation Notes

This example intentionally:

- ✅ Validates every request's bearer token via real OIDC discovery + JWKS
  signature verification (issuer, audience, expiry) — no shortcuts
- ✅ Rejects with HTTP 401 on any validation failure, before the MCP server
  processes the request
- ❌ Does NOT implement any OAuth authorization-server endpoints — that's
  entirely Bifrost + your identity provider's job under `token_exchange`
- ❌ Does NOT cache or store anything about the caller — every request is
  validated fresh, mirroring how Bifrost itself never persists a caller's
  exchanged token to disk

## See Also

- [Token Exchange (On-Behalf-Of) auth](../../../docs/mcp/auth/token-exchange.mdx)
- [MCP Authentication Overview](../../../docs/mcp/auth/overview.mdx)
- [oauth-demo-server](../oauth-demo-server) — the per-user OAuth counterpart,
  which *is* a full authorization server, for when the upstream service runs
  its own OAuth instead of trusting your identity provider
