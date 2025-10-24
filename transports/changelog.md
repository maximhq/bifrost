<!-- The pattern we follow here is to keep the changelog for the latest version -->
<!-- Old changelogs are automatically attached to the GitHub releases -->

- chore: version update core to 1.2.13 and framework to 1.1.15
- feat: added headers support for OTel configuration. Value prefixed with env will be fetched from environment variables (env.<ENV_VAR_NAME>)
- feat: emission of OTel resource spans is completely async - this brings down inference overhead to < 1µsecond
- fix: added latency calculation for vertex native requests
- feat: added cached tokens and reasoning tokens to the usage in ui
- fix: cost calculation for vertex requests
- feat: added global region support for vertex API
- fix: added filter for extra fields in chat completions request for Mistral provider
- fix: added wildcard validation for allowed origins in UI security settings
- fix: fixed code field in pending_safety_checks for Responses API