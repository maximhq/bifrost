# E2E API tests (Newman / Postman)

End-to-end API tests for the Bifrost API using Postman collections and [Newman](https://www.npmjs.com/package/newman) (CLI).

## Contents

### V1 Endpoint Tests

| Path | Description |
|------|-------------|
| `bifrost-v1-complete.postman_collection.json` | Postman collection: all `/v1` endpoints (models, chat, completions, responses, embeddings, audio, images, count tokens, batches, files, containers, MCP) |
| `bifrost-v1.postman_environment.json` | Optional/legacy Postman environment (OpenAI). `run-newman-tests.sh` uses **BIFROST_*** environment variables as the fallback when no provider-specific env file is passed (see script and `--help`). |
| `run-newman-tests.sh` | Script to run the V1 collection with Newman (single provider or all providers). |

### Integration Endpoint Tests

| Path | Description |
|------|-------------|
| `bifrost-openai-integration.postman_collection.json` | OpenAI integration endpoints: `/openai/v1/*`, `/openai/*`, `/openai/deployments/*` (38 requests) |
| `bifrost-anthropic-integration.postman_collection.json` | Anthropic integration endpoints: `/anthropic/v1/*` (13 requests) |
| `bifrost-bedrock-integration.postman_collection.json` | Bedrock integration endpoints: `/bedrock/model/*`, `/bedrock/files/*`, `/bedrock/model-invocation-*` (13 requests, including List Objects S3 ListObjectsV2). Auth via request headers set from collection/environment variables. |
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
| `provider-capabilities.json` | Provider capability matrix: per-provider map of booleans (e.g. `chat_completions: true`, `embedding: false`) for batch, file, container, embedding, speech, transcription, image. Derived from `core/providers/*/provider.go` NewUnsupportedOperationError. Used by integration collections to skip unsupported requests when run with all providers. |
| `fixtures/` | Sample files for multipart requests: `sample.mp3`, `sample.jsonl`, `sample.txt` |
| `setup-plugin.sh` | Builds the hello-world plugin for API Management plugin tests. Run automatically by API Management and all-integration runners. |
| `setup-mcp.sh` | Starts the test MCP server (`examples/mcps/http-no-ping-server`) on http://localhost:3001/ so Add/Update/Delete MCP Client tests can pass. Run automatically by API Management and all-integration runners. |
| `newman-reports/` | Test reports organized by collection type (e.g., `openai-integration/`, `anthropic-integration/`). HTML/JSON reports when using `--html` / `--json`. |

## Prerequisites

- [Newman](https://www.npmjs.com/package/newman): `npm install -g newman`
- Bifrost server running (e.g. `http://localhost:8080`) with at least one provider configured (API keys, etc.)

## Test infrastructure setup

Before running **API Management** or **all integration** tests, the runners optionally run:

- **`setup-plugin.sh`** – Builds `examples/plugins/hello-world` into `build/hello-world.so` (native OS/arch). If the plugin fails to build, plugin tests may fail with "plugin not found" / "failed to load"; those failures are treated as expected when the plugin is missing.
- **`setup-mcp.sh`** – Builds and starts the test MCP server (`examples/mcps/http-no-ping-server`) on **http://localhost:3001/** so the collection’s test MCP client (connection string `http://localhost:3001/`) can connect. If the server is already listening on 3001 or the script is skipped, MCP client tests accept 404/500 as fallback.

Both are called automatically by `run-newman-api-tests.sh` and `run-all-integration-tests.sh`.

To run setup manually (from this directory):

```bash
./setup-plugin.sh
./setup-mcp.sh
```

No Weaviate/cache setup is required: tests accept 405 for unimplemented cache endpoints.

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

**Retry logic (CI)**  
When `CI=1` or `CI=true` is set (case-insensitive), each failing request in the V1 collection is retried up to 3 times before moving to the next request. This helps with flaky tests in CI. The runner passes the value through to Newman when the environment variable is set (e.g. `CI=1 ./run-newman-tests.sh --env openai` or `CI=true ./run-newman-tests.sh --env openai`). Retry attempts are logged to the console as `[RETRY] Request "..." failed (attempt n/3). Retrying...`.

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

**V1 collection ("documented unsupported" assertion)**  
The **"Or documented unsupported (allowed request types)"** test passes only when the request’s operation category is marked as unsupported for the current provider in **`provider-capabilities.json`** (`providers.<name>.<operation> === false`). The request name is mapped to one of: `chat_completions`, `text_completion`, `responses`, `count_tokens`, `batch`, `file_upload`, `file_list`, `file_retrieve`, `file_delete`, `file_content`, `container_create`, `container_list`, `container_retrieve`, `container_delete`, `container_file_create`, `container_file_list`, `container_file_retrieve`, `container_file_content`, `container_file_delete`, `embedding`, `speech`, `transcription`, `list_models`, `image_generation`, `image_variation`, `image_edit`, `video_generation`, `video_retrieve`, `video_download`, `video_delete`, `video_list`, `video_remix`, `rerank`. These match the operation types in `core/schemas/bifrost.go` (e.g. `FileUploadRequest`, `ContainerFileContentRequest`). **`provider-capabilities.json` is the single source of truth:** the V1 run script (`run-newman-tests.sh`) loads it at run time and passes it to Newman as globals; the collection does not define or embed `provider_capabilities`.

### Expected failures (known limitations)

Some failures are expected and do not indicate bugs:

- **Authentication (401)** – Provider envs may use placeholder or invalid API keys; 401 is then expected. Some OpenAI integration endpoints may show 401 even with valid keys if keys are not configured for all endpoint types.

- **Batch API config (500)** – **"no batch-enabled keys found"** / **"no config found for batch APIs"** when batch endpoints are not configured for that provider.

  **To fix:** In Bifrost's config (or provider config), enable "Use for Batch APIs" on at least one API key for the provider, e.g. in config JSON:
  ```json
  {
    "providers": [
      {
        "name": "openai",
        "api_keys": [
          {
            "key": "sk-...",
            "use_for_batch_apis": true
          }
        ]
      }
    ]
  }
  ```

- **Model incompatibility** – Some models do not support certain operations (e.g. Azure gpt-4o does not support text completions, OpenAI chat models cannot be used for text completions); these may return 400 errors.

- **Responses API with tools** – The V1 "Create Response with Tools" test uses only a function tool (no `web_search`). Using `web_search` or other tool types can trigger 500 errors from the provider (OpenAI has had known 500s with web search on the Responses API).

- **Bedrock** – Model (converse/invoke) or S3 file operations may fail with 403/500 if AWS keys or S3 are not configured. The Bedrock **integration** collection (`bifrost-bedrock-integration.postman_collection.json`) tests `/bedrock/*` and supports auth via **request headers** (set from collection or environment variables by the collection’s pre-request script). Credentials can be provided via env vars (e.g. `BIFROST_BEDROCK_API_KEY`, `BIFROST_BEDROCK_ACCESS_KEY`, `BIFROST_BEDROCK_SECRET_KEY`, `BIFROST_BEDROCK_REGION`) when using the runner with `--env bedrock`; the runner passes these into Postman variables, which the pre-request script forwards as `x-bf-bedrock-*` headers. Set authentication in `provider_config/bifrost-v1-bedrock.postman_environment.json` or collection variables:
  - **Option A:** `bedrock_api_key` (API key authentication) and optionally `bedrock_region` (default: us-east-1)
  - **Option B:** `bedrock_access_key`, `bedrock_secret_key`, `bedrock_region` (required), and optionally `bedrock_session_token` (for temporary credentials)
  - For S3 operations: set `s3_bucket` and `s3_key` to a bucket you have access to; List Objects (GET `/bedrock/files/{bucket}`) is included and supports optional query params (`prefix`, `max-keys`, `continuation-token`)
  - For batch operations: set `role_arn` to an IAM role with Bedrock batch permissions; ensure `inputDataConfig` and `outputDataConfig` S3 URIs exist
  - The V1 collection skips file/batch requests when `file_id` / `batch_id` are placeholders

- **Composite integrations** – Cohere/OpenAI/Nebius etc. can show 401/402/500 due to keys, billing, or provider limits (e.g. tool calling, embeddings).

- **Plugin tests** – If `setup-plugin.sh` did not build the hello-world plugin, Create/Get/Update Plugin tests may fail with "plugin not found" / "failed to load"; the test suite treats these as acceptable when the plugin is missing.

## Integration Endpoint Testing Strategy

### Native Integration Endpoints

Each major provider has its own integration test collection that tests provider-specific endpoint patterns:

- **OpenAI Integration** (`/openai/*`): Tests standard paths (`/openai/v1/chat/completions`), no-v1 paths (`/openai/chat/completions`), and Azure deployment paths (`/openai/deployments/{deployment-id}/chat/completions`). Covers 38 endpoints including chat, completions, embeddings, audio, images, batches, files, and containers.

- **Anthropic Integration** (`/anthropic/*`): Tests Anthropic-specific paths with different batch result endpoint pattern (`/anthropic/v1/messages/batches/{batch_id}/results` vs OpenAI's pattern). Covers 13 endpoints including messages, complete, count tokens, batches, and files.

- **Bedrock Integration** (`/bedrock/*`): Tests AWS Bedrock patterns with ARN-based batch job identifiers and S3 file operations. Covers 13 endpoints including converse, invoke, batch jobs, S3 operations (PUT/GET/HEAD/DELETE Object and List Objects), and List Batch Jobs with optional query params.

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

### Skipping unsupported operations (integration collections)

When integration tests are run for **all providers** (e.g. `./run-newman-openai-integration.sh` without `--env`), each collection is executed once per provider environment. Some providers do not support batch, file, container, embedding, audio, or image operations. To avoid failing on those requests, each integration collection has:

- **Collection-level prerequest**: Runs before every request. Reads the current **provider** from the environment and the **request name**. If the request maps to an operation category for which the provider has `false` in `provider_capabilities` (e.g. `providers.anthropic.embedding === false`), the request is **skipped** via `postman.setNextRequest(nextRequestName)` so the next request in execution order runs instead.
- **Embedded variables**: `execution_order` (JSON array of request names in depth-first order) and `request_to_operation` (JSON map of request name → operation category). For the **V1** collection, `provider_capabilities` is **not** embedded: it is loaded from `provider-capabilities.json` at run time by `run-newman-tests.sh` and passed to Newman as globals.

**Config file**: `provider-capabilities.json` in this directory is a map per provider of capability flags (e.g. `chat_completions`, `embedding`, `batch`) to booleans. It is the single source of truth for which operations each provider supports (aligned with `core/providers/*/provider.go` returning `NewUnsupportedOperationError`).

**Updating capabilities or request mappings**:

1. **Change provider support**: Edit `provider-capabilities.json` and set each capability to `true` or `false` under `providers.<name>` (e.g. `providers.anthropic.embedding: false`). It is the only source of truth; the V1 run script loads it at run time (no embedded copy in the collection).
2. **Change which requests are skippable**: Edit `scripts/update-collection-capabilities.js` (function `getRequestToOperationMap`) to adjust the request-name → operation map for each collection.
3. **Re-inject variables into a collection**: From this directory run:
   ```bash
   node scripts/update-collection-capabilities.js bifrost-openai-integration.postman_collection.json --inject
   ```
   This re-extracts execution order, re-reads `provider-capabilities.json`, and overwrites the collection variables and the prerequest script. Run for each integration collection you changed.

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
