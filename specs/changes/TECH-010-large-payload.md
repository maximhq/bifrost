# TECH-010 — Large Payload Optimization

**Feature ID:** LPAY  
**SRS Reference:** §3.21 (LPAY-01 → LPAY-07)  
**CR Reference:** CR-ENT-001, CR-ENT-002  
**Version:** 1.0 | **Date:** 2026-04-08  
**Status:** Design Ready

---

## 1. Overview

Optimize handling of large LLM payloads (multimodal images, long documents, batch requests) to prevent memory exhaustion and improve throughput. Currently all request/response bodies are buffered fully in memory.

**Target improvements:**
- `LPAY-01`: Stream request bodies through providers without full in-memory buffering
- `LPAY-02`: Object storage offload for payloads > configurable threshold (default: 10MB)
- `LPAY-03`: Chunked multipart streaming for image/audio endpoints
- `LPAY-04`: Parallel chunk upload for batch requests
- `LPAY-05`: Memory-mapped request body cache for deduplication
- `LPAY-06`: Configurable payload size limits per virtual key
- `LPAY-07`: Background payload cleanup with configurable TTL

---

## 2. Architecture Mapping

```
core/network/
├── multipart.go        (EXISTING — MODIFY) Add streaming multipart builder
└── streaming_body.go   (NEW) StreamingBodyReader — threshold-based memory/disk

framework/
├── payloadstore/       (NEW package)
│   ├── store.go        PayloadStore interface
│   ├── local.go        Local disk store (temp files)
│   ├── s3.go           AWS S3 store
│   ├── gcs.go          Google Cloud Storage store
│   └── config.go       PayloadStoreConfig

transports/bifrost-http/
├── handlers/
│   └── inference.go    (MODIFY) Switch buffering → streaming for large payloads
└── lib/
    └── payload.go      (NEW) Payload middleware — size check, offload decision
```

---

## 3. Streaming Body Threshold

```go
// core/network/streaming_body.go

const DefaultLargePayloadThreshold = 10 * 1024 * 1024  // 10MB

type StreamingBodyReader struct {
    threshold   int64
    size        int64
    reader      io.Reader
    memBuf      *bytes.Buffer     // used until threshold
    tempFile    *os.File          // used above threshold  
    onDisk      bool
    tempDir     string
}

// NewStreamingBodyReader wraps an io.Reader with transparent disk spillover
func NewStreamingBodyReader(r io.Reader, contentLength int64, threshold int64, tempDir string) *StreamingBodyReader {
    if contentLength > threshold {
        // Known large payload — go directly to disk
        f, _ := os.CreateTemp(tempDir, "bifrost-payload-*")
        return &StreamingBodyReader{reader: r, tempFile: f, onDisk: true, threshold: threshold}
    }
    return &StreamingBodyReader{reader: r, memBuf: bytes.NewBuffer(make([]byte, 0, min(contentLength, 1<<20))), threshold: threshold}
}

func (s *StreamingBodyReader) Write(p []byte) (int, error) {
    if s.onDisk {
        return s.tempFile.Write(p)
    }
    n, err := s.memBuf.Write(p)
    s.size += int64(n)
    if s.size >= s.threshold {
        // Spill to disk
        s.tempFile, _ = os.CreateTemp(s.tempDir, "bifrost-payload-*")
        s.tempFile.Write(s.memBuf.Bytes())
        s.memBuf = nil
        s.onDisk = true
    }
    return n, err
}

func (s *StreamingBodyReader) Reader() io.Reader {
    if s.onDisk {
        s.tempFile.Seek(0, io.SeekStart)
        return s.tempFile
    }
    return bytes.NewReader(s.memBuf.Bytes())
}

func (s *StreamingBodyReader) Cleanup() {
    if s.tempFile != nil {
        s.tempFile.Close()
        os.Remove(s.tempFile.Name())
    }
}
```

---

## 4. Object Storage Offload

For payloads stored beyond the request lifecycle (file uploads, cached images):

```go
// framework/payloadstore/store.go

type PayloadStore interface {
    Put(ctx context.Context, key string, r io.Reader, size int64, meta PayloadMeta) (string, error)
    Get(ctx context.Context, key string) (io.ReadCloser, *PayloadMeta, error)
    Delete(ctx context.Context, key string) error
    GetPresignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
    Cleanup(ctx context.Context, olderThan time.Duration) (int, error)
}

type PayloadMeta struct {
    ContentType  string
    OriginalSize int64
    VirtualKeyID string
    RequestID    string
    ExpireAt     time.Time
}

// framework/payloadstore/s3.go
type S3PayloadStore struct {
    client *s3.Client  // aws-sdk-go-v2
    bucket string
    prefix string      // key prefix, e.g., "bifrost/payloads/"
}

func (s *S3PayloadStore) Put(ctx context.Context, key string, r io.Reader, size int64, meta PayloadMeta) (string, error) {
    fullKey := s.prefix + key
    _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
        Bucket:        aws.String(s.bucket),
        Key:           aws.String(fullKey),
        Body:          r,
        ContentLength: aws.Int64(size),
        ContentType:   aws.String(meta.ContentType),
        Metadata: map[string]string{
            "x-bifrost-request-id": meta.RequestID,
            "x-bifrost-vk-id":      meta.VirtualKeyID,
        },
    })
    return fullKey, err
}
```

---

## 5. Large Payload Middleware

```go
// transports/bifrost-http/lib/payload.go

type LargePayloadConfig struct {
    Threshold       int64         // bytes; default 10MB
    MaxRequestSize  int64         // hard limit; default 100MB
    UseObjectStore  bool
    TempDir         string        // for disk spill
    PayloadStore    payloadstore.PayloadStore
}

func LargePayloadMiddleware(cfg LargePayloadConfig) Middleware {
    return func(h fasthttp.RequestHandler) fasthttp.RequestHandler {
        return func(ctx *fasthttp.RequestCtx) {
            contentLength := int64(ctx.Request.Header.ContentLength())
            
            // Hard limit check  
            if contentLength > cfg.MaxRequestSize {
                ctx.SetStatusCode(413)
                return
            }
            
            // Enable streaming path for large payloads
            if contentLength > cfg.Threshold {
                ctx.SetUserValue("large_payload_mode", true)
                // Set Bifrost context key so plugins know to truncate logging
            }
            h(ctx)
        }
    }
}
```

---

## 6. Multipart Streaming (Images/Audio)

Modify `core/network/multipart.go` to support streaming builds:

```go
// core/network/multipart.go (MODIFY)

// StreamingMultipartBuilder constructs multipart bodies without buffering the full payload
type StreamingMultipartBuilder struct {
    pw     *io.PipeWriter
    pr     *io.PipeReader
    mw     *multipart.Writer
    done   chan error
}

func NewStreamingMultipartBuilder() *StreamingMultipartBuilder {
    pr, pw := io.Pipe()
    mw := multipart.NewWriter(pw)
    return &StreamingMultipartBuilder{pw: pw, pr: pr, mw: mw, done: make(chan error, 1)}
}

func (b *StreamingMultipartBuilder) AddFileField(fieldName, filename string, content io.Reader) {
    go func() {
        w, err := b.mw.CreateFormFile(fieldName, filename)
        if err != nil { b.done <- err; return }
        if _, err := io.Copy(w, content); err != nil { b.done <- err; return }
        b.done <- nil
    }()
}

func (b *StreamingMultipartBuilder) AddField(name, value string) error {
    return b.mw.WriteField(name, value)
}

func (b *StreamingMultipartBuilder) Close() error {
    b.mw.Close()
    b.pw.Close()
    return <-b.done
}

func (b *StreamingMultipartBuilder) Reader() io.Reader { return b.pr }
func (b *StreamingMultipartBuilder) ContentType() string { return b.mw.FormDataContentType() }
```

---

## 7. Virtual Key Payload Size Limits

Extend `VirtualKeysTable` and governance enforcement:

```go
// framework/configstore/tables/governance.go (MODIFY)
type VirtualKeysTable struct {
    // ... existing fields ...
    MaxPayloadSize  *int64  `gorm:"column:max_payload_size"`  // bytes; nil = global default
}

// plugins/governance/precheck.go (MODIFY)
func (g *GovernancePlugin) checkPayloadSize(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
    vk := getVirtualKeyFromContext(ctx)
    if vk == nil || vk.MaxPayloadSize == nil { return nil }
    
    size := estimateRequestSize(req)
    if size > *vk.MaxPayloadSize {
        return fmt.Errorf("payload size %d exceeds virtual key limit %d", size, *vk.MaxPayloadSize)
    }
    return nil
}
```

---

## 8. Payload Deduplication

For identical large payloads (same image sent multiple times), cache the offloaded reference:

```go
// framework/payloadstore/dedup.go

type DeduplicatingPayloadStore struct {
    inner   PayloadStore
    kvStore schemas.KVStore
}

func (d *DeduplicatingPayloadStore) Put(ctx context.Context, key string, r io.Reader, size int64, meta PayloadMeta) (string, error) {
    // Hash first N bytes to detect duplicates
    hr := newHashReader(r)
    dedupKey := "payload:dedup:" + hex.EncodeToString(hr.Hash())
    
    if existing, _ := d.kvStore.Get(ctx, dedupKey); existing != "" {
        return existing, nil  // return existing reference
    }
    
    ref, err := d.inner.Put(ctx, key, hr, size, meta)
    if err == nil {
        d.kvStore.Set(ctx, dedupKey, ref, meta.ExpireAt.Sub(time.Now()))
    }
    return ref, err
}
```

---

## 9. Payload Config API

```go
// payload store configuration in PUT /api/config body

// GET /api/payload/config       — current payload store config
// PUT /api/payload/config       — update config (admin+)
// GET /api/payload/stats        — storage usage, item count
// DELETE /api/payload/cleanup   — trigger manual cleanup (operator+)
```

---

## 10. Performance Targets

| Metric | Target |
|--------|--------|
| 50MB image request memory overhead | < 5MB (streaming) |
| 100MB file upload throughput | > 50MB/s (disk spill path) |
| Payload dedup hit rate | Measured; reported in GET /api/payload/stats |
| Temp file cleanup lag | < 60s after request completion |
