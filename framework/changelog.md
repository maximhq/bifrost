- feat: `service_tier` column on provider keys and key aliases (migration `add_key_service_tier_column`)
- feat: storage and token minting for the MCP `client_credentials` auth type (migration `add_mcp_client_client_credentials_json_column`)
- feat: storage for the MCP `aws_sigv4` auth type (migration `add_mcp_client_aws_sigv4_json_column`)
- feat: `oauth2_server_config.browser_base_url` and `additional_issuer_urls` for MCP OAuth on several hostnames
- feat: per-user MCP OAuth flows record the page the sign-in started from (migration `add_mcp_oauth_flow_origin_column`)
- feat: relay async jobs settle on the shared job accounting engine, with a relay settler, a Relay Logs recorder and a per-job poll schedule (migration `widen_batch_jobs_provider_status_and_endpoint`)
- feat: Relay Logs store `job_debug` for each job row, with an unpriced filter and search on job id (migration `relay_logs_add_job_debug_widen_job_status`)
  <Warning>
  `widen_batch_jobs_provider_status_and_endpoint` and `relay_logs_add_job_debug_widen_job_status` cannot be rolled back: narrowing the columns or dropping `job_debug` would lose stored relay job details.
  </Warning>
- feat: notifications can carry a subject and be resolved once it is handled (migrations `add_notification_subject_resolution_columns` and `add_notification_subject_index`)
- fix: replicas sharing a config store take a per-token lock before redeeming an MCP refresh token
- fix: Responses retrieve, retrieve-stream and cancel requests are priced at zero, so a Gemini interaction run is billed once
