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
- [ ] **Tool choice: specific function** (`tool_choice: { type: "function", function: { name: "x" } }`)
- [ ] **Parallel tool calls** (`parallel_tool_calls: true/false`)
- [ ] **Response format JSON object** (`response_format: { type: "json_object" }`)
- [ ] **Logprobs** (`logprobs: true, top_logprobs: N`)
- [ ] **Logit bias** (`logit_bias: { token_id: bias }`)
- [ ] **Seed for deterministic output** (`seed: 12345`)
- [ ] **Stop sequences** (`stop: ["END"]`)
- [ ] **N completions** (`n: 3`)
- [ ] **Temperature / top_p / frequency_penalty / presence_penalty**
- [ ] **Stream options with usage** (`stream_options: { include_usage: true }`)
- [ ] **Service tier** (`service_tier: "scale" | "default" | "priority"`)
- [ ] **Audio input** (`input_audio` content block, gpt-4o-audio-preview)
- [ ] **Audio output** (`modalities: ["text","audio"], audio: {voice, format}`)
- [ ] **Web search options** (`web_search_options` for chat-completions web search)
- [ ] **Predicted outputs** (`prediction: { type: "content", content: "..." }`)
- [ ] **Store + metadata for evals** (`store: true, metadata: {...}`)

### Responses API (`POST /v1/responses`)

- [x] Basic input (`input` string)
- [x] Web search (`tools: [{ type: "web_search_preview" }]`)
- [x] Code interpreter (`tools: [{ type: "code_interpreter", container: { type: "auto" }}]`)
- [ ] **File search** (dropped earlier; needs vector_store setup) — `tools: [{ type: "file_search", vector_store_ids: [...] }]`
- [ ] **Computer use preview** (`tools: [{ type: "computer_use_preview", display_width, display_height, environment }]`)
- [ ] **MCP tool** (`tools: [{ type: "mcp", server_label, server_url }]`)
- [ ] **Image generation** (`tools: [{ type: "image_generation" }]` requires gpt-image-1 access)
- [ ] **Reasoning summary** (`reasoning: { summary: "auto" }` for o3/gpt-5)
- [ ] **Background mode** (`background: true`) — async execution
- [ ] **Truncation strategy** (`truncation: "auto"`)
- [ ] **Tool choice for Responses API** (`tool_choice: { type: "file_search" }` etc.)
- [ ] **Conversation continuation** (`previous_response_id: "..."`)
- [ ] **Input as messages array** (Responses also accepts messages-shape input)
- [ ] **Stream events** (Responses streams structured event types: response.output_item.added, etc.)
- [ ] **Multimodal input items** (text + image_url + input_file in one request)
- [ ] **PDF input** (`input_file` with PDF data)
- [ ] **include array** (`include: ["file_search_call.results", "message.input_image.image_url"]`)
- [ ] **Custom tool** (`tools: [{ type: "custom", name, description, input_schema }]`)
- [ ] **Token counting endpoint** (`POST /v1/responses/input_tokens`)

### Other endpoints

- [ ] **Embeddings** (`POST /v1/embeddings`)
- [ ] **Audio speech (TTS)** (`POST /v1/audio/speech`)
- [ ] **Audio transcription** (`POST /v1/audio/transcriptions`)
- [ ] **Image generation** (`POST /v1/images/generations`)
- [ ] **Image edit** (`POST /v1/images/edits`)
- [ ] **Image variation** (`POST /v1/images/variations`)
- [ ] **Batch API** (`POST /v1/batches` + `GET /v1/batches/{id}`)
- [ ] **Files API** (`POST /v1/files`, etc.)
- [ ] **Models list** (`GET /v1/models`)
- [ ] **Containers API** (`POST /v1/containers` for code-interpreter sandboxes)
- [ ] **Videos API** (`POST /v1/videos` for Sora)
- [ ] **Rerank** (`POST /v1/rerank`)

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
- [ ] **Prompt caching persistent / 1-hour** (`cache_control: { type: "ephemeral", ttl: "1h" }`)
- [ ] **Web fetch tool** (`web_fetch_20250910`, `web_fetch_20260209`, `web_fetch_20260309`)
- [ ] **Memory tool** (`memory_20250818`)
- [ ] **Tool search** (`tool_search_tool_bm25`, `tool_search_tool_regex`)
- [ ] **MCP toolset** (`mcp_toolset` server reference)
- [ ] **Code execution v2** (`code_execution_20250825`)
- [ ] **Code execution programmatic** (`code_execution_20260120`)
- [ ] **Computer use new-gen** (`computer_20251124` + `text_editor_20250728` + `bash_20250124` for Opus 4.7/4.6/Sonnet 4.6)
- [ ] **PDF input** (`{ type: "document", source: { type: "base64", media_type: "application/pdf" } }`)
- [ ] **Citations** (`citations: { enabled: true }` on document blocks)
- [ ] **Stop sequences** (`stop_sequences: ["END"]`)
- [ ] **Sampling** — `temperature` / `top_p` / `top_k` (deprecated for Opus 4.7+ — should NOT be sent)
- [ ] **Service tier** (`service_tier: "auto" | "standard_only"`)
- [ ] **Effort** (`output_config: { effort: "low" | "medium" | "high" | "max" }` for Opus 4.5/4.6)
- [ ] **Format / structured output** (`output_config: { format: { type: "json_schema", schema: {...} } }`)
- [ ] **Defer loading** (`defer_loading: true` on tools)
- [ ] **Allowed callers** (`allowed_callers: [...]` on tools)
- [ ] **Eager input streaming** (`eager_input_streaming: true` on tools; beta)
- [ ] **Strict tool input** (`strict: true` for structured-outputs validation)
- [ ] **Tool input examples** (`input_examples: [{ input, description }]`)
- [ ] **Skills + container** (advanced-tool-use bundle)
- [ ] **Stream parameters** — `stream_options` (Anthropic's variant)
- [ ] **Beta header explicit list** (`betas: ["computer-use-2025-11-24", "prompt-caching-2024-07-31", ...]`)

### Beta headers (each is a feature gate)

- [x] `computer-use-2025-01-24`
- [ ] **`computer-use-2025-11-24`** (paired with new-gen tools)
- [ ] **`prompt-caching-2024-07-31`** (cache_control validation)
- [ ] **`output-128k-2025-02-19` / `output-300k-2026-03-24`** (extended output for batch API)
- [ ] **`token-efficient-tools-2025-02-19`** (smaller tool definitions)
- [ ] **`fine-grained-tool-streaming-2025-05-14`**
- [ ] **`extended-thinking-2025-01-15`**
- [ ] **`fast-mode-2026-02-01`** (Opus 4.6 only)
- [ ] **`compact-2025-09-15`** (compaction)
- [ ] **`context-management-2025-09-15` / `context-1m-2025-09-15`**
- [ ] **`files-api-2025-04-14`**
- [ ] **`mcp-client-2025-09-15`**
- [ ] **`tool-examples-2025-10-29`**
- [ ] **`advanced-tool-use-2025-09-15`** (bundle: defer_loading + allowed_callers + skills)
- [ ] **`interleaved-thinking-2025-05-14`**
- [ ] **`skills-2025-10-29`**
- [ ] **`redact-thinking-2025-09-15`**
- [ ] **`task-budgets-2025-09-15`**
- [ ] **`eager-input-streaming-2025-10-29`**

### Other endpoints

- [ ] **Token counting** (`POST /v1/messages/count_tokens`)
- [ ] **Message Batches** (`POST /v1/messages/batches` + cancel + retrieve + results)
- [ ] **Files API** (`POST /v1/files`, list, retrieve, delete, content)
- [ ] **Models list** (`GET /v1/models`)
- [ ] **Text Completions API** (legacy `POST /v1/complete`)

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
- [ ] **Streaming** (`POST /model/{modelId}/converse-stream`)
- [ ] **Vision** (`content: [{ image: { format, source: { bytes } } }]`)
- [ ] **Document input** (`content: [{ document: { format, name, source: { bytes } } }]`)
- [ ] **Video input** (`content: [{ video: { format, source } }]`)
- [ ] **Tool result** (`content: [{ toolResult: { toolUseId, content, status } }]`)
- [ ] **Stop sequences** (`inferenceConfig: { stopSequences: [...] }`)
- [ ] **Temperature / topP** (warned for Opus 4.7 which rejects them)
- [ ] **Tool choice** (`toolConfig: { toolChoice: { auto/any/tool: { name } } }`)
- [ ] **Guardrail config** (`guardrailConfig: { guardrailIdentifier, guardrailVersion, trace }`)
- [ ] **Additional model request fields** (`additionalModelRequestFields: { ... }`) — provider-specific passthrough
- [ ] **Additional model response field paths** (`additionalModelResponseFieldPaths`)
- [ ] **Prompt variables** (`promptVariables: { var: { text } }`) — for managed prompts
- [ ] **Performance config** (`performanceConfig: { latency: "standard" | "optimized" }`)
- [ ] **Request metadata** (`requestMetadata: { ... }`) — for billing tags

### InvokeModel API (`POST /model/{modelId}/invoke`)

- [ ] **Direct invoke** with provider-native body (Anthropic shape, Cohere shape, etc.)
- [ ] **Invoke streaming** (`POST /model/{modelId}/invoke-with-response-stream`)
- [ ] **Async invocation jobs** (`POST /model-invocation-job` + list + get + stop)

### Cross-region inference profiles

- [x] `global.anthropic.claude-*` (4 entries: opus-4-7, sonnet-4-6, opus-4-6, opus-4-5...)
- [x] `us.amazon.nova-*` (3 entries: pro, lite, micro)
- [ ] **EU geo profile** (`eu.anthropic.claude-*`)
- [ ] **APAC geo profile** (`apac.anthropic.claude-*`)
- [ ] **JP geo profile** (`jp.anthropic.claude-*`)
- [ ] **AU geo profile** (`au.anthropic.claude-haiku-4-5`)

### Other Bedrock surfaces

- [ ] **Application inference profiles** (custom profiles created via API)
- [ ] **Bedrock Agents** (`POST /agents/{agentId}/invokeAgent`)
- [ ] **Bedrock Knowledge Bases** (`POST /knowledgebases/{id}/retrieve`)
- [ ] **Bedrock Guardrails** (`POST /guardrails/{id}/apply`)
- [ ] **Provisioned throughput** model invocation
- [ ] **Bedrock-mantle endpoint** for Anthropic Messages API on Bedrock (newer surface, alongside bedrock-runtime)

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
- [ ] **Tool config** (`toolConfig: { functionCallingConfig: { mode: AUTO / ANY / NONE, allowedFunctionNames } }`)
- [ ] **Stop sequences** (`generationConfig: { stopSequences }`)
- [ ] **Temperature / topP / topK / candidateCount** (`generationConfig`)
- [ ] **Max output tokens** (`generationConfig: { maxOutputTokens }`)
- [ ] **Response logprobs** (`generationConfig: { responseLogprobs: true, logprobs: N }`)
- [ ] **Presence/frequency penalty** (`generationConfig: { presencePenalty, frequencyPenalty }`)
- [ ] **Cached content** (`cachedContent: "cachedContents/abc123"`)
- [ ] **PDF input** (`fileData` with `application/pdf` mime)
- [ ] **Audio input** (audio/mp3, audio/wav file data)
- [ ] **Video input** (video/mp4 file data)
- [ ] **YouTube URL input** (`fileData: { fileUri: "https://www.youtube.com/watch?v=..." }`)
- [ ] **Code execution outputs** (`executableCode`, `codeExecutionResult` parts)
- [ ] **Search grounding with retrieval config** (`tools: [{ googleSearchRetrieval: { dynamicRetrievalConfig } }]`)
- [ ] **URL context** (`tools: [{ urlContext: {} }]`)
- [ ] **Live API** (websocket-based bidirectional streaming)
- [ ] **Function responses** (`role: "function"` parts with `functionResponse`)
- [ ] **Thinking response signature** (return `thoughtSignature` to continue thinking across turns)

### Other endpoints

- [ ] **Count tokens** (`POST /v1beta/models/{model}:countTokens`)
- [ ] **Embed content** (`POST /v1beta/models/{model}:embedContent`)
- [ ] **Batch embed** (`POST /v1beta/models/{model}:batchEmbedContents`)
- [ ] **Cached content CRUD** (`POST /v1beta/cachedContents`, list, get, update, delete)
- [ ] **Files API** (`POST /v1beta/files` upload, list, get, delete)
- [ ] **Models list** (`GET /v1beta/models`)
- [ ] **Tuned models** (`POST /v1beta/tunedModels`)
- [ ] **Operations** (`GET /v1beta/operations/{name}` for long-running ops)

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
- [ ] **Vertex AI Search grounding** (`tools: [{ retrieval: { vertexAiSearch: { datastore } } }]`)
- [ ] **Vertex AI Search dynamic** (`tools: [{ retrieval: { dynamicRetrievalConfig } }]`)
- [ ] **Custom search grounding via RAG corpora** (`vertexRagStore`)
- [ ] **Long-context caching** (`cachedContent` Vertex variant)
- [ ] **Global / multi-region / regional endpoints** (`region: "global" | "us" | "eu" | "us-east5"`)
- [ ] **Provisioned Throughput** (`x-goog-spend-limit-id` header)
- [ ] **Request response logging** (Vertex-side, not API-visible)
- [ ] **Imagen image generation** (`POST /publishers/google/models/imagen-3.0-generate-002:predict`)
- [ ] **Veo video generation** (`POST /publishers/google/models/veo-001:predict`)

### Anthropic-on-Vertex specific

- [x] Claude Opus 4.7 in user's region (`global` / `us-east5` / `europe-west1`)
- [ ] **Claude Sonnet 4.6 / 4.5 / Haiku 4.5** (regional gating — must use `global` or `us-east5`)
- [ ] **`anthropic_version: "vertex-2023-10-16"` in body** (Vertex-specific replacement for the header)
- [ ] **Vertex `:streamRawPredict` endpoint** for SSE streaming
- [ ] **Beta headers via body field** (`anthropic_beta` instead of HTTP header)
- [ ] **Anthropic on multi-region endpoints** (`https://aiplatform.us.rep.googleapis.com`, `eu.rep`)

### Vertex Model Garden (3rd-party publishers)

- [ ] **Llama on Vertex** (`publishers/meta/models/llama-4-maverick-17b-128e-instruct-maas:predict`)
- [ ] **Mistral on Vertex** (`publishers/mistralai/models/mistral-large-2411`)
- [ ] **DeepSeek on Vertex**
- [ ] **Gemma on Vertex** (`publishers/google/models/gemma-3-27b-it`)

---

## Azure OpenAI

Sources:
- REST API: <https://learn.microsoft.com/en-us/azure/ai-services/openai/reference>
- Latest API version index: <https://learn.microsoft.com/en-us/azure/ai-services/openai/api-version-deprecation>

### Deployment-style chat completions

- [x] Basic chat (`POST /openai/deployments/{deployment}/chat/completions?api-version=...`)
- [ ] **Azure Chat Completions w/ tools** (function calling, parallel tool calls)
- [ ] **Azure Chat Completions w/ vision** (`gpt-4o` deployment)
- [ ] **Azure Chat Completions w/ structured output** (json_schema)
- [ ] **Azure Chat Completions w/ streaming**
- [ ] **Azure Chat Completions w/ system message**
- [ ] **Azure-specific data sources / On Your Data** (`data_sources: [{ type: "azure_search", parameters: {...} }]`)
- [ ] **Azure content filters** (`content_filter_results` in response)
- [ ] **Reasoning effort on Azure** (o1 / o3 deployments)
- [ ] **Audio input on Azure** (gpt-4o-audio-preview deployment)

### Azure Responses API (preview)

- [ ] **Responses on Azure** (`POST /openai/deployments/{deployment}/responses?api-version=2025-04-01-preview`)
- [ ] **Web search on Azure Responses** (`web_search_preview`)
- [ ] **Code interpreter on Azure Responses**

### Other Azure endpoints

- [ ] **Azure embeddings** (`POST /openai/deployments/{deployment}/embeddings?api-version=...`)
- [ ] **Azure DALL-E** (`POST /openai/deployments/{deployment}/images/generations`)
- [ ] **Azure Whisper** (`POST /openai/deployments/{deployment}/audio/transcriptions`)
- [ ] **Azure TTS** (`POST /openai/deployments/{deployment}/audio/speech`)
- [ ] **Azure files / fine-tuning** (admin surface)
- [ ] **Azure Batch API**

---

## Cross-cutting (Bifrost-specific)

These exercise Bifrost's translation layer between provider shapes — every check below uses the unified
`POST /v1/chat/completions` endpoint with `provider/model` prefix routing.

- [x] OpenAI / Anthropic / Bedrock / Gemini / Vertex Basic Chat (50 cross-model entries)
- [~] Function calling cross-cut (4 providers tested; Vertex partially)
- [~] Structured output cross-cut (OpenAI + Gemini + Vertex; **Anthropic + Bedrock missing**)
- [~] Streaming cross-cut (4 providers tested; Vertex/Azure missing)
- [~] Vision cross-cut (OpenAI + Anthropic + Gemini; **Bedrock + Vertex + Azure missing**)
- [~] Web search cross-cut (3 providers; **Bedrock + Vertex + Azure missing**)
- [ ] **Code execution cross-cut** (Anthropic + Gemini)
- [ ] **Tool choice forced cross-cut** (multi-provider)
- [ ] **Computer use via cross-model** (`anthropic/claude-...` with computer_2025x tools — verifies Bifrost's translation; currently only tested via /anthropic drop-in)
- [ ] **Extended/adaptive thinking via cross-model**
- [ ] **Prompt caching via cross-model**
- [ ] **System message cross-cut** (every provider via `/v1/chat/completions`)
- [ ] **Multi-turn conversation cross-cut** (provider-specific role normalization)
- [ ] **Stop sequences cross-cut** (each provider has different stop semantics)
- [ ] **Sampling-params normalization** (Bifrost should silently drop temperature for Opus 4.7+)
- [ ] **Failover scenarios** (request to provider X falls back to provider Y on 5xx)
- [ ] **Virtual keys / governance** (`X-Bifrost-VK` header with allowed_models)
- [ ] **Rate limit propagation** (provider 429 → Bifrost 429 with Retry-After preserved)

---

## Passthrough surface (Bifrost catch-all forwarding)

Currently only Basic Chat is exercised through any `*_passthrough/*` route. Every advanced feature
should be re-tested through passthrough since the translation layer is bypassed.

- [x] OpenAI passthrough chat completions
- [x] OpenAI passthrough responses
- [x] Anthropic passthrough messages
- [x] Azure passthrough deployment chat
- [x] GenAI passthrough generateContent
- [ ] **OpenAI passthrough w/ web_search**
- [ ] **OpenAI passthrough w/ code_interpreter**
- [ ] **Anthropic passthrough w/ computer_use** (verify auth header strip + beta header injection)
- [ ] **Anthropic passthrough w/ extended thinking**
- [ ] **Anthropic passthrough w/ prompt caching**
- [ ] **Anthropic passthrough w/ web_search_20260209**
- [ ] **GenAI passthrough w/ googleSearch**
- [ ] **GenAI passthrough w/ codeExecution**
- [ ] **Azure passthrough w/ tools**
- [ ] **Streaming through any passthrough route**
- [ ] **Vision through any passthrough route**
- [ ] **All passthrough routes with disallowed auth headers** (verify they're stripped, not forwarded)

---

## Priority order for backlog burn-down

1. **Cross-model feature variants** — biggest gap, highest leverage; ~20 new requests close most of the matrix
2. **Anthropic beta headers + new-gen tools** — lots of low-hanging features Bifrost claims to support
3. **Bedrock streaming + Bedrock vision** — Converse API has both but harness has neither
4. **Passthrough advanced features** — proves the byte-for-byte forwarding handles complex bodies
5. **Azure beyond Basic Chat** — Azure has the worst coverage; even one tools/streaming test would help
6. **OpenAI Responses API server tools** — file_search needs setup, but computer_use_preview, mcp, image_generation are testable
7. **Vertex Anthropic global routing** — once `GOOGLE_LOCATION=global` is set, all 4-5 deferred Claude variants come back
