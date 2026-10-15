## 🐞 Fixed

- **Active Requests Gauge** - `bifrost_active_requests` no longer drifts upward when clients cancel streaming requests before the stream starts (for example HTTP 499 while waiting for upstream headers), or when a plugin rejects a streaming request. Each request now holds at most one slot and releases it exactly once, so dashboards and alerts built on the inflated value will see lower, correct numbers
