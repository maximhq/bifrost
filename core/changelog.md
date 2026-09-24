[fix]: apply guardrails to Bedrock Anthropic InvokeModel requests [@axelray-dev](https://github.com/axelray-dev)
- [fix]: Bedrock InvokeModel keeps cache_control when any message's content is a plain string. BedrockMessage.Content is typed as content blocks, so one bare string anywhere in messages[] failed the standard unmarshal and diverted the whole request into the AI21 string fallback, which rebuilt the messages without calling applyMessageContentCacheControl. Every cache_control in the request was dropped rather than only the one on the string message, so prompt caching went off silently with just the system cachePoint surviving. The fallback now makes the same translation the standard path does (#7336) [@basil-k-aji-dev](https://github.com/basil-k-aji-dev)
- [fix]: web search action sources round-trip their name and no longer fabricate an empty url. OpenAI Responses web search can return specialized API sources ({"type":"api","name":"oai-weather"}) that carry a name and no URL; the typed source schema only modeled type and a required url, so decode dropped name and re-encode emitted "url":"", and the OpenAI request-side source sanitization rebuilt sources without name. url is now omitempty and name survives both the schema round-trip and the sanitization path (#7356) [@g-yixuan](https://github.com/g-yixuan)
- [fix]: preserve opted-in Anthropic extra params through Responses conversion [@wangrat](https://github.com/wangrat)
- feat: pinned provider keys on routing fallbacks via key_id on each fallback (#7470)
- feat: forward OpenAI async tools, output_schema and tunnel_id on Responses, stripping async for unsupported models with a SupportsAsyncTools datasheet override (#7242)
- feat: prompt-cache breakpoints for the GPT-6 family with a SupportsPromptCacheBreakpoint datasheet override (#7240)
- feat: reasoning effort none for gpt-6-sol and gpt-6-luna (#7492)
- fix: strip unsupported temperature, top_logprobs and logprobs alongside top_p for OpenAI reasoning models, and count an omitted effort as none only for models that default to no reasoning (#7239)
- fix: Responses wire shapes for structured MCP tool errors, object-form conversation, array-form MCP allowed_tools, approval_request_id and in/nin file search filters (#7241)
  <Warning>Go SDK callers: `ResponsesMCPApprovalResponse.ApprovalResponseID` is now `ApprovalRequestID`, the message type is now `mcp_approval_response`, and `ResponsesToolMessage.Error` and `ResponsesParameters.Conversation` are now union types, where they used to be `*string`.</Warning>
- fix: keep cache_control breakpoints for Anthropic models routed through OpenRouter (#7521)
- fix: move routing key pins with their provider when session affinity reorders the chain (#7468)
- fix: match session affinity routes on provider and model together, and drop bindings the request followed into a failure (#7473)
- fix: merge multiple system and developer messages for Databricks-hosted Gemini models (#7461)
- fix: treat Bedrock's cross-account or cross-model encrypted reasoning rejection as recoverable and retry without it
- fix: decision emulation uses tool_choice auto for gpt-oss on Bedrock Mantle, and recovers leaked parameter tags with surrounding whitespace
- fix: report Gemini transcription usage even when the transcript is empty
