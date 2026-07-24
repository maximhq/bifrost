- feat: added support for Anthropic's `fallbacks: "default"` preset via the new `AnthropicFallbacks` wrapper type, preserving it through request round-trips and injecting the `server-side-fallback-2026-07-01` beta header for default routing
    <Warning>
    `AnthropicMessageRequest.Fallbacks` changed from `[]AnthropicFallbackEntry` to `*AnthropicFallbacks`. Code constructing this struct directly must wrap entries as `&AnthropicFallbacks{Entries: ...}`.
    </Warning>
- feat: added the `mid-conversation-tool-changes-2026-07-01` beta header and `MidConvToolChanges` feature flag for Anthropic and Bedrock Mantle
- fix: added Opus 5 support to the Anthropic provider via `IsOpus5Plus`, inheriting the Opus 4.8 request surface for unsupported sampling params, native effort, fast mode, and mid-conversation system messages
