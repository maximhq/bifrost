- feat: A2A operation metrics export (#8575)
- chore: upgraded core to v1.13.0 and framework to v1.10.0

## 🐞 Fixed

- **Active Requests Gauge** - `bifrost_active_requests` no longer drifts upward when clients cancel streaming requests before the stream starts (for example HTTP 499 while waiting for upstream headers), or when a plugin rejects a streaming request. Each request now holds at most one slot and releases it exactly once, so dashboards and alerts built on the inflated value will see lower, correct numbers
