# Async Inference E2E Tests

End-to-end tests for Bifrost's async inference feature (`/v1/async/*` endpoints and integration route headers).

## Running

```bash
go test ./... -timeout 300s
```

With virtual keys (enables VK-scoped auth tests):

```bash
BIFROST_VK=sk-bf-... BIFROST_ALT_VK=sk-bf-... go test ./... -timeout 300s
```

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `BIFROST_BASE_URL` | `http://localhost:8080` | Bifrost gateway URL |
| `BIFROST_VK` | — | Primary virtual key; enables VK-mode tests |
| `BIFROST_ALT_VK` | — | Second virtual key; enables cross-VK auth tests |
| `BIFROST_POLL_TIMEOUT` | `30s` | Max time to wait for a job to reach terminal state |
| `BIFROST_POLL_INTERVAL` | `500ms` | Polling cadence |
| `BIFROST_INTEGRATION_PATH` | `/openai/v1/responses` | Override integration route path |
| `BIFROST_INTEGRATION_MODEL` | `openai/gpt-4o-mini` | Override model for integration route tests |
| `ASYNC_*_MODEL` | see below | Override model per endpoint (e.g. `ASYNC_CHAT_COMPLETION_MODEL`) |

### Model overrides

| Variable | Default |
|---|---|
| `ASYNC_TEXT_COMPLETION_MODEL` | `openai/gpt-3.5-turbo-instruct` |
| `ASYNC_CHAT_COMPLETION_MODEL` | `openai/gpt-4o-mini` |
| `ASYNC_RESPONSES_MODEL` | `openai/gpt-4o-mini` |
| `ASYNC_EMBEDDINGS_MODEL` | `openai/text-embedding-3-small` |
| `ASYNC_SPEECH_MODEL` | `openai/tts-1` |
| `ASYNC_TRANSCRIPTION_MODEL` | `openai/whisper-1` |
| `ASYNC_IMAGE_GEN_MODEL` | `openai/dall-e-3` |
| `ASYNC_IMAGE_EDIT_MODEL` | `openai/dall-e-2` |
| `ASYNC_IMAGE_VARIATION_MODEL` | `openai/dall-e-2` |
| `ASYNC_RERANK_MODEL` | `cohere/rerank-english-v3.0` |
| `ASYNC_OCR_MODEL` | `mistral/mistral-ocr-latest` |
| `ASYNC_OCR_IMAGE_URL` | carpenter-ant CDN URL |

## Test files

| File | What it covers |
|---|---|
| `submit_test.go` | All 11 endpoints return 202, well-formed job envelope, immediate poll status |
| `lifecycle_test.go` | Jobs reach terminal state, 404 for non-existent/wrong-type, result shape |
| `auth_test.go` | VK scoping, cross-VK isolation, all three auth header formats |
| `ttl_test.go` | Default/custom/invalid TTL, TTL expiry → 404 |
| `validation_test.go` | Stream rejection, malformed JSON, missing required fields, wrong HTTP method |
| `integration_route_test.go` | `x-bf-async` / `x-bf-async-id` headers on `/openai/v1/responses` |

## Notes

- Tests skip gracefully when the gateway is unreachable (`/health` check at startup).
- Most tests run in two modes: **global** (no VK) and **with_vk** (when `BIFROST_VK` is set).
- Integration route tests use the Responses API path — `AsyncChatResponseConverter` is not implemented on any route; only `AsyncResponsesResponseConverter` is wired up.
- `BIFROST_ALT_VK` is only required for cross-VK isolation tests (`TestAuth_VKScoped_DifferentKey_Returns404`, `TestIntegration_VKScope_DifferentKey_Returns4xx`).
