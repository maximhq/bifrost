# Voice Live Realtime Plugin (PoC)

Serves **Azure AI Voice Live** realtime models — `gpt-realtime`, `phi4-mm-realtime` — over Bifrost's
existing `GET /v1/realtime` endpoint, using **only the HTTP transport plugin hooks**. No core change.

Bifrost has no Voice Live support today. This plugin adds it from the outside by hijacking the
client socket in `HTTPTransportPreHook`, completing the WebSocket upgrade itself, and relaying frames
to Voice Live in both directions.

## Implementational details

An HTTP→WebSocket upgrade is a 101 response plus a hijack of the underlying TCP socket. fasthttp
exposes that as `RequestCtx.Hijack`, and `HTTPTransportPreHook` runs inside the per-request goroutine
that owns the connection. So the only question is whether a plugin can reach the `*fasthttp.RequestCtx`.

`TransportInterceptorMiddleware` builds the plugin's context like this:

```go
bifrostCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline) // parent IS the *fasthttp.RequestCtx
bifrostCtx.SetValue(schemas.BifrostContextKeyModelCatalog, ...)  // always stamped
pluginCtx := bifrostCtx.WithPluginScope(&pluginName)             // what the hook receives
resp, err := plugin.HTTPTransportPreHook(pluginCtx, req)
```

and `WithPluginScope` (`core/schemas/context.go`) copies the parent across while leaving the scoped
value map empty:

```go
scoped := &BifrostContext{
	parent:        bc.parent,   // ← the *fasthttp.RequestCtx
	done:          bc.done,
	pluginScope:   name,
	valueDelegate: bc,
}
```

`GetParentCtxWithUserValues()` wraps `parent` in one `context.WithValue` layer per user value — and
the scoped context has none. So on the **plugin-scoped** context it returns the parent *unwrapped*:

```go
rctx, ok := ctx.GetParentCtxWithUserValues().(*fasthttp.RequestCtx) // ok == true
```

(The **root** context does not work: it always has at least the model catalog set, so its parent
comes back wrapped in `*context.valueCtx`, which the stdlib gives you no way to unwrap.)

`TestRequestCtxIsReachableFromPluginScope` in `main_test.go` asserts exactly this. If it ever fails,
this plugin is dead and the fix is a core affordance — see [If this breaks](#if-this-breaks).

### The flow

1. `HTTPTransportPreHook` sees `GET /v1/realtime` with `Upgrade: websocket`.
2. Model not in our list → `return nil, nil`, and Bifrost's own realtime handler serves it unchanged.
3. Ours → capacity check, recover the `RequestCtx`, `upgrader.Upgrade(rctx, handler)`.
4. Return `&schemas.HTTPResponse{StatusCode: 101}` so the transport stops and Bifrost's handler is
   bypassed. `Headers` and `Body` are `nil` deliberately: `applyHTTPResponseToCtx` only *sets* what
   it is given, so the handshake `Upgrade` already wrote survives untouched.
5. In the hijacked goroutine: dial Voice Live and pump frames both ways until either side closes.

### THE ONE RULE

**Do not touch the `*fasthttp.RequestCtx` inside the hijack handler.** fasthttp recycles it as soon as
the request handler returns — which is *before* the hijack handler runs. Everything the connection
needs (model, upstream URL, headers, timeouts) is captured into locals **before** `Upgrade` is called.
The hijacked `net.Conn`, and the `*ws.Conn` wrapping it, outlive the `RequestCtx` and are safe.

This is the same hazard `core/schemas/context.go` guards against when it refuses to read values
through to a pooled `RequestCtx` parent — that refusal exists because reading a recycled ctx caused a
real SIGSEGV.

## What this costs you

Short-circuiting at the transport layer means **Bifrost core never sees these connections**:

| Lost | Consequence |
|---|---|
| `PreLLMHook` / `PostLLMHook` | no governance, budgets, or rate limits per turn |
| Logging plugin | connections and turns do not appear in Bifrost logs |
| Telemetry / OTEL | no spans, no metrics |
| Cost tracking | no usage or spend attribution |
| `bfws.SessionManager` + connection pool | limits and pooling are the plugin's problem |

The plugin enforces its own `max_connections` for exactly that reason. Auth is **not** lost —
`HTTPTransportPreHook` runs *after* the transport's auth middlewares, so the caller is already
authenticated by the time the plugin claims the upgrade.

This is why the PoC is a PoC. The production shape is a `VoiceLiveProvider` in
`core/providers/voicelive/` implementing `schemas.RealtimeProvider`, which gets all of the above for
free. This plugin is how you find out whether Voice Live is worth that work — and it doubles as a
working reference for the wire protocol.

## Building

```bash
make build     # -> build/voicelive-realtime.so
go test ./...  # proves the hijack, the relay, and the fall-through
```

> **Build against the same core as the `bifrost-http` binary you will load this into.**
> Like every example here, `go.mod` carries `replace github.com/maximhq/bifrost/core => ../../../core`,
> which resolves to your checkout. Go's plugin loader requires the plugin and the host to agree on
> the exact version of every shared package, so a plugin built from a checkout that has drifted from
> the deployed binary fails to load with `different version of package`. Build both from the same
> source tree — ideally in the same CI job — or pin the plugin to the core version the target binary
> reports (`go version -m ./bifrost-http | grep bifrost/core`). See
> [Writing Go Plugins → Version Mismatch Errors](https://docs.getbifrost.ai/plugins/writing-go-plugin).

The tests run a real WebSocket client against a fake Voice Live server through a faithful replica of
`TransportInterceptorMiddleware`, and cover: claiming `gpt-realtime`, claiming `phi4-mm-realtime`
with a `azure/` prefix, text **and binary** frame relay, the upstream URL and `api-key` shape, and
unclaimed models falling through to Bifrost.

## Configuration

```json
{
  "plugins": [
    {
      "enabled": true,
      "name": "voicelive-realtime",
      "path": "/path/to/voicelive-realtime.so",
      "config": {
        "endpoint": "https://my-resource.services.ai.azure.com",
        "api_key": "env.AZURE_VOICE_LIVE_KEY",
        "api_version": "2025-10-01",
        "models": ["gpt-realtime", "phi4-mm-realtime"],
        "max_connections": 100,
        "allowed_origins": ["https://app.example.com"],
        "dial_timeout_seconds": 10,
        "idle_timeout_seconds": 300
      }
    }
  ]
}
```

| Option | Type | Default | Description |
|---|---|---|---|
| `endpoint` | string | — | **Required.** Azure AI resource root. `http(s)` is rewritten to `ws(s)`. Cleartext (`http://`/`ws://`) is rejected at startup outside loopback, since the `api-key` travels on this connection. A bare host defaults to `wss://`. |
| `api_key` | SecretVar | — | **Required.** Literal, `env.NAME`, or `vault.path/to/secret`. Sent as the `api-key` header. |
| `api_version` | string | `2025-10-01` | Voice Live `api-version`. **Check this against your resource** — Azure revises it, and a stale value fails the handshake. |
| `models` | string[] | `["gpt-realtime", "phi4-mm-realtime"]` | Models this plugin claims. Everything else falls through to Bifrost. |
| `max_connections` | integer | `100` | Concurrent plugin-owned connections. Bifrost's session manager does not see hijacked sockets. |
| `allowed_origins` | string[] | same-host | Origin allowlist. `["*"]` allows any. Empty uses the library's safe default. |
| `dial_timeout_seconds` | integer | `10` | Upstream handshake timeout. |
| `idle_timeout_seconds` | integer | `300` | Read deadline on both sockets. |

## Trying it

```bash
# Claimed by the plugin -> proxied to Voice Live
websocat "ws://localhost:8080/v1/realtime?model=gpt-realtime" \
  -H "Authorization: Bearer $BIFROST_KEY"

# Also claimed
websocat "ws://localhost:8080/v1/realtime?model=phi4-mm-realtime" \
  -H "Authorization: Bearer $BIFROST_KEY"

# Not claimed -> Bifrost's native realtime handler, unchanged
websocat "ws://localhost:8080/v1/realtime?model=openai/gpt-4o-realtime-preview" \
  -H "Authorization: Bearer $BIFROST_KEY"
```

Session config, voices, avatars and noise-suppression settings are passed through verbatim in the
`session.update` event, so Azure-specific extensions work without the plugin knowing about them.

## Known limitations

- **The path must already be routed.** Bifrost's router 404s unknown paths before any middleware runs
  (`server.go`'s `Router.NotFound`), so a plugin can only intercept paths Bifrost already registers.
  `/v1/realtime` is registered, which is why this works; a brand-new `/v1/voice-live` path would not.
- **No reconnect or upstream pooling.** Each client connection dials Voice Live fresh.
- **`GetParentCtxWithUserValues()` returning the raw `RequestCtx` is undocumented internal behaviour.**
  It follows from `WithPluginScope` leaving the scoped value map empty. Nothing guarantees that stays
  true.
- Go loads a `.so` once per path, so package-level state is shared by every plugin entry pointing at it.
