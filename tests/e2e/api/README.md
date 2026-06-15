# E2E API tests (Newman / Postman)

End-to-end API tests for the Bifrost API using Postman collections and [Newman](https://www.npmjs.com/package/newman) (CLI).

## Contents

### V1 Endpoint Tests

| Path | Description |
|------|-------------|
| `bifrost-v1-complete.postman_collection.json` | Postman collection: all `/v1` endpoints (models, chat, completions, responses, embeddings, audio, images, count tokens, batches, files, containers, MCP) |
| `bifrost-v1.postman_environment.json` | Optional/legacy Postman environment (OpenAI). `run-newman-inference-tests.sh` uses **BIFROST_*** environment variables as the fallback when no provider-specific env file is passed (see script and `--help`). |
| `run-newman-inference-tests.sh` | Script to run the V1 collection with Newman (single provider or all providers). |

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

### Model Catalog Wiring Tests

| Path | Description |
|------|-------------|
| `collections/bifrost-model-catalog-wiring.postman_collection.json` | Generated collection asserting that management-API mutations (add/update/delete provider and key, toggle key, alias) propagate into the model catalog read endpoints. **Generated — do not hand-edit.** |
| `runners/build-model-catalog-wiring-collection.py` | Generator for the collection above. Holds the scenario spec (the source of truth) and emits the JSON. |
| `runners/individual/run-newman-model-catalog-wiring-tests.sh` | Script to run the model-catalog wiring collection. |

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

Both are called automatically by `runners/run-newman-api-tests.sh` and `runners/run-all-integration-tests.sh`.

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
./runners/run-newman-inference-tests.sh

# Run for a single provider (by name or path to .json env)
./runners/run-newman-inference-tests.sh --env openai
./runners/run-newman-inference-tests.sh --env provider_config/bifrost-v1-openai.postman_environment.json

# Options
./runners/run-newman-inference-tests.sh --help
./runners/run-newman-inference-tests.sh --folder "Chat Completions"
./runners/run-newman-inference-tests.sh --html --verbose
```

### Routing Harness Ledger

Harness days are journaled in `routing/ledger-YYYY-MM-DD.md` (gitignored, one
file per day the routing/catalog suites run). Each ledger holds: an
open-divergences snapshot, per-suite scenario tables (setup / expected /
actual, with ✅ / ⚠️ recalibrated / 🐞 bug-found markers), the day's run
results, and day notes. Append to the current day's file during a session;
never rewrite past days.

### API Management Extensions

The API management runner can merge additional Postman folders maintained
outside this repo:

```bash
./runners/run-newman-api-tests.sh --extra-collection /path/to/extra.postman_collection.json
```

You can also pass extensions via environment variable:

```bash
BIFROST_API_EXTRA_COLLECTION=/path/to/extra.postman_collection.json \
  ./runners/run-newman-api-tests.sh
```

The default run loads no extra collections. Downstream repos pass their own
collections at run time, so the shared management requests live here while
assertions specific to those repos stay with them.

**Retry logic (CI)**
When `CI=1` or `CI=true` is set (case-insensitive), each failing request in the V1 collection is retried up to 3 times before moving to the next request. This helps with flaky tests in CI. The runner passes the value through to Newman when the environment variable is set (e.g. `CI=1 ./runners/run-newman-inference-tests.sh --env openai` or `CI=true ./runners/run-newman-inference-tests.sh --env openai`). Retry attempts are logged to the console as `[RETRY] Request "..." failed (attempt n/3). Retrying...`.

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

### Model Catalog Wiring Tests

These tests cover the path **HTTP mutation → config write → server-side catalog
hook → read endpoint**: the wiring that keeps the model catalog (`/api/models`,
`/api/models/details`) in sync with provider and key changes made through the
management API. Each scenario stands up an isolated custom provider backed by a
real upstream (OpenAI), drives a sequence of mutations, and asserts the catalog
reflects each one.

What it covers (one scenario per contract):

- **Add provider + key** — a gated key surfaces its allowed model.
- **Update key model set** — changing a key's allow-list re-gates the catalog.
- **Disable / re-enable key** — a disabled key drops its models; re-enabling restores them.
- **Delete one of two keys** — only the deleted key's models drop; the sibling's survive.
- **Delete provider** — the provider and its models disappear from the catalog.
- **Alias resolution** — an inference call via a key alias routes to the underlying model.

Run locally (from this directory):

```bash
./runners/individual/run-newman-model-catalog-wiring-tests.sh
```

Requirements:

- Bifrost running at `{{base_url}}` (default `http://localhost:8080`), ideally
  against a clean config store so no pre-existing `catwiring-*` providers linger.
- `openai_api_key` available — either in the seed env file (`generated/seed.env`
  or `$BIFROST_E2E_SEED_ENV`) or exported in the shell. Scenarios whose required
  credentials are missing skip themselves rather than fail.

Notes:

- Every resource is named `catwiring-openai-<scenario>-<run-id>`, where the
  run-id is built once per run from `e2e_seed_prefix` plus a timestamp nonce, so
  parallel runs never collide and a failed run leaves no blocking state.
- The catalog's live-model cache is populated asynchronously by the key hooks, so
  every post-mutation read polls with exponential backoff (up to 8 attempts)
  instead of asserting immediately.
- Each scenario has a cleanup folder that deletes its provider (cascading to its
  keys); it runs even when a mid-scenario step fails, and accepts 200/204/404.
- To change or extend the scenarios, edit
  `runners/build-model-catalog-wiring-collection.py` and re-run it, then commit
  both the script and the regenerated collection:

  ```bash
  python3 runners/build-model-catalog-wiring-collection.py
  ```

Required seed-env vars: `openai_api_key`, plus `e2e_seed_prefix` for
run-id namespacing. The runner also forwards the full per-provider credential set
(`anthropic_api_key`, `azure_*`, `bedrock_*`, `vertex_*`, etc.) so per-provider
expansion needs no runner change.

### Auth Matrix Tests

| Path | Description |
|------|-------------|
| `collections/bifrost-v1-auth-matrix.postman_collection.json` | Asserts the separation between inference auth (governance / virtual key) and dashboard auth (admin password on `/api/*`). |
| `runners/individual/run-newman-auth-matrix-tests.sh` | Boots a fresh server per config combination and runs the collection against each. |

Unlike the other runners, this one **boots its own servers** — each of the four
combinations needs a different boot config, so it cannot reuse a shared running
server. It requires a built `bifrost-http` binary.

It sweeps the 2x2 of `client.enforce_auth_on_inference` x
`governance.auth_config.is_enabled` (admin password) with a pre-seeded virtual key
and an unreachable dummy provider (so "auth passed" surfaces as a non-401 upstream
error). Per combination it asserts:

- **VK-authenticated inference is never blocked by the auth layer** (never 401),
  in every combination — including admin-password-on. This is the core regression
  guard: the admin middleware must not reject virtual-key inference.
- **No-VK inference** is rejected by governance with `virtual_key_required` only
  when `enforce_auth_on_inference` is on; otherwise it passes the auth layer.
- **Admin Basic auth is not a substitute for a VK** on inference (same governance
  rejection when enforce is on).
- **`/api/config`** requires admin creds (admin-middleware `Unauthorized`) only
  when admin password auth is on; it is open otherwise.

Run locally (from this directory):

```bash
./runners/individual/run-newman-auth-matrix-tests.sh --binary /path/to/bifrost-http
# options: --port <port> (default 8090), --html, --json, --verbose, --bail
```

### Test Success Criteria

A request **passes** if either:
- The response status is 2xx, or
- The response is 4xx/5xx but the error indicates the operation is not supported by the provider (e.g. `error.code === "unsupported_operation"` or message like "operation is not supported" / "not supported by X provider").

Any other non-2xx (e.g. 401 with a wrong API key) fails the test.

**V1 collection ("documented unsupported" assertion)**  
The **"Or documented unsupported (allowed request types)"** test passes only when the request’s operation category is marked as unsupported for the current provider in **`provider-capabilities.json`** (`providers.<name>.<operation> === false`). The request name is mapped to one of: `chat_completions`, `chat_completions_with_tools`, `text_completion`, `responses`, `responses_with_tools`, `count_tokens`, `batch_create`, `batch_create_file`, `batch_list`, `batch_retrieve`, `batch_cancel`, `batch_results`, `file_upload`, `file_batch_input`, `file_list`, `file_retrieve`, `file_delete`, `file_content`, `container_create`, `container_list`, `container_retrieve`, `container_delete`, `container_file_create`, `container_file_create_reference`, `container_file_list`, `container_file_retrieve`, `container_file_content`, `container_file_delete`, `embedding`, `speech`, `transcription`, `list_models`, `image_generation`, `image_variation`, `image_edit`, `video_generation`, `video_retrieve`, `video_download`, `video_delete`, `video_list`, `video_remix`, `rerank`. These match the operation types in `core/schemas/bifrost.go` (e.g. `FileUploadRequest`, `ContainerFileContentRequest`). **`provider-capabilities.json` is the single source of truth:** the V1 run script (`run-newman-tests.sh`) loads it at run time and passes it to Newman as globals; the collection does not define or embed `provider_capabilities`.

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
- **Embedded variables**: `execution_order` (JSON array of request names in depth-first order) and `request_to_operation` (JSON map of request name → operation category). For the **V1** collection, `provider_capabilities` is **not** embedded: it is loaded from `provider-capabilities.json` at run time by `run-newman-inference-tests.sh` and passed to Newman as globals.

**Config file**: `provider-capabilities.json` in this directory is a map per provider of capability flags (e.g. `chat_completions`, `embedding`, `batch`) to booleans. It is the single source of truth for which operations each provider supports (aligned with `core/providers/*/provider.go` returning `NewUnsupportedOperationError`).

**Updating capabilities or request mappings**:

1. **Change provider support**: Edit `provider-capabilities.json` and set each capability to `true` or `false` under `providers.<name>` (e.g. `providers.anthropic.embedding: false`). It is the only source of truth; the V1 inference run script loads it at run time (no embedded copy in the collection).
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
- `runners/run-newman-inference-tests.sh` ← `docs/openapi/run-newman-inference-tests.sh`
- `provider_config/*.postman_environment.json` and `provider_config/README.md` ← `docs/openapi/provider_config/` (if syncing from docs)
- `fixtures/*` ← `docs/openapi/fixtures/`
