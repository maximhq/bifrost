# Bifrost Review Rules

## Review Priorities

Focus on correctness, regressions, concurrency bugs, streaming behavior, data races, API compatibility, and missing tests. Avoid comments that are only style preferences unless the style issue creates a real maintenance or behavior risk.

When reviewing a PR, first identify which Bifrost area is touched:

- `core/`: provider lifecycle, inference routing, MCP, schemas, provider implementations, pooling.
- `framework/`: persistence, streaming accumulators, vector stores, tracing, encryption.
- `transports/`: HTTP server, handlers, SDK integrations, config schema.
- `plugins/`: governance, logging, telemetry, semantic cache, compatibility plugins.
- `ui/`: React workspace interface and shared components.
- `tests/e2e/`: Playwright end-to-end tests.
- `docs/`: Mintlify documentation.

## Repository Rules

- Cross-module imports work locally through `go.work`, but releaseable module changes still need explicit `require` entries in the relevant module.
- Apply standard Go review practices where they affect correctness or maintainability: clear ownership, small interfaces, explicit error handling and wrapping, context propagation and cancellation, bounded goroutines/channels, race-safe shared state, deterministic tests, and table-driven coverage for behavior changes.
- Apply Go security practices: do not log secrets or sensitive request/response bodies by default, use constant-time comparison for secrets/tokens, prefer standard-library or well-reviewed crypto over custom crypto, validate all untrusted input, enforce timeouts and size limits, and avoid unsafe reflection or `unsafe` unless clearly justified.
- Apply general security review for auth, authorization, and data handling: fail closed on ambiguous permissions, enforce least privilege, redact sensitive values in logs/errors, prevent SSRF/path traversal/header injection, avoid SQL injection via parameterized queries, use safe file permissions, and check new dependencies for supply-chain and vulnerability risk.
- For provider-level tests, prefer `make test-core` over bare `go test` because the Make target runs the shared provider scenario suite.
- Provider converters must remain pure transformation functions with no HTTP calls, logging, or side effects.
- OpenAI provider converter changes affect OpenAI-compatible providers that delegate to OpenAI helpers, including Groq, Cerebras, Ollama, Perplexity, OpenRouter, Parasail, Nebius, xAI, and SGL.
- Streaming provider paths must use the provider streaming client, not the unary client.
- Provider HTTP code uses `fasthttp` acquire/release patterns. Verify requests, responses, scanner buffers, and streaming responses are released on every success and error path.
- Provider interface changes are high-blast-radius: check all providers, request type constants, allowed-request gates, handlers, integrations, config schema, UI constants, docs, and tests.
- Pooled objects must have every field reset before returning to a pool.
- Channel lifecycle changes must preserve the ProviderQueue atomic closing flag plus `sync.Once` close/signal pattern.
- Hot provider/plugin config reloads should build new slices/maps and atomically swap pointers. Do not mutate shared slices in place.
- Plugin pre-hooks run in registration order; post-hooks run in reverse order.
- Plugin errors are logged as warnings, not returned to callers. Pre-hooks can short-circuit; post-hooks can recover from errors or invalidate responses. Fallbacks re-run the full plugin pipeline.
- `AllowedRequests == nil` means all operations are allowed. Non-nil values only allow explicitly true fields.
- Do not set Bifrost reserved context keys from handlers or plugins.
- `transports/config.schema.json` is the source of truth for config fields.
- Config duration fields serialized as JSON must use milliseconds even when represented as `time.Duration` in Go. Mutable config maps such as `ExtraHeaders` must be defensively copied.
- Whenever a migration is added or changed, verify it avoids deadlocks and long blocking locks on large tables. Index creation in migrations must be concurrent where the database supports it.
- If a migration cannot be rolled back, explicitly flag it as non-rollbackable.
- HTTP handlers are dependency-injected structs with `RegisterRoutes`, and route middleware should be composed through `lib.ChainMiddlewares`.
- SDK integration layers must stay drop-in compatible with OpenAI, Anthropic, Bedrock, Google GenAI, LangChain, LiteLLM, and PydanticAI request/response shapes where relevant.
- MCP tool availability is filtered globally, by client, by tool, and per request; all levels must continue to agree before a tool is exposed.
- Alert when frontend code uses browser crypto APIs such as `crypto`, `crypto.subtle`, or `globalThis.crypto` because they can fail in non-HTTPS contexts, except localhost and other secure contexts.
- UI changes must preserve `data-testid` attributes used by E2E tests.
- E2E API payloads must be constructed directly as object literals. Do not serialize through maps, Records, `Object.fromEntries`, or JSON round trips.

## Live Store

Every piece of data a process keeps (configuration, runtime state, caches) lives in `framework/livestore`. AGENTS.md ("Data lives in the live store", "Writes reach memory the same way for every entity") and `.claude/skills/live-store/SKILL.md` hold the full rules. Review only code a PR adds or changes against them:

- Flag new maps, `sync.Map`s or record slices a component keeps as a cache, index or state. They belong in a live-store collection (every row of a config-database kind), cache (only what was asked for) or table (what the process writes itself), or in `tests/rawmaps/allowlist.txt` with a reason.
- In-flight working state (one request, stream or run, keyed by its ID and removed when it ends: stream accumulators, pending log entries, traces being built, running jobs) may stay in a map with an `inflight` allowlist entry. Flag one whose entry is not removed on every ending path, errors and cancellation included, and state labelled in-flight that a later request reuses.
- `Store.Collections` holds config-store collections only; derived tables several packages read go in `Store.Views`, rebuilt by a subscriber once per publish. Records hold only what their loader fills, and provider settings and keys stay separate values.
- Request-path reads go through typed handles and never modify a value read from the store.
- Delete: holders are read inside the delete's transaction; the record is removed with a `Remove` ref and no read; bound caches are evicted; holders are re-read with `lib.AdminRetry()`; a failed re-read answers 500.
- Create or update: the written rows are re-read with `lib.AdminRetry()`; on failure memory keeps the previous record and the handler answers 500.
- A partial update merges onto a database read, never onto the live record; a settings save never rewrites child rows.
- Only an explicit delete removes a record without reading it; a reload or reconcile re-reads a held record its list does not name.
- A change another process made is applied with `lib.DBLookupRetry()`.
- A write path that re-reads needs a test where the re-read fails; a delete needs a test that it removes without reading.

## Path-Specific Checks

For `core/providers/**`:

- Verify error converters populate provider, model, and request type metadata.
- Verify unsupported operations return the repository's standard unsupported-operation error behavior.
- For new provider operations, check all providers and the provider interface are updated consistently.
- For streaming responses, check idle timeout handling and chunk accumulation behavior.
- For streaming provider code, check use of `BuildStreamingClient` or `BuildStreamingHTTPClient` and `ReleaseStreamingResponse` rather than unary-client timeout behavior.

For `core/mcp/**`:

- Check tool filtering across global, client, tool, and per-request filters.
- Review agent loops for bounded depth, deterministic tool execution behavior, and safe error propagation.
- Preserve deterministic tool registration, discovery, and filtering behavior across in-process, SSE, streamable HTTP, and code-mode clients.

For `framework/streaming/**`:

- Validate delta copying, accumulator ownership, and final response construction.
- Watch for aliasing bugs where chunks or response fields can be mutated after being published.
- Preserve the pattern where streaming chunks are accumulated into a full response for post-hooks without changing already-published chunks.

For `transports/bifrost-http/handlers/**`:

- Verify handler changes keep SDK integration behavior compatible with OpenAI, Anthropic, Bedrock, Google GenAI, LangChain, LiteLLM, and PydanticAI where relevant.
- Check request parsing, error status codes, and middleware ordering.
- Configuration changes go to the database first and memory follows (see Live Store above). A create or update re-reads the written record; if that read fails, memory keeps the record's previous version and the handler answers 500 saying the write was saved but not reloaded. A delete removes its record without a read; if the holders' re-read then fails, the holders keep their previous records and the handler answers 500, while the deleted record stays removed. Do not ask for a rollback of the committed write.

For `plugins/governance/**`:

- Review budget, rate-limit, virtual key, and RBAC paths for fail-closed behavior where security is involved.
- Check that fallback and retry attempts do not double-count or undercount usage unexpectedly.
- Preserve nil-versus-empty access semantics where configured lists distinguish allow-all, deny-all, and explicit allow-list behavior.

For `ui/**`:

- Check interactive workflows for loading, empty, error, and success states.
- Alert on browser crypto API usage because it requires secure contexts and can fail outside HTTPS, except localhost and other secure contexts.
- Preserve existing component conventions and avoid broad visual rewrites in unrelated PRs.
- Reuse existing constants, page structure, shared components, and `data-testid` naming patterns before introducing one-off UI conventions.

For `docs/**`:

- Check examples against actual config schema, handler names, provider support, and Make targets.
- New docs pages should be included in `docs/docs.json` when they are intended to be navigable.
