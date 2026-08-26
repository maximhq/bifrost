- feat: `bifrost_overhead_latency_microseconds` histogram derived from the root span's overhead attribute, with a microsecond-scale bucket set (#6345)
- chore: remove the legacy `gen_ai.*`-namespaced Bifrost-internal attributes, `gen_ai.usage.prompt_tokens`/`completion_tokens` and the nanosecond `time_to_first_token` attribute; `buildSpanAttrs` and `entitySetFromAttrs` read the canonical `bifrost.*` keys and `time_to_first_chunk` directly (#6403)
  <Warning>
  Dashboards and alerts that read the legacy `gen_ai.*` Bifrost-internal attributes or the nanosecond TTFT attribute must migrate to the `bifrost.*` keys and `time_to_first_chunk` (seconds).
  </Warning>
- feat: add a no-op `HTTPTransportPreAuthHook` for the new pre-authentication transport phase (#6375)
- chore: upgraded core to v1.8.0 and framework to v1.6.0
