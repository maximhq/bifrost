## 🔒 Security

- **Go Dependency CVE Remediation** — Updated `golang.org/x` dependencies flagged by Docker Scout, clearing 20 advisories (severity up to 10.0): `crypto` v0.49.0 → v0.52.0, `net` v0.52.0 → v0.55.0, `sys` v0.42.0 → v0.45.0, `text` v0.35.0 → v0.37.0, `term` v0.41.0 → v0.43.0 (cli). Verified with `govulncheck` against the live Go vulnerability database: zero vulnerabilities remain in any module (#3900)
- **Hardened Container Image** — Removed the standalone GNU `wget` package from the Alpine runtime image, eliminating CVE-2025-69194 (8.8); the `HEALTHCHECK` now uses the built-in busybox `wget` applet, with no functional change

## ✨ Features

- **GigaChat Config and OpenAPI Exposure** — Exposed GigaChat in transport config and OpenAPI provider schemas
- **GigaChat Key Config Schema** — Added config schema validation for GigaChat auth modes and per-key endpoint overrides

## 🐞 Fixed

- **Ollama Streaming Auth** — Ollama streaming text and chat requests now forward the configured API key as an `Authorization: Bearer` header (#3906)
- **SGL Streaming Auth** — SGL provider now sends the `Authorization` header on streaming requests (#3307) (thanks [@hensapir](https://github.com/hensapir)!)
- **Governance & Logging APIs** — Removed the `from_memory` query parameter; virtual key and config list APIs now return consistent DB-backed results, with VK names batch-fetched in a single query (#3903)