- [fix]: Anthropic requests send every custom tool `input_schema` with a `"type":"object"` root, which Anthropic requires. A chat function tool whose `parameters` root has no `type` was sent without `input_schema`, and the Responses converter sent such a root without `type`; both were rejected with a 400. A root `anyOf` / `oneOf` / `allOf` is now rewritten into an object schema, as for a typed root, and any other typeless root (`{}`, a bare `properties`, `description` or `$ref` object) gets `"type":"object"` with its other keys kept [@KamilDziemba](https://github.com/KamilDziemba)
- [fix]: Anthropic-compatible responses report stop_reason "stop_sequence" with the matched stop_sequence instead of end_turn/null when Claude (Anthropic or Vertex) stops on a requested stop sequence, in both streaming and non-streaming responses. The matched sequence now survives the Bifrost Responses round trip via an optional stop_sequence field; OpenAI-style "stop" finishes still map to end_turn [@jimseiwert](https://github.com/jimseiwert)
- [feat]: Typesafe can back a custom provider (base_provider_type "typesafe"). The Typesafe provider now answers to the custom name for lookup, key selection and reporting, honours allowed_requests for decisions and list models, applies request_path_overrides, supports keyless listing, and the native model listing strips the custom provider prefix [@akshaydeo](https://github.com/akshaydeo)
- [fix]: chat streams converted from a Responses upstream send the assistant role exactly once, on the first chunk. Turns with only reasoning and function_call items sent no role at all, so clients such as LangChain.js dropped the tool calls (#7693) [@hmdsefi](https://github.com/hmdsefi)
- [fix]: preserve Mistral streaming text arrays and reject unsupported content blocks [@xujiantop-crypto](https://github.com/xujiantop-crypto)
- [fix]: Anthropic error envelope always uses a documented error.type. Errors with no type were labelled api_error regardless of status (400 validation and unsupported-operation errors included), and Gemini/Vertex gRPC statuses such as INVALID_ARGUMENT were passed through verbatim. The type is now kept when already valid, mapped from google.rpc status names, or derived from the HTTP status the error is sent with [@jimseiwert](https://github.com/jimseiwert)
- [fix]: Gemini/Vertex Responses conversion keeps function tools declared without input_schema instead of dropping them, and orders each batch of function responses to match the preceding function calls so Vertex (which strips call ids and pairs by position) no longer mis-pairs parallel tool results [@jimseiwert](https://github.com/jimseiwert)
- [feat]: Bedrock guardrail selective evaluation. Converse guardContent blocks (messages and system) were parsed on ingress and then dropped in the Bedrock -> Bifrost conversion, and guardrailConfig had no tagSuffix, so neither AWS scoping mechanism reached the provider and agentic scaffolding tripped PROMPT_ATTACK on every turn. Canonical text blocks now carry a guard_content marker that both Bedrock converters honour, guardrailConfig accepts tagSuffix (and forwards streamProcessingMode on ingress), inline <amazon-bedrock-guardrails-guardContent_{suffix}> tags become guardContent entries on the Converse egress, and guard markers render as tags with amazon-bedrock-guardrailConfig on the Anthropic-native InvokeModel egress. The invoke ingress also reads the X-Amzn-Bedrock-Guardrail* request headers and lifts amazon-bedrock-guardrailConfig and a top-level guardrailConfig into the typed config; guardContent covers the image arm (png/jpeg inline bytes) as well as text; the same guard_content marker is available on Chat Completions content blocks, tagSuffix is validated (1-20 alphanumeric) with a 400 on every path, and InvokeModel-shaped replies carry amazon-bedrock-guardrailAction and amazon-bedrock-trace (last chunk on the streaming route) (#7696) [@akshaydeo](https://github.com/akshaydeo)
- [fix]: map chat completion n to Gemini candidateCount (#7388) [@axelray-dev](https://github.com/axelray-dev)
- [fix]: Bedrock InvokeModel keeps cache_control when any message's content is a plain string. BedrockMessage.Content is typed as content blocks, so one bare string anywhere in messages[] failed the standard unmarshal and diverted the whole request into the AI21 string fallback, which rebuilt the messages without calling applyMessageContentCacheControl. Every cache_control in the request was dropped rather than only the one on the string message, so prompt caching went off silently with just the system cachePoint surviving. The fallback now makes the same translation the standard path does (#7336) [@basil-k-aji-dev](https://github.com/basil-k-aji-dev)
- [fix]: record output items before closing open items on Gemini streaming Responses. When parts (thoughts, function calls, text) and finishReason share a single upstream frame, response.completed now correctly populates the Output array instead of emitting empty output (#7341) [@wofiporia](https://github.com/wofiporia)
- [fix]: web search action sources round-trip their name and no longer fabricate an empty url. OpenAI Responses web search can return specialized API sources ({"type":"api","name":"oai-weather"}) that carry a name and no URL; the typed source schema only modeled type and a required url, so decode dropped name and re-encode emitted "url":"", and the OpenAI request-side source sanitization rebuilt sources without name. url is now omitempty and name survives both the schema round-trip and the sanitization path (#7356) [@g-yixuan](https://github.com/g-yixuan)
- [fix]: The Claude Code billing header is stripped again when an effort-only system message comes first. Since 2.2.6 kept per-message `output_config`, an effort-only `role:"system"` message (`content: []` with `output_config.effort`) ahead of the `x-anthropic-billing-header` system message pushed the header out of the first slot, so it survived ingress: guardrails evaluated it and non-Anthropic fallbacks received it as prompt text. Extraction now skips leading effort-only items, and Anthropic attempts get the header restored in place right after them (#8039) [@akshaydeo](https://github.com/akshaydeo)
- fix: a zero provider usage.cost returns as total_cost 0 instead of an empty cost object (#9000) [@kunalgoesbyken](https://github.com/kunalgoesbyken)
[fix]: preserve cache-write TTL usage on Bedrock Invoke responses [@gauravch-code](https://github.com/gauravch-code)
- feat: baseten - new provider for Model APIs and dedicated deployments: chat, Responses, embeddings, transcription, decisions through System One, image generation on dedicated deployments (including `/predict` URLs) and rerank on BEI deployments
- feat: kimchi - new provider for chat, Responses, embeddings and list models (thanks [@arnasnn](https://github.com/arnasnn)!)
- feat: machgen - new provider for image generation, image edit and async video generation (submit, retrieve, download), with model listing (thanks [@nishkarsh-machgen](https://github.com/nishkarsh-machgen)!)
- feat: soniox - new provider for async speech-to-text, text-to-speech with streaming, speech sessions and real-time transcription
- feat: sarvam - chat and model listing move to Token Factory v2, and Sarvam Prism serves decisions (thanks [@vikashmaddi-sarvam](https://github.com/vikashmaddi-sarvam)!)
  <Warning>
  `sarvam-30b` is no longer served. Use `sarvam-105b`.
  </Warning>
- feat: gemini - native decisions for `gemini-decision-*` models and an `input_file` part for PDFs, audio and video; empty or mistyped input is rejected before the call (thanks [@Xytronix](https://github.com/Xytronix)!)
- feat: realtime - Gemini Live on the Gemini API and Vertex, Vertex Live through the GenAI native protocol, ElevenLabs Scribe and Soniox real-time transcription
- feat: speech sessions - `SpeechSessionProvider` interface for incremental text-to-speech over a WebSocket, implemented by ElevenLabs and Soniox
- feat: elevenlabs - passthrough and native WebSocket dialing for Scribe and text-to-dialogue, raw response kept for `with-timestamps`
- feat: vertex - text-to-speech through Cloud Text-to-Speech (Chirp) and Gemini TTS models
- feat: regex key aliases - `regex:` alias names over the requested model with `model_id` templates that reuse capture groups
- feat: usage estimation - cancelled or timed-out chat, text and Responses streams get missing usage estimated with a tokenizer, marked in `usage.estimation`
- feat: bedrock - native `outputConfig` structured output for supported models, with a one-time retry on the synthetic tool when Bedrock rejects the schema; `supports_native_structured_output` datasheet field
- feat: custom tools - custom (freeform) tools are rewritten as function tools for every model without native support and restored as custom calls on chat and Responses, streaming included
- feat: anthropic - forward `thinking.block_binding` with its beta header on the Claude API
- feat: anthropic - fast mode for `claude-fable-5-2` on Vertex
- feat: databricks - OAuth M2M secrets limited to specific scopes work through a scope fallback, and scope errors show the provider's message
- feat: opencode - stable `x-opencode-session` header per virtual key, resolved from client headers (thanks [@sstreichan](https://github.com/sstreichan)!)
- feat: files - Files API uploads stream from a `FileSource` instead of a byte slice on OpenAI and Azure
- feat: websocket stream hooks return the pre-hook request, so raw relays can forward plugin edits
- feat: `bifrost_provider_queue_wait_seconds` timing on the attempt window
- fix: anthropic - mid-conversation system reminders keep one form across turns, decided from the emitted messages, with placement controlled by the `supports_native_mid_conversation_system` datasheet field; the `<system-reminder>` envelope drops its trailing newline and the cache breakpoint stays on the marked block
- fix: vertex - Claude request bodies always end with `tools`, `system`, `messages` so prompt cache reads hit
- fix: anthropic - streamed usage counts each cached prompt token once on chat, Responses and passthrough streams (thanks [@is911](https://github.com/is911) and [@wangkanai](https://github.com/wangkanai)!)
- fix: prompt cache auto-injection checks an alias's canonical model (thanks [@bardrop](https://github.com/bardrop)!)
- fix: `prompt_cache_options` reaches only models that accept it
- fix: anthropic - a deferred web search is replayed without a made-up `web_search_tool_result`, and its late result stays in place on replay, Responses and streams
- fix: anthropic - history with MCP or server-tool blocks no longer sends a roleless message to OpenAI-compatible providers; Responses items with no chat equivalent are dropped on the Responses-to-Chat fallback (thanks [@1AbdullahButt](https://github.com/1AbdullahButt)!)
- fix: bedrock - `/anthropic/v1/messages` with `output_config.format` streams one well-formed text block, and plain-text answers are no longer lost
- fix: bedrock - honour `network_config.base_url` and apply the dial-time address policy to the net/http client
- fix: bedrock - a caller's `anthropic_beta` on Converse is merged with derived betas (thanks [@fus3r](https://github.com/fus3r)!)
- fix: bedrock - InvokeModel keeps `tool_reference` blocks inside a client `tool_result` (thanks [@kunalgoesbyken](https://github.com/kunalgoesbyken)!)
- fix: bedrock - Claude Responses requests with computer tools go through InvokeModel, and replayed `tool_use` blocks keep `toolset_name` (thanks [@davidelct](https://github.com/davidelct)!)
- fix: vertex - Anthropic betas are sent in the `anthropic-beta` header, including on CountTokens
- fix: bedrock mantle - unsupported effort-dependent sampling parameters are stripped for OpenAI models
- fix: logprobs survive conversion between Chat Completions and Responses
- fix: gemini - function calls without an ID get unique IDs and pair with responses in order
- fix: chat custom tools keep the `name` and `description` nested under `custom`
- fix: gemini - image `output_format` and `output_compression` map to Vertex image output options, and the Gemini API no longer receives options it rejects (thanks [@carlreid](https://github.com/carlreid)!)
- fix: mistral - `reasoning_effort` follows the model's datasheet ladder
- fix: elevenlabs - speech `with_timestamps` returns character times instead of null (thanks [@abmorrill4](https://github.com/abmorrill4)!)
  <Warning>
  Breaking for Go SDK users: `SpeechAlignment.CharStartTimesMs` / `CharEndTimesMs` are renamed to `CharacterStartTimesSeconds` / `CharacterEndTimesSeconds` (JSON `char_*_times_ms` → `character_*_times_seconds`).
  </Warning>
- fix: elevenlabs - non-streaming `wav` speech returns a real WAV file
- fix: openai - empty `text`, `srt` and `vtt` transcriptions are valid responses
- fix: openai - streaming speech on `tts-1` and `tts-1-hd` returns a 400
- fix: gemini - audio `fileData` parts are no longer treated as transcription
- fix: vertex - model listing on `us` and `eu` keys uses the regional endpoint host, and token errors show the underlying cause
- fix: streams cancelled before the upstream's first byte run their terminal post-hooks
- fix: core - isolate per-request identity and cancellation for nil-context Go SDK calls
  <Warning>
  Behaviour change for Go SDK users passing a `nil` `*schemas.BifrostContext`: nil-context requests no longer inherit values from `bifrost.ctx` or the `Init` parent context (`BifrostContextKeyVirtualKey`, `BifrostContextKeyAPIKeyName`, `BifrostContextKeyAPIKeyID`, `BifrostContextKeyAvailableProviders`, or custom plugin keys). Pass an explicit `*schemas.BifrostContext` when sharing key selection, governance, or custom values across calls.
  </Warning>
- chore: brotli 1.2.8 and dependency updates
- chore: upgraded Go to 1.27.4
