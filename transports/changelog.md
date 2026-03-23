## 🐞 Fixed

- **Responses API Tool Types** — Normalized versioned/provider-specific tool type strings (e.g. `web_search_20250305`) to their canonical types for correct routing
- **Postgres Indexing Deadlocks** — Merged concurrent indexing goroutines into a single sequential goroutine to prevent Postgres deadlocks
- **Provider Histogram Index** — Deferred provider histogram index creation to background goroutine to avoid blocking pod startup
