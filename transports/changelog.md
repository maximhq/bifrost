## ✨ Features

- **Optional Dimension Header Propagation to Child Spans** — when an OTEL profile sets `propagate_trace_attributes`, `x-bf-dim-*` headers stored on the trace are merged onto every exported span (root, LLM call, plugin, retry, fallback, MCP tool), not just the root HTTP span. Reserved suffixes `path` and `method` are skipped, and span-level attributes win on conflict (#3770)
