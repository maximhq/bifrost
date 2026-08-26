- feat: `bifrost_overhead_latency_microseconds` histogram measured across the HTTP transport hooks as total request time minus time blocked on upstream provider sockets, with a microsecond-scale bucket set (#6345)
- chore: `x-bf-prom-*` request headers are no longer consumed as Prometheus label dimensions in `collectPrometheusKeyValues` and `applyCustomLabels` (the prefix is still stripped from forwarded requests), and legacy `gen_ai.*` attribute emission is removed (#6403)
  <Warning>
  Deployments that relied on `x-bf-prom-*` request headers to add Prometheus label dimensions lose those labels; use the supported custom label configuration instead.
  </Warning>
- feat: add a no-op `HTTPTransportPreAuthHook` for the new pre-authentication transport phase (#6375)
- chore: upgraded core to v1.8.0 and framework to v1.6.0
