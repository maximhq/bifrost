# Bifrost Reviewer Memory

## Pool Patterns
- `core/pool/pool_debug.go` uses `sync.Map` with `uintptr` keys for object tracking
- `BifrostError` pool: `AcquireBifrostError()` always acquires a fresh `BifrostErrorField` from its own pool
- `ReleaseBifrostError()` releases nested `ErrorField` before putting parent back
- Bedrock uses raw `sync.Pool` (not `core/pool`) for `BedrockConverseResponse`

## Provider Patterns
- Bedrock: two endpoint types -- `bedrock-runtime` (inference) and `bedrock` (control plane)
- `BaseURL` replaces the full host; providers append paths to it
- Most providers use `fasthttp`; Bedrock uses `net/http` due to AWS SDK

## NetworkConfig
- `MaxRetries` default is 0 -- `CheckAndSetDefaults` intentionally skips it (zero is valid)
- `DefaultRequestTimeoutInSeconds` = 30, `RetryBackoffInitial` = 500ms, `RetryBackoffMax` = 5s

## Handler Patterns
- `addProvider` uses pointer types for optional configs; `updateProvider` uses value types
- Validation runs BEFORE CheckAndSetDefaults in both handlers
