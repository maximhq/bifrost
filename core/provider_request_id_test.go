package bifrost

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func newProviderRequestIDTestContext() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	return ctx
}

func providerRequestIDTestConfig(maxRetries int) *schemas.ProviderConfig {
	return &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{MaxRetries: maxRetries},
		ProviderRequestID: &schemas.ProviderRequestIDConfig{
			Enabled: true,
		},
	}
}

func TestExecuteRequestWithRetriesCapturesProviderRequestID(t *testing.T) {
	ctx := newProviderRequestIDTestContext()
	config := providerRequestIDTestConfig(0)
	logger := NewDefaultLogger(schemas.LogLevelError)

	result, err := executeRequestWithRetries(ctx, config, func(_ schemas.Key) (string, *schemas.BifrostError) {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"X-Request-ID": " req-success "})
		return "ok", nil
	}, nil, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-test", nil, logger)
	if err != nil || result != "ok" {
		t.Fatalf("executeRequestWithRetries() = (%q, %v), want (ok, nil)", result, err)
	}
	if got := GetStringFromContext(ctx, schemas.BifrostContextKeyProviderRequestID); got != "req-success" {
		t.Fatalf("current provider request ID = %q, want req-success", got)
	}
	if got := GetStringFromContext(ctx, schemas.BifrostContextKeyProviderRequestIDHeader); got != "x-request-id" {
		t.Fatalf("provider request ID header = %q, want x-request-id", got)
	}
	trail, _ := ctx.Value(schemas.BifrostContextKeyProviderRequestIDTrail).([]schemas.ProviderRequestIDRecord)
	if len(trail) != 1 || trail[0].Attempt != 0 || trail[0].Provider != schemas.OpenAI || trail[0].RequestID != "req-success" || trail[0].StatusCode != nil {
		t.Fatalf("unexpected provider request ID trail: %#v", trail)
	}
}

func TestExecuteRequestWithRetriesCapturesCustomProviderRequestIDHeader(t *testing.T) {
	ctx := newProviderRequestIDTestContext()
	config := providerRequestIDTestConfig(0)
	config.ProviderRequestID.HeaderName = " X-Custom-Trace "
	logger := NewDefaultLogger(schemas.LogLevelError)

	result, err := executeRequestWithRetries(ctx, config, func(_ schemas.Key) (string, *schemas.BifrostError) {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"x-CUSTOM-trace": " custom-request-id "})
		return "ok", nil
	}, nil, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-test", nil, logger)
	if err != nil || result != "ok" {
		t.Fatalf("executeRequestWithRetries() = (%q, %v), want (ok, nil)", result, err)
	}
	if got := GetStringFromContext(ctx, schemas.BifrostContextKeyProviderRequestID); got != "custom-request-id" {
		t.Fatalf("current provider request ID = %q, want custom-request-id", got)
	}
	if got := GetStringFromContext(ctx, schemas.BifrostContextKeyProviderRequestIDHeader); got != "x-custom-trace" {
		t.Fatalf("provider request ID header = %q, want x-custom-trace", got)
	}
	trail, _ := ctx.Value(schemas.BifrostContextKeyProviderRequestIDTrail).([]schemas.ProviderRequestIDRecord)
	if len(trail) != 1 || trail[0].RequestID != "custom-request-id" || trail[0].HeaderName != "x-custom-trace" {
		t.Fatalf("unexpected provider request ID trail: %#v", trail)
	}
}

func TestExecuteRequestWithRetriesPreservesAttemptTrail(t *testing.T) {
	ctx := newProviderRequestIDTestContext()
	config := providerRequestIDTestConfig(1)
	logger := NewDefaultLogger(schemas.LogLevelError)
	attempt := 0

	result, err := executeRequestWithRetries(ctx, config, func(_ schemas.Key) (string, *schemas.BifrostError) {
		if attempt == 0 {
			attempt++
			ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"x-request-id": "req-429"})
			return "", createBifrostError("rate limited", Ptr(429), nil, false)
		}
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"x-request-id": "req-final"})
		return "ok", nil
	}, nil, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-test", nil, logger)
	if err != nil || result != "ok" {
		t.Fatalf("executeRequestWithRetries() = (%q, %v), want (ok, nil)", result, err)
	}
	trail, _ := ctx.Value(schemas.BifrostContextKeyProviderRequestIDTrail).([]schemas.ProviderRequestIDRecord)
	if len(trail) != 2 {
		t.Fatalf("trail length = %d, want 2: %#v", len(trail), trail)
	}
	if trail[0].Attempt != 0 || trail[0].RequestID != "req-429" || trail[0].StatusCode == nil || *trail[0].StatusCode != 429 {
		t.Fatalf("unexpected first trail record: %#v", trail[0])
	}
	if trail[1].Attempt != 1 || trail[1].RequestID != "req-final" || trail[1].StatusCode != nil {
		t.Fatalf("unexpected second trail record: %#v", trail[1])
	}
	if got := GetStringFromContext(ctx, schemas.BifrostContextKeyProviderRequestID); got != "req-final" {
		t.Fatalf("current provider request ID = %q, want req-final", got)
	}
}

func TestExecuteRequestWithRetriesDoesNotReuseHistoricalID(t *testing.T) {
	ctx := newProviderRequestIDTestContext()
	config := providerRequestIDTestConfig(1)
	logger := NewDefaultLogger(schemas.LogLevelError)
	attempt := 0

	_, err := executeRequestWithRetries(ctx, config, func(_ schemas.Key) (string, *schemas.BifrostError) {
		if attempt == 0 {
			attempt++
			ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"x-request-id": "req-history"})
			return "", createBifrostError("rate limited", Ptr(429), nil, false)
		}
		// Simulate a connection failure before response headers are available.
		return "", createBifrostError(schemas.ErrProviderNetworkError, nil, nil, false)
	}, nil, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-test", nil, logger)
	if err == nil {
		t.Fatal("expected final network error")
	}
	if got := GetStringFromContext(ctx, schemas.BifrostContextKeyProviderRequestID); got != "" {
		t.Fatalf("current provider request ID = %q, want empty", got)
	}
	trail, _ := ctx.Value(schemas.BifrostContextKeyProviderRequestIDTrail).([]schemas.ProviderRequestIDRecord)
	if len(trail) != 1 || trail[0].RequestID != "req-history" {
		t.Fatalf("unexpected trail: %#v", trail)
	}
}

func TestExecuteRequestWithRetriesCapturesKeylessAndStreamingFirstChunkError(t *testing.T) {
	ctx := newProviderRequestIDTestContext()
	config := providerRequestIDTestConfig(1)
	logger := NewDefaultLogger(schemas.LogLevelError)
	attempt := 0

	stream, err := executeRequestWithRetries(ctx, config, func(key schemas.Key) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		if key.ID != "" {
			t.Fatalf("keyless request received key ID %q", key.ID)
		}
		ch := make(chan *schemas.BifrostStreamChunk, 1)
		if attempt == 0 {
			attempt++
			ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"x-request-id": "req-stream-429"})
			ch <- &schemas.BifrostStreamChunk{BifrostError: createBifrostError("rate limited", Ptr(429), nil, false)}
		} else {
			ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"x-request-id": "req-stream-ok"})
			ch <- &schemas.BifrostStreamChunk{BifrostChatResponse: &schemas.BifrostChatResponse{}}
		}
		close(ch)
		return ch, nil
	}, nil, schemas.ChatCompletionStreamRequest, schemas.OpenAI, "gpt-test", nil, logger)
	if err != nil {
		t.Fatalf("executeRequestWithRetries() error = %v", err)
	}
	if stream == nil {
		t.Fatal("expected successful stream")
	}
	if chunk := <-stream; chunk == nil || chunk.BifrostChatResponse == nil {
		t.Fatalf("unexpected successful stream chunk: %#v", chunk)
	}
	trail, _ := ctx.Value(schemas.BifrostContextKeyProviderRequestIDTrail).([]schemas.ProviderRequestIDRecord)
	if len(trail) != 2 || trail[0].RequestID != "req-stream-429" || trail[1].RequestID != "req-stream-ok" {
		t.Fatalf("unexpected stream trail: %#v", trail)
	}
	if trail[0].StatusCode == nil || *trail[0].StatusCode != 429 {
		t.Fatalf("stream error status not captured: %#v", trail[0])
	}
}

func TestExecuteRequestWithRetriesDisabledCaptureClearsStaleValues(t *testing.T) {
	ctx := newProviderRequestIDTestContext()
	ctx.SetValue(schemas.BifrostContextKeyProviderRequestID, "stale")
	ctx.SetValue(schemas.BifrostContextKeyProviderRequestIDHeader, "x-stale")
	ctx.SetValue(schemas.BifrostContextKeyProviderRequestIDTrail, []schemas.ProviderRequestIDRecord{{RequestID: "stale"}})
	config := createTestConfig(0, 0, 0)
	logger := NewDefaultLogger(schemas.LogLevelError)

	_, err := executeRequestWithRetries(ctx, config, func(_ schemas.Key) (string, *schemas.BifrostError) {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"x-request-id": "must-not-capture"})
		return "ok", nil
	}, nil, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-test", nil, logger)
	if err != nil {
		t.Fatalf("executeRequestWithRetries() error = %v", err)
	}
	if got := GetStringFromContext(ctx, schemas.BifrostContextKeyProviderRequestID); got != "" {
		t.Fatalf("disabled capture retained current ID %q", got)
	}
	if trail := ctx.Value(schemas.BifrostContextKeyProviderRequestIDTrail); trail != nil {
		t.Fatalf("disabled capture wrote trail context data: %#v", trail)
	}
}

func TestExecuteRequestWithRetriesCaptures5xxProviderRequestID(t *testing.T) {
	ctx := newProviderRequestIDTestContext()
	config := providerRequestIDTestConfig(0)
	logger := NewDefaultLogger(schemas.LogLevelError)

	_, err := executeRequestWithRetries(ctx, config, func(_ schemas.Key) (string, *schemas.BifrostError) {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"x-request-id": "req-503"})
		return "", createBifrostError("service unavailable", Ptr(503), nil, false)
	}, nil, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-test", nil, logger)
	if err == nil {
		t.Fatal("expected 503 error")
	}
	if got := GetStringFromContext(ctx, schemas.BifrostContextKeyProviderRequestID); got != "req-503" {
		t.Fatalf("current provider request ID = %q, want req-503", got)
	}
	trail, _ := ctx.Value(schemas.BifrostContextKeyProviderRequestIDTrail).([]schemas.ProviderRequestIDRecord)
	if len(trail) != 1 || trail[0].StatusCode == nil || *trail[0].StatusCode != 503 {
		t.Fatalf("unexpected 5xx trail: %#v", trail)
	}
}

func TestExecuteRequestWithRetriesCapturesStreamingHeaderBeforeFirstChunk(t *testing.T) {
	ctx := newProviderRequestIDTestContext()
	config := providerRequestIDTestConfig(0)
	logger := NewDefaultLogger(schemas.LogLevelError)
	providerStream := make(chan *schemas.BifrostStreamChunk, 1)
	resultErr := make(chan error, 1)

	go func() {
		stream, err := executeRequestWithRetries(ctx, config, func(_ schemas.Key) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"x-request-id": "req-stream-early"})
			return providerStream, nil
		}, nil, schemas.ChatCompletionStreamRequest, schemas.OpenAI, "gpt-test", nil, logger)
		if err != nil {
			resultErr <- fmt.Errorf("executeRequestWithRetries() error = %v", err)
			return
		}
		chunk := <-stream
		if chunk == nil || chunk.BifrostChatResponse == nil {
			resultErr <- fmt.Errorf("unexpected stream chunk: %#v", chunk)
			return
		}
		resultErr <- nil
	}()

	deadline := time.After(2 * time.Second)
	for GetStringFromContext(ctx, schemas.BifrostContextKeyProviderRequestID) != "req-stream-early" {
		select {
		case <-deadline:
			t.Fatal("provider request ID was not captured before the first stream chunk")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	providerStream <- &schemas.BifrostStreamChunk{BifrostChatResponse: &schemas.BifrostChatResponse{}}
	close(providerStream)
	if err := <-resultErr; err != nil {
		t.Fatal(err)
	}
}

func TestExecuteRequestWithRetriesRequestIDTrailFollowsKeyRetrySemantics(t *testing.T) {
	logger := NewDefaultLogger(schemas.LogLevelError)
	keys := []schemas.Key{{ID: "key-1", Name: "first"}, {ID: "key-2", Name: "second"}}

	t.Run("same key network retry", func(t *testing.T) {
		ctx := newProviderRequestIDTestContext()
		config := providerRequestIDTestConfig(1)
		keyProviderCalls := 0
		keyProvider := func(_ map[string]bool, _ map[string]bool) (schemas.Key, error) {
			keyProviderCalls++
			return keys[0], nil
		}
		attempt := 0
		var selected []string
		_, err := executeRequestWithRetries(ctx, config, func(key schemas.Key) (string, *schemas.BifrostError) {
			selected = append(selected, key.ID)
			ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"x-request-id": fmt.Sprintf("req-network-%d", attempt)})
			attempt++
			if attempt == 1 {
				return "", createBifrostError(schemas.ErrProviderDoRequest, nil, nil, false)
			}
			return "ok", nil
		}, keyProvider, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-test", nil, logger)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if keyProviderCalls != 1 || len(selected) != 2 || selected[0] != "key-1" || selected[1] != "key-1" {
			t.Fatalf("unexpected same-key retry: calls=%d selected=%v", keyProviderCalls, selected)
		}
		trail, _ := ctx.Value(schemas.BifrostContextKeyProviderRequestIDTrail).([]schemas.ProviderRequestIDRecord)
		if len(trail) != 2 || trail[0].RequestID != "req-network-0" || trail[1].RequestID != "req-network-1" {
			t.Fatalf("unexpected same-key request ID trail: %#v", trail)
		}
	})

	t.Run("rate limit rotates key", func(t *testing.T) {
		ctx := newProviderRequestIDTestContext()
		config := providerRequestIDTestConfig(1)
		keyProvider := func(used map[string]bool, _ map[string]bool) (schemas.Key, error) {
			for _, key := range keys {
				if !used[key.ID] {
					return key, nil
				}
			}
			return schemas.Key{}, fmt.Errorf("no key available")
		}
		attempt := 0
		var selected []string
		_, err := executeRequestWithRetries(ctx, config, func(key schemas.Key) (string, *schemas.BifrostError) {
			selected = append(selected, key.ID)
			ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"x-request-id": fmt.Sprintf("req-rotation-%d", attempt)})
			attempt++
			if attempt == 1 {
				return "", createBifrostError("rate limited", Ptr(429), nil, false)
			}
			return "ok", nil
		}, keyProvider, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-test", nil, logger)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(selected) != 2 || selected[0] != "key-1" || selected[1] != "key-2" {
			t.Fatalf("unexpected key rotation: %v", selected)
		}
		trail, _ := ctx.Value(schemas.BifrostContextKeyProviderRequestIDTrail).([]schemas.ProviderRequestIDRecord)
		if len(trail) != 2 || trail[0].RequestID != "req-rotation-0" || trail[1].RequestID != "req-rotation-1" {
			t.Fatalf("unexpected rotated-key request ID trail: %#v", trail)
		}
	})
}

func TestExecuteRequestWithRetriesKeySelectionFailureDoesNotCreateProviderRequestID(t *testing.T) {
	ctx := newProviderRequestIDTestContext()
	config := providerRequestIDTestConfig(0)
	logger := NewDefaultLogger(schemas.LogLevelError)
	handlerCalled := false

	_, err := executeRequestWithRetries(ctx, config, func(_ schemas.Key) (string, *schemas.BifrostError) {
		handlerCalled = true
		return "", nil
	}, func(_ map[string]bool, _ map[string]bool) (schemas.Key, error) {
		return schemas.Key{}, fmt.Errorf("key selection failed")
	}, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-test", nil, logger)
	if err == nil {
		t.Fatal("expected key selection error")
	}
	if handlerCalled {
		t.Fatal("request handler should not run after key selection failure")
	}
	if got := GetStringFromContext(ctx, schemas.BifrostContextKeyProviderRequestID); got != "" {
		t.Fatalf("unexpected current provider request ID %q", got)
	}
	trail, _ := ctx.Value(schemas.BifrostContextKeyProviderRequestIDTrail).([]schemas.ProviderRequestIDRecord)
	if len(trail) != 0 {
		t.Fatalf("unexpected provider request ID trail: %#v", trail)
	}
}

func TestClearCtxForFallbackClearsProviderRequestIDState(t *testing.T) {
	ctx := newProviderRequestIDTestContext()
	ctx.SetValue(schemas.BifrostContextKeyProviderRequestID, "parent-id")
	ctx.SetValue(schemas.BifrostContextKeyProviderRequestIDHeader, "x-request-id")
	ctx.SetValue(schemas.BifrostContextKeyProviderRequestIDTrail, []schemas.ProviderRequestIDRecord{{RequestID: "parent-id"}})

	clearCtxForFallback(ctx)

	if ctx.Value(schemas.BifrostContextKeyProviderRequestID) != nil || ctx.Value(schemas.BifrostContextKeyProviderRequestIDHeader) != nil || ctx.Value(schemas.BifrostContextKeyProviderRequestIDTrail) != nil {
		t.Fatalf("fallback retained provider request ID state: id=%v header=%v trail=%v",
			ctx.Value(schemas.BifrostContextKeyProviderRequestID),
			ctx.Value(schemas.BifrostContextKeyProviderRequestIDHeader),
			ctx.Value(schemas.BifrostContextKeyProviderRequestIDTrail))
	}
}
