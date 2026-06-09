- [fix]: mistral custom providers can override request paths for all endpoints [@georg-wolflein](https://github.com/georg-wolflein)
- [fix]: allow mistral as a base_provider_type for custom providers [@georg-wolflein](https://github.com/georg-wolflein)
[fix]: warn callers not to truncate the `#t=` temp-token fragment on MCP inline-auth links [@MarcusPeng](https://github.com/MarcusPeng)
[fix]: zero pooled ChannelMessage references on release to avoid pinning request bodies [@citrocat](https://github.com/citrocat)
- fix: preserve Gemini file upload MIME types for GenAI file URI completions
- fix: Gemini video reference fields map to instances [@vojthor](https://github.com/vojthor)
- fix: accept object-valued tool-call arguments (e.g. tool_search_call) on the Responses API streaming path
- fix: recover from idle-timeout timer-goroutine panic that could crash the process
- fix: deterministic MCP tool ordering for prompt cache stability (closes #2347)
- fix: pass through `gs://` image URLs on Vertex Gemini (closes #4402)
- fix: signal Bedrock max_output_tokens truncation on Responses API [@jeremym-tanium](https://github.com/jeremym-tanium)
- fix: round-trip Anthropic `redacted_thinking` blocks on the responses surface so multi-turn tool use with redacted reasoning can be replayed (closes #5093) [@fus3r](https://github.com/fus3r)
[fix]: omit role from OpenAI Responses non-message items [@nettee](https://github.com/nettee)
- fix: reset `HasEmittedWebSearch` when recycling pooled Gemini responses stream state so grounded streaming requests keep emitting `web_search_call` items (closes #5113) [@fus3r](https://github.com/fus3r)
