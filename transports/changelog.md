## 🐞 Fixed

- **Ollama Streaming Auth** — Ollama streaming text and chat requests now forward the configured API key as an `Authorization: Bearer` header (#3906)
- **SGL Streaming Auth** — SGL provider now sends the `Authorization` header on streaming requests (#3307) (thanks [@hensapir](https://github.com/hensapir)!)
- **Governance & Logging APIs** — Removed the `from_memory` query parameter; virtual key and config list APIs now return consistent DB-backed results, with VK names batch-fetched in a single query (#3903)
