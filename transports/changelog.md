## ✨ Features

- **Codex with ChatGPT sign-in** - `/chatgpt_passthrough` now forwards `GET /backend-api/codex/models`, so Codex signed in with ChatGPT can run through Bifrost with the virtual key in `x-bf-vk`, following OpenAI's gateway guide. The model list skips the virtual key's model allowlist because it runs no model; each responses turn is still checked. Only a token ChatGPT issued is forwarded; any other bearer on that route keeps the stored OpenAI key, and `/openai_passthrough` is unchanged

## 🔒 Security

- **Go 1.27.3** - Security release for [CVE-2026-111943](https://groups.google.com/g/golang-announce/c/66pojueaQek) in `crypto/x509`: certificate chains through a small set of intermediates from a specific Certificate Authority are no longer accepted by `Verify`, which covers every TLS connection Bifrost makes to providers, MCP servers and other upstreams. Bifrost now builds with Go 1.27.3, and the Docker images (`Dockerfile`, `Dockerfile.local`, `Dockerfile.redhat`) move to `golang:1.27.3-alpine3.24`. Custom Go plugins must be rebuilt with Go 1.27.3 to load.

## 🔧 Maintenance

- **Dependency Upgrades** - Bumped core to v1.13.1 and framework to v1.10.1 across all modules

## 🗄️ Database Migrations

- No new database migrations in this release.
