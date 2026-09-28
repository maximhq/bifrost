## ✨ Features

- **Pinned Keys on Routing Fallbacks** - Each routing-rule fallback can pin a provider key via `key_id`, or `provider_key_name` in config.json. The UI rule editor lets you pick or clear a key per fallback. Unpinned fallbacks keep the legacy `provider/model` string, so existing rules keep their config hash (#7470, #7379, #7380, #7381)
- **OpenAI Async Tool Execution** - The `async` flag on Responses tools and tool calls, `output_schema` on function tools and `tunnel_id` on MCP tools are now forwarded to OpenAI. `async` is stripped for models without support, and the datasheet `supports_async_tools` field can override this (#7242)
- **GPT-6 Prompt Cache Breakpoints** - Prompt-cache breakpoints now cover the GPT-6 family on OpenAI, Azure, Bedrock and Bedrock Mantle. The datasheet `supports_prompt_cache_breakpoint` field can override this (#7240)
- **GPT-6 Sol and Luna Reasoning Off** - `reasoning.effort: "none"` is forwarded for `gpt-6-sol` and `gpt-6-luna`. Other GPT-6 models keep reasoning on (#7492)

## 🐞 Fixed

- **Anthropic Bedrock Request Metadata** - Cover Anthropic and PydanticAI request metadata passthrough to Bedrock [@wangrat](https://github.com/wangrat)
- **Kimi and DeepSeek with Claude Code** - Tool-schema regex patterns are rewritten (`\0` to `\x00`, lookaround assertions stripped) for Moonshot and DeepSeek models only. kimi-k3 on Bedrock no longer returns an empty stream, and every other model gets byte-identical schemas (#7430)
- **Anthropic Billing Header Leak** - Claude Code's `x-anthropic-billing-header` system block is removed at Messages ingress and restored only for Anthropic-family attempts, including fallbacks and alias targets, so it no longer pollutes GPT or Gemini prompts (#7431)
- **MCP Egress Proxy** - MCP HTTP/SSE connections honor `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` again (broken since core v1.8.5). Link-local and unspecified destinations are refused before the proxy is dialed (#7437)
- **OpenAI `computer` Tool** - The bare `{"type":"computer"}` tool is no longer rewritten to `computer_use_preview`, which fixes computer use on GPT-6 Astra and GPT-5.6 (#7426) (thanks [@abhishekgahlot2](https://github.com/abhishekgahlot2)!)
- **Streaming Memory Leaks** - The request context is cancelled on every stream exit path, not only on write errors, which stops leaked disconnect watchers from pinning request contexts. `StripEmptyThinkingBlocks` rewrites the body once instead of once per block, and Anthropic beta-header gating no longer decodes the full request body (#7406)
- **Bedrock cachePoint Leak** - Bedrock `cachePoint` markers are stripped copy-on-write for non-Bedrock providers and kept for Bedrock fallbacks. The compat plugin no longer mutates the shared request. Nova reasoning signatures are stripped on the wire only (#7182)
- **Bedrock Empty JSON Keys** - Tool results containing an empty-string object key (such as Cursor's `list_directory`) are sent as text, so Converse no longer rejects them (#7396)
- **Bedrock cache_control on String Content** - InvokeModel keeps every `cache_control` when any message's content is a plain string (#7360) (thanks [@basil-k-aji-dev](https://github.com/basil-k-aji-dev)!)
- **Web Search Source Names** - Responses web search API sources keep their `name` and no longer emit an empty `url` (#7358) (thanks [@g-yixuan](https://github.com/g-yixuan)!)
- **Grok 4.7 xhigh Reasoning** - `xhigh` reasoning effort is no longer downgraded to `high` (#7403) (thanks [@nettee](https://github.com/nettee)!)
- **Allow-All Provider Access** - Virtual keys that allow every provider now list models from, and route to, every configured provider. The governance routing log names providers excluded for having no weight (#7375)
- **OpenAI Chat Stream Framing** - Bundled raw finish and usage frames on the OpenAI chat stream passthrough each get their own `data:` prefix (#7440)
- **Responses Deep Copy** - `DeepCopyResponsesMessage` now deep copies cache controls, provider-native parts, computer/MCP/code-interpreter tool fields and annotations, so copies no longer share pointers with the original (#7422)
- **Request Preparation Performance** - Responses requests are decoded once instead of several times, and the compat plugin clones only the fields it writes (#7412, #7097) (thanks [@G-XD](https://github.com/G-XD)!)
- **Virtual Key PUT Round-Trip** - PUT `/api/governance/virtual-keys/{vk_id}` no longer drops provider-config key associations when the request body omits `key_ids`. The GET response exposes `allow_all_keys` and `keys` but not `key_ids`, so the standard GET -> edit -> PUT round-trip silently flipped AllowAllKeys to false and detached every key, and inference through the virtual key then failed with "no keys found for provider". An omitted `key_ids` list now leaves the existing associations untouched; an explicit list (including `[]`) still replaces them (#7347) (thanks [@xiechimon](https://github.com/xiechimon)!)
- **OpenAI Sampling Parameters on Reasoning Models** - `temperature`, `top_logprobs` and `logprobs` are now stripped alongside `top_p` on chat and Responses when the model and effort do not support them. An omitted `reasoning.effort` now counts as `none` only for models that default to no reasoning (#7239)
- **Responses API Wire Shapes** - Structured MCP tool-call errors, object-form `conversation`, array-form MCP `allowed_tools`, `approval_request_id` on MCP approval responses, and `in`/`nin` file search filters now decode and re-encode correctly (#7241)
  <Warning>Go SDK callers: `ResponsesMCPApprovalResponse.ApprovalResponseID` is now `ApprovalRequestID`, the message type is now `mcp_approval_response`, and `ResponsesToolMessage.Error` and `ResponsesParameters.Conversation` are now union types, where they used to be `*string`.</Warning>
- **OpenRouter Anthropic Cache Breakpoints** - Anthropic models routed through OpenRouter now keep their `cache_control` breakpoints, based on the model capability (#7521)
- **Session Affinity with Pinned Keys** - When session affinity reorders the chain, a routing rule's key pin now moves with its provider, so the pinned key is never looked up under the wrong provider (#7468)
- **Session Affinity Route Matching** - A session's route is now matched on provider and model together. Bindings the request followed into a failure are dropped (#7473)
- **Databricks Gemini System Prompts** - Multiple system and developer messages are merged into one for Gemini models hosted on Databricks, which reject more than one system prompt (#7461)
- **Bedrock Encrypted Reasoning Replay** - Bedrock's "encrypted reasoning was created for a different account or model" error now triggers the strip-and-retry path for unverifiable reasoning
- **Decisions on Bedrock Mantle** - Decision emulation now sends `tool_choice: "auto"` for gpt-oss models on Bedrock Mantle, which reject `"required"`. Leaked parameter tags with surrounding whitespace are now recovered
- **Gemini Transcription Usage** - Usage is reported even when the transcript is empty
- **Routing Rule Enabled State** - Syncing or updating a routing rule that omits `enabled` keeps the stored value, where it used to write NULL
- **Telemetry User Labels Toggle** - `user_labels_enabled` is now saved with the telemetry config (#7490)

## 🗄️ Database Migrations

- No new database migrations in this release.
