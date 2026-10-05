# Bifrost Harness Coverage Backlog

Per-provider inventory of features sourced from each provider's official docs, cross-referenced
against the current harness collection (`provider-harness.json`). Each row is a candidate test
addition; checked rows are already covered.

Legend:
- `[x]` covered by harness
- `[ ]` not yet covered (candidate addition)
- `[~]` partially covered (only some variants tested)

---

## OpenAI

Sources:
- Chat Completions API: <https://platform.openai.com/docs/api-reference/chat>
- Responses API: <https://platform.openai.com/docs/api-reference/responses>
- Models: <https://platform.openai.com/docs/models>

### Chat Completions (`POST /v1/chat/completions`)

- [x] Basic chat (`messages` array)
- [x] System message (system role)
- [x] Multi-turn conversation history
- [x] Streaming (`stream: true`)
- [x] Vision (`image_url` content block)
- [x] Function calling (`tools[].type=function`)
- [x] Tool choice forced (`tool_choice: "required"`)
- [x] Structured output (`response_format: json_schema`)
- [x] Reasoning effort (`reasoning_effort: "high"` for gpt-5/o3)
- [x] Reasoning effort forwarded to xAI (grok-4.5 / grok-4.6 / grok-4.20-multi-agent) — folder 50
- [x] **Tool choice: specific function** (`tool_choice: { type: "function", function: { name: "x" } }`) — audited: covered (weak: status/shape assertions only)
- [x] **Parallel tool calls** (`parallel_tool_calls: true/false`) — built: folder 144.3/144.4
- [x] **Response format JSON object** (`response_format: { type: "json_object" }`) — audited: covered (weak: status/shape assertions only)
- [x] **Logprobs** (`logprobs: true, top_logprobs: N`) — audited: covered (weak: status/shape assertions only)
- [x] **Logit bias** (`logit_bias: { token_id: bias }`) — built: folder 144.2
- [x] **Seed for deterministic output** (`seed: 12345`) — audited: covered (weak: status/shape assertions only)
- [x] **Stop sequences** (`stop: ["END"]`) — audited: covered (weak: status/shape assertions only)
- [x] **N completions** (`n: 3`) — built: folder 144.1
- [x] **Temperature / top_p / frequency_penalty / presence_penalty** — audited: covered (weak: status/shape assertions only)
- [x] **Stream options with usage** (`stream_options: { include_usage: true }`) — audited: covered (weak: status/shape assertions only)
- [x] **Service tier** (`service_tier: "scale" | "default" | "priority"`) — built: folder 144.5 (default echo) + 155.17 [PREVIEW] (priority); scale is not accepted on every key
- [x] **Audio input** (`input_audio` content block, gpt-4o-audio-preview) — gated: folder 153.8 [PREVIEW] (1s wav generated at build time; needs gpt-4o-audio-preview on the key)
- [x] **Audio output** (`modalities: ["text","audio"], audio: {voice, format}`) — gated: folder 150.3 [PREVIEW] (needs gpt-4o-audio-preview on the key)
- [x] **Web search options** (`web_search_options` for chat-completions web search)
- [x] **Predicted outputs** (`prediction: { type: "content", content: "..." }`) — audited: covered (weak: status/shape assertions only)
- [x] **Store + metadata for evals** (`store: true, metadata: {...}`) — audited: covered (weak: status/shape assertions only)

### Responses API (`POST /v1/responses`)

- [x] Basic input (`input` string)
- [x] Web search (`tools: [{ type: "web_search_preview" }]`)
- [x] Code interpreter (`tools: [{ type: "code_interpreter", container: { type: "auto" }}]`)
- [x] **File search** (dropped earlier; needs vector_store setup) — `tools: [{ type: "file_search", vector_store_ids: [...] }]` — built: folder 155.1 [PREVIEW] (set openaiVectorStoreId)
- [x] **Computer use preview** (`tools: [{ type: "computer_use_preview", display_width, display_height, environment }]`) — built: folder 155.2 [PREVIEW] (needs computer-use-preview model access)
- [x] **MCP tool** (`tools: [{ type: "mcp", server_label, server_url }]`) — drop-path covered for non-MCP providers (Bedrock + Vertex) via "MCP Tool Handling cross-cut" (regression #3795); **OpenAI/Anthropic forward-to-connector path still untested** — built: folder 152.1 (OpenAI forwards, asserts mcp_list_tools) / 152.2 [EXPECT-4XX] unreachable server; Anthropic mcp_toolset is 145.7 [PREVIEW]
- [x] **Image generation** (`tools: [{ type: "image_generation" }]` requires gpt-image-1 access) — folder 76 (#7059 / PR #7060): bare-string `action` decode + the settings echoed back on the completed item, across native `/v1/responses` (non-streaming + streaming) and the `/openai` drop-in stream
- [x] **Reasoning summary** (`reasoning: { summary: "auto" }`) — OpenAI passthrough in "12. Backlog Coverage" (`summary_index + obfuscation preserved`); the `reasoning_summary_*` event fields themselves across Gemini/Vertex/Anthropic/Bedrock in "72. Reasoning Summary Streaming Event Fields"
- [x] **Background mode** (`background: true`) — async execution — audited: covered (weak: status/shape assertions only)
- [x] **Truncation strategy** (`truncation: "auto"`) — audited: covered (weak: status/shape assertions only)
- [x] **Tool choice for Responses API** (`tool_choice: { type: "file_search" }` etc.) — built: folder 152.3 (web_search_preview forced); the file_search form stays blocked with File search
- [x] **Conversation continuation** (`previous_response_id: "..."`) — built: folder 144.6a/b
- [x] **Input as messages array** (Responses also accepts messages-shape input) — audited: covered
- [x] **Stream events** (Responses streams structured event types: response.output_item.added, etc.) — built: folder 144.7
- [x] **Multimodal input items** (text + image_url + input_file in one request) — built: folder 152.4
- [x] **PDF input** (`input_file` with PDF data) — audited: covered
- [x] **include array** (`include: ["file_search_call.results", "message.input_image.image_url"]`) — built: folder 152.5 (reasoning.encrypted_content)
- [x] **Custom tool** (`tools: [{ type: "custom", name, description, input_schema }]`) — built: folder 152.6 (real type:custom tool, asserts custom_tool_call)
- [x] **Token counting endpoint** (`POST /v1/responses/input_tokens`) — audited: covered

### Other endpoints

- [x] **Embeddings** (`POST /v1/embeddings`) - folder 53: batch-input arity (53.C1), `dimensions` maps to OpenAI `dimensions` (53.D1), `encoding_format: "base64"` returns a packed string (53.E1)
- [x] **Audio speech (TTS)** (`POST /v1/audio/speech`) — audited: covered
- [x] **Audio transcription** (`POST /v1/audio/transcriptions`) — audited: covered
- [x] **Image generation** (`POST /v1/images/generations`) — audited: covered
- [x] **Image edit** (`POST /v1/images/edits`) — built: folder 150.1 (openai/gpt-image-1, multipart)
- [x] **Image variation** (`POST /v1/images/variations`) — gated: folder 150.2 [PREVIEW] (dall-e-2 availability unconfirmed)
- [x] **Batch API** (`POST /v1/batches` + `GET /v1/batches/{id}`): OpenAI/Anthropic covered in folder `12. Backlog Coverage / OpenAI/Anthropic/Gemini/Azure Round 3` — upload input file (OpenAI only), create, retrieve, cancel, all asserted. Gemini native batch (`/genai/v1beta/models/{model}:batchGenerateContent` + `/genai/v1beta/batches`) covered separately in folder `55. Gemini Native Batch API` — create/list/retrieve/cancel, inline requests (no file upload needed). Vertex batch covered in folder `11c. Vertex Batches`. Azure batch (via `/openai/v1/batches` with `provider:"azure"` / `?provider=azure`, no dedicated route - reuses the OpenAI drop-in with inline `requests` auto-uploaded server-side) and Bedrock batch create/retrieve/cancel (`/bedrock/model-invocation-job*`, needs an S3 bucket + IAM role_arn not yet in the harness env) remain uncovered. Settled cost/pricing not covered here (async, no test hook for the sweeper) - see `plugins/logging/costfidelity_test.go` / `framework/batchaccounting/*_test.go` for that.
- [x] **Files API** (`POST /v1/files`, etc.) — audited: covered
- [x] **Models list** (`GET /v1/models`) — audited: covered
- [x] **Containers API** (`POST /v1/containers` for code-interpreter sandboxes) — built: folder 152.7a-d (create, retrieve, list, delete)
- [x] **Videos API** (`POST /v1/videos` for Sora) — built: folder 155.3a/b [PREVIEW] create + retrieve (paid; set include_preview=1)
- [x] **Rerank** (`POST /v1/rerank`) - folder 56, cross-provider across cohere/bedrock/vertex

---

## Anthropic

Sources:
- Messages API: <https://platform.claude.com/docs/en/api/messages>
- Models: <https://platform.claude.com/docs/en/about-claude/models/overview>
- Beta features: <https://platform.claude.com/docs/en/api/beta-headers>
- Tool use overview: <https://platform.claude.com/docs/en/agents-and-tools/tool-use/overview>

### Messages API (`POST /v1/messages`)

- [x] Basic chat (`messages` array)
- [x] System (`system` block)
- [x] Streaming (SSE)
- [x] Vision (image source: url, base64)
- [x] Custom tool use (`tools: [{ name, description, input_schema }]`)
- [x] Tool choice forced (`tool_choice: { type: "any" }`)
- [x] Web search basic (`web_search_20250305`)
- [x] Web search with dynamic filtering (`web_search_20260209` + code_execution)
- [x] Web search domain filter (`allowed_domains` / `blocked_domains`)
- [x] Web search user location (`user_location: { type, city, region, country, timezone }`)
- [x] Code execution (`code_execution_20250522`)
- [x] Computer use (`computer_20250124` + bash + text_editor; beta header)
- [x] Extended thinking (`thinking: { type: "enabled", budget_tokens }`)
- [x] Adaptive thinking (`thinking: { type: "adaptive" }` for Opus 4.7)
- [x] Prompt caching ephemeral (`cache_control: { type: "ephemeral" }`)
- [x] **Prompt caching persistent / 1-hour** (`cache_control: { type: "ephemeral", ttl: "1h" }`) - folder 64.1 asserts `"ttl":"1h"` reaching the Anthropic, Vertex Claude and Bedrock wires (and being dropped where the dialect cannot carry it) for an **injected** breakpoint via `prompt_cache.ttl`. A client-sent `cache_control.ttl` on the request itself is still uncovered. — audited: covered (weak: status/shape assertions only)
- [x] **Web fetch tool** (`web_fetch_20250910`, `web_fetch_20260209`, `web_fetch_20260309`) — built: folder 145.6 (web_fetch_20260209) / 155.19 (20250910 + beta header) / 155.20 (20260309), all [PREVIEW]
- [x] **Memory tool** (`memory_20250818`) — audited: covered (weak: status/shape assertions only)
- [x] **Tool search** (`tool_search_tool_bm25`, `tool_search_tool_regex`) - Anthropic accept-path in folder 12; Bedrock InvokeModel routing (tool_search + defer_loading) pinned in folder 71
- [x] **MCP toolset** (`mcp_toolset` server reference) — gated: folder 145.7 [PREVIEW]
- [x] **Code execution v2** (`code_execution_20250825`) — audited: covered
- [x] **Code execution programmatic** (`code_execution_20260120`) — audited: covered
- [x] **Computer use new-gen** (`computer_20251124` + `text_editor_20250728` + `bash_20250124` for Opus 4.7/4.6/Sonnet 4.6) — audited: covered
- [x] **PDF input** (`{ type: "document", source: { type: "base64", media_type: "application/pdf" } }`) - folder 75 (cowork-attachments): native + streaming + `/v1/chat/completions` `file.file_data` + `/v1/responses` `input_file.file_data`, plus Files API `file_id` in all three shapes, each asserting the wire payload via `x-bf-send-back-raw-request`
- [x] **Citations** (`citations: { enabled: true }` on document blocks) — audited: covered (weak: status/shape assertions only)
- [x] **Stop sequences** (`stop_sequences: ["END"]`) — audited: covered (weak: status/shape assertions only)
- [x] **Sampling** — `temperature` / `top_p` / `top_k` (deprecated for Opus 4.7+ — should NOT be sent) — built: folder 145.3 (Opus 4.8 stripped) / 145.4 (haiku control)
- [x] **Service tier** (`service_tier: "auto" | "standard_only"`) — built: folder 145.5
- [x] **Effort** (`output_config: { effort: "low" | "medium" | "high" | "max" }` for Opus 4.5/4.6) — audited: covered
- [x] **Format / structured output** (`output_config: { format: { type: "json_schema", schema: {...} } }`) — audited: covered (weak: status/shape assertions only)
- [x] **Defer loading** (`defer_loading: true` on tools) — audited: covered
- [x] **Allowed callers** (`allowed_callers: [...]` on tools) — audited: covered
- [x] **Eager input streaming** (`eager_input_streaming: true` on tools; beta) — audited: covered
- [x] **Strict tool input** (`strict: true` for structured-outputs validation) — audited: covered (weak: status/shape assertions only)
- [x] **Tool input examples** (`input_examples: [{ input, description }]`) — audited: covered
- [x] **Skills + container** (advanced-tool-use bundle) — audited: covered (weak: status/shape assertions only)
- [x] **Stream parameters** — `stream_options` (Anthropic's variant) — built: folder 145.1
- [x] **Beta header explicit list** (`betas: ["computer-use-2025-11-24", "prompt-caching-2024-07-31", ...]`) — built: folder 153.4 (comma-separated values accepted) / 153.5 [EXPECT-400] unknown value surfaces Anthropic's error

### Beta headers (each is a feature gate)

- [x] `computer-use-2025-01-24`
- [x] **`computer-use-2025-11-24`** (paired with new-gen tools) — audited: covered
- [x] **`prompt-caching-2024-07-31`** (cache_control validation) — built: folder 145.2
- [x] **`output-128k-2025-02-19` / `output-300k-2026-03-24`** (extended output for batch API) — built: folder 157.1a/b [PREVIEW]: output-300k-2026-03-24 is a real Message Batches-only beta (Opus 4.7/4.8/5, Sonnet 4.6/5; not Bedrock/Vertex/Foundry), so the case creates a batch with max_tokens 200000 and cancels it; output-128k is retired
- [x] **`token-efficient-tools-2025-02-19`** (smaller tool definitions) — audited: covered (weak: status/shape assertions only)
- [x] **`fine-grained-tool-streaming-2025-05-14`** — audited: covered
- [x] **`extended-thinking-2025-01-15`** — re-scoped: this is not a real header: Anthropic documents that plain thinking: {type: enabled, budget_tokens} needs no beta header; the only thinking beta is interleaved-thinking-2025-05-14 (already covered). The behavior is pinned by 148.13/148.14 and an unknown beta value by 153.5
- [x] **`fast-mode-2026-02-01`** (Opus 4.6 only) — audited: covered (weak: status/shape assertions only)
- [x] **`compact-2025-09-15`** (compaction) — accept-path smoke in Round 10; live compaction-billing iteration sum pinned in folder 59
- [x] **`context-management-2025-09-15` / `context-1m-2025-09-15`** — compaction billing via `compact_20260112` on `/v1/responses` (folder 59)
- [x] **`files-api-2025-04-14`** — audited: covered
- [x] **`mcp-client-2025-09-15`** — audited: covered (weak: status/shape assertions only)
- [x] **`tool-examples-2025-10-29`** — audited: covered (weak: status/shape assertions only)
- [x] **`advanced-tool-use-2025-09-15`** (bundle: defer_loading + allowed_callers + skills) — audited: covered (weak: status/shape assertions only)
- [x] **`interleaved-thinking-2025-05-14`** — audited: covered (weak: status/shape assertions only)
- [x] **`skills-2025-10-29`** — audited: covered (weak: status/shape assertions only)
- [x] **`redact-thinking-2025-09-15`** — audited: covered (weak: status/shape assertions only)
- [x] **`task-budgets-2025-09-15`** — audited: covered
- [x] **`eager-input-streaming-2025-10-29`** — audited: covered

### Other endpoints

- [x] **Token counting** (`POST /v1/messages/count_tokens`) — audited: covered (weak: status/shape assertions only)
- [x] **Message Batches** (`POST /v1/messages/batches` + cancel + retrieve + results): create/retrieve/cancel/list asserted in folder `12. Backlog Coverage / OpenAI/Anthropic/Gemini/Azure Round 3` and `Anthropic Backlog`; `results` (post-settlement) not covered - requires a completed batch, no fast test path (sweeper poll is real-time, hard-coded 1 min interval) — built: create/retrieve/cancel/list in folder 12; results via 155.16 [PREVIEW] (set anthropicSettledBatchId) and the 151.14 unknown-id probe
- [x] **Files API** (`POST /v1/files`, list, retrieve, delete, content) — audited: covered
- [x] **Models list** (`GET /v1/models`) — audited: covered (weak: status/shape assertions only)
- [x] **Text Completions API** (legacy `POST /v1/complete`) — gated: folder 151.13 [EXPECT-4XX] error-envelope probe; a successful legacy completion needs a retired Claude 2.x model

---

## AWS Bedrock

Sources:
- Converse API: <https://docs.aws.amazon.com/bedrock/latest/userguide/conversation-inference-call.html>
- Inference profiles: <https://docs.aws.amazon.com/bedrock/latest/userguide/inference-profiles-support.html>
- Anthropic on Bedrock: <https://platform.claude.com/docs/en/build-with-claude/claude-in-amazon-bedrock>

### Converse API (`POST /model/{modelId}/converse`)

- [x] Basic conversation (`messages: [{ role, content: [{ text }] }]`)
- [x] System block (`system: [{ text }]`)
- [x] Inference config (`inferenceConfig: { maxTokens }`)
- [x] Tool config (`toolConfig: { tools: [{ toolSpec: { name, inputSchema } }] }`)
- [x] **Streaming** (`POST /model/{modelId}/converse-stream`) — audited: covered (weak: status/shape assertions only)
- [x] **Vision** (`content: [{ image: { format, source: { bytes } } }]`) — audited: covered (weak: status/shape assertions only)
- [x] **Document input** (`content: [{ document: { format, name, source: { bytes } } }]`) — the converter into this block is covered by folder 42 (#5472: OpenAI `type:"file"` / Responses `input_file` document uploads via `/v1/chat/completions` and `/v1/responses`, xlsx/docx/csv/pdf/txt + `file_url`). A native Converse-shaped `document` block posted directly at `/bedrock/model/{id}/converse` is covered by folder 77 (#7072), which also pins text-format documents (txt/md/csv/html) across every ingress - they shipped as a bare `source.text` and 400d.
- [x] **Video input** (`content: [{ video: { format, source } }]`) — built: folder 155.9 [PREVIEW] (set bedrockVideoB64 to a base64 mp4)
- [x] **Tool result** (`content: [{ toolResult: { toolUseId, content, status } }]`) — built: folder 146.3
- [x] **Stop sequences** (`inferenceConfig: { stopSequences: [...] }`) — audited: covered (weak: status/shape assertions only)
- [x] **Temperature / topP** (warned for Opus 4.7 which rejects them) — built: folder 146.5
- [x] **Tool choice** (`toolConfig: { toolChoice: { auto/any/tool: { name } } }`) — built: folder 146.1/146.2 (any, auto)
- [x] **Guardrail config** (`guardrailConfig: { guardrailIdentifier, guardrailVersion, trace }`) — audited: covered
- [x] **Additional model request fields** (`additionalModelRequestFields: { ... }`) — provider-specific passthrough — built: folder 146.4
- [x] **Additional model response field paths** (`additionalModelResponseFieldPaths`) — built: folder 153.6 (/stop_sequence round trip per the AWS docs) / 153.7 [EXPECT-400] malformed pointer
- [x] **Prompt variables** (`promptVariables: { var: { text } }`) — for managed prompts — built: folder 155.8 [PREVIEW] (set bedrockPromptArn, URL-encoded)
- [x] **Performance config** (`performanceConfig: { latency: "standard" | "optimized" }`) — audited: covered (weak: status/shape assertions only)
- [x] **Request metadata** (`requestMetadata: { ... }`) — Anthropic and PydanticAI ingress, folder 113

### InvokeModel API (`POST /model/{modelId}/invoke`)

- [x] **Direct invoke** with Anthropic-native provider body, incl. image/tool_use/tool_result content blocks — folder 37 (#5560)
- [x] **Direct invoke** with Cohere-native embedding body — folder 58.C (PR #6335)
- [x] **Invoke streaming** (`POST /model/{modelId}/invoke-with-response-stream`) — folder 36 (#5629), folder 37 (#5560)
- [x] **Async invocation jobs** (`POST /model-invocation-job` + list + get + stop) — built: folder 152.8 (list) + 155.5a-c [PREVIEW] (create, get, stop; set the bedrockBatch* variables)

### Cross-region inference profiles

- [x] `global.anthropic.claude-*` (4 entries: opus-4-7, sonnet-4-6, opus-4-6, opus-4-5...)
- [x] `us.amazon.nova-*` (3 entries: pro, lite, micro)
- [x] **EU geo profile** (`eu.anthropic.claude-*`) — gated: folder 146.7 [PREVIEW]
- [x] **APAC geo profile** (`apac.anthropic.claude-*`) — gated: folder 146.8 [PREVIEW]
- [x] **JP geo profile** (`jp.anthropic.claude-*`) — gated: folder 146.9 [PREVIEW]
- [x] **AU geo profile** (`au.anthropic.claude-haiku-4-5`) — gated: folder 146.10 [PREVIEW]

### Embeddings (`POST /model/{modelId}/invoke`)

Sources:
- Titan Text Embeddings: <https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-titan-embed-text.html>
- Cohere Embed v4: <https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-embed-v4.html>
- Cohere Embed v3: <https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-embed-v3.html>

Bedrock is the only provider carrying two incompatible embedding envelopes behind one name.
`DetermineEmbeddingModelType` picks between them by substring match on the model id, so the same
`/v1/embeddings` request behaves differently depending on whether the id contains `titan` or
`cohere`. Native InvokeModel embeddings are available through `/bedrock/model/{id}/invoke`,
with `/langchain/model/{id}/invoke` providing the same route plus LangChain compatibility.
Normalized embeddings remain available through `/v1/embeddings`, `/openai/v1/embeddings`,
`/genai/.../:embedContent` and `/cohere/v2/embed`.

- [x] **Titan V2 baseline** (`inputText`, one vector out, `inputTextTokenCount` to usage) - folder 53.A1
- [x] **Titan `dimensions`** (1024 default | 512 | 256) - folder 53.A2
- [x] **Titan array-input rejection** (no batch shape; the array used to be joined into one `inputText`, now a 400; #3471) - folder 53.A3
- [x] **Titan `normalize`** (default true; proven via L2 norm of the returned vector) - folder 53.A4 / 53.A5
- [x] **Titan `embeddingTypes`** (camelCase; `embeddingsByType` recovered through `x-bf-send-back-raw-response`) - folder 53.A6
- [x] **Titan native InvokeModel typed envelopes** (`binary` alone and `float` + `binary`) — folder 58.A / 58.B (PR #6335)
- [x] **Cohere v4 `input_type`** (required by AWS; both the native `/cohere/v2/embed` route and `extra_params`) - folder 53.B1 / 53.B4
- [x] **Cohere v4 `embedding_types`** (`embeddings_by_type` int8 parse branch) - folder 53.B2
- [x] **Cohere v4 native InvokeModel typed envelope** (`float`, `int8`, `uint8`, `binary`, `ubinary`) — folder 58.C (PR #6335)
- [x] **LangChain Cohere singular `embedding` alias** while preserving native plural `embeddings` — folder 58.D (PR #6335)
- [x] **Normalized Titan dual representations without raw-response leakage** — folder 58.E (PR #6335)
- [x] **Cohere v4 array input** (one vector per text, the arity divergence against Titan) - folder 53.B5
- [x] **Cohere v4 `output_dimension`** (256 | 512 | 1024 | 1536) - folder 53.B6 / 53.D4
- [x] **Usage backfill from `X-Amzn-Bedrock-Input-Token-Count`** (Cohere embed omits usage from the body; #3917) - folder 53.B7
- [x] **Titan G1** (`amazon.titan-embed-text-v1`) - `inputText` only, no `dimensions`/`normalize`; sending either is expected to be rejected — gated: folder 152.9a/b [PREVIEW] (model may not be enabled in the account)
- [x] **Titan multimodal** (`amazon.titan-embed-image-v1`) - `inputImage` now mapped from image content parts and from the native invoke body, plus `embeddingConfig.outputEmbeddingLength` (#3632) - folder 112.A / 112.B
- [x] **Nova multimodal** (`amazon.nova-2-multimodal-embeddings-v1:0`) - the third Bedrock embedding envelope: `taskType: SINGLE_EMBEDDING` + one modality, per-vector modality labels, `detailLevel`, and `AUDIO_VIDEO_SEPARATE` returning two vectors for one input - folder 114.A / 114.B
- [x] **Cohere v4 multimodal** (`images` data-URI array, `inputs` interleaved text+image blocks) - `ToBedrockCohereEmbeddingRequest` now builds `inputs[]` from multimodal content parts - folder 111.F1
- [x] **Cohere v3** (`cohere.embed-english-v3`) - fixed 1024 dims, `truncate` is `NONE|START|END` on v3 versus `NONE|LEFT|RIGHT` on v4, so the shared converter cannot validate the enum — built: folder 152.10a (truncate END, 1024 dims) / 152.10b [EXPECT-4XX] (v4 value RIGHT)
- [x] **`truncate` / `max_tokens` passthrough** to Cohere on Bedrock — built: folder 150.5 (RIGHT + max_tokens) / 150.6 (v3 value END as [EXPECT-4XX])
- [x] **Drop-in routes with parameters** - §8.3.I/§8.3.J send a bare single string to `/openai/v1/embeddings` and `:embedContent`; neither carries `dimensions`, `encoding_format` or any extra param — gated: folder 146.6 covers `dimensions` on /openai/v1/embeddings; `:embedContent` and `encoding_format` still bare

### Other Bedrock surfaces

- [x] **Application inference profiles** (custom profiles created via API) — built: folder 155.7 [PREVIEW] (set bedrockAppInferenceProfileArn, URL-encoded)
- [x] **Bedrock Agents** (`POST /agents/{agentId}/invokeAgent`) — fixed: new /bedrock_passthrough (core/providers/bedrock/passthrough.go) forwards only this operation, SigV4-signed, to the key's region host; unit tests in passthrough_test.go (red against the old unsupported stub, green after) and folder 162.1 against a local TLS fixture with an HTTPS_PROXY tripwire (green): signed, forwarded to bedrock-agent-runtime, event stream returned untouched. The real AWS path is POST /agents/{id}/agentAliases/{alias}/sessions/{session}/text
- [x] **Bedrock Knowledge Bases** (`POST /knowledgebases/{id}/retrieve`) — fixed: new /bedrock_passthrough (core/providers/bedrock/passthrough.go) forwards only this operation, SigV4-signed, to the key's region host; unit tests in passthrough_test.go (red against the old unsupported stub, green after) and folder 162.2 against the local fixture (green): signed, forwarded to bedrock-agent-runtime, retrievalResults returned verbatim
- [x] **Bedrock Guardrails** (`POST /guardrails/{id}/apply`) — fixed: new /bedrock_passthrough (core/providers/bedrock/passthrough.go) forwards only this operation to the key's region host, using the key's Bedrock API key as a bearer token when it holds only that, or SigV4 with its AWS credentials; unit tests in passthrough_test.go (red against the old unsupported stub, green after) and folder 162.3 against the local fixture (green): SigV4-signed, forwarded to bedrock-runtime. Folder 162.11 covers the bearer-token path. The real AWS path is POST /guardrail/{id}/version/{version}/apply
- [x] **Provisioned throughput** model invocation — built: folder 155.6 [PREVIEW] (set bedrockProvisionedModelArn, URL-encoded)
- [x] **Bedrock-mantle endpoint** for Anthropic Messages API on Bedrock (newer surface, alongside bedrock-runtime) — audited: covered

---

## Google Gemini (AI Studio)

Sources:
- generateContent API: <https://ai.google.dev/api/generate-content>
- Models: <https://ai.google.dev/gemini-api/docs/models>
- Function calling: <https://ai.google.dev/gemini-api/docs/function-calling>
- Caching: <https://ai.google.dev/gemini-api/docs/caching>

### generateContent (`POST /v1beta/models/{model}:generateContent`)

- [x] Basic content (`contents: [{ parts: [{ text }] }]`)
- [x] System instruction (`systemInstruction: { parts: [{ text }] }`)
- [x] Multi-turn (`role: "user" | "model"` in contents)
- [x] Function calling (`tools: [{ functionDeclarations: [...] }]`)
- [x] Google search grounding (`tools: [{ googleSearch: {} }]`)
- [x] Code execution (`tools: [{ codeExecution: {} }]`)
- [x] File data (`parts: [{ fileData: { mimeType, fileUri } }]`)
- [x] Inline data (vision base64)
- [x] Safety settings (`safetySettings: [{ category, threshold }]`)
- [x] Structured output via responseSchema (`generationConfig: { responseMimeType, responseSchema }`)
- [x] Thinking budget (`generationConfig: { thinkingConfig: { thinkingBudget } }`)
- [x] Streaming (`POST /v1beta/models/{model}:streamGenerateContent?alt=sse`)
- [x] **Tool config** (`toolConfig: { functionCallingConfig: { mode: AUTO / ANY / NONE, allowedFunctionNames } }`) — built: folder 147.1/147.2/147.3
- [x] **Stop sequences** (`generationConfig: { stopSequences }`) — built: folder 152.11 (asserts truncation and finishReason STOP)
- [x] **Temperature / topP / topK / candidateCount** (`generationConfig`) — built: folder 147.7 (candidateCount) + 156.1-156.3 [EXPECT-4XX] out-of-range values
- [x] **Max output tokens** (`generationConfig: { maxOutputTokens }`) — built: folder 147.4/147.5
- [x] **Response logprobs** (`generationConfig: { responseLogprobs: true, logprobs: N }`) — built: folder 157.2 [EXPECT-4XX] out-of-range logprobs proves the field reaches Google; 157.3 [PREVIEW] is the positive case (model support is not confirmable from the docs)
- [x] **Presence/frequency penalty** (`generationConfig: { presencePenalty, frequencyPenalty }`) — built: folder 152.12 (penalties on the Gemini wire via raw_request)
- [x] **Cached content** (`cachedContent: "cachedContents/abc123"`) — built: folder 151.5 (asserts cachedContentTokenCount>0)
- [x] **PDF input** (`fileData` with `application/pdf` mime) — audited: covered (weak: status/shape assertions only)
- [x] **Audio input** (audio/mp3, audio/wav file data) — audited: covered (weak: status/shape assertions only)
- [x] **Video input** (video/mp4 file data) — built: folder 147.9
- [x] **YouTube URL input** (`fileData: { fileUri: "https://www.youtube.com/watch?v=..." }`) — audited: covered (weak: status/shape assertions only)
- [x] **Code execution outputs** (`executableCode`, `codeExecutionResult` parts) — built: folder 152.13 (executableCode + codeExecutionResult OUTCOME_OK, F(50))
- [x] **Search grounding with retrieval config** (`tools: [{ googleSearchRetrieval: { dynamicRetrievalConfig } }]`) — built: folder 151.10 [EXPECT-4XX] live pin
- [x] **URL context** (`tools: [{ urlContext: {} }]`) — built: folder 152.14 (urlContextMetadata SUCCESS)
- [~] **Live API** (websocket-based bidirectional streaming) — blocked: not a Bifrost feature: Bifrost's realtime websocket gateway (/v1/realtime) is implemented for openai, azure and elevenlabs only, and nothing in core or transports speaks Gemini BidiGenerateContent, so there is no surface to test. Covering it means building a Gemini realtime provider, which is product work, not harness coverage; newman also cannot drive a websocket
- [x] **Function responses** (`role: "function"` parts with `functionResponse`) — built: folder 147.6
- [x] **Thinking response signature** (return `thoughtSignature` to continue thinking across turns): folder `49.` replays a captured server-side tool turn (toolCall/toolResponse + thoughtSignature) in `contents`; Gemini validates signatures upstream, so a 2xx pins the round-trip

### Other endpoints

- [x] **Count tokens** (`POST /v1beta/models/{model}:countTokens`) — audited: covered (weak: status/shape assertions only)
- [x] **Embed content** (`POST /v1beta/models/{model}:embedContent`) - §8.3.J posts the native shape at the drop-in route; folder 53 covers the parameter surface via `/v1/embeddings` (arity 53.C2, `outputDimensionality` 53.D2, `encoding_format` ignored 53.E4). Native `:embedContent` carrying `taskType`/`title`/`outputDimensionality` in the Gemini body is still uncovered. — built: folder 147.8 (native :embedContent with taskType/title/outputDimensionality)
- [x] **Batch embed** (`POST /v1beta/models/{model}:batchEmbedContents`) — audited: covered
- [x] **Cached content CRUD** (`POST /v1beta/cachedContents`, list, get, update, delete): typed lifecycle implemented for both Gemini and Vertex; harness `Gemini: list cached contents` runs against real upstream (list only; create/retrieve/update/delete not yet exercised) — built: folder 151.1-151.6 (create, get, patch ttl, list, generate with cachedContent, delete)
- [x] **Files API** (`POST /v1beta/files` upload, list, get, delete) — built: folder 153.1a-d (resumable initiate, finalize, get, delete)
- [x] **Models list** (`GET /v1beta/models`) — audited: covered (weak: status/shape assertions only)
- [x] **Tuned models** (`POST /v1beta/tunedModels`) — built: folder 158.1 via /genai_passthrough, which forwards arbitrary paths to Google; Bifrost models no native tunedModels route, and creating a tuning job is paid and not exercised
- [x] **Operations** (`GET /v1beta/operations/{name}` for long-running ops) — built: folder 158.2 [EXPECT-4XX] via /genai_passthrough (an unknown name returns Google's error envelope); the video-operation routes Bifrost does model are covered in 101

---

## Google Vertex AI

Sources:
- Generative AI on Vertex: <https://cloud.google.com/vertex-ai/generative-ai/docs>
- Anthropic on Vertex: <https://platform.claude.com/docs/en/api/claude-on-vertex-ai>
- Model Garden: <https://cloud.google.com/model-garden>

Vertex's API surface for Gemini largely mirrors AI Studio's generateContent — see the Gemini section above. Vertex-specific features are listed here.

### Gemini-on-Vertex specific

- [x] Basic generateContent (Gemini 2.5 family in supported region)
- [x] Function calling
- [x] Web search grounding
- [x] Structured output (responseSchema)
- [x] **Vertex AI Search grounding** (`tools: [{ retrieval: { vertexAiSearch: { datastore } } }]`) — built: folder 151.11 [EXPECT-4XX] + 155.10 [PREVIEW] real datastore (set vertexAiSearchDatastore)
- [x] **Vertex AI Search dynamic** (`tools: [{ retrieval: { dynamicRetrievalConfig } }]`) — built: folder 151.10 (legacy googleSearchRetrieval) + 156.4 [EXPECT-4XX] (retrieval.dynamicRetrievalConfig); both are live pins confirmed on the first run
- [x] **Custom search grounding via RAG corpora** (`vertexRagStore`) — built: folder 151.12 [EXPECT-4XX] + 155.11 [PREVIEW] real corpus (set vertexRagCorpus)
- [x] **Long-context caching** (`cachedContent` Vertex variant) — gated: folder 151.7-151.9 [PREVIEW] (needs a real vertexProject; resource-name model form is best-effort until a live run)
- [x] **Global / multi-region / regional endpoints** (`region: "global" | "us" | "eu" | "us-east5"`) — built: folder 161.1-161.5, verified against a local TLS-intercepting proxy fixture (green): global, us/eu multi-region, us-east5, and a regional key that stays single-region for Gemini; each asserts the host and the locations path segment
- [x] **Provisioned Throughput** (`x-goog-spend-limit-id` header) — built: folder 161.10-161.12, verified against the local fixture (green): Bifrost sends spend-limit ids through extra-header forwarding (x-bf-eh-x-goog-spend-limit-id -> x-goog-spend-limit-id), and its service-tier analog sends X-Vertex-AI-LLM-Shared-Request-Type on the global endpoint only
- [x] **Request response logging** (Vertex-side, not API-visible) — exempt: Vertex-side logging is not API-visible, so no harness case applies
- [x] **Imagen image generation** (`POST /publishers/google/models/imagen-3.0-generate-002:predict`) — gated: folder 147.10 [PREVIEW] (+147.11 gemini/imagen-4)
- [x] **Veo video generation** (`POST /publishers/google/models/veo-001:predict`) — built: folder 155.4 [PREVIEW] create only (paid, long-running)

### Anthropic-on-Vertex specific

- [x] Claude Opus 4.7 in user's region (`global` / `us-east5` / `europe-west1`)
- [x] **Claude Sonnet 4.6 / 4.5 / Haiku 4.5** (regional gating - must use `global` or `us-east5`; Sonnet 4.6 cross-cut variants added in Cross-Cut Round 4 covering structured output, function calling, streaming, vision, tool_choice, stop sequences, multi-turn, system message, web search, PDF, sampling-params; Haiku 4.5 + Sonnet 4.5 still uncovered) — gated: folder 147.12 (haiku-4-5) / 147.13 (sonnet-4-5), both [PREVIEW] for regional quota
- [x] **`anthropic_version: "vertex-2023-10-16"` in body** (Vertex-specific replacement for the header) — folder 62 (PR #6639), `[PREVIEW]` rows
- [x] **Vertex `:streamRawPredict` endpoint** for SSE streaming — folder 62 (PR #6639): terminates on `message_stop`, usage via the Anthropic parser (`[PREVIEW]`)
- [x] **Beta headers via body field** (`anthropic_beta` instead of HTTP header) — built: re-scoped: a client-sent anthropic_beta body field is not an ingress Bifrost parses; the real behaviour is Bifrost moving the anthropic-beta header into the Vertex body, pinned by 12.B8
- [x] **Anthropic on multi-region endpoints** (`https://aiplatform.us.rep.googleapis.com`, `eu.rep`) — built: folder 161.6-161.9, verified against the local fixture (green): Claude on a us key, promoted to aiplatform.us.rep from an unpinned us-central1 key, kept on us-central1 with force_single_region, and the OAuth token path

### Vertex Model Garden (3rd-party publishers)

- [x] **Llama on Vertex** (`publishers/meta/models/llama-4-maverick-17b-128e-instruct-maas:predict`) — audited: covered (weak: status/shape assertions only)
- [x] **Mistral on Vertex** (`publishers/mistralai/models/mistral-large-2411`) — audited: covered (weak: status/shape assertions only)
- [x] **DeepSeek on Vertex** — gated: folder 150.7 [PREVIEW] (exact MaaS model id unpinned until a live run)
- [x] **Gemma on Vertex** (`publishers/google/models/gemma-3-27b-it`) — built: folder 155.15 [PREVIEW] (set vertexGemmaModel to your self-deployed endpoint)

---

## Azure OpenAI

Sources:
- REST API: <https://learn.microsoft.com/en-us/azure/ai-services/openai/reference>
- Latest API version index: <https://learn.microsoft.com/en-us/azure/ai-services/openai/api-version-deprecation>

### Deployment-style chat completions

- [x] Basic chat (`POST /openai/deployments/{deployment}/chat/completions?api-version=...`)
- [x] **Azure Chat Completions w/ tools** (function calling, parallel tool calls) — built: folder 148.1
- [x] **Azure Chat Completions w/ vision** (`gpt-4o` deployment) — audited: covered (weak: status/shape assertions only)
- [x] **Azure Chat Completions w/ structured output** (json_schema) — audited: covered (weak: status/shape assertions only)
- [x] **Azure Chat Completions w/ streaming** — audited: covered (weak: status/shape assertions only)
- [x] **Azure Chat Completions w/ system message** — audited: covered (weak: status/shape assertions only)
- [x] **Azure-specific data sources / On Your Data** (`data_sources: [{ type: "azure_search", parameters: {...} }]`) — built: folder 148.6 [EXPECT-4XX] probe + 155.12 [PREVIEW] real index (set azureSearch*)
- [x] **Azure content filters** (`content_filter_results` in response) — fixed: core/schemas/chatcompletions.go now models prompt_filter_results and per-choice content_filter_results (additive, omitempty); unit test TestChatResponsePreservesAzureContentFilterAnnotations (red before, green after) and folder 159, verified against the local fixture (red measured before the fix, green after)
- [x] **Reasoning effort on Azure** (o1 / o3 deployments) — audited: covered (weak: status/shape assertions only)
- [x] **Audio input on Azure** (gpt-4o-audio-preview deployment) — built: folder 155.13 [PREVIEW]

### Azure Responses API (preview)

- [x] **Responses on Azure** (`POST /openai/deployments/{deployment}/responses?api-version=2025-04-01-preview`) — built: folder 148.5
- [x] **Web search on Azure Responses** (`web_search_preview`) — gated: folder 150.4 [PREVIEW] (needs an Azure web-search deployment)
- [x] **Code interpreter on Azure Responses** — audited: covered (weak: status/shape assertions only)

### Other Azure endpoints

- [x] **Azure embeddings** (`POST /openai/deployments/{deployment}/embeddings?api-version=...`) — built: folder 148.2
- [x] **Azure DALL-E** (`POST /openai/deployments/{deployment}/images/generations`) — built: folder 148.3
- [x] **Azure Whisper** (`POST /openai/deployments/{deployment}/audio/transcriptions`) — built: folder 152.15
- [x] **Azure TTS** (`POST /openai/deployments/{deployment}/audio/speech`) — built: folder 148.4
- [x] **Azure files / fine-tuning** (admin surface) — built: files in 47.10/62/101; fine-tuning admin list via /azure_passthrough in folder 158.3 (Bifrost has no native fine-tuning route; creating a job is paid and not exercised)
- [x] **Azure Batch API** — built: folder 155.14 (list) + 157.4a/b [PREVIEW] (inline-request create + cancel; set azureBatchDeployment to a Global Batch deployment)

---

## Cross-cutting (Bifrost-specific)

These exercise Bifrost's translation layer between provider shapes — every check below uses the unified
`POST /v1/chat/completions` endpoint with `provider/model` prefix routing.

- [x] OpenAI / Anthropic / Bedrock / Gemini / Vertex Basic Chat (50 cross-model entries)
- [x] Function calling cross-cut (OpenAI + Anthropic + Bedrock + Gemini + Vertex Claude + Vertex Gemini via Cross-Cut Round 4; Azure via Cross-Cut Round 4)
- [x] Structured output cross-cut (OpenAI + Anthropic + Bedrock + Gemini + Vertex Gemini + Vertex Claude via Cross-Cut Round 4; Azure via Cross-Cut Round 4)
- [x] Streaming cross-cut (OpenAI + Anthropic + Bedrock + Gemini + Vertex Claude + Vertex Gemini + Azure via Cross-Cut Round 4)
- [x] Vision cross-cut (OpenAI + Anthropic + Bedrock + Gemini + Vertex Gemini + Vertex Claude + Azure via Cross-Cut Round 4)
- [x] Web search cross-cut (Anthropic + Bedrock + Vertex Claude (sonnet) + Vertex Gemini (google_search) via Cross-Cut Round 4) — built: folder 149.1 (openai/gpt-4o-mini-search-preview url_citation annotations)
- [x] **Code execution cross-cut** (Anthropic + Gemini + Bedrock + Vertex Claude (opus)) — built: folder 149.2 (vertex/gemini-2.5-flash)
- [x] **Tool choice forced cross-cut** (OpenAI + Bedrock + Vertex Claude via Cross-Cut Round 4; Anthropic, Gemini and Azure via the folder 12 cases `Cross-cut: anthropic/claude-opus-4-7 tool_choice forced`, `Cross-cut: gemini/gemini-2.5-pro tool_choice forced` and `Cross-cut: azure/{{azureDeployment}} tool_choice forced`) — audited: covered
- [x] **Computer use via cross-model** (`anthropic/claude-...` with computer_2025x tools - verifies Bifrost's translation; currently only tested via /anthropic drop-in and `vertex/claude-opus-4-7` preview at L1279) — built: folder 153.3 via /v1/responses computer_use_preview (/v1/chat tools only support function and custom, so a chat-shaped case cannot exist)
- [x] **Extended/adaptive thinking via cross-model** (Anthropic enabled + Bedrock enabled/adaptive + Vertex Claude enabled/adaptive covered; anthropic-direct adaptive Opus 4.7 via the folder 12 case `Cross-cut: anthropic/claude-opus-4-7 adaptive thinking`) — audited: covered (weak: status/shape assertions only)
- [x] **OpenAI Responses reasoning item id/encrypted_content round-trip via Anthropic drop-in** (`/anthropic/v1/messages` → openai/gpt-5: turn-1 `redacted_thinking` block replayed on turn 2 without OpenAI's item-id mismatch 400 — pins #5186; folder 38)
- [x] **Reasoning/thinking multi-turn replay across the criss-cross matrix** (folder 39: OpenAI-shaped request → Anthropic model reverse direction, plus native-chat Anthropic-origin `reasoning_details` replay per #4943 previously uncovered by the harness; Gemini `thoughtSignature` replay (distinct smuggling mechanism from the OpenAI/Anthropic encrypted_content envelope, #5186) has its own folders 43 and 125, and Azure-hosted reasoning models are covered by folder 125 (`azure/gpt-5.6-luna`) and the `[PREVIEW]` `azure/gpt-5.4-mini` cases in folders 47 and 48) — audited: covered
- [x] **Reasoning-signature replay onto a model that refuses the field** (folder 60: native `/v1/chat/completions` → `bedrock/moonshotai.kimi-k2.5` with a foreign `reasoning_details[].signature`. Bedrock answers "This model doesn't support the reasoningContent.reasoningText.signature field" — a *field-not-accepted* refusal, not the *payload-unverifiable* wording folder 44 pins, so `isEncryptedReasoningRejection` missed it and the 400 reached the client. Also the first Bedrock and first chat-shape coverage of the fail-soft: a different carrier (`reasoning_details[].signature`) and a different strip (`stripChatUnverifiableReasoning`) than folder 44's `encrypted_content`/`stripResponsesEncryptedContent`.)
- [x] **Encrypted reasoning replayed across a provider switch** (folder 120, feature slug `encrypted reasoning provider switch`: 15 producer/consumer pairs on `/v1/responses` where a real token minted on one provider is replayed, unary and streaming, on another provider hosting the same family. GPT: bedrock ↔ bedrock_mantle, openai ↔ azure, azure → bedrock_mantle, openai → bedrock. Claude: anthropic ↔ vertex, anthropic → bedrock, bedrock → bedrock_mantle, bedrock_mantle → anthropic. Gemini: gemini ↔ vertex with a forced tool call. Grok: xai → bedrock. Plus one cross-family openai → anthropic. Pins the status-plus-family-word fail-soft gate (`shouldStripReasoningAfterClientError`) that replaced the per-provider wording matcher after Mantle's "invalid encrypted reasoning" reached Codex unhealed; folder 115 covers only a model switch within one provider.)
- [x] **Bedrock Converse reasoning wire shape per model family** (folder 67: `bedrock/us.openai.gpt-5.6-luna` on `/v1/chat/completions` and `bedrock/us.xai.grok-4.6` on `/v1/responses`, capture+replay pairs. OpenAI and xAI on Converse return `reasoningContent.redactedContent`, an opaque blob, where Anthropic and DeepSeek return `reasoningContent.reasoningText{text,signature}`; Bifrost modelled only the latter, dropped the blob on ingress and re-emitted an empty unsigned `reasoningText` on replay, which Converse answers with an opaque 500 "The system encountered an unexpected error during processing." Only reachable on turn 2. The `us.` prefix is load-bearing — a bare id routes to bedrock-mantle and never reaches Converse, which is why the pre-existing `bedrock/openai.gpt-5.6-sol` rows could not catch this. The reasoningText half stays covered for Anthropic-on-Bedrock by folder 46.)
- [x] **Native `/v1/chat/completions` `reasoning_details` id-loss for OpenAI-origin encrypted reasoning** (same bug *class* as #5186 but a separate code path: `core/schemas/mux.go` `ToChatMessages` never populates `ChatReasoningDetails.ID` on egress, and `ToResponsesMessages` mints a fresh `rs_` id on replay regardless, unaffected by the `/anthropic` surface fix. No tracked issue yet — file one before adding a harness case; a currently-red case with no owner/fix-in-flight breaks this collection's regression-pin convention.) — fixed: core/schemas/mux.go now carries the reasoning item id on every reasoning_details entry and replays it (the encrypted detail's id wins when items merge); unit test TestReasoningItemIDSurvivesChatRoundTrip (red before, green after) and folder 160 against the local fixture: pre-fix the fixture counted a replay-id-mismatch and the gateway had to retry, post-fix it counts replay-id-ok with no retry
- [x] **Responses->Chat `finish_reason` derivation on the compat chat->responses path** (#6831, folder 71, feature slug `finish-reason-derivation`: `bedrock_mantle/openai.gpt-5.6-luna` on `/v1/chat/completions` for stop / tool_calls / length plus the streaming terminal chunk. `ToBifrostChatResponse` never set `FinishReason` and the field has no `omitempty`, so every converted non-streaming reply serialized `"finish_reason": null`, breaking agent loops that terminate on `tool_calls`. Every row pins `extra_fields.converted_request_type == 'responses'` first: `markForConversion` is datasheet-driven and `x-bf-compat` only *enables* the check, so on a chat-capable model nothing converts and the row would pass vacuously - which is exactly how folder 19 sat green on `openai/gpt-4o-mini` until this was caught. Folder 19 rows 1-2 were moved onto the same converting model as part of #6831, and that folder gained the `chat-responses-tool-replay` slug.)
- [x] **Prompt caching via cross-model** (Anthropic + Bedrock 1h + Vertex Claude 1h covered)
- [x] **System message cross-cut** (Vertex Claude added in Round 4; Azure added in Round 4; **other providers were already implicit via cross-cut entries** - if explicit test needed, file a ticket) — audited: covered
- [x] **Multi-turn conversation cross-cut** (Vertex Claude added in Round 4; remaining providers still cross-cut-implicit only) — audited: covered
- [x] **Stop sequences cross-cut** (OpenAI + Anthropic + Gemini already; + Bedrock + Vertex Claude + Vertex Gemini added in Cross-Cut Round 4)
- [x] **Sampling-params normalization** (Bifrost should silently drop temperature for Opus 4.7+; Anthropic-direct + Vertex Claude Opus 4.7 covered) — built: folder 146.5 (Bedrock Opus 4.7 Converse body carries no temperature/topP)
- [x] **MCP tool stripping for non-MCP providers** (Bifrost silently drops provider-side `type:"mcp"` server tools from a Responses request for Bedrock + Vertex instead of erroring; function tools — how local/configured MCP servers surface — survive — regression #3795. Folder "11. Cross-Provider Feature Tests / MCP Tool Handling cross-cut": 16-item matrix over {opus, sonnet} × {lone-mcp, mcp+function, multi-tool #3795 shape} plus /openai drop-in and streaming axes)
- [x] **Failover scenarios** (request to provider X falls back to provider Y on 5xx) — built: folder 154.5/154.6/154.7 against a local fixture returning a real HTTP 503 and 429 (hit counts prove one attempt per model); a real provider outage is not reproducible
- [x] **Virtual keys / governance** (`X-Bifrost-VK` header with allowed_models) - covered by `bifrost-v1-vk-quota` (quota endpoint contract), `bifrost-v1-rate-limit` (429 request/token limits, 402 budget), and `bifrost-v1-vk-rotation-cooldown` (rotation grace windows at 1m/3m); allowed_models enforcement in `bifrost-v1-vk-expiry` and `bifrost-routing-wiring`; `regex:` entries in `allowed_models` / `blacklisted_models` on VKs and `models` / `blacklisted_models` on provider keys in the `Governance - VK Regex Entries` folder of `bifrost-api-management` (400 on an unparsable pattern or `regex:*`, list-models filtering with no pattern surfaced as a model, 200 for a matched model, 403 for models outside the allow regex or matching the block regex, a PUT mixing an exact name with a regex entry, and a provider-key round-trip)
- [x] **Rate limit propagation** (provider 429 → Bifrost 429 with the provider's retry hint preserved in `extra_fields.retry_after_ms`) — built: folder 154.3/154.4 against the local fixture: the provider hint reaches the client as extra_fields.retry_after_ms (7000 from Retry-After: 7, 2500 from retry-after-ms); Bifrost does not emit a Retry-After HTTP header

---

## Passthrough surface (Bifrost catch-all forwarding)

Currently only Basic Chat is exercised through any `*_passthrough/*` route. Every advanced feature
should be re-tested through passthrough since the translation layer is bypassed.

- [x] OpenAI passthrough chat completions
- [x] OpenAI passthrough responses
- [x] Anthropic passthrough messages
- [x] Azure passthrough deployment chat
- [x] GenAI passthrough generateContent
- [x] **OpenAI passthrough w/ web_search** — audited: covered (weak: status/shape assertions only)
- [x] **OpenAI passthrough w/ code_interpreter** — audited: covered (weak: status/shape assertions only)
- [x] **Anthropic passthrough w/ computer_use** (verify auth header strip + beta header injection) — built: folder 153.2, re-scoped: passthrough forwards the client beta header and never injects one, so the case pins forwarding plus credential stripping
- [x] **Anthropic passthrough w/ extended thinking** (folder 51 sends `thinking: { type: "enabled", budget_tokens }` through `/anthropic/v1/messages` and pins the encrypted-reasoning fail-soft's raw rewrite against Anthropic's "latest assistant message cannot be modified" rule — PR #6110; a fresh non-replay request asserting a `thinking` block comes back through passthrough, and the streaming variant, are still open) — audited: partial, Add script to the existing case asserting content[] has type "thinking" with non-empty thinking+signature — built: folder 148.13 (fresh thinking block with signature) / 148.14 (streaming twin)
- [x] **Anthropic passthrough w/ prompt caching** — built: folder 148.12a/b
- [x] **Anthropic passthrough w/ web_search_20260209** — built: folder 148.11
- [x] **GenAI passthrough w/ googleSearch** — audited: covered (weak: status/shape assertions only)
- [x] **GenAI passthrough w/ codeExecution** — audited: covered (weak: status/shape assertions only)
- [x] **Azure passthrough w/ tools** — audited: covered (weak: status/shape assertions only)
- [x] **Streaming through any passthrough route** — audited: covered (weak: status/shape assertions only)
- [x] **Vision through any passthrough route** — audited: covered
- [x] **All passthrough routes with disallowed auth headers** (verify they're stripped, not forwarded) — built: folder 148.7-148.10 (openai, anthropic, azure, genai)

---

## Priority order for backlog burn-down

1. **Cross-model feature variants** — biggest gap, highest leverage; ~20 new requests close most of the matrix
2. **Anthropic beta headers + new-gen tools** — lots of low-hanging features Bifrost claims to support
3. **Bedrock streaming + Bedrock vision** — Converse API has both but harness has neither
4. **Passthrough advanced features** — proves the byte-for-byte forwarding handles complex bodies
5. **Azure beyond Basic Chat** — Azure has the worst coverage; even one tools/streaming test would help
6. **OpenAI Responses API server tools** — file_search needs setup, but computer_use_preview, mcp, image_generation are testable
7. **Vertex Anthropic global routing** — once `GOOGLE_LOCATION=global` is set, all 4-5 deferred Claude variants come back
