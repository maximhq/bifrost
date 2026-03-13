- fix: MCP health monitor now automatically reconnects clients after consecutive failures using exponential backoff retry logic
- fix: MCP clients that fail initial connection on startup are retained in disconnected state and automatically recovered by the health monitor
- fix: MCP tool retrieval during connection no longer hangs indefinitely for failing STDIO/SSE connections — bounded by a 30s timeout
- fix: toolChoice silently dropped on Bedrock /converse and /converse-stream endpoints — auto, any, and specific tool constraints now correctly propagate to the model
- feat: adds option to select specific API key for routing rules
- feat: adds support for multiple weighted routing targets for probabilistic routing
- [breaking change] feat: routing rules no longer support top-level `provider`/`model` fields; replace with a `targets` array — e.g. `"targets": [{"provider": "openai", "model": "gpt-4o", "weight": 1.0}]`
- fix: preserve original audio filename in transcription requests
- fix: async jobs stuck in "processing" on marshal failure now correctly transition to "failed"
- feat: adds attachment support in Maxim plugin
- feat: add x-bf-api-key-id header support for explicit key selection by ID, with priority over x-bf-api-key name selection
- fix: streaming tool call indices for multiple parallel tool calls in chat completions stream
- fix: handle request body passthrough for count tokens endpoint for Anthropic and Vertex providers

## Migration Guide

### Routing Rules — `targets` array (breaking)

Routing rules now route requests via a `targets` array instead of top-level `provider` and `model` fields. This enables weighted probabilistic routing across multiple targets.

#### config.json

Before:
```json
{
  "id": "rule-1",
  "name": "Route to GPT-4o",
  "cel_expression": "true",
  "provider": "openai",
  "model": "gpt-4o"
}
```

After:
```json
{
  "id": "rule-1",
  "name": "Route to GPT-4o",
  "cel_expression": "true",
  "targets": [
    { "provider": "openai", "model": "gpt-4o", "weight": 1.0 }
  ]
}
```

For probabilistic routing across multiple targets, weights must sum to 1:
```json
{
  "id": "rule-2",
  "name": "Split traffic",
  "cel_expression": "true",
  "targets": [
    { "provider": "openai",    "model": "gpt-4o",          "weight": 0.7 },
    { "provider": "anthropic", "model": "claude-sonnet-4-6", "weight": 0.3 }
  ]
}
```

To pin a specific API key for a target, add `key_id`:
```json
"targets": [
  { "provider": "openai", "model": "gpt-4o", "key_id": "<key-uuid>", "weight": 1.0 }
]
```

#### API

The `POST /api/governance/routing-rules` and `PUT /api/governance/routing-rules/:id` request bodies follow the same shape. On `PUT`, omit `targets` entirely to leave existing targets unchanged — sending `"targets": []` is now a 400 error.

Before:
```json
{ "provider": "openai", "model": "gpt-4o" }
```

After:
```json
{ "targets": [{ "provider": "openai", "model": "gpt-4o", "weight": 1.0 }] }
```
