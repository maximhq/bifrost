- [feat]: `exa` is a configurable provider for `POST /v1/tools/search` and `POST /v1/tools/fetch` [@Radiergummi](https://github.com/Radiergummi)
- [feat]: Search and Fetch fallbacks may name a provider alone; an unknown one is refused [@Radiergummi](https://github.com/Radiergummi)
- [feat]: `POST /v1/tools/search` takes the provider-neutral Search fields [@Radiergummi](https://github.com/Radiergummi)
- [feat]: DeepSeek web tools at `POST /v1/tools/search` and `POST /v1/tools/fetch`, routed to DeepSeek when a body carries no model [@jimaldon](https://github.com/jimaldon)

## 🔒 Security

- **Go 1.27.3** - Security release for [CVE-2026-111943](https://groups.google.com/g/golang-announce/c/66pojueaQek) in `crypto/x509`: certificate chains through a small set of intermediates from a specific Certificate Authority are no longer accepted by `Verify`, which covers every TLS connection Bifrost makes to providers, MCP servers and other upstreams. Bifrost now builds with Go 1.27.3, and the Docker images (`Dockerfile`, `Dockerfile.local`, `Dockerfile.redhat`) move to `golang:1.27.3-alpine3.24`. Custom Go plugins must be rebuilt with Go 1.27.3 to load.

## 🔧 Maintenance

- **Dependency Upgrades** - Bumped core to v1.13.1 and framework to v1.10.1 across all modules

## 🗄️ Database Migrations

- No new database migrations in this release.
