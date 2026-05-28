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
- Pooled objects must have every field reset before returning to a pool.
- Channel lifecycle changes must preserve the ProviderQueue atomic closing flag plus `sync.Once` close/signal pattern.
- Plugin pre-hooks run in registration order; post-hooks run in reverse order.
- `AllowedRequests == nil` means all operations are allowed. Non-nil values only allow explicitly true fields.
- Do not set Bifrost reserved context keys from handlers or plugins.
- `transports/config.schema.json` is the source of truth for config fields.
- Whenever a migration is added or changed, verify it avoids deadlocks and long blocking locks on large tables. Index creation in migrations must be concurrent where the database supports it.
- If a migration cannot be rolled back, explicitly flag it as non-rollbackable.
- Alert when frontend code uses browser crypto APIs such as `crypto`, `crypto.subtle`, or `globalThis.crypto` because they can fail in non-HTTPS contexts, except localhost and other secure contexts.
- UI changes must preserve `data-testid` attributes used by E2E tests.
- E2E API payloads must be constructed directly as object literals. Do not serialize through maps, Records, `Object.fromEntries`, or JSON round trips.

## Path-Specific Checks

For `core/providers/**`:

- Verify error converters populate provider, model, and request type metadata.
- Verify unsupported operations return the repository's standard unsupported-operation error behavior.
- For new provider operations, check all providers and the provider interface are updated consistently.
- For streaming responses, check idle timeout handling and chunk accumulation behavior.

For `core/mcp/**`:

- Check tool filtering across global, client, tool, and per-request filters.
- Review agent loops for bounded depth, deterministic tool execution behavior, and safe error propagation.

For `framework/streaming/**`:

- Validate delta copying, accumulator ownership, and final response construction.
- Watch for aliasing bugs where chunks or response fields can be mutated after being published.

For `transports/bifrost-http/handlers/**`:

- Verify handler changes keep SDK integration behavior compatible with OpenAI, Anthropic, Bedrock, Google GenAI, LangChain, LiteLLM, and PydanticAI where relevant.
- Check request parsing, error status codes, and middleware ordering.

For `plugins/governance/**`:

- Review budget, rate-limit, virtual key, and RBAC paths for fail-closed behavior where security is involved.
- Check that fallback and retry attempts do not double-count or undercount usage unexpectedly.

For `ui/**`:

- Check interactive workflows for loading, empty, error, and success states.
- Alert on browser crypto API usage because it requires secure contexts and can fail outside HTTPS, except localhost and other secure contexts.
- Preserve existing component conventions and avoid broad visual rewrites in unrelated PRs.

For `docs/**`:

- Check examples against actual config schema, handler names, provider support, and Make targets.
- New docs pages should be included in `docs/docs.json` when they are intended to be navigable.
