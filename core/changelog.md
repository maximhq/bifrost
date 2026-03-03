- feat: added response header forwarding for providers
- feat: added streaming compression support for providers
- feat: added bedrock invoke and count tokens support
- feat: added gzip compression for azure speech streams
- fix: standardized cached tokens handling across all providers
  <Note>
    **Breaking change.** Cache tokens moved from usage top-level into `ChatPromptTokensDetails` (`prompt_tokens_details`) and `ResponsesResponseInputTokens` (`input_tokens_details`), with standardized keys. If you have persisted data (logs, analytics, or custom storage), migrate: (1) move `usage.cache_read_input_tokens` → `usage.prompt_tokens_details.cached_read_tokens` or `usage.input_tokens_details.cached_read_tokens`; (2) move `usage.cache_creation_input_tokens` → `usage.prompt_tokens_details.cached_write_tokens` or `usage.input_tokens_details.cached_write_tokens`.
  </Note>
<<<<<<< HEAD
- feat: add Azure DefaultAzureCredential support — use managed identity when no API key or Entra ID credentials provided
- feat: add Bedrock STS AssumeRole support (role_arn, external_id, session_name) in key config for cross-account access
=======
- fix: handle nil properties in tool function parameters
- fix: fixed logprobs JSON tag in BifrostResponseChoice (thanks [@robechun](https://github.com/robechun)!)
- fix: added proper status check for openrouter keys
>>>>>>> ae9c673e9 (v1.4.10 version cut pr)
