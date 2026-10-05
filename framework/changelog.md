- fix: oauth2_server_config.issuer_url is required when mcp_server_auth_mode is oauth or both; the issuer is never derived from the request Host header (#7863)
  <Warning>
  Set oauth2_server_config.issuer_url before upgrading any deployment with MCP OAuth discovery enabled, or config load fails.
  </Warning>
- fix: track whether enforce_auth_on_inference was set explicitly so creating the first admin can default inference auth on (#7864)
- fix: redact secret-bearing alias and Bedrock endpoint values in client config responses (#7858)
- chore: upgraded core to v1.11.2
