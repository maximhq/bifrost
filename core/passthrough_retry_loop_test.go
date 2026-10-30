package bifrost

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func pilotRetryContext() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	return ctx
}
func pilotRetryConfig(max int) *schemas.ProviderConfig {
	return createTestConfig(max, time.Millisecond, 5*time.Millisecond)
}
func pilotResponse(status int, code, content string, h map[string]string) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{PassthroughResponse: &schemas.BifrostPassthroughResponse{
		StatusCode: status, Body: []byte(content), Headers: h,
	}}
}
func TestPassthroughLoopRetriesOrdinary429(t *testing.T) {
	calls := 0
	handler := func(_ schemas.Key) (*schemas.BifrostResponse, *schemas.BifrostError) {
		calls++
		if calls == 1 {
			return pilotResponse(429, "rate_limit_exceeded", `{"error":{"code":"rate_limit_exceeded"}}`, map[string]string{"Retry-After": "1"}), nil
		}
		return pilotResponse(200, "", "ok", nil), nil
	}
	got, err := executeRequestWithRetries(pilotRetryContext(), pilotRetryConfig(2), handler, nil, schemas.PassthroughRequest, schemas.OpenAI, "gpt-test", nil, NewDefaultLogger(schemas.LogLevelError))
	if err != nil || got == nil || got.PassthroughResponse == nil || got.PassthroughResponse.StatusCode != 200 || calls != 2 {
		t.Fatalf("calls=%d response=%+v err=%+v", calls, got, err)
	}
}
func TestPassthroughLoopRespectsNoRetry(t *testing.T) {
	calls := 0
	handler := func(_ schemas.Key) (*schemas.BifrostResponse, *schemas.BifrostError) {
		calls++
		return pilotResponse(429, "", "original-raw-429", map[string]string{"x-should-retry": "false"}), nil
	}
	got, err := executeRequestWithRetries(pilotRetryContext(), pilotRetryConfig(3), handler, nil, schemas.PassthroughRequest, schemas.OpenAI, "gpt-test", nil, NewDefaultLogger(schemas.LogLevelError))
	if err != nil || got == nil || got.PassthroughResponse == nil || got.PassthroughResponse.StatusCode != 429 || string(got.PassthroughResponse.Body) != "original-raw-429" || calls != 1 {
		t.Fatalf("calls=%d response=%+v err=%+v", calls, got, err)
	}
}
func TestPassthroughLoopExhaustionPreservesRaw429(t *testing.T) {
	calls := 0
	handler := func(_ schemas.Key) (*schemas.BifrostResponse, *schemas.BifrostError) {
		calls++
		return pilotResponse(429, "", "original-raw-429", map[string]string{"Retry-After": "1"}), nil
	}
	got, err := executeRequestWithRetries(pilotRetryContext(), pilotRetryConfig(2), handler, nil, schemas.PassthroughRequest, schemas.OpenAI, "gpt-test", nil, NewDefaultLogger(schemas.LogLevelError))
	if err != nil || got == nil || got.PassthroughResponse == nil || got.PassthroughResponse.StatusCode != 429 || string(got.PassthroughResponse.Body) != "original-raw-429" || calls != 3 {
		t.Fatalf("calls=%d response=%+v err=%+v", calls, got, err)
	}
}
func TestPassthroughLoopStream429BeforeOutput(t *testing.T) {
	calls := 0
	handler := func(_ schemas.Key) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		calls++
		stream := make(chan *schemas.BifrostStreamChunk, 2)
		if calls == 1 {
			stream <- &schemas.BifrostStreamChunk{BifrostPassthroughResponse: &schemas.BifrostPassthroughResponse{StatusCode: 429, Body: []byte(`{"error":{"code":"rate_limit_exceeded"}}`)}}
		} else {
			stream <- &schemas.BifrostStreamChunk{BifrostPassthroughResponse: &schemas.BifrostPassthroughResponse{StatusCode: 200, Body: []byte("event: response.completed\\n\\n")}}
		}
		close(stream)
		return stream, nil
	}
	got, err := executeRequestWithRetries(pilotRetryContext(), pilotRetryConfig(2), handler, nil, schemas.PassthroughStreamRequest, schemas.OpenAI, "gpt-test", nil, NewDefaultLogger(schemas.LogLevelError))
	if err != nil || calls != 2 || got == nil {
		t.Fatalf("calls=%d got=%v err=%+v", calls, got, err)
	}
	first, ok := <-got
	if !ok || first.BifrostPassthroughResponse == nil || first.BifrostPassthroughResponse.StatusCode != 200 {
		t.Fatalf("first=%+v ok=%v", first, ok)
	}
	for range got {
	}
}
func TestPassthroughLoopStreamNoReplayHeader(t *testing.T) {
	calls := 0
	handler := func(_ schemas.Key) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		calls++
		stream := make(chan *schemas.BifrostStreamChunk, 1)
		stream <- &schemas.BifrostStreamChunk{BifrostPassthroughResponse: &schemas.BifrostPassthroughResponse{StatusCode: 429, Headers: map[string]string{"x-should-retry": "false"}, Body: []byte("raw-error")}}
		close(stream)
		return stream, nil
	}
	got, err := executeRequestWithRetries(pilotRetryContext(), pilotRetryConfig(3), handler, nil, schemas.PassthroughStreamRequest, schemas.OpenAI, "gpt-test", nil, NewDefaultLogger(schemas.LogLevelError))
	if err != nil || calls != 1 || got == nil {
		t.Fatalf("calls=%d got=%v err=%+v", calls, got, err)
	}
	first, ok := <-got
	if !ok || first.BifrostPassthroughResponse == nil || first.BifrostPassthroughResponse.StatusCode != 429 || string(first.BifrostPassthroughResponse.Body) != "raw-error" {
		t.Fatalf("first=%+v ok=%v", first, ok)
	}
	for range got {
	}
}

func TestPassthroughLoopStreamLateReplayRefusedMarkerNeverReplays(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
	}{
		{"split replay refused", []string{`{"error":{"code":"upstream_reset_`, `replay_refused"}}`}},
		{"late insufficient quota", []string{`{"error":{"code":"rate_limit_exceeded"}}`, `{"error":{"code":"insufficient_quota"}}`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			wantHeaders := map[string]string{"Content-Type": "application/json", "X-Upstream": "kept"}
			handler := func(_ schemas.Key) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
				calls++
				stream := make(chan *schemas.BifrostStreamChunk, len(tt.parts))
				for i, part := range tt.parts {
					headers := map[string]string(nil)
					if i == 0 {
						headers = wantHeaders
					}
					stream <- &schemas.BifrostStreamChunk{BifrostPassthroughResponse: &schemas.BifrostPassthroughResponse{StatusCode: 429, Headers: headers, Body: []byte(part)}}
				}
				close(stream)
				return stream, nil
			}
			got, err := executeRequestWithRetries(pilotRetryContext(), pilotRetryConfig(3), handler, nil, schemas.PassthroughStreamRequest, schemas.OpenAI, "gpt-test", nil, NewDefaultLogger(schemas.LogLevelError))
			if err != nil || got == nil || calls != 1 {
				t.Fatalf("calls=%d got=%v err=%+v", calls, got, err)
			}
			var body string
			var first *schemas.BifrostPassthroughResponse
			for chunk := range got {
				if chunk.BifrostPassthroughResponse == nil {
					t.Fatalf("unexpected chunk: %+v", chunk)
				}
				if first == nil {
					first = chunk.BifrostPassthroughResponse
				}
				body += string(chunk.BifrostPassthroughResponse.Body)
			}
			wantBody := ""
			for _, part := range tt.parts {
				wantBody += part
			}
			if first == nil || first.StatusCode != 429 || first.Headers["Content-Type"] != wantHeaders["Content-Type"] || first.Headers["X-Upstream"] != wantHeaders["X-Upstream"] || body != wantBody {
				t.Fatalf("raw response lost: first=%+v body=%q want=%q", first, body, wantBody)
			}
		})
	}
}
