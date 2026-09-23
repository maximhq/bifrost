---
title: "v2.2.2"
description: "v2.2.2 changelog - 2026-09-23"
---
<Tabs>
  <Tab title="NPX">
    ```bash
    npx -y @maximhq/bifrost --transport-version v2.2.2
    ```
  </Tab>
  <Tab title="Docker">
    ```bash
    docker pull maximhq/bifrost:v2.2.2
    docker run -p 8080:8080 maximhq/bifrost:v2.2.2
    ```
  </Tab>
</Tabs>

<Update label="Bifrost(HTTP)" description="2.2.2">
## ✨ Features

- **Typesafe Provider and Decisions API** - New typesafe provider, `/v1/decisions` endpoint and `/typesafe` integration. Providers without native decision support now answer decision requests through forced tool-calling on their chat model, whether used as the primary or as a fallback. Probabilities are normalized to sum to exactly 1 and the chosen option must be the most likely one. Decision requests are priced from the datasheet and logged with their answers (#7355, #7361, #7384, #7440)
- **Provider-Level Session Affinity** - A session (from `x-bf-session-id` or the session header that Claude Code, Codex CLI or OpenCode already send) stays on the provider and key that last served it. Affinity only reorders the chain routing built and never restores a provider routing excluded. The logs UI shows it as a routing engine
- **Claude Opus 5.5 Support** - Computer use sends `computer_toolset_20260801` on the Anthropic API and Vertex, while Bedrock and Azure keep `computer_20251124`. `toolset_name` is carried on both halves of each call/result pair across typed, raw passthrough and streaming paths. Disabled thinking and forced tool choice are rejected for Opus 5.5+, and the datasheet `supports_reasoning_disable` field can override this (#7433, #7434, #7441)
- **Claude Code Auto-Mode Safeguards Passthrough** - `safeguards` and `safeguard_results` are forwarded byte-for-byte on requests, responses and stream events to the direct Anthropic provider and stripped on every other provider. The `dangerous-tool-use` and `auto-mode-classifier` betas are gated the same way. Unknown Anthropic SSE events are forwarded raw on the Anthropic passthrough (#7393, #7440)

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

## 🗄️ Database Migrations

- No new database migrations in this release.

## 🐙 Closed GitHub Issues

- [#7223](https://github.com/maximhq/bifrost/issues/7223) - MCP client HTTP transport ignores HTTP_PROXY/HTTPS_PROXY and fails on any deployment behind an egress proxy
- [#7336](https://github.com/maximhq/bifrost/issues/7336) - Bedrock InvokeModel drops message-level cache_control when any historical message content is a JSON string
- [#7356](https://github.com/maximhq/bifrost/issues/7356) - Responses web search API source name is dropped during round-trip
- [#7402](https://github.com/maximhq/bifrost/issues/7402) - Grok 4.7 xhigh reasoning effort is silently downgraded to high
- [#7411](https://github.com/maximhq/bifrost/issues/7411) - Redundant JSON decoding in Responses request preparation
- [#7425](https://github.com/maximhq/bifrost/issues/7425) - Responses: `{"type":"computer"}` is rewritten to `computer_use_preview`, breaking GPT-6 Astra / GPT-5.6 computer use

</Update>
<Update label="Core" description="1.10.1">
- feat: typesafe provider, /v1/decisions endpoint, and decision emulation via forced tool-calling for providers without native decision support (#7355, #7361, #7384, #7440)
- feat: provider-level session affinity through the SessionAffinity seam, bound on request outcome
- feat: Claude Opus 5.5 computer_toolset_20260801 support with toolset_name round-trip, and disabled-thinking/forced-tool-choice gating overridable from the datasheet (#7433, #7434, #7441)
- feat: safeguards/safeguard_results passthrough for Claude Code auto-mode on direct Anthropic, stripped elsewhere; raw carrier for unknown Anthropic stream events (#7393, #7440)
- fix: rewrite tool-schema regex NUL escapes and lookarounds for Moonshot and DeepSeek models only (#7430)
- fix: strip Anthropic billing header at ingress and restore it only for Anthropic-family attempts (#7431)
- fix: honor HTTP_PROXY/HTTPS_PROXY/NO_PROXY for MCP connections with a pre-proxy link-local guard (#7437)
- fix: stop rewriting OpenAI `computer` tool to computer_use_preview (#7426) (thanks [@abhishekgahlot2](https://github.com/abhishekgahlot2)!)
- fix: linear StripEmptyThinkingBlocks and beta-header gating without full body decode (#7406)
- fix: strip Bedrock cachePoint markers copy-on-write for non-Bedrock providers, preserved for fallbacks (#7182)
- fix: Bedrock tool results with empty-string JSON keys sent as text (#7396)
- fix: deep copy extended Responses fields in DeepCopyResponsesMessage (#7422)
- fix: frame bundled OpenAI chat stream raw frames with a data prefix each (#7440)
- fix: stream errors after startup events (response.created, in_progress, empty role delta) now reach retry and fallback for OpenAI models on every host (OpenAI, Bedrock, Bedrock Mantle, Vertex, custom providers), not only Azure; an overloaded stream no longer reaches the client as an error when a fallback is configured
- [perf]: avoid redundant JSON decoding in Responses request preparation
- [fix]: preserve xhigh reasoning effort for Grok 4.7 [@nettee](https://github.com/nettee)
- [fix]: Bedrock InvokeModel keeps cache_control when any message's content is a plain string. BedrockMessage.Content is typed as content blocks, so one bare string anywhere in messages[] failed the standard unmarshal and diverted the whole request into the AI21 string fallback, which rebuilt the messages without calling applyMessageContentCacheControl. Every cache_control in the request was dropped rather than only the one on the string message, so prompt caching went off silently with just the system cachePoint surviving. The fallback now makes the same translation the standard path does (#7336) [@basil-k-aji-dev](https://github.com/basil-k-aji-dev)
- [fix]: web search action sources round-trip their name and no longer fabricate an empty url. OpenAI Responses web search can return specialized API sources (`{"type":"api","name":"oai-weather"}`) that carry a name and no URL; the typed source schema only modeled type and a required url, so decode dropped name and re-encode emitted "url":"", and the OpenAI request-side source sanitization rebuilt sources without name. url is now omitempty and name survives both the schema round-trip and the sanitization path (#7356) [@g-yixuan](https://github.com/g-yixuan)

</Update>
<Update label="Framework" description="1.7.3">
- feat: decision request pricing and "decisions" usage type in the datasheet
- chore: upgraded core to v1.10.0
- fix: replace streaming gate replay-buffer size accounting with cached zero-marshal estimates (eliminates per-chunk MarshalJSON on the full-hold path)

</Update>
<Update label="compat" description="0.3.2">
- fix: Bedrock cachePoint handling moved to core dispatch; plugin no longer mutates the shared request (#7182)
- fix: clone only Reasoning, ToolChoice and Tools in PreLLMHook (#7097)
- chore: upgraded core to v1.10.0 and framework to v1.7.3

</Update>
<Update label="governance" description="1.8.2">
- fix: allow-all virtual keys list and route every configured provider (#7375)
- fix: routing log names providers excluded for having no weight
- chore: upgraded core to v1.10.0 and framework to v1.7.3

</Update>
<Update label="jsonparser" description="1.6.5">
- chore: upgraded core to v1.10.0 and framework to v1.7.3

</Update>
<Update label="logging" description="1.8.2">
- feat: log decision requests with usage, cost and answers (#7355)
- chore: upgraded core to v1.10.0 and framework to v1.7.3

</Update>
<Update label="maxim" description="1.7.5">
- chore: upgraded core to v1.10.0 and framework to v1.7.3

</Update>
<Update label="mocker" description="1.6.5">
- chore: upgraded core to v1.10.0 and framework to v1.7.3

</Update>
<Update label="modelcatalogresolver" description="1.1.5">
- chore: upgraded core to v1.10.0 and framework to v1.7.3

</Update>
<Update label="otel" description="1.5.5">
- chore: upgraded core to v1.10.0 and framework to v1.7.3

</Update>
<Update label="prompts" description="1.1.5">
- chore: upgraded core to v1.10.0 and framework to v1.7.3

</Update>
<Update label="routing" description="1.1.2">
- chore: complexity routing session keys built through the shared session-state builder
- chore: upgraded core to v1.10.0 and framework to v1.7.3

</Update>
<Update label="semanticcache" description="1.6.5">
- chore: upgraded core to v1.10.0 and framework to v1.7.3

</Update>
<Update label="telemetry" description="1.8.1">
- chore: shared label-value splice helper for metric labels (#7378)
- chore: upgraded core to v1.10.0 and framework to v1.7.3

</Update>
