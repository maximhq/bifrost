## ✨ Features

- **Dimension Header Propagation to Child Spans** — `x-bf-dim-*` HTTP headers are now stored on the trace at ingress and merged onto every exported span (root, LLM call, plugin pre/posthook, retry, fallback, MCP tool) by observability plugins, instead of attaching only to the root HTTP span. Reserved suffixes `path` and `method` are still skipped, and span-level attributes win on conflict (#3770)

## 🐞 Fixed

- **VK Budget Quota & Reload APIs** — The virtual key quota and reload (rotate) APIs now hydrate governance data (model configs and budgets) before returning, so budget information is accurate instead of missing or stale. Also added proper error handling when fetching model config during hydration.
