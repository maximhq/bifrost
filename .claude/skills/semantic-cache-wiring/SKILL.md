---
name: semantic-cache-wiring
description: Use when a change touches semantic cache behaviour (the semantic_cache plugin and its config, what a request writes, hits or misses and what a hit replays, direct and semantic mode, the x-bf-cache-* request headers, extra_fields.cache_debug and the x-bifrost-cache-* response headers, identity scoping, the /api/cache clear endpoints, the vector stores the plugin runs on, cache fields on log rows, logs filters and stats) or when asked to add semantic cache tests. Lists the cases each kind of semantic cache change needs, where they go in the semantic cache wiring suite, how to write them and how to run them. Semantic cache cases go in that suite, never in the provider harness.
---

# Semantic cache wiring suite

Every end-to-end semantic cache case lives in the semantic cache wiring suite: `tests/e2e/api/runners/build-semantic-cache-wiring.mjs` generates `tests/e2e/api/collections/bifrost-semantic-cache-wiring.postman_collection.json`, which is committed. Edit the generator, regenerate, and commit both; never hand-edit the JSON. The generator embeds the assertion library (`tests/e2e/api/runners/lib/semantic-cache-wiring.mjs`) in the collection as `scw_lib`, so a library change needs a regenerate too. `tests/e2e/api/runners/individual/run-newman-semantic-cache-wiring-tests.sh` boots one clean Bifrost per vector store (Redis and Weaviate, concurrently by default; pgvector on request) on its own SQLite config and logs stores, except that the pgvector run logs to a fresh Postgres database, and runs the collection against each; CI runs it (Redis and Weaviate) from `.github/workflows/scripts/test-api-integrations.sh`. The run is live and paid (OpenAI, Anthropic, Gemini, Cohere, OpenRouter and Bedrock): run the offline checks in section 5 yourself and hand the live commands to the user.

Read AGENTS.md "Every semantic cache change ships with semantic cache wiring cases" first. Cite names, not line numbers: the helpers below are names in the generator and in `tests/e2e/api/runners/lib/semantic-cache-wiring.mjs` on `dev`.

## 1. What goes where

| What changed | Test it in |
|---|---|
| Semantic cache behaviour a client can observe: whether a request is written, hits or misses, what a hit replays, `cache_debug`, the `x-bifrost-cache-*` headers, the plugin and cache admin API, the cache fields on log rows | this suite, plus Go unit tests |
| A function's logic, a race, an error path no request can reach | Go unit tests beside the code: `plugins/semanticcache/*_test.go`, written against its fake stores (such as `vectorRequiringStore` and `panickingAddStore` in `plugin_paths_test.go`) so they run with no key and no store (section 5); `transports/bifrost-http/lib/semantic_cache_config_test.go`, `transports/bifrost-http/handlers/cache_test.go` and `plugins_test.go` |
| A vector store's own behaviour (search, scores, filters, delete, namespaces, dimensions) | `framework/vectorstore/<store>_test.go` against `tests/docker-compose.yml` (AGENTS.md gotcha 19), plus this suite on Redis and Weaviate |
| The suite's assertion library | `tests/e2e/api/runners/lib/semantic-cache-wiring.test.mjs` |
| The dashboard's caching settings | Playwright (`tests/e2e/features/config/config-caching.spec.ts`) |
| How a provider converts a request or response the cache does not answer | the provider harness (`.claude/skills/harness-test-writer/SKILL.md`) |

A change that reaches both a provider's conversion and the cache needs both. `tests/semanticcache/` is an older Go suite that CI does not run: add no new cases there.

## 2. The axes a case is placed on

Every semantic cache change sits on some of these axes. Section 3 says which ones a kind of change must cover; cover every value the change can reach, not only the one named in the issue.

- **Cache mode:** direct-only (`dimension: 1` and no `provider`; `DIRECT()`), semantic (an embedding `provider`, `embedding_model` and `dimension` above 1; `SEMANTIC()`), and the per-request `x-bf-cache-type` (`direct`, `semantic`, anything else).
- **Vector store:** Redis and Weaviate, which the runner boots by default and CI runs, and pgvector, which it boots on request (`--store pgvector`, or `--store all` for the three). Every store gives the same answer, so a row asserts one expectation on each. The plugin also runs on the other stores in `framework/vectorstore` (Qdrant, Pinecone, Chromem), which the runner cannot boot yet (section 6).
- **Request type:** chat completions, text completions, Responses, embeddings, speech, transcription and image generation, each streamed and not where the type streams (`ROUTES`). A request the plugin leaves alone (`/v1/responses/input_tokens`, an error answer) asserts `outcome: "none"`.
- **Route:** `/v1/*`, and the SDK routes `/openai`, `/anthropic`, `/genai`, `/cohere` and `/bedrock`. Only `/v1` and `/openai` bodies carry `cache_debug`; on the others the outcome is read from the headers (`source: "headers"`, `noBodyCacheDebug: true`) and from the log row.
- **Where the outcome shows:** `extra_fields.cache_debug` on a JSON body, the final chunk of a stream (`stream.finalOnly`), the `x-bifrost-cache-*` headers (`headersMatch`), the log row reached through `x-bifrost-request-id` (`saveReq`, `logRow`, `cdEquals`), the `cache_hit_types` logs filter and `/api/logs/stats`.
- **Outcome:** `miss` (looked up and written), `direct`, `semantic`, and `none` (no lookup: no `cache_debug` and no cache headers).
- **What makes two requests share an entry:** the cache key (`x-bf-cache-key`, `default_cache_key`), the model and provider (`cache_by_model`, `cache_by_provider`), every request parameter, the system prompt (`exclude_system_prompt`), tools and `tool_choice`, attachments, message normalization (letter case, surrounding whitespace, unicode), conversation length (`conversation_history_threshold`) and the earlier turns, `previous_response_id`, stream or not, the request type, the caller's identity, and the namespace.
- **Per-request headers:** `x-bf-cache-key`, `x-bf-cache-ttl`, `x-bf-cache-threshold`, `x-bf-cache-type` and `x-bf-cache-no-store` (`H.*`), each with valid, edge and invalid values.
- **Caller identity:** virtual key A, virtual key B and no virtual key (`H.vk`), with `disable_identity_scoping` off (the default) and on.
- **Plugin state:** created, updated (a PUT merges over the stored config), disabled, deleted and re-created, all through `/api/plugins`. A plugin loaded from `config.json` at boot is not covered yet (section 6).
- **Entry lifecycle:** TTL from the plugin and from the header, clear by `cache_id`, clear by key, a namespace switch, a dimension change.
- **Embedding provider state:** healthy, an invalid key, deleted and re-created.

## 3. The cases each kind of change needs

Each block lists what the cases must prove and the folders they go in (P preconditions, M plugin management, D direct cache, X other request types, T streams, H request headers, C clear endpoints, K response headers, G identity scoping, S semantic mode, O logs). For every kind of change, also cover direct-only and semantic mode. Where the change has a switch (a config field or a request header), cover it on and back off, and a config change taking effect on the next request without a restart. Where the change reaches a request type that streams, cover a stream and a non-stream request; a stream that section 6 lists as not covered today is added to the suite, or the PR says why it is out of reach.

**A new or changed plugin config field** (M, plus the folder where it has its effect):
- Its default when absent; set; cleared with `null` on a PUT; each invalid value's exact 400 message from both `POST /api/plugins` and `PUT /api/plugins/semantic_cache`, with no plugin left behind after a refused create; GET echoing it.
- Its effect: a write and a hit with it on, and the row that shows the difference with it off.
- Outside the suite: `transports/config.schema.json`, the Helm chart's values and schema, the dashboard's form validation, and `ValidateSemanticCacheConfig` with its unit test.

**An input to the cache key**: a request parameter, message normalization, tools, the system prompt, attachments, the model or provider (D, S):
- For each input that must keep entries apart: a write, a hit on the same request, and a miss with only that input changed (`distinct`). For each input that must not (reordered tools, letter case, surrounding whitespace): a hit on the first entry (`same`).
- Both modes, a stream, and Responses and text completions where the input exists there.
- In semantic mode, the paraphrase too: with the input unchanged it is a semantic hit, with the input changed it misses.

**A request type, a route, or what a hit replays** (X, K, T, S):
- A write, a hit and a miss on another input, on that type and on its stream where the type streams.
- The replay equals the original: content (`fields` with `save` and `equals`), model and usage, and binary payloads (images, audio, vectors) by `fingerprint`.
- Where the outcome shows on that route: body and headers agreeing (`headersMatch`) on `/v1` and `/openai`, headers only and no body `cache_debug` on the other SDK routes, and the log row (`logRow`).
- The same text on another request type misses (D22).
- In semantic mode, a paraphrase is a semantic hit, or the type takes the direct path and the row says so (embeddings, S20).
- A request that is not cached sends no `cache_debug` and no cache headers (K8).

**Streams** (T, and the stream rows of X, G and S):
- The replay has the same chunk count, event types, texts, finish reasons, tool-call fragments and usage (`stream.save`, `stream.compare`); `cache_debug` rides only on the final chunk (`finalOnly`); the `[DONE]` terminator where the route sends one (`done`); the headers match the final chunk (`headersMatch: "stream"`).
- A no-store stream writes nothing; a tool-call stream is written only with `cache_tool_call_responses`.

**A per-request header** (H, and S for the threshold):
- Each valid value; letter case where the value is a boolean; edge and invalid values (0, negative, above 1, bare numbers, garbage) and what each falls back to; the header against `default_cache_key`; the header on a write and on a read.

**Semantic matching**: embeddings, the threshold, similarity, conversation handling (S):
- A paraphrase is a semantic hit carrying `similarity` at or above `threshold`, `provider_used`, `model_used` and `input_tokens`; an unrelated prompt misses; a near-miss pair stays under the plugin's default threshold on every store (S29); the threshold header's bounds (S4 to S6).
- A follow-up turn matches its earlier turns exactly and only its newest message by meaning, on chat and Responses (S30).
- A request that offers tools (in `tools`, or through `web_search_options` on chat), or carries a tool call or a tool result, is matched exactly only, on chat and Responses, streamed and not: its write runs no embedding, the same request is a direct hit, a paraphrase misses, and a semantic-only lookup (`x-bf-cache-type: semantic`) is not cached and the same request is a direct miss afterwards (S22, S31). That it writes no entry under any id is a store fact, asserted in the plugin's Go test.
- An embedding failure (an invalid key, a deleted provider) still answers and writes, and leaves a `semantic_cache` warning in the log row's `plugin_logs` (S26, S27).

**Identity scoping** (G):
- Virtual key A, virtual key B and no virtual key each get their own entry, in direct and semantic mode, streamed and not, under a header key and under `default_cache_key`.
- With scoping off, all three share one entry; turning it back on separates them again.
- Clearing by key clears every identity's entry; clearing by `cache_id` clears only that one.

**The plugin and cache admin API, TTL and entry lifecycle** (M, C, H):
- Create, conflict, read, list, PUT merge, disable, delete and re-create, with exact statuses and messages; the clear endpoints refused while the plugin is disabled or deleted; entries surviving disable, enable, delete and re-create.
- Clear by `cache_id` and by key, including a URL-encoded key with spaces and colons; unknown IDs and keys; TTL expiry from the plugin and from the header.

**Vector store behaviour** (M, S, and `framework/vectorstore`):
- Every row that touches the store passes on Redis, on Weaviate and on pgvector with the same expectation; similarity is cosine on every store (the cosine check in each store's test file, built with `unitVectorAtCosine`).
- A dimension mismatch answers 400 and keeps the previous config (M18); a namespace switch isolates entries (S24); clearing an entry that is not there succeeds (C3).

**`cache_debug`, the response headers and logs** (K, O):
- One header per `cache_debug` field, equal to the body (`headersMatch: "all"`), on every inference route that ran a lookup, and none when no lookup ran.
- The log row's `cache_debug` equals the response's (`cdEquals`); the `cache_hit_types` filter lists hits and not misses; stats count both hit types.

## 4. Writing a case

- **Folder and ID:** pick the folder from section 3. The ID is the folder letter plus the next free number (`grep -oE '\bS[0-9]+\b' tests/e2e/api/runners/build-semantic-cache-wiring.mjs | sort -V -u | tail -1`), and every request of the case starts its name with it: `"S31 <what this step checks>"`. Names are unique; the generator throws on a duplicate.
- **Helpers:** `write` (a keyed request that must miss), `hit` (must hit: polled with backoff and sent with `x-bf-cache-no-store`, so a read that misses never writes the entry it waits for), `miss` (must miss, also no-store), `infer` (anything else, such as `outcome: "none"`), and `api`, `putPlugin`, `getPlugin`, `clearId`, `clearKey`, `logRow`. Bodies come from `chat`, `chatSystem`, `chatMessages`, `chatStream`, `responses`, `text`, `embedding`, `image` and `withImage`; semantic rows take their prompts from `PAIRS` (paraphrases at 0.9 cosine or more, unrelated prompts under 0.5), so 0.85 is never borderline.
- **The spec:** every request asserts through `scw.run(spec)`. `outcome` checks every `cache_debug` field that outcome implies; `save`, `same` and `distinct` compare `cache_id`s; `fields`, `fingerprint`, `stream`, `headersMatch`, `source`, `pluginLogs` and `ids` cover the rest. A new kind of check goes into the library with a case in `semantic-cache-wiring.test.mjs`, never into one request's script.
- **Isolation:** every cache key goes through `H.key`, which adds the run ID, and every namespace through `NS`, which adds the run's suffix, so runs never read each other's entries. A row that must miss runs only after the entry it is isolated from was seen to hit. The only fixed waits are TTL rows (the TTL plus two seconds) and "nothing was written" rows (`{ wait: 2 }`).
- **Config:** a row that changes the plugin calls `putPlugin(..., DIRECT({...}))` or `SEMANTIC({...})` and restores the folder's baseline afterwards, so later rows never inherit it. `RESET` names every config field with `null`, because a PUT merges over the stored config.
- **Providers:** a row that needs a provider the run does not create adds it to `STANDARD_PROVIDERS` or `CUSTOM_PROVIDERS`, and its key variable to the runner's `REQUIRED_ENV`. Keys are always `env.<NAME>`, never written into the collection.
- **Assert facts, exactly:** the status, the message (`message`), the outcome. Never return early on an error status, and never assert a 500 as the expected answer.
- **A known bug:** the library has no skip. Add the row in the PR that fixes the bug, asserting the intended answer.
- **Red before green:** get your red-then-green loop from a unit test against a fake store, which you can run (section 5). The row itself only runs live, so the handed-over block names the rows that should fail on the code before the change and pass after.
- **Regenerate:** `node tests/e2e/api/runners/build-semantic-cache-wiring.mjs` rewrites the collection; on unchanged code it reproduces the committed file byte for byte, so `git diff` shows only your rows.

## 5. Running

Run these yourself; none needs a provider key:

```bash
# the plugin tests you wrote, on its fake stores (no key, no store)
(cd plugins/semanticcache && go test -count=1 -v -run '<TestName>' .)
# the transport tests that cover the cache endpoints and the plugin config
(cd transports/bifrost-http && go test ./lib/... ./handlers/... -run '<TestName>')
# a vector store you changed (needs the compose stack, no key)
docker compose -f tests/docker-compose.yml up -d && (cd framework && go test ./vectorstore/...)

# the assertion library (offline)
node tests/e2e/api/runners/lib/semantic-cache-wiring.test.mjs

# the generator: regenerates the collection, fails on a duplicate request name
node tests/e2e/api/runners/build-semantic-cache-wiring.mjs
```

Most of the plugin package's existing tests are not offline. `NewTestSetup` and its variants build a real Bifrost client with `env.OPENAI_API_KEY` and a real vector store (Weaviate on `localhost:9000` by default), and the test calls `t.Skipf` when the store is unreachable or the upstream call fails. Without the key or the store, `go test ./...` in `plugins/semanticcache` still prints `ok`, with most of its tests skipped. So write a new unit test on a fake store, run it by name, and read the `-v` output: a `--- SKIP` is reported as skipped, never as passed.

The whole plugin package and the live suite are the user's to trigger, since both need provider keys and cost money: end the report with both, the suite scoped to the folders the change reaches, with every placeholder filled in. The smallest scope is a folder (`--folder`, repeatable, with its exact name); the setup folder and P always run, and O also runs D, X and S, whose saved requests it reads. The runner gives each store it boots the next port from `--port`, in the order Redis, Weaviate, pgvector, so check those are free; the pgvector run also needs `psql` and the compose Postgres (or `PGVECTOR_HOST`/`PGVECTOR_PORT`/`PGVECTOR_USER`/`PGVECTOR_PASSWORD`), and it refuses to start unless `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, `COHERE_API_KEY`, `OPENROUTER_API_KEY`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `AWS_REGION` are set.

```bash
docker compose -f tests/docker-compose.yml up -d
# the whole plugin package: needs OPENAI_API_KEY; every --- SKIP line is a test that did not run
(cd plugins/semanticcache && set -o pipefail && go test -count=1 -v ./... 2>&1 | grep -E -- '--- (FAIL|SKIP)|^(ok|FAIL)')
# build from the local modules: a plain `make build` sets GOWORK=off and uses the published plugin
[ -f go.work ] || make setup-workspace
make build LOCAL=1
lsof -nP -iTCP:<port> -sTCP:LISTEN; lsof -nP -iTCP:<port+1> -sTCP:LISTEN
tests/e2e/api/runners/individual/run-newman-semantic-cache-wiring-tests.sh --binary tmp/bifrost-http --port <port> --store both --folder "<folder>"
```

The folder names are `M: plugin management`, `D: direct cache on chat completions`, `X: every other request type`, `T: streams`, `H: per-request headers`, `C: cache clear endpoints`, `K: cache response headers`, `G: identity scoping`, `S: semantic mode` and `O: logs`. Report the rows you added and the folders the user should run, red before the change and green after. A change that reaches several folders (a cache key input, a config field, a request type) names every folder it reaches.

## 6. Adding a new value to an axis

A new request type, route, config field, request header, `cache_debug` field or response header, vector store, caller identity or cache key input is a new value on one of the axes in section 2. Its own new rows are not enough:

1. **Add it to every existing row that enumerates the axis.** Grep for an existing value of the axis to find them: a request type in `ROUTES` and in the X, T, K, S and O rows (S30 loops over `["chat", "responses"]`); a config field in `RESET`, `DIRECT` or `SEMANTIC` and the M validation rows; a request parameter in D17's `params` list; a `cache_debug` field or response header in the library's `HEADER_FIELDS` (and `EMBEDDING_FIELDS` when only an embedding sets it), with a library test; a route in `ROUTES` and K; an identity in G's `vk(...)` rows. A row that deliberately leaves the new value out says why in a comment beside it.
2. **Update this skill.** Add the value to its axis in section 2 and to every block in section 3 that names that axis. A new kind of change gets its own block in section 3. Keep the AGENTS.md rule and the review configs (`.coderabbit.yaml`, `.greptile/`) in step when the rule itself changes.
3. **Extend the runner when the suite cannot produce the setup.** A new vector store needs an entry in the runner's `vector_store_block`, a `--store` value, its service in `tests/docker-compose.yml`, and, if CI is to run it, the CI job's egress allowlist in `.github/workflows/release-pipeline.yml`. A store that needs another logs store gets it in `logs_store_block`, as pgvector does. A plugin loaded from `config.json` needs a `plugins` block in the runner's `write_config`.

Not covered today, so a change that reaches one of these either extends the suite for it or says in the PR why it is out of reach: the vector stores other than Redis, Weaviate and pgvector (and pgvector in CI, which runs Redis and Weaviate); the plugin loaded from `config.json`; streamed speech, transcription and image generation; Responses and text completion streams in semantic mode; speech and transcription in semantic mode; Responses over WebSocket; the LiteLLM, LangChain and PydanticAI routes; fallbacks; the vector store going away mid-run.
