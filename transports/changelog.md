## 🐞 Fixed

- **List Models Metadata Passthrough** — OpenAI-compatible `/v1/models` responses now preserve rich upstream model metadata at the top level while keeping Bifrost's normalized IDs, response envelope, and non-destructive pricing enrichment.
- **VK Budget Quota & Reload APIs** — The virtual key quota and reload (rotate) APIs now hydrate governance data (model configs and budgets) before returning, so budget information is accurate instead of missing or stale. Also added proper error handling when fetching model config during hydration.
