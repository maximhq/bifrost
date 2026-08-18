package llmtests

import (
	"net/http"
	"net/http/httptest"

	"github.com/maximhq/bifrost/core/schemas"
)

// =============================================================================
// MINIMAL MOCK PROVIDER BACKEND — response/error-code handling only
// =============================================================================
//
// No shared mock provider backend existed in the codebase prior to this file
// (provider test packages that needed one, e.g. providers/replicate/replicate_test.go,
// each hand-rolled an ad-hoc httptest.NewServer inline). This is a minimal,
// intentionally narrow, shared helper for exercising Bifrost's error
// normalization pipeline end-to-end against a fake provider backend — it does
// NOT simulate successful responses, streaming payloads, or request
// validation; it only ever returns a configured status code + body,
// regardless of the request. Point a provider's NetworkConfig.BaseURL at its
// URL to simulate that provider returning a given raw error response.

// MockErrorServer is an httptest-backed fake provider endpoint that always
// responds with a fixed status code and body, regardless of request path,
// method, or content. Embeds *httptest.Server, so Close() is available
// directly; callers should `defer server.Close()`.
type MockErrorServer struct {
	*httptest.Server
}

// NewMockErrorServer starts a MockErrorServer that responds to every request
// with statusCode and the given raw JSON body. contentType defaults to
// "application/json" if empty.
func NewMockErrorServer(statusCode int, body string, contentType string) *MockErrorServer {
	if contentType == "" {
		contentType = "application/json"
	}
	return &MockErrorServer{
		Server: httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", contentType)
			w.WriteHeader(statusCode)
			_, _ = w.Write([]byte(body))
		})),
	}
}

// NewMockErrorServerWithHeaders is like NewMockErrorServer but also sets the
// given response headers before the body is written — needed for providers
// whose error signal lives in a header rather than (or in addition to) the
// body, e.g. AWS Bedrock's X-Amzn-Errortype.
func NewMockErrorServerWithHeaders(statusCode int, body string, contentType string, headers map[string]string) *MockErrorServer {
	if contentType == "" {
		contentType = "application/json"
	}
	return &MockErrorServer{
		Server: httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for k, v := range headers {
				w.Header().Set(k, v)
			}
			w.Header().Set("Content-Type", contentType)
			w.WriteHeader(statusCode)
			_, _ = w.Write([]byte(body))
		})),
	}
}

// NoOpTestLogger is a minimal schemas.Logger implementation that discards
// everything — for provider construction in tests that don't care about log
// output. Shared here so individual provider test files don't each need to
// redeclare it (as providers/replicate/replicate_test.go currently does
// locally).
type NoOpTestLogger struct{}

func (l *NoOpTestLogger) Debug(string, ...any)                   {}
func (l *NoOpTestLogger) Info(string, ...any)                    {}
func (l *NoOpTestLogger) Warn(string, ...any)                    {}
func (l *NoOpTestLogger) Error(string, ...any)                   {}
func (l *NoOpTestLogger) Fatal(string, ...any)                   {}
func (l *NoOpTestLogger) SetLevel(schemas.LogLevel)              {}
func (l *NoOpTestLogger) SetOutputType(schemas.LoggerOutputType) {}
func (l *NoOpTestLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}
