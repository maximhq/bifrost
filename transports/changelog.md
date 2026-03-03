- fix: route parallel tool call argument deltas by id/index to prevent argument merging during streaming
- feat: add count tokens support for bedrock
- feat: add async rerank support
- fix: count tokens route fixed to match openai schema
  <Warning>
    **Breaking change.** The count tokens route has moved from `/v1/count_tokens` to `/v1/responses/input_tokens`, and the request body field has been renamed from incorrect `messages` to `input`. Please update your clients accordingly.
  </Warning>
- fix: added missing routing logic for bedrock integration
- fix: nil properties in tool function parameters handled
- fix: standardized cached tokens handling for all providers
  <Note>
    **Breaking change.** Cache tokens moved from usage top-level into `prompt_tokens_details` (Chat) and `input_tokens_details` (Responses), with standardized keys. If you have persisted data (logs, analytics, or custom storage), migrate: (1) move `usage.cache_read_input_tokens` → `usage.prompt_tokens_details.cached_read_tokens` or `usage.input_tokens_details.cached_read_tokens`; (2) move `usage.cache_creation_input_tokens` → `usage.prompt_tokens_details.cached_write_tokens` or `usage.input_tokens_details.cached_write_tokens`.
  </Note>
- fix: nil handling for customer_id in update team