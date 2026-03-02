- fix: filter openrouter list models response based on allowed models on key
- feat: add count tokens support for bedrock
- fix: nil properties in tool function parameters handled
- fix: standardized cached tokens handling for all providers
  <Note>
    **Breaking change.** Cache tokens moved from usage top-level into `ChatPromptTokensDetails` (`prompt_tokens_details`) and `ResponsesResponseInputTokens` (`input_tokens_details`), with standardized keys. If you have persisted data (logs, analytics, or custom storage), migrate: (1) move `usage.cache_read_input_tokens` → `usage.prompt_tokens_details.cached_read_tokens` or `usage.input_tokens_details.cached_read_tokens`; (2) move `usage.cache_creation_input_tokens` → `usage.prompt_tokens_details.cached_write_tokens` or `usage.input_tokens_details.cached_write_tokens`.
  </Note>
- feat: add Azure DefaultAzureCredential support — use managed identity when no API key or Entra ID credentials provided
- feat: add Bedrock STS AssumeRole support (role_arn, external_id, session_name) in key config for cross-account access