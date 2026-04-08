# Bifrost AI Gateway — API Overview & Client Guide

> **Version:** 1.0.0 | **License:** Apache 2.0  
> **Base URL:** `http://<host>:8080` (mặc định port `8080`)  
> **OpenAPI Spec:** `docs/openapi/openapi.yaml`  
> **Official Docs:** https://docs.getbifrost.ai

---

## Giới thiệu

Bifrost là một **AI Gateway hiệu suất cao** hợp nhất 20+ AI providers (OpenAI, Anthropic, AWS Bedrock, Google Vertex, Azure, …) qua **một API thống nhất tương thích OpenAI**. Ở benchmark 5.000 RPS, Bifrost chỉ thêm **11 µs** overhead mỗi request.

**Tính năng cốt lõi:**
- Unified OpenAI-compatible API cho toàn bộ providers
- Automatic fallback & load balancing giữa các providers/keys
- Virtual Keys với budget & rate limit
- Semantic caching (giảm chi phí & latency)
- Built-in observability (logs, metrics, histograms)
- MCP (Model Context Protocol) Gateway
- Drop-in replacement cho OpenAI / Anthropic / GenAI / Bedrock SDKs

---

## Khởi động nhanh

```bash
# NPX
npx -y @maximhq/bifrost

# Docker
docker run -p 8080:8080 -v $(pwd)/data:/app/data maximhq/bifrost
```

Gọi API ngay lập tức:
```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [{"role": "user", "content": "Hello, Bifrost!"}]
  }'
```

---

## Cấu trúc API

Bifrost cung cấp **7 nhóm endpoint** chính:

| Nhóm | Prefix | Mô tả |
|------|--------|-------|
| **Unified Inference** | `/v1/` | AI inference qua format Bifrost chuẩn |
| **Async Inference** | `/v1/async/` | Inference bất đồng bộ, poll theo job ID |
| **Provider Integrations** | `/{provider}/` | Drop-in replacement cho SDK gốc |
| **Framework Integrations** | `/litellm/`, `/langchain/`, `/pydanticai/` | Tích hợp AI frameworks |
| **Provider Management** | `/api/providers` | CRUD cấu hình providers & API keys |
| **Governance** | `/api/governance/` | Virtual Keys, Teams, Customers, Budgets, Routing |
| **Observability** | `/api/logs`, `/api/mcp-logs` | Logs, stats, analytics |
| **Configuration** | `/api/config`, `/api/version` | Runtime configuration |
| **Health** | `/health` | Health check |
| **Infrastructure** | `/ws`, `/mcp`, `/metrics` | WebSocket, MCP server, Prometheus |

---

## Authentication

### 1. Virtual Key (Inference — khuyến nghị)
```
Authorization: Bearer <virtual_key>
```
hoặc header:
```
X-BF-VK: <virtual_key>
```

### 2. Direct API Key (nếu server bật `allow_direct_keys`)
```
X-BF-API-Key: <provider_api_key>
```

### 3. Dashboard Auth (Admin endpoints)
```
Cookie: bifrost_session=<session_token>
```
Đăng nhập qua `POST /api/session/login`.

---

## Model Format

Tất cả inference endpoints sử dụng format `provider/model`:

```
openai/gpt-4o
openai/gpt-4o-mini
anthropic/claude-3-5-sonnet-20241022
anthropic/claude-3-opus-20240229
bedrock/anthropic.claude-3-5-sonnet-20241022-v2:0
vertex/gemini-2.0-flash
gemini/gemini-1.5-pro
groq/llama-3.3-70b-versatile
mistral/mistral-large-latest
ollama/llama3.2
azure/<deployment-id>
```

---

## Fallback & Load Balancing

Thêm `fallbacks` để tự động failover:

```json
{
  "model": "openai/gpt-4o",
  "fallbacks": [
    "anthropic/claude-3-5-sonnet-20241022",
    "bedrock/anthropic.claude-3-sonnet"
  ],
  "messages": [{"role": "user", "content": "Hello"}]
}
```

---

## 1. Unified Inference API (`/v1/`)

### Chat Completions
```
POST /v1/chat/completions
```
**Request:**
```json
{
  "model": "openai/gpt-4o",
  "messages": [
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "What is Bifrost?"}
  ],
  "fallbacks": ["anthropic/claude-3-5-sonnet-20241022"],
  "stream": false,
  "temperature": 0.7,
  "max_completion_tokens": 1000,
  "tools": [...],
  "tool_choice": "auto"
}
```

**Streaming:** Thêm `"stream": true` → nhận Server-Sent Events (SSE)

---

### Text Completions (Legacy)
```
POST /v1/completions
```
```json
{
  "model": "openai/gpt-3.5-turbo-instruct",
  "prompt": "Once upon a time",
  "max_tokens": 100,
  "temperature": 0.9
}
```

---

### Responses API (OpenAI Responses)
```
POST  /v1/responses
POST  /v1/responses/input_tokens   # Count tokens
```
```json
{
  "model": "openai/gpt-4o",
  "input": "Explain quantum computing"
}
```

---

### Embeddings
```
POST /v1/embeddings
```
```json
{
  "model": "openai/text-embedding-3-small",
  "input": "The food was delicious",
  "encoding_format": "float",
  "dimensions": 512
}
```

---

### Rerank
```
POST /v1/rerank
```
```json
{
  "model": "cohere/rerank-english-v3.0",
  "query": "What is AI?",
  "documents": [
    {"text": "Artificial intelligence is..."},
    {"text": "The weather today is..."}
  ],
  "top_n": 2,
  "return_documents": true
}
```

---

### Audio

**Text-to-Speech:**
```
POST /v1/audio/speech
```
```json
{
  "model": "openai/tts-1",
  "input": "Hello, this is Bifrost speaking",
  "voice": "alloy",
  "response_format": "mp3",
  "speed": 1.0
}
```

**Transcription (Speech-to-Text):**
```
POST /v1/audio/transcriptions     (multipart/form-data)
```
Fields: `model`, `file`, `language`, `prompt`, `response_format`

---

### Images

**Generate:**
```
POST /v1/images/generations
```
```json
{
  "model": "openai/dall-e-3",
  "prompt": "A futuristic city at sunset",
  "n": 1,
  "size": "1024x1024",
  "quality": "hd",
  "response_format": "url"
}
```

**Edit:** `POST /v1/images/edits` (multipart/form-data)  
**Variations:** `POST /v1/images/variations` (multipart/form-data)

---

### Videos
```
POST   /v1/videos                          # Generate
GET    /v1/videos                          # List
GET    /v1/videos/{video_id}               # Retrieve
GET    /v1/videos/{video_id}/content       # Download
DELETE /v1/videos/{video_id}              # Delete
POST   /v1/videos/{video_id}/remix        # Remix
```

---

### Files
```
POST   /v1/files                    # Upload
GET    /v1/files                    # List
GET    /v1/files/{file_id}          # Retrieve
DELETE /v1/files/{file_id}         # Delete
GET    /v1/files/{file_id}/content # Download content
```

---

### Batches
```
POST /v1/batches                          # Create batch
GET  /v1/batches                          # List batches
GET  /v1/batches/{batch_id}               # Get batch
POST /v1/batches/{batch_id}/cancel        # Cancel
GET  /v1/batches/{batch_id}/results       # Get results
```

---

### Containers
```
POST   /v1/containers                                           # Create
GET    /v1/containers                                           # List
GET    /v1/containers/{container_id}                           # Get
DELETE /v1/containers/{container_id}                          # Delete
POST   /v1/containers/{container_id}/files                    # Upload file
GET    /v1/containers/{container_id}/files                    # List files
GET    /v1/containers/{container_id}/files/{file_id}          # Get file
GET    /v1/containers/{container_id}/files/{file_id}/content  # Download
DELETE /v1/containers/{container_id}/files/{file_id}         # Delete file
```

---

### Models
```
GET /v1/models?provider=openai    # List models
```

---

## 2. Async Inference API (`/v1/async/`)

Submit job → nhận `job_id` → poll kết quả:

```
POST /v1/async/chat/completions           → { job_id: "..." }
GET  /v1/async/chat/completions/{job_id}  → { status: "completed", result: ... }
```

Tất cả inference types đều hỗ trợ async:

| Submit | Poll |
|--------|------|
| `POST /v1/async/chat/completions` | `GET /v1/async/chat/completions/{job_id}` |
| `POST /v1/async/completions` | `GET /v1/async/completions/{job_id}` |
| `POST /v1/async/responses` | `GET /v1/async/responses/{job_id}` |
| `POST /v1/async/embeddings` | `GET /v1/async/embeddings/{job_id}` |
| `POST /v1/async/audio/speech` | `GET /v1/async/audio/speech/{job_id}` |
| `POST /v1/async/audio/transcriptions` | `GET /v1/async/audio/transcriptions/{job_id}` |
| `POST /v1/async/images/generations` | `GET /v1/async/images/generations/{job_id}` |
| `POST /v1/async/images/edits` | `GET /v1/async/images/edits/{job_id}` |
| `POST /v1/async/images/variations` | `GET /v1/async/images/variations/{job_id}` |

---

## 3. Provider Integration APIs (Drop-in Replacement)

Thay đổi `base_url` trong SDK có sẵn, không cần thay đổi code:

### OpenAI SDK
```diff
- base_url = "https://api.openai.com"
+ base_url = "http://localhost:8080/openai"
```
Endpoints:
```
POST /openai/v1/chat/completions
POST /openai/v1/completions
POST /openai/v1/embeddings
POST /openai/v1/responses
GET  /openai/v1/models
POST /openai/v1/audio/speech
POST /openai/v1/audio/transcriptions
POST /openai/v1/images/generations
POST /openai/v1/files
...
```

### Azure OpenAI SDK
```
POST /openai/openai/deployments/{deployment-id}/chat/completions
POST /openai/openai/deployments/{deployment-id}/embeddings
```

### Anthropic SDK
```diff
- base_url = "https://api.anthropic.com"
+ base_url = "http://localhost:8080/anthropic"
```
Endpoints:
```
POST /anthropic/v1/messages
POST /anthropic/v1/messages/batches
POST /anthropic/v1/messages/count_tokens
POST /anthropic/v1/complete
GET  /anthropic/v1/models
GET  /anthropic/v1/files
```

### Google GenAI SDK
```diff
- api_endpoint = "https://generativelanguage.googleapis.com"
+ api_endpoint = "http://localhost:8080/genai"
```
Endpoints:
```
POST /genai/v1beta/models/{model}:generateContent
POST /genai/v1beta/models/{model}:streamGenerateContent
POST /genai/v1beta/models/{model}:embedContent
POST /genai/v1beta/models/{model}:countTokens
POST /genai/v1beta/models/{model}:predict
GET  /genai/v1beta/models
```

### AWS Bedrock SDK
```
POST /bedrock/model/{modelId}/converse
POST /bedrock/model/{modelId}/converse-stream
POST /bedrock/model/{modelId}/invoke
POST /bedrock/model-invocation-jobs
```

### Cohere SDK
```
POST /cohere/v2/chat
POST /cohere/v2/embed
POST /cohere/v1/tokenize
```

---

## 4. Framework Integrations

| Framework | Prefix | Providers hỗ trợ |
|-----------|--------|-----------------|
| LiteLLM | `/litellm/` | OpenAI, Anthropic, GenAI, Bedrock, Cohere |
| LangChain | `/langchain/` | OpenAI, Anthropic, GenAI, Bedrock, Cohere |
| PydanticAI | `/pydanticai/` | OpenAI, Anthropic, GenAI, Bedrock, Cohere |

Ví dụ LiteLLM:
```
POST /litellm/v1/chat/completions
POST /litellm/anthropic/v1/messages
POST /litellm/genai/v1beta/models/{model}:generateContent
```

---

## 5. Provider Management API (`/api/providers`)

### Danh sách endpoints

| Method | Path | Mô tả |
|--------|------|-------|
| `GET` | `/api/providers` | List all providers |
| `GET` | `/api/providers/{provider}` | Get provider config |
| `POST` | `/api/providers` | Add new provider |
| `PUT` | `/api/providers/{provider}` | Update provider config |
| `DELETE` | `/api/providers/{provider}` | Remove provider |
| `GET` | `/api/keys` | List all API keys |
| `GET` | `/api/models` | List models (with filters) |
| `GET` | `/api/models/details` | List models with capability metadata |
| `GET` | `/api/models/parameters` | Get model parameters |
| `GET` | `/api/models/base` | List base models |

### Thêm Provider

```
POST /api/providers
```
```json
{
  "provider": "openai",
  "keys": [
    {
      "value": "sk-...",
      "models": ["gpt-4o", "gpt-4o-mini"],
      "weight": 1.0
    }
  ],
  "network_config": {
    "timeout": 30,
    "max_retries": 3
  },
  "concurrency_and_buffer_size": {
    "concurrency": 100,
    "buffer_size": 1000
  }
}
```

### List Models với filter

```
GET /api/models?provider=openai&query=gpt&limit=20
GET /api/models/details?provider=anthropic&unfiltered=true
```

---

## 6. Governance API (`/api/governance/`)

### Virtual Keys

| Method | Path | Mô tả |
|--------|------|-------|
| `GET` | `/api/governance/virtual-keys` | List VKs (pagination, search) |
| `POST` | `/api/governance/virtual-keys` | Create VK |
| `GET` | `/api/governance/virtual-keys/{vk_id}` | Get VK |
| `PUT` | `/api/governance/virtual-keys/{vk_id}` | Update VK |
| `DELETE` | `/api/governance/virtual-keys/{vk_id}` | Delete VK |

**Tạo Virtual Key:**
```json
{
  "name": "my-app-key",
  "description": "Production key for my-app",
  "provider_configs": [
    {
      "provider": "openai",
      "weight": 0.7,
      "allowed_models": ["gpt-4o", "gpt-4o-mini"]
    },
    {
      "provider": "anthropic",
      "weight": 0.3
    }
  ],
  "budget": {
    "max_limit": 100.0,
    "reset_duration": "1M",
    "calendar_aligned": true
  },
  "rate_limit": {
    "token_max_limit": 1000000,
    "token_reset_duration": "1h",
    "request_max_limit": 10000,
    "request_reset_duration": "1m"
  },
  "is_active": true
}
```

---

### Teams

| Method | Path | Mô tả |
|--------|------|-------|
| `GET` | `/api/governance/teams` | List teams |
| `POST` | `/api/governance/teams` | Create team |
| `GET` | `/api/governance/teams/{team_id}` | Get team |
| `PUT` | `/api/governance/teams/{team_id}` | Update team |
| `DELETE` | `/api/governance/teams/{team_id}` | Delete team |

---

### Customers

| Method | Path | Mô tả |
|--------|------|-------|
| `GET` | `/api/governance/customers` | List customers |
| `POST` | `/api/governance/customers` | Create customer |
| `GET` | `/api/governance/customers/{customer_id}` | Get customer |
| `PUT` | `/api/governance/customers/{customer_id}` | Update customer |
| `DELETE` | `/api/governance/customers/{customer_id}` | Delete customer |

---

### Routing Rules

| Method | Path | Mô tả |
|--------|------|-------|
| `GET` | `/api/governance/routing-rules` | List rules |
| `POST` | `/api/governance/routing-rules` | Create rule |
| `GET` | `/api/governance/routing-rules/{rule_id}` | Get rule |
| `PUT` | `/api/governance/routing-rules/{rule_id}` | Update rule |
| `DELETE` | `/api/governance/routing-rules/{rule_id}` | Delete rule |

**Tạo Routing Rule:**
```json
{
  "name": "prod-routing",
  "cel_expression": "model == 'gpt-4o'",
  "targets": [
    {"provider": "openai", "model": "gpt-4o", "weight": 0.7},
    {"provider": "anthropic", "model": "claude-3-5-sonnet-20241022", "weight": 0.3}
  ],
  "fallbacks": ["bedrock/anthropic.claude-3-sonnet"],
  "priority": 10,
  "enabled": true
}
```

---

### Model Configs

| Method | Path | Mô tả |
|--------|------|-------|
| `GET` | `/api/governance/model-configs` | List model configs |
| `POST` | `/api/governance/model-configs` | Create config |
| `GET` | `/api/governance/model-configs/{mc_id}` | Get config |
| `PUT` | `/api/governance/model-configs/{mc_id}` | Update config |
| `DELETE` | `/api/governance/model-configs/{mc_id}` | Delete config |

---

### Provider Governance

| Method | Path | Mô tả |
|--------|------|-------|
| `GET` | `/api/governance/providers` | List provider governance |
| `PUT` | `/api/governance/providers/{provider_name}` | Update governance |
| `DELETE` | `/api/governance/providers/{provider_name}` | Remove governance |

---

### Budgets & Rate Limits (Read-only)

```
GET /api/governance/budgets
GET /api/governance/rate-limits
```

---

### Pricing Overrides

```
GET    /api/governance/pricing-overrides
POST   /api/governance/pricing-overrides
GET    /api/governance/pricing-overrides/{id}
PUT    /api/governance/pricing-overrides/{id}
DELETE /api/governance/pricing-overrides/{id}
```

---

## 7. Observability API (`/api/logs`)

### Log Search & Management

| Method | Path | Mô tả |
|--------|------|-------|
| `GET` | `/api/logs` | Search logs với filters |
| `GET` | `/api/logs/{id}` | Get log entry by ID |
| `GET` | `/api/logs/stats` | Aggregate statistics |
| `GET` | `/api/logs/filterdata` | Available filter values |
| `GET` | `/api/logs/rankings` | Model usage rankings |
| `GET` | `/api/logs/dropped` | Dropped request count |
| `DELETE` | `/api/logs` | Delete logs |
| `POST` | `/api/logs/recalculate-cost` | Recalculate costs |

**Log Search parameters:**
```
GET /api/logs?providers=openai,anthropic
             &models=gpt-4o
             &status=success
             &start_time=2024-01-01T00:00:00Z
             &end_time=2024-01-31T23:59:59Z
             &virtual_key_ids=vk-abc123
             &content_search=hello
             &min_latency=0.5
             &max_cost=0.01
             &sort_by=timestamp
             &order=desc
             &limit=50
             &offset=0
```

---

### Histograms & Analytics

| Endpoint | Mô tả |
|----------|-------|
| `GET /api/logs/histogram` | Request counts theo thời gian |
| `GET /api/logs/histogram/tokens` | Token usage theo thời gian |
| `GET /api/logs/histogram/cost` | Chi phí theo thời gian |
| `GET /api/logs/histogram/models` | Model usage breakdown |
| `GET /api/logs/histogram/latency` | Latency percentiles |
| `GET /api/logs/histogram/cost/by-provider` | Cost by provider |
| `GET /api/logs/histogram/tokens/by-provider` | Tokens by provider |
| `GET /api/logs/histogram/latency/by-provider` | Latency by provider |

---

### MCP Logs

```
GET    /api/mcp-logs
GET    /api/mcp-logs/stats
GET    /api/mcp-logs/filterdata
GET    /api/mcp-logs/histogram
GET    /api/mcp-logs/histogram/cost
GET    /api/mcp-logs/histogram/top-tools
DELETE /api/mcp-logs
```

---

## 8. Configuration API

| Method | Path | Mô tả |
|--------|------|-------|
| `GET` | `/api/config` | Get current config |
| `PUT` | `/api/config` | Update config |
| `GET` | `/api/version` | Get version |
| `GET` | `/api/proxy-config` | Get proxy config |
| `PUT` | `/api/proxy-config` | Update proxy config |
| `POST` | `/api/pricing/force-sync` | Force pricing sync |

---

## 9. Session Management

```
POST /api/session/login
POST /api/session/logout
GET  /api/session/is-auth-enabled
GET  /api/session/ws-ticket
```

---

## 10. Plugins

```
GET    /api/plugins
GET    /api/plugins/{name}
PUT    /api/plugins/{name}
DELETE /api/plugins/{name}
```

---

## 11. MCP (Model Context Protocol)

```
POST /v1/mcp/tool/execute          # Execute MCP tool
GET  /api/mcp/clients              # List MCP clients
POST /api/mcp/client               # Add MCP client
GET  /api/mcp/client/{id}          # Get MCP client
PUT  /api/mcp/client/{id}          # Update MCP client
DELETE /api/mcp/client/{id}        # Remove MCP client
POST /api/mcp/client/{id}/reconnect # Reconnect client
```

---

## 12. Infrastructure

```
GET /health        # Health check
GET /metrics       # Prometheus metrics
WS  /ws            # WebSocket (real-time UI updates)
GET /mcp           # MCP server endpoint (SSE)
```

**Health Response:**
```json
{
  "status": "ok",
  "components": {
    "db_pings": "ok"
  }
}
```

---

## 13. Prompt Repository

```
GET    /api/prompt-repo/folders
POST   /api/prompt-repo/folders
GET    /api/prompt-repo/folders/{id}
PUT    /api/prompt-repo/folders/{id}
DELETE /api/prompt-repo/folders/{id}

GET    /api/prompt-repo/prompts
POST   /api/prompt-repo/prompts
GET    /api/prompt-repo/prompts/{id}
PUT    /api/prompt-repo/prompts/{id}
DELETE /api/prompt-repo/prompts/{id}
GET    /api/prompt-repo/prompts/{id}/versions
GET    /api/prompt-repo/prompts/{id}/sessions
```

---

## 14. Cache Management

```
DELETE /api/cache/clear/{requestId}
DELETE /api/cache/clear-by-key/{cacheKey}
```

---

## Supported Providers (20+)

| Provider | Model Format | Notes |
|----------|-------------|-------|
| OpenAI | `openai/gpt-4o` | Full feature support |
| Anthropic | `anthropic/claude-3-5-sonnet-20241022` | Claude 3, 3.5, 4 |
| AWS Bedrock | `bedrock/anthropic.claude-3-5-sonnet-20241022-v2:0` | Native AWS auth |
| Google Vertex | `vertex/gemini-2.0-flash` | OAuth2 auth |
| Azure OpenAI | `azure/<deployment-id>` | Deployment management |
| Google Gemini | `gemini/gemini-1.5-pro` | Vision, audio, embeddings |
| Groq | `groq/llama-3.3-70b-versatile` | Ultra-fast LPU inference |
| Mistral | `mistral/mistral-large-latest` | Tool support |
| Cohere | `cohere/command-r-plus` | Chat, embeddings, rerank |
| Cerebras | `cerebras/llama3.1-8b` | High-speed inference |
| Ollama | `ollama/llama3.2` | Local self-hosted |
| Hugging Face | `huggingface/...` | Chat, TTS, STT |
| OpenRouter | `openrouter/...` | Multi-provider routing |
| Perplexity | `perplexity/...` | Web search + reasoning |
| ElevenLabs | `elevenlabs/...` | TTS/STT specialist |
| Nebius | `nebius/...` | OpenAI-compatible |
| xAI | `xai/grok-...` | Vision + reasoning |
| Parasail | `parasail/...` | Chat with tool calling |
| Replicate | `replicate/...` | Async prediction |
| SGL | `sgl/...` | SGLang runtime |
| vLLM | `vllm/...` | Self-hosted OpenAI-compat |

---

## Response Error Format

```json
{
  "error": {
    "type": "invalid_request_error",
    "message": "model should be in provider/model format",
    "code": "invalid_model",
    "param": "model"
  }
}
```

---

## Client Quick Reference

### Python (OpenAI SDK drop-in)
```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8080/openai",
    api_key="your-virtual-key"  # hoặc bất kỳ API key nào
)

response = client.chat.completions.create(
    model="gpt-4o",  # Bifrost tự map sang openai/gpt-4o
    messages=[{"role": "user", "content": "Hello!"}]
)
```

### Python (Unified Bifrost API)
```python
import requests

response = requests.post(
    "http://localhost:8080/v1/chat/completions",
    headers={"Authorization": "Bearer your-virtual-key"},
    json={
        "model": "openai/gpt-4o",
        "fallbacks": ["anthropic/claude-3-5-sonnet-20241022"],
        "messages": [{"role": "user", "content": "Hello!"}]
    }
)
```

### JavaScript/TypeScript
```typescript
import OpenAI from 'openai';

const client = new OpenAI({
  baseURL: 'http://localhost:8080/openai',
  apiKey: 'your-virtual-key',
});

const response = await client.chat.completions.create({
  model: 'gpt-4o',
  messages: [{ role: 'user', content: 'Hello!' }],
});
```

### Anthropic SDK drop-in
```python
import anthropic

client = anthropic.Anthropic(
    base_url="http://localhost:8080/anthropic",
    api_key="your-virtual-key"
)

message = client.messages.create(
    model="claude-3-5-sonnet-20241022",
    messages=[{"role": "user", "content": "Hello!"}]
)
```

---

## Files trong specs/

| File | Mô tả |
|------|-------|
| `api_overview.md` | **File này** — Tổng quan đầy đủ API |
| `../docs/openapi/openapi.yaml` | OpenAPI 3.1 specification đầy đủ |
| `../docs/overview.mdx` | Official docs overview |
| `../docs/quickstart/gateway/` | Gateway setup guides |
| `../docs/features/` | Feature documentation |
| `../docs/integrations/` | SDK integration guides |
| `../docs/providers/` | Provider-specific docs |
