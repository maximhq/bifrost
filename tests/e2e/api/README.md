# E2E API tests (Newman / Postman)

End-to-end API tests for the Bifrost API using Postman collections and [Newman](https://www.npmjs.com/package/newman) (CLI).

## Contents

### V1 Endpoint Tests

| Path | Description |
|------|-------------|
| `bifrost-v1-complete.postman_collection.json` | Postman collection: all `/v1` endpoints (models, chat, completions, responses, embeddings, audio, images, count tokens, batches, files, containers, MCP) |
| `bifrost-v1.postman_environment.json` | Default Postman environment (OpenAI). Used when no provider-specific env exists. |
| `run-newman-tests.sh` | Script to run the V1 collection with Newman (single provider or all providers). |

### Integration Endpoint Tests

| Path | Description |
|------|-------------|
| `bifrost-openai-integration.postman_collection.json` | OpenAI integration endpoints: `/openai/v1/*`, `/openai/*`, `/openai/deployments/*` (38 requests) |
| `bifrost-anthropic-integration.postman_collection.json` | Anthropic integration endpoints: `/anthropic/v1/*` (13 requests) |
| `bifrost-bedrock-integration.postman_collection.json` | Bedrock integration endpoints: `/bedrock/model/*`, `/bedrock/files/*`, `/bedrock/model-invocation-*` (12 requests) |
| `bifrost-composite-integrations.postman_collection.json` | Composite integrations: GenAI, Cohere, LiteLLM, LangChain, PydanticAI, Health (21 requests) |
| `run-newman-openai-integration.sh` | Script to run OpenAI integration tests |
| `run-newman-anthropic-integration.sh` | Script to run Anthropic integration tests |
| `run-newman-bedrock-integration.sh` | Script to run Bedrock integration tests |
| `run-newman-composite-integration.sh` | Script to run composite integration tests |
| `run-all-integration-tests.sh` | Master script to run all integration test suites |

### Shared Resources

| Path | Description |
|------|-------------|
| `provider_config/` | Per-provider Postman env `.json` files (`bifrost-v1-openai.postman_environment.json`, etc.). Reused across all collections. |
| `fixtures/` | Sample files for multipart requests: `sample.mp3`, `sample.jsonl`, `sample.txt` |
| `newman-reports/` | Test reports organized by collection type (e.g., `openai-integration/`, `anthropic-integration/`). HTML/JSON reports when using `--html` / `--json`. |

## Prerequisites

- [Newman](https://www.npmjs.com/package/newman): `npm install -g newman`
- Bifrost server running (e.g. `http://localhost:8080`) with at least one provider configured (API keys, etc.)

## Run tests

From this directory (`tests/e2e/api`):

### V1 Endpoint Tests

```bash
# Run for all providers in parallel (each provider_config/bifrost-v1-*.postman_environment.json except sgl and ollama)
./run-newman-tests.sh

# Run for a single provider (by name or path to .json env)
./run-newman-tests.sh --env openai
./run-newman-tests.sh --env provider_config/bifrost-v1-openai.postman_environment.json

# Options
./run-newman-tests.sh --help
./run-newman-tests.sh --folder "Chat Completions"
./run-newman-tests.sh --html --verbose
```

### Integration Endpoint Tests

```bash
# Run all integration test suites for all providers
./run-all-integration-tests.sh

# Run all integration test suites for a single provider
./run-all-integration-tests.sh --env openai

# Run a specific integration test suite
./run-newman-openai-integration.sh           # OpenAI integration endpoints
./run-newman-anthropic-integration.sh        # Anthropic integration endpoints
./run-newman-bedrock-integration.sh          # Bedrock integration endpoints
./run-newman-composite-integration.sh        # Composite integrations + Health

# Run with options
./run-newman-openai-integration.sh --html --verbose
./run-newman-openai-integration.sh --env azure   # Test Azure-specific paths
```

### Test Success Criteria

A request **passes** if either:
- The response status is 2xx, or
- The response is 4xx/5xx but the error indicates the operation is not supported by the provider (e.g. `error.code === "unsupported_operation"` or message like "operation is not supported" / "not supported by X provider").

Any other non-2xx (e.g. 401 with a wrong API key) fails the test.

## Integration Endpoint Testing Strategy

### Native Integration Endpoints

Each major provider has its own integration test collection that tests provider-specific endpoint patterns:

- **OpenAI Integration** (`/openai/*`): Tests standard paths (`/openai/v1/chat/completions`), no-v1 paths (`/openai/chat/completions`), and Azure deployment paths (`/openai/deployments/{deployment-id}/chat/completions`). Covers 38 endpoints including chat, completions, embeddings, audio, images, batches, files, and containers.

- **Anthropic Integration** (`/anthropic/*`): Tests Anthropic-specific paths with different batch result endpoint pattern (`/anthropic/v1/messages/batches/{batch_id}/results` vs OpenAI's pattern). Covers 13 endpoints including messages, complete, count tokens, batches, and files.

- **Bedrock Integration** (`/bedrock/*`): Tests AWS Bedrock patterns with ARN-based batch job identifiers and S3 file operations. Covers 12 endpoints including converse, invoke, batch jobs, and S3 operations.

### Composite Integration Testing (Delegation)

The composite integrations collection tests **routing** for frameworks that delegate to other integrations:

- **LiteLLM, LangChain, PydanticAI**: These are pass-through routers that prefix requests with their framework name, then delegate to the underlying integration. For example:
  - `POST /litellm/v1/chat/completions` → delegates to OpenAI integration logic
  - `POST /litellm/anthropic/v1/messages` → delegates to Anthropic integration logic
  - `POST /langchain/bedrock/model/{model}/converse` → delegates to Bedrock integration logic

Rather than duplicating 100+ tests for each composite integration, we test **representative routes** (5 per composite) to validate routing works correctly. Comprehensive endpoint coverage is provided by the base integration tests.

- **GenAI**: Tests Google Gemini API format endpoints (2 requests)
- **Cohere**: Tests Cohere API format endpoints (3 requests)
- **Health**: Tests the `/health` endpoint (1 request)

### Batches, Files, Containers (mirror core tests)

Execution order and request shapes match the core Go tests (`core/internal/llmtests/batch.go`, `containers.go`):

- **Files** run first: Upload File (sets `file_id`), List, Retrieve, Get Content. No delete yet so the file can be used by Batches.
- **Batches**: Create Batch (Inline) sets `batch_id`; Create Batch (File-based) uses `file_id`; List (with `limit=10`), Retrieve, Cancel, Results use `batch_id`; then Delete File.
- **Containers**: Create Container sets `container_id`; List, Retrieve; Create Container File (Upload) sets `container_file_id`; List/Retrieve/Content/Delete container file use `container_file_id`; Delete Container last.

Request bodies match core (e.g. batch inline with `custom_id`/`body`/`Say hello`, container create with `name: "bifrost-test-container"`).

## Syncing from OpenAPI docs

The collection and supporting files are maintained under `docs/openapi/`. To refresh this e2e copy:

- `bifrost-v1-complete.postman_collection.json` ← `docs/openapi/bifrost-v1-complete.postman_collection.json`
- `bifrost-v1.postman_environment.json` ← `docs/openapi/bifrost-v1.postman_environment.json`
- `run-newman-tests.sh` ← `docs/openapi/run-newman-tests.sh`
- `provider_config/*.postman_environment.json` and `provider_config/README.md` ← `docs/openapi/provider_config/` (if syncing from docs)
- `fixtures/*` ← `docs/openapi/fixtures/`
