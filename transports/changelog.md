## 🐞 Fixed

- **VK Budget Quota & Reload APIs** — The virtual key quota and reload (rotate) APIs now hydrate governance data (model configs and budgets) before returning, so budget information is accurate instead of missing or stale. Also added proper error handling when fetching model config during hydration.