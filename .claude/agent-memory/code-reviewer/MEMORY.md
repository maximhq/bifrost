# Bifrost Code Review Memory

## Pool Patterns
- `core/pool/pool_debug.go` uses `sync.Map` with `uintptr` keys for tracking -- address reuse is expected
- `BifrostError` pool acquires a separate `BifrostErrorField` from its own pool on every `AcquireBifrostError()`
- `ReleaseBifrostError` releases the nested `ErrorField` back to `bifrostErrorFieldPool` before putting parent back
- Bedrock uses raw `sync.Pool` (not `core/pool`) for `BedrockConverseResponse` -- inconsistent with other pools

## Provider Patterns
- Bedrock has two endpoint types: `bedrock-runtime` (inference) and `bedrock` (control plane/list models)
- `BaseURL` override replaces the full host -- providers append paths to it
- AWS SigV4 signing is done in `signAWSRequest()` in bedrock.go
- Most providers use `fasthttp`, but Bedrock uses `net/http` due to AWS SDK requirements

## NetworkConfig Defaults
- `MaxRetries` default is 0 -- CheckAndSetDefaults intentionally does NOT set MaxRetries (zero is valid)
- `DefaultRequestTimeoutInSeconds` = 30, `DefaultRetryBackoffInitial` = 500ms, `DefaultRetryBackoffMax` = 5s
- `DefaultConcurrency` = 1000, `DefaultBufferSize` = 5000

## Handler Patterns (transports/bifrost-http/handlers/providers.go)
- `addProvider`: payload uses pointer types for optional configs (`*NetworkConfig`, `*ConcurrencyAndBufferSize`)
- `updateProvider`: payload uses value types (non-pointer `NetworkConfig`, `ConcurrencyAndBufferSize`)
- Validation runs BEFORE CheckAndSetDefaults in both handlers

## Config File
- `examples/configs/loadtest/config.json` -- missing trailing newline (no newline at EOF)
