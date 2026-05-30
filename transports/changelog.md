## 🔒 Security

- **Go Dependency CVE Remediation** — Updated `golang.org/x` dependencies flagged by Docker Scout across all modules, clearing 20 advisories (severity up to 10.0). Verified with `govulncheck` against the live Go vulnerability database: zero vulnerabilities remain in any module (#3900).
  - `golang.org/x/crypto` v0.49.0 → v0.52.0, `golang.org/x/net` v0.52.0 → v0.55.0, `golang.org/x/sys` v0.42.0 → v0.45.0, `golang.org/x/text` v0.35.0 → v0.37.0, `golang.org/x/term` v0.41.0 → v0.43.0 (cli)
  - **Critical:** CVE-2026-46595 (10.0), CVE-2026-39821 (9.6), CVE-2026-39830, CVE-2026-39831, CVE-2026-39832, CVE-2026-39833, CVE-2026-39834, CVE-2026-42508 (9.1)
  - **High:** CVE-2026-39829, CVE-2026-33814 (7.5)
  - **Medium/Low:** CVE-2026-39827, CVE-2026-25680 (6.5), CVE-2026-39828 (6.3), CVE-2026-42506, CVE-2026-42502, CVE-2026-27136, CVE-2026-25681 (6.1), CVE-2026-46598, CVE-2026-39835 (5.3), CVE-2026-39824 (3.3)
- **Removed standalone `wget` from container image** — The Alpine runtime image no longer installs the GNU `wget` package (carrier of CVE-2025-69194, 8.8); the `HEALTHCHECK` now uses the built-in busybox `wget` applet instead, eliminating the vulnerability with no functional change

## 🐞 Fixed

- **Ollama Streaming Auth** — Ollama streaming text and chat requests now forward the configured API key as an `Authorization: Bearer` header (#3906)
- **SGL Streaming Auth** — SGL provider now sends the `Authorization` header on streaming requests (#3307) (thanks [@hensapir](https://github.com/hensapir)!)
- **Governance & Logging APIs** — Removed the `from_memory` query parameter; virtual key and config list APIs now return consistent DB-backed results, with VK names batch-fetched in a single query (#3903)
