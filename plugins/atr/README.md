# ATR Plugin

Screens LLM requests with [Agent Threat Rules (ATR)](https://github.com/Agent-Threat-Rule/agent-threat-rules)
— an open, MIT-licensed detection-rule standard for AI-agent / LLM / MCP threats
(prompt injection, tool poisoning, credential exfiltration, skill supply-chain
attacks).

The plugin keeps the gateway **language-agnostic**: instead of porting the ATR
engine to Go, it calls an OpenAI-compatible `/v1/moderations` endpoint backed by
ATR (for example `pyatr.adapters.openai_moderation`, or any service that returns
the OpenAI moderation shape). On a flagged prompt the request is short-circuited
in `PreLLMHook` with a `403` before the provider call.

## Usage

```go
import "github.com/maximhq/bifrost/plugins/atr"

plugin, err := atr.Init(&atr.Config{
    Endpoint:   "http://localhost:8000/v1/moderations", // ATR-backed moderation
    FailClosed: false,                                   // fail open if ATR is down
})
// register `plugin` with your Bifrost instance like any other LLMPlugin
```

Or construct directly: `atr.New(endpoint, failClosed)`.

## Config

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `endpoint` | string | — (required) | OpenAI-compatible `/v1/moderations` URL backed by ATR. |
| `fail_closed` | bool | `false` | When the moderation endpoint is unreachable: `true` blocks the request, `false` lets it through. |

## Behavior

- `PreLLMHook` flattens the chat messages, POSTs `{"input": "<text>"}` to the
  endpoint, and reads the standard `{"results":[{"flagged":bool,"categories":{…}}]}`
  response. If `flagged`, it returns an `LLMPluginShortCircuit` carrying a `403`
  `BifrostError` whose message lists the matched categories.
- Empty prompts and benign prompts pass through untouched.
- `PreRequestHook` / `PostLLMHook` are pass-throughs.

## Tests

`go test ./...` — covers prompt extraction, block-on-flagged, allow-on-benign,
fail-open, fail-closed, and config validation (uses `httptest`, no live endpoint
required).
