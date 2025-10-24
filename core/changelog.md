<!-- The pattern we follow here is to keep the changelog for the latest version -->
<!-- Old changelogs are automatically attached to the GitHub releases -->

- bug: fixed embedding request not being handled in `GetExtraFields()` method of `BifrostResponse`
- fix: added latency calculation for vertex native requests
- feat: added cached tokens and reasoning tokens to the usage metadata for chat completions
- feat: added global region support for vertex API
- fix: added filter for extra fields in chat completions request for Mistral provider
- fix: fixed ResponsesComputerToolCallPendingSafetyCheck code field