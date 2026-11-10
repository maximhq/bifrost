package openai

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestBuildPassthroughURLWithUpstreamOverride verifies host-backed passthrough
// routes do not receive OpenAI's /v1 prefix.
func TestBuildPassthroughURLWithUpstreamOverride(t *testing.T) {
	provider := NewOpenAIProvider(&schemas.ProviderConfig{}, passthroughTestLogger{})

	req := &schemas.BifrostPassthroughRequest{
		Path:        "/backend-api/codex/responses",
		RawQuery:    "conversation=abc",
		UpstreamURL: "https://chatgpt.com",
	}

	got, err := provider.buildPassthroughURL(req)
	if err != nil {
		t.Fatalf("buildPassthroughURL unexpected error: %v", err)
	}
	want := "https://chatgpt.com/backend-api/codex/responses?conversation=abc"
	if got != want {
		t.Fatalf("buildPassthroughURL = %q, want %q", got, want)
	}
}

// TestBuildPassthroughURLRejectsForeignAuthority verifies a remainder that would move the
// resolved authority off the configured upstream is refused instead of being dialed. Only
// the upstream-override branch forwards the path verbatim; the default branch always
// prepends /v1, which anchors the path on the OpenAI host.
func TestBuildPassthroughURLRejectsForeignAuthority(t *testing.T) {
	provider := NewOpenAIProvider(&schemas.ProviderConfig{}, passthroughTestLogger{})

	req := &schemas.BifrostPassthroughRequest{Path: "//evil.example/x", UpstreamURL: "https://chatgpt.com"}
	if got, err := provider.buildPassthroughURL(req); err == nil {
		t.Fatalf("buildPassthroughURL(%q) = %q, want error", req.Path, got)
	}
}

// TestBuildPassthroughURLDefaultsToOpenAIV1 verifies normal OpenAI passthrough
// routes keep the existing /v1 URL construction.
func TestBuildPassthroughURLDefaultsToOpenAIV1(t *testing.T) {
	provider := NewOpenAIProvider(&schemas.ProviderConfig{}, passthroughTestLogger{})

	req := &schemas.BifrostPassthroughRequest{
		Path:     "/v1/responses",
		RawQuery: "stream=true",
	}

	got, err := provider.buildPassthroughURL(req)
	if err != nil {
		t.Fatalf("buildPassthroughURL unexpected error: %v", err)
	}
	want := "https://api.openai.com/v1/responses?stream=true"
	if got != want {
		t.Fatalf("buildPassthroughURL = %q, want %q", got, want)
	}
}

// passthroughTestLogger discards provider logs in passthrough URL tests.
type passthroughTestLogger struct{}

// Debug discards a debug log.
func (passthroughTestLogger) Debug(string, ...any) {}

// Info discards an info log.
func (passthroughTestLogger) Info(string, ...any) {}

// Warn discards a warning log.
func (passthroughTestLogger) Warn(string, ...any) {}

// Error discards an error log.
func (passthroughTestLogger) Error(string, ...any) {}

// Fatal discards a fatal log.
func (passthroughTestLogger) Fatal(string, ...any) {}

// SetLevel ignores log level changes.
func (passthroughTestLogger) SetLevel(schemas.LogLevel) {}

// SetOutputType ignores output type changes.
func (passthroughTestLogger) SetOutputType(schemas.LoggerOutputType) {}

// LogHTTPRequest returns a no-op structured log builder.
func (passthroughTestLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return passthroughTestLogEvent{}
}

// passthroughTestLogEvent discards structured HTTP log fields.
type passthroughTestLogEvent struct{}

// Str discards a string field.
func (passthroughTestLogEvent) Str(string, string) schemas.LogEventBuilder {
	return passthroughTestLogEvent{}
}

// Int discards an integer field.
func (passthroughTestLogEvent) Int(string, int) schemas.LogEventBuilder {
	return passthroughTestLogEvent{}
}

// Int64 discards an int64 field.
func (passthroughTestLogEvent) Int64(string, int64) schemas.LogEventBuilder {
	return passthroughTestLogEvent{}
}

// Send discards the built log event.
func (passthroughTestLogEvent) Send() {}

func passthroughEncodingHeader(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func TestPassthroughStreamEncoding(t *testing.T) {
	plain := []byte(`{"model":"test-model","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, err := z.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name           string
		raw, gzip      bool
		status         int
		noOtherHeaders bool
	}{
		{"raw_identity_control", true, false, 200, false},
		{"buffered_gzip_control", false, true, 200, false},
		{"raw_gzip", true, true, 200, false},
		{"raw_gzip_no_other_headers", true, true, 200, true},
		{"raw_gzip_error_response", true, true, 429, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := plain
			if tc.gzip {
				wire = compressed.Bytes()
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Accept-Encoding") != "identity" {
					t.Errorf("unexpected Accept-Encoding: %q", r.Header.Get("Accept-Encoding"))
				}
				if tc.noOtherHeaders {
					w.Header()["Content-Type"] = nil
					w.Header()["Date"] = nil
				} else {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("X-Request-ID", "encoding-test")
					w.Header().Set("Set-Cookie", "secret=value")
					w.Header().Set("Authorization", "secret")
				}
				w.Header().Set("Content-Length", strconv.Itoa(len(wire)))
				if tc.gzip {
					w.Header().Set("Content-Encoding", "gzip")
				}
				w.WriteHeader(tc.status)
				if _, err := w.Write(wire); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			p := NewOpenAIProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{
				BaseURL: srv.URL, AllowPrivateNetwork: true, DefaultRequestTimeoutInSeconds: 5,
			}}, passthroughTestLogger{})
			ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(10*time.Second))
			defer ctx.Cancel()
			req := &schemas.BifrostPassthroughRequest{
				Method: "POST", Path: "/chat/completions",
				Body:        []byte(`{"model":"test-model","messages":[{"role":"user","content":"hi"}],"stream":false}`),
				SafeHeaders: map[string]string{"Content-Type": "application/json", "Accept-Encoding": "identity"},
			}
			var body []byte
			var headers map[string]string
			var usage *schemas.BifrostPassthroughUsage
			dataChunks, finalChunks := 0, 0
			wantEncoding := ""
			if tc.raw && tc.gzip {
				wantEncoding = "gzip"
			}
			if tc.raw {
				ch, err := p.PassthroughStream(ctx, func(_ *schemas.BifrostContext, r *schemas.BifrostResponse, e *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
					return r, e
				}, nil, schemas.Key{}, req)
				if err != nil {
					t.Fatalf("PassthroughStream: %+v", err)
				}
				for chunk := range ch {
					if chunk.BifrostError != nil {
						t.Fatalf("stream error: %+v", chunk.BifrostError)
					}
					r := chunk.BifrostPassthroughResponse
					if r == nil {
						t.Fatal("nil passthrough response")
					}
					if r.StatusCode != tc.status {
						t.Errorf("status=%d, want %d", r.StatusCode, tc.status)
					}
					if len(r.Body) > 0 {
						dataChunks++
					} else {
						finalChunks++
					}
					headers = r.Headers
					body = append(body, r.Body...)
					if r.PassthroughUsage != nil {
						usage = r.PassthroughUsage
					}
					if got := passthroughEncodingHeader(headers, "Content-Encoding"); got != wantEncoding {
						t.Errorf("chunk Content-Encoding=%q, want %q", got, wantEncoding)
					}
				}
				if dataChunks == 0 || finalChunks != 1 {
					t.Errorf("data chunks=%d final chunks=%d", dataChunks, finalChunks)
				}
				if !bytes.Equal(body, wire) {
					t.Error("raw body differs from upstream bytes")
				}
			} else {
				r, err := p.Passthrough(ctx, schemas.Key{}, req)
				if err != nil {
					t.Fatalf("Passthrough: %+v", err)
				}
				headers, body, usage = r.Headers, r.Body, r.PassthroughUsage
				if !bytes.Equal(body, plain) {
					t.Error("buffered body was not decoded correctly")
				}
				if passthroughEncodingHeader(headers, "Content-Encoding") != "" {
					t.Error("buffered response has stale encoding")
				}
			}
			for _, name := range []string{"Content-Length", "Transfer-Encoding", "Connection", "Set-Cookie", "Authorization"} {
				if value := passthroughEncodingHeader(headers, name); value != "" {
					t.Errorf("filtered header %s leaked: %q", name, value)
				}
			}
			if !tc.noOtherHeaders && passthroughEncodingHeader(headers, "X-Request-ID") != "encoding-test" {
				t.Error("safe response header was lost")
			}
			if !tc.raw || !tc.gzip {
				if usage == nil || usage.LLMUsage == nil || usage.LLMUsage.TotalTokens != 3 {
					t.Errorf("control usage=%+v, want 3 tokens", usage)
				}
			}
			contextHeaders, _ := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders).(map[string]string)
			if got := passthroughEncodingHeader(contextHeaders, "Content-Encoding"); got != wantEncoding {
				t.Errorf("context Content-Encoding=%q, want %q", got, wantEncoding)
			}
			t.Logf("bytes=%d gzip=%t response_encoding=%q context_encoding=%q usage_present=%t", len(body), bytes.HasPrefix(body, []byte{0x1f, 0x8b}), passthroughEncodingHeader(headers, "Content-Encoding"), passthroughEncodingHeader(contextHeaders, "Content-Encoding"), usage != nil)
		})
	}
}

// TestPassthroughStreamEncodingCancellation keeps cancellation on the raw body reader.
func TestPassthroughStreamEncodingCancellation(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/octet-stream")
		if _, err := w.Write([]byte{0x1f, 0x8b}); err != nil {
			t.Error(err)
		}
		w.(http.Flusher).Flush()
		<-release
	}))
	defer server.Close()
	defer close(release)
	provider := NewOpenAIProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true}}, passthroughTestLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(5*time.Second))
	defer ctx.Cancel()
	ch, err := provider.PassthroughStream(ctx, func(_ *schemas.BifrostContext, r *schemas.BifrostResponse, e *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return r, e
	}, nil, schemas.Key{}, &schemas.BifrostPassthroughRequest{Method: "GET", Path: "/files/test/content"})
	if err != nil {
		t.Fatal(err)
	}
	first := <-ch
	if first == nil || first.BifrostPassthroughResponse == nil {
		t.Fatalf("expected raw chunk, got %+v", first)
	}
	if got := first.BifrostPassthroughResponse; !bytes.Equal(got.Body, []byte{0x1f, 0x8b}) || passthroughEncodingHeader(got.Headers, "Content-Encoding") != "gzip" {
		t.Fatalf("unexpected raw chunk: %+v", got)
	}
	ctx.Cancel()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timer.C:
			t.Fatal("cancelled raw stream did not close")
		}
	}
}

// BenchmarkPassthroughStreamEncoding includes the local HTTP round trip and raw stream lifecycle.
func BenchmarkPassthroughStreamEncoding(b *testing.B) {
	for _, encoding := range []string{"", "gzip"} {
		b.Run(encoding, func(b *testing.B) {
			wire := []byte(`{"ok":true}`)
			if encoding != "" {
				var compressed bytes.Buffer
				gz := gzip.NewWriter(&compressed)
				if _, err := gz.Write(wire); err != nil {
					b.Fatal(err)
				}
				if err := gz.Close(); err != nil {
					b.Fatal(err)
				}
				wire = compressed.Bytes()
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if encoding != "" {
					w.Header().Set("Content-Encoding", encoding)
				}
				if _, err := w.Write(wire); err != nil {
					b.Error(err)
				}
			}))
			defer server.Close()
			provider := NewOpenAIProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true}}, passthroughTestLogger{})
			runner := func(_ *schemas.BifrostContext, r *schemas.BifrostResponse, e *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				return r, e
			}
			req := &schemas.BifrostPassthroughRequest{Method: "GET", Path: "/files/test/content"}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
				ch, err := provider.PassthroughStream(ctx, runner, nil, schemas.Key{}, req)
				if err != nil {
					b.Fatal(err)
				}
				for chunk := range ch {
					if chunk.BifrostError != nil {
						b.Fatal(chunk.BifrostError)
					}
				}
				ctx.Cancel()
			}
		})
	}
}
