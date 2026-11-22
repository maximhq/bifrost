# Request Guard

`requestguard` is an opt-in Bifrost plugin for chat completion and Responses
requests. It evaluates model capabilities and request budgets, can recommend or
select a compatible model, and can share duplicate executions through a bounded
coalescer.

## Architecture and hook order

The HTTP transport runs its transport hooks around the LLM plugin pipeline. For
request guard, the relevant phases are:

1. `PreRequestHook` runs once for the top-level request after routing plugins.
   It extracts a scalar feature snapshot, estimates input/output tokens and cost,
   evaluates capability and budget policies, and may rewrite the provider/model
   to a compatible recommendation. It does not retain the pooled request.
2. `PreLLMHook` runs for each provider attempt. It re-extracts features so it
   sees tools injected after `PreRequestHook`, reevaluates limits, stores a
   bounded `GuardDecision`, and may block or join an eligible coalesced call.
3. `PostLLMHook` runs in reverse plugin order. It completes unary flights or
   publishes stream chunks and closes a flight on the final chunk or error. It
   also adds a concise intervention to response guardrail metadata when a
   warning, downgrade, recommendation, or coalescing decision exists.

The server registers request guard only when its built-in plugin entry is
enabled. It runs after the model catalog resolver so decisions use the final
provider/model selected by routing. `PreRequestHook` cannot reject a request;
enforced blocks are returned as `LLMPluginShortCircuit` values from
`PreLLMHook`.

The decision in `BifrostContext` contains only bounded scalar metadata and
small, capped result lists. Stream buffers stay in the coalescer, keyed by the
request fingerprint and bounded by `max_in_flight`, `max_buffered_chunks`, and
`max_buffered_bytes`. The request context retains only the leader's flight
handle; it never stores a request pointer or stream payload. Coalescing always
includes a SHA-256 discriminator built from resolved `Grant.Identity`
identifiers. Optional virtual-key and customer scopes add their identifiers
when enabled; they cannot disable the mandatory tenant boundary. Requests
without a resolved identity and large-payload passthrough requests bypass
coalescing. The configured bypass header is captured from the HTTP transport
request before inference begins.

## Configuration

Add an enabled built-in plugin entry to `config.json`. `wait_timeout` is in
milliseconds. The example enables capability blocking, budget checks, model
recommendations, unary coalescing, and response headers:

```json
{
  "plugins": [
    {
      "name": "requestguard",
      "enabled": true,
      "config": {
        "enabled": true,
        "capability_guard": {
          "mode": "block",
          "unknown_policy": "allow",
          "check_tools": true,
          "check_parallel_tools": true,
          "check_tool_choice": true,
          "check_structured_output": true,
          "check_vision": true,
          "check_streaming": true,
          "check_context_limit": true,
          "check_max_output_tokens": true,
          "allow_fallbacks_on_block": false
        },
        "budget_guard": {
          "mode": "block",
          "max_input_tokens": 0,
          "max_total_tokens": 0,
          "max_estimated_cost_usd": 0.05,
          "default_output_tokens": 1024,
          "safety_margin_multiplier": 1.1,
          "chars_per_token": 4,
          "image_token_estimate": 1024,
          "allow_fallbacks_on_block": false
        },
        "recommender": {
          "enabled": true,
          "max_recommendations": 3,
          "same_provider_only": true,
          "min_savings_pct": 10,
          "auto_downgrade_on_capability_failure": false,
          "auto_downgrade_on_budget_exceeded": false
        },
        "coalescer": {
          "enabled": true,
          "enable_streaming": false,
          "max_in_flight": 128,
          "max_temperature": 0,
          "wait_timeout": 25,
          "max_buffered_chunks": 1024,
          "max_buffered_bytes": 1048576,
          "include_virtual_key_scope": true,
          "include_customer_scope": true,
          "bypass_header": "X-Bifrost-RequestGuard-Bypass"
        },
        "headers": {
          "enabled": true,
          "prefix": "X-Bifrost-RequestGuard"
        },
        "metrics": {
          "enabled": true
        }
      }
    }
  ]
}
```

`capability_guard.mode` and `budget_guard.mode` accept `off`, `audit`, `warn`,
`block`, or `downgrade`. Unknown catalog capabilities follow
`capability_guard.unknown_policy`: `allow`, `warn`, or `block`. Setting either
auto-downgrade option selects the top compatible recommendation when one meets
the configured constraints. Chat and Responses payloads are supported.

## HTTP response headers

When `headers.enabled` is true, request guard sets headers using the configured
prefix (default `X-Bifrost-RequestGuard`):

| Header | Value |
| --- | --- |
| `<Prefix>-Action` | `allow`, `audit`, `warn`, `block`, or `downgrade` |
| `<Prefix>-Estimated-Cost` | Estimated USD cost, or `unknown` without catalog pricing |
| `<Prefix>-Recommendation` | Top `provider/model` recommendation, or `none` |
| `<Prefix>-Coalesced` | `leader`, `follower`, `bypass`, or `none` |
| `<Prefix>-Original-Model` | Set only when the request was downgraded |

## Prometheus metrics

Collectors register on the supplied `Metrics.Registerer`. Without one,
requestguard creates a private registry and never mutates Prometheus'
`DefaultRegisterer`.

| Metric | Labels / type |
| --- | --- |
| `bifrost_requestguard_requests_blocked_total` | Counter: `reason`, `provider`, `model` |
| `bifrost_requestguard_capability_failures_total` | Counter: `capability`, `provider`, `model` |
| `bifrost_requestguard_downgrades_total` | Counter: `reason`, `from_model`, `to_model` |
| `bifrost_requestguard_coalesced_executions_avoided_total` | Counter: `provider`, `model`, `stream` |
| `bifrost_requestguard_estimated_savings_usd_total` | Counter: `source`, `provider`, `model` |
| `bifrost_requestguard_eval_duration_seconds` | Histogram: `stage` |
