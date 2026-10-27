## Features

- **Azure Blob Storage** - Added log payload and skills-file storage with default Azure identity or connection-string authentication, JSON/gzip and Parquet support, and config-file/Helm configuration. Structured environment and vault references can omit `value`.

## 🔒 Security

- **Go 1.27.3** - Security release for [CVE-2026-111943](https://groups.google.com/g/golang-announce/c/66pojueaQek) in `crypto/x509`: certificate chains through a small set of intermediates from a specific Certificate Authority are no longer accepted by `Verify`, which covers every TLS connection Bifrost makes to providers, MCP servers and other upstreams. Bifrost now builds with Go 1.27.3, and the Docker images (`Dockerfile`, `Dockerfile.local`, `Dockerfile.redhat`) move to `golang:1.27.3-alpine3.24`. Custom Go plugins must be rebuilt with Go 1.27.3 to load.

## 🐞 Fixed

- **Provider Edits Survive config.json Changes** - In the default `split` mode, changing a provider in config.json or Helm (for example only `network_config.max_retries`) no longer resets the rest of that provider on restart. Fields the file sets overwrite the stored value; fields it leaves out keep the stored value, including UI edits, instead of falling back to defaults. `network_config` and `concurrency_and_buffer_size` merge per sub-field, changed keys merge the same way, and keys added in the UI stay. A provider key's `weight` is now optional: a new key gets 1 and an existing key keeps its stored weight. `source_of_truth: "config.json"` is unchanged

## 🔧 Maintenance

- **Dependency Upgrades** - Bumped core to v1.13.1 and framework to v1.10.1 across all modules

## 🗄️ Database Migrations

- No new database migrations in this release.
- **MiniMax Provider** - New `minimax` provider for chat completions, Responses, image generation, async video generation, text-to-speech and speech-to-text, with `thinking` passthrough and the `base_resp` error envelope mapped to HTTP status codes
- **BytePlus Provider** - New `byteplus` provider for BytePlus ModelArk: Seed chat completions with `reasoning_effort` mapped onto Ark's `thinking` switch, the native Responses API, embeddings through the multimodal endpoint (one call per input entry), Seedream image generation and editing, and async Seedance video generation with list support
