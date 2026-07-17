## 🐞 Fixed

- **SSRF Hardening on Plugin Downloads (breaking)**: Custom plugin (`.so`) downloads now go through an SSRF-hardened HTTP client - the download is refused if the URL resolves to a loopback, private (RFC 1918), CGNAT, link-local, or otherwise non-public address, including on every server restart since already-configured plugins are re-verified at boot. Deployments hosting a plugin on an internal/private-network URL must add that host to the new `server.plugin_download_private_allowlist` config (see the `transports` changelog) or switch to a local file path.

## ✨ Features (unreleased)

- feat: add opt-in client.stream_keepalive_interval for SSE keepalives (0 = disabled) [@jeremym-tanium](https://github.com/jeremym-tanium)
