package bifrost

import (
	"context"
	"strings"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// newEncryptedReasoningRequest builds a Responses request whose input replays a
// reasoning item carrying encrypted_content, the shape a coding CLI sends back on
// every follow-up turn.
func newEncryptedReasoningRequest(encrypted string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-5.6-sol",
			Input: []schemas.ResponsesMessage{
				{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{
						ContentStr: schemas.Ptr("run the tests"),
					},
				},
				{
					ID:   schemas.Ptr("rs_067d4968"),
					Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
					ResponsesReasoning: &schemas.ResponsesReasoning{
						Summary: []schemas.ResponsesReasoningSummary{
							{Type: schemas.ResponsesReasoningContentBlockTypeSummaryText, Text: "planning the run"},
						},
						EncryptedContent: schemas.Ptr(encrypted),
					},
				},
			},
		},
	}
}

func encryptedContentError() *schemas.BifrostError {
	return &schemas.BifrostError{
		StatusCode: schemas.Ptr(400),
		Error: &schemas.ErrorField{
			Type:    schemas.Ptr("invalid_request_error"),
			Code:    schemas.Ptr("invalid_encrypted_content"),
			Message: "The encrypted content for item rs_067d4968 could not be verified. Reason: Encrypted content could not be decrypted or parsed.",
		},
	}
}

// TestExecuteRequestWithRetries_StripsEncryptedContentAndRetries pins the fail-soft
// path for reasoning items the target upstream cannot decrypt (a different org, key,
// tenancy, or provider minted them). The retry must happen even at the default
// MaxRetries of 0, since this is not a transient failure the retry budget covers.
func TestExecuteRequestWithRetries_StripsEncryptedContentAndRetries(t *testing.T) {
	config := createTestConfig(0, time.Millisecond, time.Millisecond)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	logger := NewDefaultLogger(schemas.LogLevelError)

	req := newEncryptedReasoningRequest("gAAAAABqc9R3ciphertext.eyJlbmRwb2ludF9zbHVnIjoieCJ9")

	callCount := 0
	var secondAttemptInput []schemas.ResponsesMessage
	handler := func(_ schemas.Key) (string, *schemas.BifrostError) {
		callCount++
		if callCount == 1 {
			return "", encryptedContentError()
		}
		secondAttemptInput = req.ResponsesRequest.Input
		return "success", nil
	}

	result, err := executeRequestWithRetries(ctx, config, handler, nil,
		schemas.ResponsesRequest, schemas.OpenAI, "gpt-5.6-sol", req, logger)

	if err != nil {
		t.Fatalf("expected the stripped retry to succeed, got %v", err)
	}
	if result != "success" {
		t.Fatalf("expected 'success', got %q", result)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 attempts (original + stripped retry), got %d", callCount)
	}
	if len(secondAttemptInput) != 2 {
		t.Fatalf("expected both input items to survive the strip, got %d", len(secondAttemptInput))
	}
	reasoning := secondAttemptInput[1].ResponsesReasoning
	if reasoning == nil {
		t.Fatal("expected the reasoning item to survive with its summary")
	}
	if reasoning.EncryptedContent != nil {
		t.Errorf("expected encrypted_content to be stripped, got %q", *reasoning.EncryptedContent)
	}
	if len(reasoning.Summary) != 1 || reasoning.Summary[0].Text != "planning the run" {
		t.Errorf("expected the summary to be preserved, got %+v", reasoning.Summary)
	}
	if secondAttemptInput[1].ID == nil || *secondAttemptInput[1].ID != "rs_067d4968" {
		t.Errorf("expected the item id to be preserved, got %+v", secondAttemptInput[1].ID)
	}
}

// TestExecuteRequestWithRetries_EncryptedContentStripRetriesOnce ensures the extra
// attempt is granted exactly once, so an upstream that keeps rejecting cannot drive
// an unbounded retry loop.
func TestExecuteRequestWithRetries_EncryptedContentStripRetriesOnce(t *testing.T) {
	config := createTestConfig(0, time.Millisecond, time.Millisecond)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	logger := NewDefaultLogger(schemas.LogLevelError)

	req := newEncryptedReasoningRequest("gAAAAABciphertext")

	callCount := 0
	handler := func(_ schemas.Key) (string, *schemas.BifrostError) {
		callCount++
		return "", encryptedContentError()
	}

	_, err := executeRequestWithRetries(ctx, config, handler, nil,
		schemas.ResponsesRequest, schemas.OpenAI, "gpt-5.6-sol", req, logger)

	if err == nil {
		t.Fatal("expected the upstream error to be returned after the stripped retry also failed")
	}
	if callCount != 2 {
		t.Fatalf("expected exactly 2 attempts, got %d", callCount)
	}
}

// TestExecuteRequestWithRetries_FailSoftRetrySkipsBackoff pins the no-backoff half of the
// fail-soft contract. Backoff exists to let a transient upstream condition clear, but an
// encrypted-reasoning rejection is deterministic: the same ciphertext will be refused for
// as long as the identity that minted it stays unavailable. Waiting buys nothing and adds
// latency to a turn the user is watching, so the stripped retry is issued immediately.
//
// Asserted on wall time because the retry path calls time.Sleep directly, with no
// injectable clock. The margin is deliberately wide (3s configured, 1s bound) so this
// cannot flake on a loaded machine while still failing loudly if the sleep returns.
// TestExecuteRequestWithRetries_OrdinaryRetryPaysBackoff is the negative control: without
// it, this test would also pass if the backoff configuration were ignored entirely.
func TestExecuteRequestWithRetries_FailSoftRetrySkipsBackoff(t *testing.T) {
	config := createTestConfig(0, 3*time.Second, 3*time.Second)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	logger := NewDefaultLogger(schemas.LogLevelError)

	req := newEncryptedReasoningRequest("gAAAAABqc9R3ciphertext")

	callCount := 0
	handler := func(_ schemas.Key) (string, *schemas.BifrostError) {
		callCount++
		if callCount == 1 {
			return "", encryptedContentError()
		}
		return "success", nil
	}

	start := time.Now()
	result, err := executeRequestWithRetries(ctx, config, handler, nil,
		schemas.ResponsesRequest, schemas.OpenAI, "gpt-5.6-sol", req, logger)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected the stripped retry to succeed, got %v", err)
	}
	if result != "success" {
		t.Fatalf("expected 'success', got %q", result)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 attempts (original + stripped retry), got %d", callCount)
	}
	if elapsed > time.Second {
		t.Fatalf("fail-soft retry must skip the %s backoff, but the call took %s",
			config.NetworkConfig.RetryBackoffInitial, elapsed)
	}
}

// TestExecuteRequestWithRetries_OrdinaryRetryPaysBackoff is the negative control for
// TestExecuteRequestWithRetries_FailSoftRetrySkipsBackoff. An ordinary retryable failure
// is exactly the transient case backoff is for, so it must still sleep -- which is what
// makes the fail-soft test's fast completion meaningful rather than a config that never
// took effect.
func TestExecuteRequestWithRetries_OrdinaryRetryPaysBackoff(t *testing.T) {
	config := createTestConfig(1, 300*time.Millisecond, 300*time.Millisecond)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	logger := NewDefaultLogger(schemas.LogLevelError)

	req := newEncryptedReasoningRequest("gAAAAABqc9R3ciphertext")

	callCount := 0
	handler := func(_ schemas.Key) (string, *schemas.BifrostError) {
		callCount++
		if callCount == 1 {
			return "", &schemas.BifrostError{
				StatusCode: schemas.Ptr(500),
				Error:      &schemas.ErrorField{Message: "upstream unavailable"},
			}
		}
		return "success", nil
	}

	start := time.Now()
	result, err := executeRequestWithRetries(ctx, config, handler, nil,
		schemas.ResponsesRequest, schemas.OpenAI, "gpt-5.6-sol", req, logger)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected the retry to succeed, got %v", err)
	}
	if result != "success" {
		t.Fatalf("expected 'success', got %q", result)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 attempts, got %d", callCount)
	}
	if elapsed < 250*time.Millisecond {
		t.Fatalf("an ordinary retryable failure must still back off (configured %s), took %s",
			config.NetworkConfig.RetryBackoffInitial, elapsed)
	}
}

// TestExecuteRequestWithRetries_FailSoftAttemptCountsTowardRotationAccounting pins the
// interaction between the fail-soft extra attempt and the attempt trail's rotation
// bookkeeping. The rotation candidate is only recorded while another iteration can still
// run, and the fail-soft strip widens the loop bound by extraAttempts -- so an attempt at
// the old MaxRetries boundary is no longer terminal and its per-key failure really can
// trigger a rotation that the trail must report.
//
// Timeline with MaxRetries=1: attempt 0 is an encrypted-content rejection (fail soft,
// same key, extraAttempts becomes 1), attempt 1 is a 429 on key-a, attempt 2 runs on
// key-b and succeeds. Attempt 1 is therefore what triggered the rotation.
func TestExecuteRequestWithRetries_FailSoftAttemptCountsTowardRotationAccounting(t *testing.T) {
	config := createTestConfig(1, time.Millisecond, time.Millisecond)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	logger := NewDefaultLogger(schemas.LogLevelError)

	req := newEncryptedReasoningRequest("gAAAAABqc9R3ciphertext")

	keyA := schemas.Key{ID: "key-a", Name: "key-a", Value: schemas.SecretVar{Val: "a"}}
	keyB := schemas.Key{ID: "key-b", Name: "key-b", Value: schemas.SecretVar{Val: "b"}}
	keyProvider := func(usedKeyIDs, deadKeyIDs map[string]bool) (schemas.Key, error) {
		if usedKeyIDs["key-a"] || deadKeyIDs["key-a"] {
			return keyB, nil
		}
		return keyA, nil
	}

	callCount := 0
	var keysSeen []string
	handler := func(key schemas.Key) (string, *schemas.BifrostError) {
		callCount++
		keysSeen = append(keysSeen, key.ID)
		switch callCount {
		case 1:
			return "", encryptedContentError()
		case 2:
			return "", &schemas.BifrostError{
				StatusCode: schemas.Ptr(429),
				Error:      &schemas.ErrorField{Message: "rate limited"},
			}
		default:
			return "success", nil
		}
	}

	result, err := executeRequestWithRetries(ctx, config, handler, keyProvider,
		schemas.ResponsesRequest, schemas.OpenAI, "gpt-5.6-sol", req, logger)

	if err != nil {
		t.Fatalf("expected the rotated attempt to succeed, got %v", err)
	}
	if result != "success" {
		t.Fatalf("expected 'success', got %q", result)
	}
	if callCount != 3 {
		t.Fatalf("expected 3 attempts (rejection, 429, rotated success), got %d (keys %v)", callCount, keysSeen)
	}
	if len(keysSeen) != 3 || keysSeen[2] != "key-b" {
		t.Fatalf("expected the final attempt to run on the rotated key, got %v", keysSeen)
	}

	trail, ok := ctx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord)
	if !ok {
		t.Fatal("expected an attempt trail in context")
	}
	if len(trail) < 2 {
		t.Fatalf("expected at least 2 recorded attempts, got %d: %+v", len(trail), trail)
	}
	// The 429 on key-a is what forced key selection onto key-b.
	rotating := trail[1]
	if rotating.KeyID != "key-a" {
		t.Fatalf("expected the second recorded attempt to be on key-a, got %+v", trail)
	}
	if !rotating.TriggeredRotation {
		t.Fatalf("expected the per-key failure that preceded the rotation to be marked TriggeredRotation, got %+v", trail)
	}
}

// TestExecuteRequestWithRetries_NoStripWhenNothingEncrypted keeps an unrelated 400
// terminal: with no encrypted_content to remove there is nothing to fail soft on,
// so the request must not burn a second upstream call.
func TestExecuteRequestWithRetries_NoStripWhenNothingEncrypted(t *testing.T) {
	config := createTestConfig(0, time.Millisecond, time.Millisecond)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	logger := NewDefaultLogger(schemas.LogLevelError)

	req := newEncryptedReasoningRequest("ciphertext")
	req.ResponsesRequest.Input[1].ResponsesReasoning.EncryptedContent = nil

	callCount := 0
	handler := func(_ schemas.Key) (string, *schemas.BifrostError) {
		callCount++
		return "", encryptedContentError()
	}

	if _, err := executeRequestWithRetries(ctx, config, handler, nil,
		schemas.ResponsesRequest, schemas.OpenAI, "gpt-5.6-sol", req, logger); err == nil {
		t.Fatal("expected the upstream error to be returned")
	}
	if callCount != 1 {
		t.Fatalf("expected a single attempt when there is nothing to strip, got %d", callCount)
	}
}

func TestStripResponsesEncryptedContent(t *testing.T) {
	t.Run("drops a reasoning item left empty by the strip", func(t *testing.T) {
		req := newEncryptedReasoningRequest("ciphertext")
		req.ResponsesRequest.Input[1].ResponsesReasoning.Summary = nil

		if !stripResponsesEncryptedContent(nil, req) {
			t.Fatal("expected the strip to report a change")
		}
		if len(req.ResponsesRequest.Input) != 1 {
			t.Fatalf("expected the now-empty reasoning item to be dropped, got %d items", len(req.ResponsesRequest.Input))
		}
	})

	t.Run("does not mutate the caller's reasoning struct", func(t *testing.T) {
		req := newEncryptedReasoningRequest("ciphertext")
		original := req.ResponsesRequest.Input[1].ResponsesReasoning

		stripResponsesEncryptedContent(nil, req)

		if original.EncryptedContent == nil || *original.EncryptedContent != "ciphertext" {
			t.Error("expected the original reasoning struct to be left untouched")
		}
	})

	t.Run("reports no change when there is nothing to strip", func(t *testing.T) {
		req := newEncryptedReasoningRequest("ciphertext")
		req.ResponsesRequest.Input[1].ResponsesReasoning.EncryptedContent = nil

		if stripResponsesEncryptedContent(nil, req) {
			t.Error("expected no change to be reported")
		}
	})

	t.Run("ignores non-responses requests", func(t *testing.T) {
		req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest}
		if stripResponsesEncryptedContent(nil, req) {
			t.Error("expected no change for a chat request")
		}
		if stripResponsesEncryptedContent(nil, nil) {
			t.Error("expected no change for a nil request")
		}
	})

	t.Run("rewrites the raw body under passthrough", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		req := newEncryptedReasoningRequest("ciphertext")
		req.ResponsesRequest.RawRequestBody = []byte(`{"model":"gpt-5.6-sol","input":[` +
			`{"type":"message","role":"user","content":"run the tests"},` +
			`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"planning"}],"encrypted_content":"cipher","unmodeled_field":7},` +
			`{"type":"reasoning","id":"rs_2","summary":[],"encrypted_content":"cipher"}` +
			`],"store":false}`)

		if !stripResponsesEncryptedContent(ctx, req) {
			t.Fatal("expected the raw body to be rewritten")
		}

		body := string(req.ResponsesRequest.RawRequestBody)
		if strings.Contains(body, "encrypted_content") {
			t.Errorf("expected encrypted_content to be gone, got %s", body)
		}
		if strings.Contains(body, `"rs_2"`) {
			t.Errorf("expected the summary-less reasoning item to be dropped, got %s", body)
		}
		if !strings.Contains(body, `"unmodeled_field":7`) {
			t.Errorf("expected fields Bifrost does not model to survive, got %s", body)
		}
		if !strings.Contains(body, `"store":false`) || !strings.Contains(body, `"run the tests"`) {
			t.Errorf("expected the rest of the body to be untouched, got %s", body)
		}
	})

	t.Run("declines large payload mode", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)

		req := newEncryptedReasoningRequest("ciphertext")
		if stripResponsesEncryptedContent(ctx, req) {
			t.Error("expected no change to be claimed when the body streams past core unparsed")
		}
		if req.ResponsesRequest.Input[1].ResponsesReasoning.EncryptedContent == nil {
			t.Error("expected the typed input to be left alone in large payload mode")
		}
	})
}

func TestIsEncryptedReasoningRejection(t *testing.T) {
	tests := []struct {
		name string
		err  *schemas.BifrostError
		want bool
	}{
		{"code match", encryptedContentError(), true},
		{
			name: "message match without a code",
			err: &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error: &schemas.ErrorField{
					Message: "The encrypted content for item rs_1 could not be verified. Reason: Encrypted content item_id did not match the target item id.",
				},
			},
			want: true,
		},
		{
			// Anthropic's refusal of a foreign payload: no code, block named in the message.
			name: "anthropic redacted_thinking rejection",
			err: &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr("invalid_request_error"),
					Message: "messages.1.content.0: Invalid `data` in `redacted_thinking` block",
				},
			},
			want: true,
		},
		{
			// Anthropic rejects a thinking signature too, but the strip only clears
			// encrypted_content, so a retry would resend the same payload.
			name: "anthropic thinking signature rejection stays unhandled",
			err: &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr("invalid_request_error"),
					Message: "messages.1.content.0: Invalid `signature` in `thinking` block",
				},
			},
			want: false,
		},
		{
			name: "unrelated anthropic 400 naming thinking",
			err: &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error:      &schemas.ErrorField{Message: "Invalid value for `thinking.budget_tokens`: must be >= 1024"},
			},
			want: false,
		},
		{
			name: "unrelated 400",
			err: &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error:      &schemas.ErrorField{Message: "Invalid 'input': value did not match any expected variant"},
			},
			want: false,
		},
		{
			name: "right code, wrong status",
			err: &schemas.BifrostError{
				StatusCode: schemas.Ptr(500),
				Error:      &schemas.ErrorField{Code: schemas.Ptr("invalid_encrypted_content")},
			},
			want: false,
		},
		{"nil error", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isEncryptedReasoningRejection(tt.err); got != tt.want {
				t.Errorf("isEncryptedReasoningRejection() = %v, want %v", got, tt.want)
			}
		})
	}
}
