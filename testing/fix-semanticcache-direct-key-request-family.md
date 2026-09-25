# fix/semanticcache-direct-key-request-family - Test Contract

## Functional Behavior

- Cache entries for chat completions, Responses, text completions, embeddings, speech, transcriptions, and image generation must be isolated by request family, even with the same cache key, provider, model, normalized input, and other parameters.
- Streaming and nonstreaming variants of the same endpoint family keep the same family. The existing `stream` parameter keeps their entries separate. WebSocket Responses belongs to the Responses family.
- Repeating an identical request within one family must still hit the direct cache. A request from another family must miss and reach the provider, in either call order.
- Semantic similarity search must select entries only from the requested family. Entries written before this key change must not be read, and an entry with an incompatible response shape must be treated as a miss.
- The fix must work with existing vector store namespaces without a schema migration or new configuration.

## Unit Tests

- Test the complete supported `RequestType` to family mapping, including stream variants and unsupported types.
- Test direct cache IDs for all supported family pairs with otherwise identical key inputs, in both orders, and confirm same-family stability and pre-fix ID isolation.
- Test that a cached response with a mismatched family, including a stream, is a miss.

## Integration / Functional Tests

- Exercise `PreLLMHook` and `PostLLMHook` with an in-memory store: same-family replay hits and chat/Responses cross-family replay misses in both directions and stream modes.
- Exercise semantic lookup with a deterministic embedding executor or store double: strict filters include the family through `params_hash`, and same-family similarity still hits.

## Smoke Tests

- Run the semanticcache package tests and verify no regressions.
- Format changed Go files and run `git diff --check`.

## E2E Tests

- Run the existing local gateway reproduction for `/v1/chat/completions` and `/v1/responses` with the same `x-bf-cache-key` in both call orders, with and without `stream`. The second family must call the capture upstream and return its own response dialect.

## Manual / cURL Tests

- Use the existing local gateway and capture-upstream harness in `kairo/transcripts/086/` to send the two endpoints with the same key. Check the saved client and forwarded HTTP bytes and count upstream calls.
