## ✨ Features

- **OCR Endpoint** — Add end-to-end support for /v1/ocr endpoint with Mistral OCR (thanks [@Vaibhav701161](https://github.com/Vaibhav701161)!)
- **Azure Passthrough** — Add Azure passthrough support for native Azure API calls
- **OpenRouter Embeddings** — Add embeddings support for OpenRouter provider (thanks [@dennypradipta](https://github.com/dennypradipta)!)
- **Redis TLS & Cluster** — Add TLS and cluster mode support for Redis connections and fix valkey-search query syntax
- **272k Token Pricing** — Add 272k token tier and priority tier support in pricing
- **OAuth MCP Hints** — Add next-step hints to OAuth MCP client creation response

## 🐞 Fixed

- **Pricing Sync Interval** — Correctly apply pricing_sync_interval and support env variables in pricing_url (thanks [@Vaibhav701161](https://github.com/Vaibhav701161)!)
- **OAuth Transient Failures** — Don't mark OAuth config expired on transient network failures
