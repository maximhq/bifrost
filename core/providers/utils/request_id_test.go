package utils

import (
	"fmt"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestExtractProviderRequestID(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		header  string
		want    string
	}{
		{name: "nil", header: "x-request-id"},
		{name: "missing", headers: map[string]string{"x-other": "id"}, header: "x-request-id"},
		{name: "case insensitive", headers: map[string]string{"X-Request-ID": " req-123 "}, header: "x-request-id", want: "req-123"},
		{name: "configured case insensitive", headers: map[string]string{"x-request-id": "req-123"}, header: "X-REQUEST-ID", want: "req-123"},
		{name: "empty", headers: map[string]string{"x-request-id": "  "}, header: "x-request-id"},
		{name: "too long", headers: map[string]string{"x-request-id": strings.Repeat("x", 513)}, header: "x-request-id"},
		{name: "combined value", headers: map[string]string{"x-request-id": "req-1, req-2"}, header: "x-request-id", want: "req-1, req-2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractProviderRequestID(tt.headers, tt.header); got != tt.want {
				t.Fatalf("ExtractProviderRequestID() = %q, want %q", got, tt.want)
			}
		})
	}
}

type providerRequestIDCaptureLogger struct {
	warnings []string
}

func (l *providerRequestIDCaptureLogger) Debug(string, ...any) {}
func (l *providerRequestIDCaptureLogger) Info(string, ...any)  {}
func (l *providerRequestIDCaptureLogger) Warn(msg string, args ...any) {
	l.warnings = append(l.warnings, fmt.Sprintf(msg, args...))
}
func (l *providerRequestIDCaptureLogger) Error(string, ...any)                   {}
func (l *providerRequestIDCaptureLogger) Fatal(string, ...any)                   {}
func (l *providerRequestIDCaptureLogger) SetLevel(schemas.LogLevel)              {}
func (l *providerRequestIDCaptureLogger) SetOutputType(schemas.LoggerOutputType) {}
func (l *providerRequestIDCaptureLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func TestExtractProviderRequestIDWithLoggerWarnsWithoutLeakingOversizedValue(t *testing.T) {
	logger := &providerRequestIDCaptureLogger{}
	oversized := strings.Repeat("sensitive-value-", 40)

	if got := ExtractProviderRequestIDWithLogger(map[string]string{"X-Request-ID": oversized}, "x-request-id", logger); got != "" {
		t.Fatalf("ExtractProviderRequestIDWithLogger() = %q, want empty", got)
	}
	if len(logger.warnings) != 1 {
		t.Fatalf("warning count = %d, want 1", len(logger.warnings))
	}
	warning := logger.warnings[0]
	if !strings.Contains(warning, "x-request-id") || !strings.Contains(warning, "512-byte limit") {
		t.Fatalf("warning missing safe diagnostics: %q", warning)
	}
	if strings.Contains(warning, oversized) || strings.Contains(warning, "sensitive-value") {
		t.Fatalf("warning leaked request ID value: %q", warning)
	}
}
