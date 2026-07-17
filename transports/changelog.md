## ✨ Features

- **Plugin Download Private Allowlist**: Added `server.plugin_download_private_allowlist` (deploy-time only, not settable via the plugin admin API) accepting hostnames, IPs, or CIDR ranges that custom plugin (`.so`) downloads are permitted to reach even when they resolve to a private/loopback/link-local/CGNAT address, for operators hosting plugins on a trusted internal artifact server.

## 🐞 Fixed

- **Unauthenticated Custom Plugin Path Creation (breaking)**: `POST /api/plugins` and `PUT /api/plugins/{name}` now reject a request that sets a custom `path` if the caller reached the endpoint only because dashboard auth is disabled or unconfigured - a custom plugin path is native code that gets `dlopen()`'d in-process, so setting one now requires genuine admin authentication (any supported method, Basic auth or a dashboard session) rather than being let through by disabled/unconfigured auth.

## ✨ Features (unreleased)

- **SSE Stream Keepalive** - Emit SSE keepalive comments at a configurable interval while a provider stream is idle, preventing idle-timeout drops on long, quiet streams (thanks [@jeremym-tanium](https://github.com/jeremym-tanium)!)
