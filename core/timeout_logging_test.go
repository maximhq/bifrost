package bifrost

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

type timeoutLogCapture struct {
	mu     sync.Mutex
	fields map[string]any
	sent   bool
}

type timeoutLogBuilder struct{ capture *timeoutLogCapture }

func (b timeoutLogBuilder) Str(key, value string) schemas.LogEventBuilder {
	b.capture.fields[key] = value
	return b
}
func (b timeoutLogBuilder) Int(key string, value int) schemas.LogEventBuilder {
	b.capture.fields[key] = value
	return b
}
func (b timeoutLogBuilder) Int64(key string, value int64) schemas.LogEventBuilder {
	b.capture.fields[key] = value
	return b
}
func (b timeoutLogBuilder) Send() { b.capture.sent = true }

type timeoutCaptureLogger struct{ capture *timeoutLogCapture }

func (timeoutCaptureLogger) Debug(string, ...any)                   {}
func (timeoutCaptureLogger) Info(string, ...any)                    {}
func (timeoutCaptureLogger) Warn(string, ...any)                    {}
func (timeoutCaptureLogger) Error(string, ...any)                   {}
func (timeoutCaptureLogger) Fatal(string, ...any)                   {}
func (timeoutCaptureLogger) SetLevel(schemas.LogLevel)              {}
func (timeoutCaptureLogger) SetOutputType(schemas.LoggerOutputType) {}
func (l timeoutCaptureLogger) LogHTTPRequest(_ schemas.LogLevel, _ string) schemas.LogEventBuilder {
	l.capture.mu.Lock()
	defer l.capture.mu.Unlock()
	l.capture.fields = make(map[string]any)
	return timeoutLogBuilder{capture: l.capture}
}

func TestExecuteRequestWithRetriesLogsTimeoutFromFirstStreamChunk(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	capture := &timeoutLogCapture{}
	logger := timeoutCaptureLogger{capture: capture}
	config := createTestConfig(0, time.Millisecond, time.Millisecond)
	config.NetworkConfig.DefaultRequestTimeoutInSeconds = 600

	handler := func(_ schemas.Key) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		stream := make(chan *schemas.BifrostStreamChunk, 1)
		stream <- &schemas.BifrostStreamChunk{BifrostError: &schemas.BifrostError{
			IsBifrostError: true,
			Error:          &schemas.ErrorField{Message: schemas.TimeoutSourceUpstreamConnection.SafeMessage()},
			ExtraFields: schemas.BifrostErrorExtraFields{
				TimeoutSource:            schemas.TimeoutSourceUpstreamConnection,
				ConfiguredTimeoutSeconds: 1,
				ElapsedMS:                27_000,
				UpstreamResponseReceived: schemas.Ptr(false),
			},
		}}
		close(stream)
		return stream, nil
	}

	_, bifrostErr := executeRequestWithRetries(
		ctx, config, handler, nil, schemas.ChatCompletionStreamRequest,
		schemas.OpenAI, "gpt-image-1", nil, logger,
	)
	if bifrostErr == nil {
		t.Fatal("expected first stream chunk error")
	}
	if !capture.sent {
		t.Fatal("expected structured timeout log for first stream chunk error")
	}
	if capture.fields["timeout_source"] != string(schemas.TimeoutSourceUpstreamConnection) {
		t.Fatalf("timeout_source = %v", capture.fields["timeout_source"])
	}
	if capture.fields["configured_timeout_seconds"] != 600 {
		t.Fatalf("configured_timeout_seconds = %v, want provider configuration 600", capture.fields["configured_timeout_seconds"])
	}
}
