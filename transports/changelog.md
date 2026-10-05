## ⚠️ Breaking Changes

- **OSS Management API Setup Lock** - While dashboard auth is not active (no admin account, or auth disabled), every non-public `/api` call on OSS Bifrost now needs the setup token, sent as the `X-Bifrost-Setup-Token` header or as the `bifrost_setup_session` cookie the dashboard gets from `POST /api/session/setup`. A missing token returns `401`. A wrong token, or no token set on the server, returns `403`. `/health`, `/api/version`, the session login routes, `/.well-known/*` and whitelisted routes stay public. The lock lifts as soon as an enabled admin is saved (#8010)
  <Warning>
  Migration: set `setup_token` in config.json (or `BIFROST_SETUP_TOKEN`) and restart. Then either enable dashboard auth, or send `X-Bifrost-Setup-Token` from scripts and API clients that call `/api` with auth off. Enterprise is not affected by the lock.
  </Warning>
- **Inference Auth On by Default** - `enforce_auth_on_inference` now defaults to `true` for fresh deployments when config.json leaves it out, file-only deployments included. Creating the first enabled admin also turns inference auth on unless the request sets it explicitly. Inference without a credential then returns `401`. A stored database value always wins, and an explicit `false` is always kept. This also applies to Bifrost Enterprise (#8010, #7864)
  <Warning>
  Migration: to keep unauthenticated inference, set `"enforce_auth_on_inference": false` in config.json, or send it explicitly when creating the first admin.
  </Warning>
- **OAuth Discovery Requires issuer_url** - When `mcp_server_auth_mode` is `oauth` or `both`, `oauth2_server_config.issuer_url` must be set to a non-empty value. Config load fails and `PUT /api/config` rejects the save otherwise. The issuer is never derived from the request `Host` header any more, and discovery responses carry `Cache-Control: no-store` (#7863)
  <Warning>
  Migration: set `oauth2_server_config.issuer_url` (env syntax `env.MY_VAR` works) before upgrading any deployment that has MCP OAuth discovery enabled, or the server will not start.
  </Warning>
- **Provider Dial Target and Proxy Changes Need Real Auth** - With dashboard auth off, these changes now return `403` unless the request carries a genuine admin credential (a session, or the OSS setup token): provider `base_url`, `allow_private_network`, key endpoint URLs (Ollama, SGL, vLLM, Azure, etc.), provider proxy, custom CA certs, skipped TLS verification, absolute `request_path_overrides`, and enabling the global proxy with a URL (#7865, #7867)
- **Private Framework Config URLs Rejected** - `pricing_url`, `model_parameters_url` and `mcp_library_url` are now checked at save time and at dial time against private, link-local and CGNAT addresses, and redirects are not followed. Air-gapped setups should use `file://` URLs (#7861)
- **Semantic Cache Scoped per Virtual Key** - Cache buckets are now partitioned by virtual key, so a shared `cache_key` or `default_cache_key` no longer serves one virtual key's cached response to another. Entries written before the upgrade under a virtual key are not reused, so expect a cold cache. Unscoped cache keys that start with `vk:` are moved to `raw:vk:`. A per-request threshold override can only raise the configured threshold, and is capped at 1.0 (#7862)
- **zstd Decoder Window Cap** - zstd-compressed bodies whose frame header asks for a window above 100 MiB are rejected before any allocation (#7859)

## ✨ Features

- **Dashboard Setup Session** - The login page has a new setup screen that trades the setup token for a 12-hour `HttpOnly; SameSite=Strict` cookie. The cookie is signed with a key derived from the token, so the browser never stores the token itself. A sidebar card flags missing dashboard auth until an admin exists. `GET /api/session/is-auth-enabled` now reports `setup_required` and `setup_token_configured`, and `PUT /api/config` accepts a setup-token request as first-admin proof (#8010)

## 🐞 Fixed

- **Claude Tool-Call Argument Streaming on Bedrock and Vertex** - Claude tool arguments now stream incrementally, so a long Write call no longer arrives as one burst after minutes of silence and Claude Code no longer aborts with "Stream idle timeout". `eager_input_streaming` defaults on for custom tools that leave it unset (every Claude model on Vertex; Sonnet 4.6, Sonnet 5+, Opus 4.7+ and Fable on Bedrock). An explicit `false` is kept. Converse carries `fine-grained-tool-streaming-2025-05-14` in `additionalModelRequestFields` (#8009)
- **Tool-Result Cache Markers for gpt-5.6+** - Anthropic `cache_control` markers on tool results (as Claude Code sends them) now become `prompt_cache_breakpoint` on both Chat Completions and Responses for gpt-5.6+ on OpenAI, Azure, Bedrock and Bedrock Mantle, so caching keeps advancing past the first tool turn. Responses also marks `input_image` and `input_file` parts. At most four breakpoints are kept, the latest ones (#8012)
- **Handler Panic Recovery** - A panic in a request handler now returns a `500` and logs a server-side stack trace instead of crashing the process (#7866)
- **Secret Redaction in Config Responses** - Env and vault-resolved values in provider alias configs (region, project ID, Azure endpoint, Vertex project, Bedrock inference profile ARN) and in Bedrock endpoint overrides are now masked in management API responses (#7858)
- **Admin Password Autofill** - The Security page's admin fields carry `autoComplete="username"` and `"new-password"`, so password managers no longer fill a saved host password into the new admin's password field (#8010)

## 🗄️ Database Migrations

- No new database migrations in this release.
