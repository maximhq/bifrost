## ✨ Features

- **Optional Dimension Header Propagation to Child Spans** — when an OTEL profile sets `propagate_trace_attributes`, `x-bf-dim-*` headers stored on the trace are merged onto every exported span (root, LLM call, plugin, retry, fallback, MCP tool), not just the root HTTP span. Reserved suffixes `path` and `method` are skipped, and span-level attributes win on conflict (#3770)
- **Virtual Key Rotation Cooldown** - New `client.vk_rotation_cooldown` setting (duration string, e.g. "5m"): after a rotation the previous key value keeps authenticating until the grace window expires. config.json VK sync now treats a changed value as an explicit rotation (with console warning) and recognizes the previously rotated-out value as "no change".
