- feat: add `cost_per_request` flat-fee pricing field across DB, cost engine, overrides and docs (#6079)
- feat(modelcatalog): resolve pricing overrides for catalog rows (#6055)
- feat: add `use_idp_credentials` to token-exchange config (#6068)
- feat: bedrock vpc endpoints support (#6064)
- feat: add additional metadata in S3 log export (#6070)
- feat: make log recalculation task cancellable backend (#5801)
- feat: add `roots_only` filter to collapse fallback chains with child aggregates (#5737)
- feat: support matview_refresh_interval "off" to disable logstore matview maintenance (thanks [@jeremym-tanium](https://github.com/jeremym-tanium)!) (#5693)
- feat: persist and resync MCP tool discoveries uniformly across all client types via a hash-gated core callback
- feat: add VK and Users filters to the OAuth Grants and MCP Auth Sessions sidebars
- feat: generalize TokenRefreshWorker's auth-mode scope and allow gating OAuthTokenRefreshWorker sweeps
- feat(mcp-guardrails): add MCP log redaction changes (#5744)
- feat: add plugin logs to mcp logs (#5746)
- fix: combine `offline_access` with `<audience>/.default` for Entra OBO instead of replacing it (#6078)
- fix: don't treat a CAS loss to a still-active concurrent refresh as a dead credential
- fix: propagate ctx through headerCredentialCache.Fill and userTokenCache.Fill so a canceled request unblocks instead of waiting on an unrelated leader
- fix: add per-entry version to the LRU cache so a rejected stale Get cannot evict a concurrently-updated value
- fix: make the OAuth flow claim atomic against concurrent reauth, close a leaked sqlDB in flows-table perf setup
- fix: route pending token_exchange clients through the verify-exchange confirm dialog
- chore: dependabot dependency updates (#6040)

<Warning>
This release adds 18 database migrations. `merge_oauth_token_tables`, `drop_oauth_config_pkce_columns`, `drop_oauth_config_token_id_column` and `mcp_tool_logs_add_redaction_mapping_column` are non-reversible. Back up your database before upgrading.
</Warning>

<Warning>
**High-throughput deployments: run the logstore migrations during a low-activity window.**

All eight logstore migrations in this release alter `logs` or `mcp_tool_logs`, the two highest-insert tables in Bifrost, and several also build indexes on them. On a busy instance those index builds block concurrent log inserts until they complete. Schedule the upgrade for a low-traffic period, or expect elevated log-write latency while the migrations run.
</Warning>
