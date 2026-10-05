package openai

import (
	"testing"

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
