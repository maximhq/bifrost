# ATR Plugin

Screens LLM requests with [Agent Threat Rules (ATR)](https://github.com/Agent-Threat-Rule/agent-threat-rules)
— an open, MIT-licensed detection-rule standard for AI-agent / LLM / MCP threats
(prompt injection, tool poisoning, credential exfiltration, skill supply-chain
attacks).

The plugin keeps the gateway **language-agnostic**: instead of porting the ATR
engine to Go, it calls an OpenAI-compatible `/v1/moderations` endpoint backed by
ATR, or any service that returns the OpenAI moderation shape. On a flagged
prompt the request is short-circuited in `PreLLMHook` with a `403` before the
provider call.

## Running an ATR moderation endpoint

[pyatr](https://pypi.org/project/pyatr/) 0.5.0 and later ships one:

```bash
pip install "pyatr>=0.5.0"
python -m pyatr.moderation --port 8000   # POST http://127.0.0.1:8000/v1/moderations
```

By default it flags only matches from ATR rules marked stable, at severity
high or above, because lower-maturity rules produce false positives on benign
text. `--lane hunt` evaluates every rule, at that cost, but the server still flags
only matches at or above `block_severity` (high by default). The reference server has
no authentication and binds to `127.0.0.1`; run it next to the gateway.

## Usage

```go
import "github.com/maximhq/bifrost/plugins/atr"

plugin, err := atr.Init(&atr.Config{
    Endpoint:   "http://127.0.0.1:8000/v1/moderations", // ATR-backed moderation
    FailClosed: false,                                   // fail open if ATR is down
}, logger) // any schemas.Logger, or nil
// register `plugin` with your Bifrost instance like any other LLMPlugin
```

Or construct directly: `atr.New(endpoint, failClosed)` (no logging).

## Config

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `endpoint` | string | — (required) | OpenAI-compatible `/v1/moderations` URL backed by ATR. |
| `fail_closed` | bool | `false` | When the moderation call fails (unreachable, non-2xx, unreadable body, or no boolean `flagged`): `true` blocks the request, `false` lets it through. |

## Behavior

- `PreLLMHook` flattens the chat messages and function tool descriptions, POSTs
  `{"input": "<text>"}` to the endpoint, and reads the standard `{"results":[{"flagged":bool,"categories":{…}}]}`
  response (read up to 1 MiB). If `flagged`, it returns an `LLMPluginShortCircuit`
  carrying a `403` `BifrostError` whose message lists the matched categories in
  sorted order.
- An unreachable endpoint, a non-2xx status, an unreadable response or a missing
  or null `flagged` is logged as a warning (without the prompt) and handled by
  `fail_closed`.
- Empty prompts and benign prompts pass through untouched.
- `PreRequestHook` / `PostLLMHook` are pass-throughs.

## Tests

`go test ./...` — covers prompt extraction, block-on-flagged, allow-on-benign,
fail-open, fail-closed, non-2xx and oversized responses, category ordering,
warning logs, and config validation (uses `httptest`, no live endpoint required).
