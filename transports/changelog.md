## ✨ Features

- **Vector Store Config UI** — Add vector store config UI on Settings > Caching page with all four providers [@dominictayloruk](https://github.com/dominictayloruk)
- **Vector Store Config API** — Add GET/PUT /api/cache/config endpoints for vector store configuration [@dominictayloruk](https://github.com/dominictayloruk)
- **Vector Store Server Validation** — Add server-side validation for Qdrant and Pinecone vector store configs [@dominictayloruk](https://github.com/dominictayloruk)
- **Vector Store Config Redaction** — Add redaction for Redis, Qdrant, and Pinecone vector store configs [@dominictayloruk](https://github.com/dominictayloruk)

## 🔧 Refactored

- **Vector Store Validation** — Move validation and secret merge logic to vectorstore.Config methods [@dominictayloruk](https://github.com/dominictayloruk)
- **Caching UI TLS Checks** — Extract isEnvVarTrue utility and deduplicate TLS checks in caching UI [@dominictayloruk](https://github.com/dominictayloruk)

## 🐞 Fixed

- **Vector Store Backend Config** — Preserve backend-only config fields when saving vector store settings from UI [@dominictayloruk](https://github.com/dominictayloruk)
- **Vector Store Env Var Switches** — Use isEnvVarTrue for cluster_mode and insecure_skip_verify switches in Redis vector store UI [@dominictayloruk](https://github.com/dominictayloruk)
- **Vector Store Secret Merge** — Prevent env-var rename from being silently overwritten during secret merge [@dominictayloruk](https://github.com/dominictayloruk)
- **Vector Store Redaction Types** — Use value types for redacted vector store config to prevent type assertion failures [@dominictayloruk](https://github.com/dominictayloruk)
- **Vector Store Redacted Secrets** — Preserve redacted secrets when updating vector store config via UI [@dominictayloruk](https://github.com/dominictayloruk)
- **Vector Store TLS Env Vars** — Account for env-var-driven TLS when building Redis vector store config payload in UI [@dominictayloruk](https://github.com/dominictayloruk)
- **Vector Store Startup** — Gracefully handle DB-persisted vector store connection failure at startup [@dominictayloruk](https://github.com/dominictayloruk)
- **Plugins Form Independence** — Render PluginsForm independently of vector store config API errors [@dominictayloruk](https://github.com/dominictayloruk)
- **Vector Store DB Fallback** — Prefer DB-stored vector store config when file config is absent [@dominictayloruk](https://github.com/dominictayloruk)
- **Vector Store Fatal Error** — Replace logger.Fatal with returned error on vector store connection failure [@dominictayloruk](https://github.com/dominictayloruk)
- **Vector Store CreatedAt Preservation** — Preserve `CreatedAt` timestamp when updating vector store config rather than zeroing it [@dominictayloruk](https://github.com/dominictayloruk)
