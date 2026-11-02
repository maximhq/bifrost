# Realtime and Live LLM hook example

This native Go plugin uses the same `PreLLMHook` and `PostLLMHook` exports as
other LLM plugins. It observes native realtime turns and Live billing units,
logs usage, and reads cost through `ctx.CalculateCostBreakdown`. Its HTTP transport
pre-hook uses `ctx.SetValueForRealtimeTurns` to carry an example user identifier
from session setup into each turn or Live billing unit.

The plugin leaves responses unchanged. Bifrost's logging and governance plugins
remain responsible for inference log storage, budget charging, and rate limits.
`ctx.Log` adds diagnostic messages to the plugin logs; it does not charge usage.
No audio, transcripts, credentials, or request bodies are logged by this example.

## Build and configure

From this directory:

```sh
GOWORK=off go mod tidy
make build
```

Build the plugin with the same Go toolchain, Bifrost core revision, and shared
dependency versions as the target gateway. The local `replace` uses this
checkout's core. Go plugins must match the host's shared packages exactly.

Add this entry to the gateway's `plugins` array:

```json
{
  "enabled": true,
  "name": "realtime-hooks",
  "path": "/absolute/path/to/build/realtime-hooks.so"
}
```

Use an already configured realtime or Live provider and send requests through
Bifrost's native WebSocket or WebRTC handlers. There is no separate realtime
plugin registration. Other request types pass through unchanged.

## What the hooks receive

| Request type | Pre-hook | Post-hook |
| --- | --- | --- |
| `RealtimeRequest` | A turn represented by `req.ResponsesRequest` | A normalized `resp.ResponsesResponse`, including reported text/audio token usage |
| `LiveRequest` | A voice or backend billing unit represented by `req.ResponsesRequest` | Usage for that unit, including `Usage.AudioSeconds` for voice units and token usage for backend units |

Live billing units are not necessarily conversation turns. A session can have
multiple voice windows and backend units. Bifrost converts cumulative provider
usage into unit usage before settlement, so do not add cumulative session totals
again in the plugin.

The transport carries values written in a unit's LLM pre-hook into that unit's
post-hook context. This example stores the request type using a private typed key.

For metadata that starts in the HTTP setup request, opt in explicitly:

```go
// In HTTPTransportPreHook, before the session snapshot:
ctx.SetValueForRealtimeTurns(userIDKey, userID)

// In PostLLMHook, on every native realtime turn or Live billing unit:
userID, _ := ctx.Value(userIDKey).(string)
```

This example reads `X-Example-User-ID` from the setup request and logs it on each
unit. Use synthetic values when testing. The header is demonstration metadata,
not authentication. A production plugin should derive trusted identity through
its authentication flow and avoid logging personal data.

Regular `SetValue` does not opt HTTP-only values into sessions. The opt-in setter
also sets the ordinary context value and respects restricted writes. Each
connection receives its own snapshot. Later unit-local writes do not change the
session or other units. Values are shallow-copied and can live until the session
ends, so use small immutable values under plugin-owned keys. Never opt in
credentials, request contexts, traces, callbacks, channels, or request-backed
buffers. This applies to native WebSocket and WebRTC sessions, including Live.

HTTP transport hooks observe the HTTP setup request, not every WebSocket frame
or realtime turn. A plugin that takes ownership of the socket and short-circuits
the native handler also takes responsibility for its processing; native turn
hooks do not automatically run on that separate relay.

Post-hooks can receive a nil response or an error, including admission refusal
and disconnects. This example preserves both. To refuse a turn in a pre-hook,
return `LLMPluginShortCircuit.Error`; returning the hook's ordinary Go error only
produces a warning. Native realtime turns currently reject synthetic response
short-circuits.

Cost requires applicable catalog pricing. A zero or unavailable cost does not
establish that usage is free. This example only calculates response cost;
error-only billed usage remains the responsibility of the built-in accounting.

## Check the example

With the plugin loaded, complete two turns on one native realtime connection.
Inspect the plugin logs for two `Starting realtime unit` messages and the
corresponding usage messages. For Live, inspect `Starting live unit` and
`audio_seconds` messages as voice windows settle, plus backend token usage when
the session delegates. Cost messages appear when the catalog returns a breakdown.
Pre-hook and post-hook messages appear together in each unit's plugin logs.

Send `X-Example-User-ID: test-alice` on that connection's HTTP setup request and
`X-Example-User-ID: test-bob` on a concurrent connection. Each settled unit should
contain only its own `user_id` in the `unit metadata` log message. Repeat with a
fresh connection without the header and verify there is no metadata message.
For browser WebRTC, put the header on the HTTP SDP setup request. Browser
WebSocket APIs cannot set arbitrary headers; use a client that supports them.
