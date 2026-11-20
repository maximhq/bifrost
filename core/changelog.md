- fix: bedrock - an encrypted reasoning detail replays for Claude on Converse too, as an empty-text `reasoningText` carrying the blob in `signature` (the form the Responses path already uses), instead of being dropped silently (#10177) [@PoisonGodv](https://github.com/PoisonGodv)
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
- chore: brotli 1.2.8 and dependency updates
- chore: upgraded Go to 1.27.4
