package integrations

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// stubChunkInterceptor terminates the stream with a fixed error, playing the
// role of the transport's pluginChunkInterceptor.
type stubChunkInterceptor struct {
	err error
}

func (s *stubChunkInterceptor) InterceptChunk(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return nil, s.err
}

// interceptorHandlerStore overrides mockHandlerStore to supply a stream chunk
// interceptor to handleStreaming.
type interceptorHandlerStore struct {
	*mockHandlerStore
	interceptor lib.StreamChunkInterceptor
}

func (s *interceptorHandlerStore) GetStreamChunkInterceptor() lib.StreamChunkInterceptor {
	return s.interceptor
}

// wrapInterceptionError mimics how the transport's pluginChunkInterceptor wraps
// plugin hook errors before they reach handleStreaming.
func wrapInterceptionError(err error) error {
	return fmt.Errorf("failed to intercept chunk with plugin %s: %w", "test-plugin", err)
}

func newStreamInterceptionError(message string) *schemas.StreamInterceptionError {
	errType := "guardrail_intervention"
	statusCode := fasthttp.StatusBadRequest
	return &schemas.StreamInterceptionError{
		BifrostError: &schemas.BifrostError{
			Type:       &errType,
			StatusCode: &statusCode,
			Error: &schemas.ErrorField{
				Type:    &errType,
				Message: message,
			},
		},
	}
}

func runHandleStreamingWithInterceptor(t *testing.T, config RouteConfig, interceptErr error) (string, bool) {
	t.Helper()

	stream := make(chan *schemas.BifrostStreamChunk, 1)
	stream <- &schemas.BifrostStreamChunk{} // any non-error chunk reaches the interceptor
	close(stream)

	handlerStore := &interceptorHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		interceptor:      &stubChunkInterceptor{err: interceptErr},
	}
	router := NewGenericRouter(nil, handlerStore, nil, nil, bifrost.NewNoOpLogger())
	ctx := &fasthttp.RequestCtx{}
	cancelCalled := false
	router.handleStreaming(ctx, nil, config, stream, func() {
		cancelCalled = true
	})

	body, err := io.ReadAll(ctx.Response.BodyStream())
	require.NoError(t, err)
	return string(body), cancelCalled
}

// A StreamInterceptionError must be emitted through the integration's error
// converter — the same pipeline as upstream provider errors — instead of being
// flattened into the wrapped interceptor error string (issue #5036).
func Test_handleStreamingInterceptionErrorUsesErrorConverter(t *testing.T) {
	config := RouteConfig{
		StreamConfig: &StreamConfig{
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return map[string]interface{}{
					"error": map[string]interface{}{
						"type":    *err.Error.Type,
						"message": err.Error.Message,
					},
				}
			},
		},
	}

	body, cancelCalled := runHandleStreamingWithInterceptor(t, config,
		wrapInterceptionError(newStreamInterceptionError("Response blocked by content policy")))

	assert.Contains(t, body, `data: `)
	assert.Contains(t, body, `"message":"Response blocked by content policy"`)
	assert.Contains(t, body, `"type":"guardrail_intervention"`)
	assert.NotContains(t, body, "failed to intercept chunk",
		"structured interception errors must not leak the internal plugin wrap")
	assert.True(t, cancelCalled, "stream must be terminated after an interception error")
}

// On Anthropic-style routes the converter returns a complete SSE string with
// the provider's native error framing; it must be sent verbatim.
func Test_handleStreamingInterceptionErrorAnthropicSSEFraming(t *testing.T) {
	config := RouteConfig{
		Type: RouteConfigTypeAnthropic,
		StreamConfig: &StreamConfig{
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return anthropic.ToAnthropicResponsesStreamError(err)
			},
		},
	}

	body, cancelCalled := runHandleStreamingWithInterceptor(t, config,
		wrapInterceptionError(newStreamInterceptionError("Response blocked by content policy")))

	assert.True(t, strings.HasPrefix(body, "event: error\ndata: "),
		"Anthropic streaming errors use the provider's native SSE framing, got: %q", body)
	assert.Contains(t, body, `"Response blocked by content policy"`)
	assert.NotContains(t, body, "failed to intercept chunk")
	assert.True(t, cancelCalled)
}

// Structured interception errors must be sanitized like upstream provider
// errors before reaching the converter.
func Test_handleStreamingInterceptionErrorIsSanitized(t *testing.T) {
	var converterErr *schemas.BifrostError
	config := RouteConfig{
		StreamConfig: &StreamConfig{
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				converterErr = err
				return map[string]string{"message": err.Error.Message}
			},
		},
	}

	body, _ := runHandleStreamingWithInterceptor(t, config,
		wrapInterceptionError(newStreamInterceptionError("panic: goroutine 42 [running] at main.go:17")))

	require.NotNil(t, converterErr)
	assert.Equal(t, lib.ClientSafeInternalErrorMessage, converterErr.Error.Message,
		"stack-trace-bearing messages must be replaced by the sanitizer")
	assert.NotContains(t, body, "main.go:17")
}

// On Bedrock routes the converted interception error must be encoded as an
// AWS event-stream exception, not SSE.
func Test_handleStreamingInterceptionErrorBedrockEventStream(t *testing.T) {
	config := RouteConfig{
		Type: RouteConfigTypeBedrock,
		StreamConfig: &StreamConfig{
			ErrorConverter: bedrockStreamErrorConverter,
		},
	}

	body, cancelCalled := runHandleStreamingWithInterceptor(t, config,
		wrapInterceptionError(newStreamInterceptionError("Response blocked by content policy")))

	require.NotEmpty(t, body)
	require.False(t, strings.HasPrefix(body, "data: "), "Bedrock streaming errors must not be plain SSE")
	require.False(t, strings.HasPrefix(body, "event: "), "Bedrock streaming errors must not be plain SSE")

	msg, err := eventstream.NewDecoder().Decode(bytes.NewReader([]byte(body)), nil)
	require.NoError(t, err)
	assert.Equal(t, "exception", eventStreamHeaderString(t, msg.Headers, ":message-type"))
	assert.Equal(t, "guardrail_intervention", eventStreamHeaderString(t, msg.Headers, ":exception-type"))
	assert.JSONEq(t, `{"__type":"guardrail_intervention","message":"Response blocked by content policy"}`, string(msg.Payload))
	assert.NotContains(t, string(msg.Payload), "failed to intercept chunk")
	assert.True(t, cancelCalled)
}

// Plain (non-structured) interceptor errors keep the existing flat behavior.
func Test_handleStreamingPlainInterceptionErrorKeepsFlatFormat(t *testing.T) {
	config := RouteConfig{
		StreamConfig: &StreamConfig{
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				t.Fatal("error converter must not be called for non-structured interception errors")
				return nil
			},
		},
	}

	body, cancelCalled := runHandleStreamingWithInterceptor(t, config,
		wrapInterceptionError(fmt.Errorf("plugin exploded")))

	assert.Contains(t, body, "event: error\ndata: ")
	assert.Contains(t, body, `{"error":"failed to intercept chunk with plugin test-plugin: plugin exploded"}`)
	assert.True(t, cancelCalled)
}
