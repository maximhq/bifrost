# TASK-010 — Large Payload Optimization

**Feature:** Large Payload Optimization  
**TECH Spec:** [TECH-010-large-payload.md](../TECH-010-large-payload.md)  
**Phase:** 5 (Performance)  
**Depends on:** TASK-014 (license)  
**Estimate:** 4 days  
**Assignee:** —  
**Status:** 🟢 Completed

---

## Context

Currently all request/response bodies are fully buffered in memory. This causes memory exhaustion for multimodal image uploads, long document inputs, and batch requests. The fix introduces:

1. **Disk spillover** — `StreamingBodyReader` spills to temp file when payload > 10MB
2. **Object storage offload** — payloads > threshold stored in S3/GCS with reference
3. **Chunked multipart** — streaming multipart builder for image/audio endpoints
4. **Virtual key size limits** — per-VK payload size enforcement

**Performance targets:**
- 50MB image request: < 5MB memory overhead
- 100MB file upload: > 50MB/s throughput (disk spill path)
- Temp file cleanup: < 60s after request completion

---

## Tasks

### TASK-010-01 — `StreamingBodyReader` (disk spill)

**Files to create:**
- `core/network/streaming_body.go`

**Implementation:** (see TECH-010 §3 for full code)

**Acceptance criteria:**
- [ ] Payload ≤ threshold (10MB default): stays in memory, zero disk activity
- [ ] Payload > threshold: spills to temp file after buffer fills
- [ ] Known large payload (Content-Length > threshold): goes directly to disk (no memory buffer)
- [ ] `Cleanup()` removes temp file; called with `defer` in request handler
- [ ] Temp directory configurable via `BIFROST_TEMP_DIR` env (default: `os.TempDir()`)
- [ ] Thread-safe: multiple goroutines can call `Reader()` concurrently (after write is complete)

---

### TASK-010-02 — `framework/payloadstore` package

**Files to create:**
- `framework/payloadstore/store.go` — `PayloadStore` interface + `PayloadMeta`
- `framework/payloadstore/local.go` — `LocalPayloadStore` (temp file, for dev)
- `framework/payloadstore/s3.go` — `S3PayloadStore` (AWS S3)
- `framework/payloadstore/dedup.go` — `DeduplicatingPayloadStore` wrapper
- `framework/payloadstore/config.go` — `PayloadStoreConfig`

**Acceptance criteria:**
- [ ] `S3PayloadStore.Put()` streams upload without loading full payload into memory
- [ ] `DeduplicatingPayloadStore`: same content (by SHA-256 hash of first 64KB) → same storage reference
- [ ] `GetPresignedURL()` returns time-limited URL for client-side download
- [ ] `Cleanup(olderThan)` deletes objects older than TTL (called by background goroutine)
- [ ] `LocalPayloadStore` used when `PayloadStoreConfig.Type="local"` (default for testing)

---

### TASK-010-03 — Large payload middleware

**Files to create:**
- `transports/bifrost-http/lib/payload.go` — `LargePayloadMiddleware`

**Acceptance criteria:**
- [ ] Request body > `MaxRequestSize` (100MB default) → immediate `413 Payload Too Large`
- [ ] Request body > `Threshold` → set `large_payload_mode=true` in request context
- [ ] When `large_payload_mode=true`: logging plugin truncates body to 1KB in logs
- [ ] Middleware integrated into the handler pipeline for all inference endpoints

---

### TASK-010-04 — Streaming multipart builder

**Files to modify:**
- `core/network/multipart.go` — add `StreamingMultipartBuilder`

**Implementation:** (see TECH-010 §6 for full code)

**Acceptance criteria:**
- [ ] `AddFileField()` copies from `io.Reader` without buffering full content in memory
- [ ] `AddField()` synchronous (string values are small)
- [ ] `Close()` waits for async file copy to complete and returns any error
- [ ] `ContentType()` returns correct `multipart/form-data; boundary=...` header value
- [ ] Existing multipart builder (non-streaming) not modified (backward compat)

---

### TASK-010-05 — Virtual key payload size limits

**Files to modify:**
- `framework/configstore/tables/governance.go` — add `MaxPayloadSize *int64` to `VirtualKeysTable`
- `plugins/governance/precheck.go` — add `checkPayloadSize()` in `PreLLMHook`
- `transports/bifrost-http/handlers/governance.go` — VK CRUD accepts `max_payload_size` field
- Migration file — `ALTER TABLE virtual_keys ADD COLUMN max_payload_size BIGINT`

**Acceptance criteria:**
- [ ] VK without `max_payload_size` → uses global threshold
- [ ] VK with `max_payload_size` → enforced in governance `PreLLMHook`
- [ ] Exceeded limit → `413` response with error message citing the VK limit
- [ ] `estimateRequestSize()` calculates payload size from content (base64-decoded if needed)

---

### TASK-010-06 — Background temp file cleanup

**Files to create:**
- `core/network/cleanup.go` (or inline in `streaming_body.go`) — `StartPayloadCleanup(tempDir, maxAge)`

**Acceptance criteria:**
- [ ] Background goroutine scans temp dir every 30s
- [ ] Files matching `bifrost-payload-*` older than `maxAge` (default: 60s) deleted
- [ ] Cleanup goroutine started at server bootstrap
- [ ] Cleanup does not delete files still open (check via `os.Stat` modification time)

---

### TASK-010-07 — Payload configuration API

**Files to create:**
- `transports/bifrost-http/handlers/payload.go`

**Endpoints:**
```
GET  /api/payload/config        — current threshold, max size, store type (admin+)
PUT  /api/payload/config        — update config (super_admin)
GET  /api/payload/stats         — storage usage, item count, dedup hit rate
DELETE /api/payload/cleanup     — trigger manual cleanup of expired payloads (operator+)
```

**Acceptance criteria:**
- [ ] All endpoints require `large_payload` feature enabled
- [ ] Stats endpoint returns `{total_size_bytes, item_count, dedup_hit_rate_pct}`
- [ ] Config changes take effect without restart (hot reload)

---

## Definition of Done

- [ ] All subtasks complete
- [ ] Performance test: 50MB payload processed with < 5MB heap growth (verified with `runtime.ReadMemStats`)
- [ ] Performance test: 100MB upload to `LocalPayloadStore` achieves > 50MB/s throughput
- [ ] Unit test: `StreamingBodyReader` spills to disk at correct threshold
- [ ] Unit test: `DeduplicatingPayloadStore` returns same reference for identical content
- [ ] Integration test: VK with `max_payload_size=1MB` → 2MB request rejected with 413
- [ ] `make build` passes
