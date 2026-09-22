## ✨ Features

- **Typesafe Provider and Decisions API** - New typesafe provider, `/v1/decisions` endpoint and `/typesafe` integration. Providers without native decision support now answer decision requests through forced tool-calling on their chat model, whether used as the primary or as a fallback. Probabilities are normalized to sum to exactly 1 and the chosen option must be the most likely one. Decision requests are priced from the datasheet and logged with their answers (#7355, #7361, #7384, #7440)
- **Provider-Level Session Affinity** - A session (from `x-bf-session-id` or the session header that Claude Code, Codex CLI or OpenCode already send) stays on the provider and key that last served it. Affinity only reorders the chain routing built and never restores a provider routing excluded. The logs UI shows it as a routing engine
- **Claude Opus 5.5 Support** - Computer use sends `computer_toolset_20260801` on the Anthropic API and Vertex, while Bedrock and Azure keep `computer_20251124`. `toolset_name` is carried on both halves of each call/result pair across typed, raw passthrough and streaming paths. Disabled thinking and forced tool choice are rejected for Opus 5.5+, and the datasheet `supports_reasoning_disable` field can override this (#7433, #7434, #7441)
- **Claude Code Auto-Mode Safeguards Passthrough** - `safeguards` and `safeguard_results` are forwarded byte-for-byte on requests, responses and stream events to the direct Anthropic provider and stripped on every other provider. The `dangerous-tool-use` and `auto-mode-classifier` betas are gated the same way. Unknown Anthropic SSE events are forwarded raw on the Anthropic passthrough (#7393, #7440)

## 🐞 Fixed

- **OTel HTTP Semantic Conventions** - The root HTTP span uses the stable attribute names (`http.request.method`, `url.path`, `http.route`, `http.response.status_code`, `user_agent.original`) instead of `http.method`, `http.url`, `http.status_code` and `http.user_agent`, and its name no longer includes the query string (#7439)
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

## 🗄️ Database Migrations

- No new database migrations in this release.

## 🐙 Closed GitHub Issues

- [#7223](https://github.com/maximhq/bifrost/issues/7223) - MCP client HTTP transport ignores HTTP_PROXY/HTTPS_PROXY and fails on any deployment behind an egress proxy
- [#7336](https://github.com/maximhq/bifrost/issues/7336) - Bedrock InvokeModel drops message-level cache_control when any historical message content is a JSON string
- [#7356](https://github.com/maximhq/bifrost/issues/7356) - Responses web search API source name is dropped during round-trip
- [#7402](https://github.com/maximhq/bifrost/issues/7402) - Grok 4.7 xhigh reasoning effort is silently downgraded to high
- [#7411](https://github.com/maximhq/bifrost/issues/7411) - Redundant JSON decoding in Responses request preparation
- [#7425](https://github.com/maximhq/bifrost/issues/7425) - Responses: `{"type":"computer"}` is rewritten to `computer_use_preview`, breaking GPT-6 Astra / GPT-5.6 computer use
