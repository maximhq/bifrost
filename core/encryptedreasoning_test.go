package bifrost

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

// newEncryptedReasoningCompactionRequest builds the /v1/responses/compact counterpart
// of newEncryptedReasoningRequest. Codex sends its remote compaction over this shape,
// replaying the same reasoning items the Responses turns carried.
func newEncryptedReasoningCompactionRequest(encrypted string) *schemas.BifrostRequest {
	responses := newEncryptedReasoningRequest(encrypted).ResponsesRequest
	return &schemas.BifrostRequest{
		RequestType: schemas.CompactionRequest,
		CompactionRequest: &schemas.BifrostCompactionRequest{
			Provider: schemas.OpenAI,
			Model:    responses.Model,
			Input:    responses.Input,
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

// TestExecuteRequestWithRetries_HealsOnEveryProviderRejection runs the whole fail-soft
// loop once per provider that can refuse a replayed payload, with the refusal that
// provider actually returns.
//
// TestIsEncryptedReasoningRejection covers the predicate in isolation; this covers the
// consequence. The two are worth keeping apart: a predicate that answers correctly is
// only useful if the retry it gates reaches the upstream with the payload rewritten,
// and the fail-soft's whole purpose is that the second attempt succeeds where the first
// could not. Every case asserts both -- two attempts, and encrypted_content gone from
// the second one.
func TestExecuteRequestWithRetries_HealsOnEveryProviderRejection(t *testing.T) {
	rejections := []struct {
		name     string
		provider schemas.ModelProvider
		model    string
		err      *schemas.BifrostError
	}{
		{
			name:     "openai",
			provider: schemas.OpenAI,
			model:    "gpt-5.6-sol",
			err:      encryptedContentError(),
		},
		{
			name:     "anthropic",
			provider: schemas.Anthropic,
			model:    "claude-haiku-4-5-20251001",
			err: &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr("invalid_request_error"),
					Message: "messages.1.content.0: Invalid `data` in `redacted_thinking` block",
				},
			},
		},
		{
			name:     "bedrock",
			provider: schemas.Bedrock,
			model:    "global.anthropic.claude-haiku-4-5-20251001-v1:0",
			err: &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr("ValidationException"),
					Message: "The signature in the reasoningContent block at messages.1.content.0 is invalid",
				},
			},
		},
		{
			name:     "vertex",
			provider: schemas.Vertex,
			model:    "claude-opus-4-8",
			err: &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error: &schemas.ErrorField{
					Message: "Publisher Model error: messages.1.content.0: Invalid `data` in `redacted_thinking` block",
				},
			},
		},
		{
			// Bedrock Mantle serves OpenAI-family models over an OpenAI-compatible
			// /v1/responses surface, so a fallback from Azure to Mantle replays an
			// Azure-minted encrypted_content at an upstream that mints its own
			// `rsn_`/`smry_`-prefixed tokens. Mantle refuses on the prefix, before
			// it ever tries to decrypt, and names neither the wire field nor any of
			// the vocabulary the other providers use.
			name:     "bedrock mantle",
			provider: schemas.BedrockMantle,
			model:    "gpt-5.6-sol",
			err: &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr("invalid_request_error"),
					Message: "encrypted content missing recognized prefix (expected `rsn_` or `smry_`)",
				},
			},
		},
		{
			name:     "gemini",
			provider: schemas.Gemini,
			model:    "gemini-2.5-flash",
			err: &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr("INVALID_ARGUMENT"),
					Message: "Unable to submit request because thought_signature is invalid.",
				},
			},
		},
	}

	for _, rejection := range rejections {
		t.Run(rejection.name, func(t *testing.T) {
			config := createTestConfig(0, time.Millisecond, time.Millisecond)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
			logger := NewDefaultLogger(schemas.LogLevelError)

			req := newEncryptedReasoningRequest("gAAAAABqc9R3ciphertext")
			req.ResponsesRequest.Provider = rejection.provider
			req.ResponsesRequest.Model = rejection.model

			callCount := 0
			var secondAttemptInput []schemas.ResponsesMessage
			handler := func(_ schemas.Key) (string, *schemas.BifrostError) {
				callCount++
				if callCount == 1 {
					return "", rejection.err
				}
				secondAttemptInput = req.ResponsesRequest.Input
				return "success", nil
			}

			result, err := executeRequestWithRetries(ctx, config, handler, nil,
				schemas.ResponsesRequest, rejection.provider, rejection.model, req, logger)

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
		})
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

	// Compaction is the one shape that resolves a replayed reasoning id server-side.
	// The id was issued by the identity that minted the encrypted_content this upstream
	// just refused, so /v1/responses/compact answers a stripped retry that still carries
	// it with 404 "Items are not persisted when `store` is set to false" -- a second
	// upstream call spent to earn a different error. Summary and content stay: those are
	// the client's own bytes, not the upstream's handle.
	//
	// TestExecuteRequestWithRetries_StripsEncryptedContentAndRetries pins the opposite
	// for /v1/responses, which accepts an inline item id it never issued and where the
	// id still links the item to the turn that produced it.
	t.Run("drops the item id from a surviving compaction reasoning item", func(t *testing.T) {
		req := newEncryptedReasoningCompactionRequest("ciphertext")

		if !stripResponsesEncryptedContent(nil, req) {
			t.Fatal("expected the strip to report a change")
		}
		if len(req.CompactionRequest.Input) != 2 {
			t.Fatalf("expected the summarised reasoning item to survive, got %d items", len(req.CompactionRequest.Input))
		}
		if req.CompactionRequest.Input[1].ID != nil {
			t.Errorf("expected the foreign item id to be dropped, got %q", *req.CompactionRequest.Input[1].ID)
		}
		if len(req.CompactionRequest.Input[1].ResponsesReasoning.Summary) != 1 {
			t.Error("expected the summary to survive alongside the dropped id")
		}
	})

	t.Run("drops the item id from a surviving raw compaction reasoning item", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		req := newEncryptedReasoningCompactionRequest("ciphertext")
		req.CompactionRequest.RawRequestBody = []byte(`{"model":"gpt-5.6-sol","input":[` +
			`{"type":"message","id":"msg_1","role":"user","content":"run the tests"},` +
			`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"planning"}],"encrypted_content":"cipher"}` +
			`]}`)

		if !stripResponsesEncryptedContent(ctx, req) {
			t.Fatal("expected the raw body to be rewritten")
		}

		body := string(req.CompactionRequest.RawRequestBody)
		if strings.Contains(body, `"rs_1"`) {
			t.Errorf("expected the foreign item id to be dropped, got %s", body)
		}
		if !strings.Contains(body, `"summary_text"`) {
			t.Errorf("expected the summary to survive alongside the dropped id, got %s", body)
		}
		if !strings.Contains(body, `"msg_1"`) {
			t.Errorf("expected items the strip did not touch to keep their ids, got %s", body)
		}
	})

	// The Responses shape keeps its ids; only compaction sheds them.
	t.Run("keeps the item id on a surviving raw responses reasoning item", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		req := newEncryptedReasoningRequest("ciphertext")
		req.ResponsesRequest.RawRequestBody = []byte(`{"model":"gpt-5.6-sol","input":[` +
			`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"planning"}],"encrypted_content":"cipher"}` +
			`]}`)

		if !stripResponsesEncryptedContent(ctx, req) {
			t.Fatal("expected the raw body to be rewritten")
		}
		if body := string(req.ResponsesRequest.RawRequestBody); !strings.Contains(body, `"rs_1"`) {
			t.Errorf("expected the item id to be preserved on the responses shape, got %s", body)
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

	// Codex's remote compaction replays the same reasoning items over
	// /v1/responses/compact, which Bifrost models as its own request shape. The
	// upstream rejects an unverifiable payload there exactly as it does on a normal
	// turn, so the strip has to reach it or the 400 is handed straight to the client.
	t.Run("strips a compaction request", func(t *testing.T) {
		req := newEncryptedReasoningCompactionRequest("ciphertext")

		if !stripResponsesEncryptedContent(nil, req) {
			t.Fatal("expected the strip to report a change on a compaction request")
		}
		if len(req.CompactionRequest.Input) != 2 {
			t.Fatalf("expected the summarised reasoning item to survive, got %d items", len(req.CompactionRequest.Input))
		}
		if req.CompactionRequest.Input[1].ResponsesReasoning.EncryptedContent != nil {
			t.Error("expected encrypted_content to be cleared on the compaction input")
		}
	})

	t.Run("rewrites a compaction raw body under passthrough", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		req := newEncryptedReasoningCompactionRequest("ciphertext")
		req.CompactionRequest.RawRequestBody = []byte(`{"model":"gpt-5.6-sol","input":[` +
			`{"type":"message","role":"user","content":"run the tests"},` +
			`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"planning"}],"encrypted_content":"cipher"}` +
			`]}`)

		if !stripResponsesEncryptedContent(ctx, req) {
			t.Fatal("expected the compaction raw body to be rewritten")
		}
		if body := string(req.CompactionRequest.RawRequestBody); strings.Contains(body, "encrypted_content") {
			t.Errorf("expected encrypted_content to be gone, got %s", body)
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
		// One replayed encrypted_content reaches each provider as a different
		// field, so each provider refuses it in its own vocabulary. The cases
		// below name the egress converter that produces the field, since that
		// -- not vendor prose -- is what the detector has to keep up with.
		{
			name: "anthropic redacted_thinking data (convertBifrostReasoningToAnthropicThinking)",
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
			name: "anthropic on vertex, same refusal wrapped by the platform",
			err: &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error: &schemas.ErrorField{
					Message: "Publisher Model error: messages.2.content.0: Invalid `data` in `redacted_thinking` block",
				},
			},
			want: true,
		},
		{
			name: "bedrock converse reasoning signature (reasoningSignatureForBedrock)",
			err: &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr("ValidationException"),
					Message: "The signature in the reasoningContent block at messages.1.content.0 is invalid",
				},
			},
			want: true,
		},
		{
			// Bedrock Mantle's OpenAI-compatible surface rejects a foreign
			// encrypted_content on its prefix, before decryption is attempted, so
			// the refusal names neither the wire field nor any of the
			// decrypt/verify vocabulary the other upstreams use.
			name: "bedrock mantle foreign encrypted content prefix",
			err: &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr("invalid_request_error"),
					Message: "encrypted content missing recognized prefix (expected `rsn_` or `smry_`)",
				},
			},
			want: true,
		},
		{
			name: "gemini thought signature (thoughtSignatureFromEncryptedContent)",
			err: &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr("INVALID_ARGUMENT"),
					Message: "Unable to submit request because thought_signature is invalid.",
				},
			},
			want: true,
		},
		// The neighbouring Anthropic 400 that must NOT match. It fires because
		// thinking blocks were dropped or rewritten, and the strip drops the
		// redacted block outright -- retrying would re-send the exact shape the
		// upstream just refused, one wasted call to reach the same error.
		{
			name: "anthropic thinking blocks modified is not an encryption refusal",
			err: &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr("invalid_request_error"),
					Message: "messages.1.content.0: `thinking` or `redacted_thinking` blocks in the latest assistant message cannot be modified. These blocks must remain as they were in the original response.",
				},
			},
			want: false,
		},
		// A request-signing failure names a signature and nothing about
		// reasoning. Stripping encrypted_content cannot fix a credential.
		{
			name: "request signing failure is not a reasoning refusal",
			err: &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr("InvalidSignatureException"),
					Message: "The request signature we calculated does not match the signature you provided. Check your AWS Secret Access Key and signing method.",
				},
			},
			want: false,
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
			// This case expected false while the strip only cleared encrypted_content
			// on Responses-shaped requests: a retry would have resent the same
			// signature and earned the same 400, so recognising the refusal only
			// bought a wasted upstream call.
			//
			// stripChatUnverifiableReasoning (#6110) removed that constraint. It is
			// the chat-shaped strip, and stripChatReasoningDetails clears
			// detail.Signature and detail.Data on every replayed assistant turn -- so
			// the retry now goes out without the signature the new model refused.
			// This exact message is the trigger that function's doc comment names:
			// a complexity or virtual-model router picks a model per turn, and turn
			// N+1 replays a thinking block minted by whoever answered turn N.
			//
			// Recognising it is therefore correct, and the remediation is real.
			name: "anthropic thinking signature rejection triggers the chat-shaped strip",
			err: &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr("invalid_request_error"),
					Message: "messages.1.content.0: Invalid `signature` in `thinking` block",
				},
			},
			want: true,
		},
		{
			// The guard the case above relies on: a bare "signature" with no reasoning
			// word must stay unrecognised. SigV4 mismatches read like this, and
			// stripping reasoning cannot fix one.
			name: "request-signing failure is not a reasoning rejection",
			err: &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error: &schemas.ErrorField{
					Message: "The request signature we calculated does not match the signature you provided",
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

// Regression test for the production sequence a Claude Code session hit when its
// Azure primary was saturated. Reconstructed from the request logs, which record
// four attempts on azure/gpt-5.6-sol (all HTTP 429), a transition to
// bedrock_mantle/openai.gpt-5.6-sol at fallback_index 1, and a single fallback
// attempt ending in HTTP 400:
//
//	{"type": "invalid_request_error", "code": "validation_error",
//	 "message": "encrypted content missing recognized prefix (expected `rsn_` or `smry_`)"}
//
// The attempt_trail is the tell. It holds exactly one entry -- attempt 0, fail_reason
// invalid_request_error, triggered_rotation false -- so the fail-soft strip in
// executeRequestWithRetries never fired. Mantle mints its own `rsn_`/`smry_`-prefixed
// tokens and rejects a foreign payload on the prefix before attempting to decrypt it,
// naming neither the encrypted_content field nor any of the decrypt/verify vocabulary
// the OpenAI, Anthropic, Bedrock Converse and Gemini refusals use. The detector saw an
// ordinary 400 and handed it to the client, ending the coding session's turn.
//
// The cases above pin the predicate and the retry it gates against stubbed handlers.
// This one pins the whole sequence over real HTTP: the primary exhausting
// its retry budget, the fallback transition, the refusal on the fallback's first
// attempt, and the retry that reaches the same upstream with encrypted_content gone
// from the serialized body.
//
// Fidelity note: the fallback runs as schemas.OpenAI rather than schemas.BedrockMantle
// because the Mantle provider computes its host from the region
// (bedrock-mantle.<region>.api.aws, see mantleOpenAIURL) and honours no BaseURL, so it
// cannot be pointed at a test server. The surface being emulated is the same one --
// Mantle serves OpenAI-family models over an OpenAI-compatible /v1/responses endpoint,
// which is why its refusal arrives in an OpenAI error envelope -- and the fail-soft
// path under test is provider-agnostic: it matches on the refusal text, not the
// provider key.
const mantleEncryptedContentRefusal = "encrypted content missing recognized prefix (expected `rsn_` or `smry_`)"

// azureRateLimitBody is the 429 envelope the primary returned on all four attempts.
const azureRateLimitBody = `{"error":{"message":"Requests to the Responses_Create Operation under Azure OpenAI API have exceeded token rate limit of your current AIServices S0 pricing tier.","type":"too_many_requests","code":"429"}}`

// mantleRefusalBody reproduces the fallback's 400 verbatim from the logged
// error_details.error object.
const mantleRefusalBody = `{"error":{"type":"invalid_request_error","code":"validation_error","message":"encrypted content missing recognized prefix (expected ` + "`rsn_`" + ` or ` + "`smry_`" + `)"}}`

// mantleRateLimitBody is the 429 envelope the Mantle-shaped upstream returns when it
// is the saturated primary rather than the fallback.
const mantleRateLimitBody = `{"error":{"message":"Too many tokens, please wait before trying again.","type":"too_many_requests","code":"429"}}`

// azureRefusalBody is the mirror-image refusal: an Azure endpoint handed a
// Mantle-minted `rsn_`-prefixed payload it has no key for. Azure and OpenAI share the
// Responses surface and its error vocabulary, so the refusal carries the stable
// invalid_encrypted_content code rather than a prefix complaint.
const azureRefusalBody = `{"error":{"type":"invalid_request_error","code":"invalid_encrypted_content","message":"The encrypted content for item rs_067d4968 could not be verified. Reason: Encrypted content could not be decrypted or parsed."}}`

// successBody is a minimal completed Responses payload for the healed retry.
const successBody = `{"id":"resp_healed_1","object":"response","created_at":1,"status":"completed","model":"gpt-5.6-sol","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"tests pass","annotations":[],"logprobs":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`

// recordingServer captures every request body it serves so the test can assert on
// what actually went over the wire, not just on hit counts.
type recordingServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
}

func (rs *recordingServer) record(body string) int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.bodies = append(rs.bodies, body)
	return len(rs.bodies)
}

func (rs *recordingServer) snapshot() []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]string(nil), rs.bodies...)
}

// newRecordingServer serves handler(attempt, body) where attempt is 1-based.
func newRecordingServer(handler func(attempt int, w http.ResponseWriter)) *recordingServer {
	rs := &recordingServer{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		handler(rs.record(string(body)), w)
	}))
	return rs
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// configureUpstream registers provider on account, pointed at url with maxRetries.
// Azure takes its endpoint from the key config; every other provider here takes it
// from NetworkConfig.BaseURL.
func configureUpstream(account *MockAccount, provider schemas.ModelProvider, keyID, url string, maxRetries int) {
	key := schemas.Key{
		ID:     keyID,
		Value:  *schemas.NewSecretVar("sk-" + keyID),
		Models: schemas.WhiteList{"*"},
		Weight: 100,
	}
	if provider == schemas.Azure {
		account.AddProvider(provider, 1, 1)
		key.AzureKeyConfig = &schemas.AzureKeyConfig{Endpoint: *schemas.NewSecretVar(url)}
	} else {
		account.AddProviderWithBaseURL(provider, 1, 1, url)
	}
	account.configs[provider].NetworkConfig.MaxRetries = maxRetries
	account.configs[provider].NetworkConfig.RetryBackoffInitial = time.Millisecond
	account.configs[provider].NetworkConfig.RetryBackoffMax = 2 * time.Millisecond
	account.SetKeysForProvider(provider, []schemas.Key{key})
}

// TestResponsesFallbackHealsEncryptedContentRefusal walks the logged production
// sequence end to end: the primary 429s through its whole retry budget, core falls
// back, the fallback refuses the replayed encrypted_content minted by the primary's
// identity, and the fail-soft strip earns one more attempt that succeeds with the
// reasoning summary intact.
//
// Both fallback directions run, because a pool that lists two upstreams for one model
// will sooner or later cross in each direction, and the two upstreams do not refuse
// alike. Azure rejects on decryption and says so with the stable
// invalid_encrypted_content code; Mantle rejects on prefix shape before decryption is
// attempted and quotes neither a code the detector knows nor the field name. Only the
// azure -> mantle direction reached users, which is precisely why the other direction
// needs pinning too: it works today by a different branch of the same predicate, and
// nothing but a test stops a later edit from taking that branch away.
func TestResponsesFallbackHealsEncryptedContentRefusal(t *testing.T) {
	const primaryMaxRetries = 3

	directions := []struct {
		name string
		// The saturated primary and the fallback that refuses the replay.
		primaryProvider, fallbackProvider schemas.ModelProvider
		primaryModel, fallbackModel       string
		primaryKeyID, fallbackKeyID       string
		rateLimitBody                     string
		// The fallback's refusal of the primary-minted ciphertext, and the
		// ciphertext itself in the shape the primary would have issued it.
		refusalBody, encryptedContent string
	}{
		{
			// The logged incident: gpt-5.6-sol pinned to azure with a
			// bedrock_mantle fallback, Claude Code replaying reasoning every turn.
			name:             "azure primary falls back to mantle",
			primaryProvider:  schemas.Azure,
			fallbackProvider: schemas.OpenAI,
			primaryModel:     "gpt-5.6-sol",
			fallbackModel:    "openai.gpt-5.6-sol",
			primaryKeyID:     "azure-hrt",
			fallbackKeyID:    "AWS Bedrock Mantle us-east-2",
			rateLimitBody:    azureRateLimitBody,
			refusalBody:      mantleRefusalBody,
			// Azure-minted: Fernet-shaped, carrying none of the `rsn_`/`smry_`
			// prefixes Mantle requires.
			encryptedContent: "gAAAAABqc9R3ciphertext.eyJlbmRwb2ludF9zbHVnIjoieCJ9",
		},
		{
			// The mirror image, which the same pool produces as soon as Mantle is
			// the saturated side.
			name:             "mantle primary falls back to azure",
			primaryProvider:  schemas.OpenAI,
			fallbackProvider: schemas.Azure,
			primaryModel:     "openai.gpt-5.6-sol",
			fallbackModel:    "gpt-5.6-sol",
			primaryKeyID:     "AWS Bedrock Mantle us-east-2",
			fallbackKeyID:    "azure-hrt",
			rateLimitBody:    mantleRateLimitBody,
			refusalBody:      azureRefusalBody,
			// Mantle-minted: prefixed exactly as Mantle stamps a reasoning body,
			// and undecryptable by any Azure deployment.
			encryptedContent: "rsn_01K9mantleciphertextpayload",
		},
	}

	for _, direction := range directions {
		t.Run(direction.name, func(t *testing.T) {
			var primaryHits atomic.Int32
			primary := newRecordingServer(func(attempt int, w http.ResponseWriter) {
				primaryHits.Add(1)
				writeJSON(w, http.StatusTooManyRequests, direction.rateLimitBody)
			})
			defer primary.Close()

			fallback := newRecordingServer(func(attempt int, w http.ResponseWriter) {
				if attempt == 1 {
					writeJSON(w, http.StatusBadRequest, direction.refusalBody)
					return
				}
				writeJSON(w, http.StatusOK, successBody)
			})
			defer fallback.Close()

			account := NewMockAccount()
			configureUpstream(account, direction.primaryProvider, direction.primaryKeyID, primary.URL, primaryMaxRetries)
			// MaxRetries 0 on the fallback so the only extra attempt it can make
			// is the fail-soft one. A second hit means the strip ran, and nothing else.
			configureUpstream(account, direction.fallbackProvider, direction.fallbackKeyID, fallback.URL, 0)

			client := newStreamTestClient(t, account)

			ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
			resp, bifrostErr := client.ResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
				Provider:  direction.primaryProvider,
				Model:     direction.primaryModel,
				Fallbacks: []schemas.Fallback{{Provider: direction.fallbackProvider, Model: direction.fallbackModel}},
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
							EncryptedContent: schemas.Ptr(direction.encryptedContent),
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: schemas.Ptr(64000),
				},
			})

			if bifrostErr != nil {
				t.Fatalf("the fallback should have healed after the strip, got %s (primary hits=%d, fallback hits=%d)",
					bifrostErr.Error.Message, primaryHits.Load(), len(fallback.snapshot()))
			}
			if resp == nil {
				t.Fatal("expected a response from the healed retry")
			}

			// 1. The primary burned its whole retry budget on 429s before core gave up.
			if got, want := int(primaryHits.Load()), primaryMaxRetries+1; got != want {
				t.Errorf("primary attempts = %d, want %d (initial + %d retries)", got, want, primaryMaxRetries)
			}

			// 2. The fallback was attempted twice: the refusal, then the stripped
			// retry. One hit is the production bug -- the 400 went straight to the client.
			bodies := fallback.snapshot()
			if len(bodies) != 2 {
				t.Fatalf("fallback attempts = %d, want 2 (refusal + stripped retry); "+
					"1 means the refusal was never recognized as an encrypted-reasoning rejection", len(bodies))
			}

			// 3. The first fallback attempt carried the primary's ciphertext, which
			// is what earned the refusal.
			if !strings.Contains(bodies[0], direction.encryptedContent) {
				t.Errorf("first fallback body should replay the primary's ciphertext, got %s", bodies[0])
			}

			// 4. The retry reached the same upstream with the ciphertext gone and the
			// visible narrative kept -- exactly what a client that never captured
			// encrypted reasoning would have sent.
			if strings.Contains(bodies[1], "encrypted_content") {
				t.Errorf("stripped retry still carries encrypted_content: %s", bodies[1])
			}
			if !strings.Contains(bodies[1], "planning the run") {
				t.Errorf("stripped retry dropped the reasoning summary: %s", bodies[1])
			}
			if !strings.Contains(bodies[1], "rs_067d4968") {
				t.Errorf("stripped retry dropped the reasoning item id: %s", bodies[1])
			}

			// 5. The strip is auditable. The logged payload's routing_engine_logs is
			// where an operator reconstructs a turn, so the fail-soft has to leave a
			// mark there.
			var stripLogged bool
			for _, entry := range ctx.GetRoutingEngineLogs() {
				if strings.Contains(entry.Message, "Stripped unverifiable encrypted reasoning content") {
					stripLogged = true
					break
				}
			}
			if !stripLogged {
				t.Error("the fail-soft strip left no routing engine log entry for operators to trace")
			}
		})
	}
}

// TestMantleEncryptedContentRefusalIsNotConfusedWithOtherValidationErrors guards the
// widened detector from the other direction. Mantle returns the same
// invalid_request_error/validation_error/400 envelope for ordinary bad requests, and
// those must not buy a retry: the strip cannot fix them, so the extra upstream call
// would be pure latency on a request that is going to fail either way.
func TestMantleEncryptedContentRefusalIsNotConfusedWithOtherValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    bool
	}{
		{
			name:    "the logged refusal",
			message: mantleEncryptedContentRefusal,
			want:    true,
		},
		{
			name:    "unsupported parameter shares the envelope but names another field",
			message: "unsupported parameter: `temperature` is not supported with this model",
			want:    false,
		},
		{
			name:    "context length shares the envelope and mentions the input",
			message: "input exceeds the maximum context length for this model",
			want:    false,
		},
		{
			name:    "a prefix complaint about an unrelated field",
			message: "model id missing recognized prefix (expected `openai.` or `anthropic.`)",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr("invalid_request_error"),
					Code:    schemas.Ptr("validation_error"),
					Message: tt.message,
				},
			}
			if got := isEncryptedReasoningRejection(err); got != tt.want {
				t.Errorf("isEncryptedReasoningRejection(%q) = %v, want %v", tt.message, got, tt.want)
			}
		})
	}
}

// newThinkingSignatureChatRequest builds the shape a complexity/virtual-model router
// produces: a multi-turn chat conversation whose assistant turn replays a thinking
// block minted by whichever model the router picked on an earlier turn. When the
// router sends the next turn to a different model, the signature travels with it and
// the upstream refuses it.
func newThinkingSignatureChatRequest(signature string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.Anthropic,
			Model:    "claude-sonnet-5",
			Input: []schemas.ChatMessage{
				{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("run the tests")},
				},
				{
					Role:    schemas.ChatMessageRoleAssistant,
					Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("On it.")},
					ChatAssistantMessage: &schemas.ChatAssistantMessage{
						ReasoningDetails: []schemas.ChatReasoningDetails{
							{
								Index:     0,
								Type:      schemas.BifrostReasoningDetailsTypeText,
								Text:      schemas.Ptr("planning the run"),
								Signature: schemas.Ptr(signature),
							},
						},
					},
				},
			},
		},
	}
}

// thinkingSignatureError is the refusal reported against smart-routing-v1: an
// Anthropic model rejecting a thinking-block signature it did not mint.
func thinkingSignatureError() *schemas.BifrostError {
	return &schemas.BifrostError{
		StatusCode: schemas.Ptr(400),
		Error: &schemas.ErrorField{
			Type:    schemas.Ptr("invalid_request_error"),
			Message: "The model returned the following errors: messages.23.content.0: Invalid `signature` in `thinking` block",
		},
	}
}

// TestIsEncryptedReasoningRejection_ThinkingSignature pins the reported refusal as a
// fail-soft trigger. It matches through the bare-"signature" branch rather than a
// field marker: the message names neither encrypted_content nor redacted_thinking.
func TestIsEncryptedReasoningRejection_ThinkingSignature(t *testing.T) {
	if !isEncryptedReasoningRejection(thinkingSignatureError()) {
		t.Fatal("expected the thinking-block signature refusal to be a fail-soft trigger")
	}

	// A 5xx carrying the same words is a transient upstream fault, not an
	// unverifiable payload; the ordinary retry path owns it.
	transient := thinkingSignatureError()
	transient.StatusCode = schemas.Ptr(503)
	if isEncryptedReasoningRejection(transient) {
		t.Error("expected a 5xx not to trigger the fail-soft strip")
	}
}

// TestStripUnverifiableReasoning_ChatShape covers the gap that let this error reach
// clients: the strip only ever handled Responses-shaped requests, so a chat-shaped
// one returned false and the retry never happened.
func TestStripUnverifiableReasoning_ChatShape(t *testing.T) {
	t.Run("clears a thinking signature and keeps the reasoning text", func(t *testing.T) {
		req := newThinkingSignatureChatRequest("ErUBCkYIBRgCKkD...")

		if !stripUnverifiableReasoning(nil, req) {
			t.Fatal("expected the strip to report a change on a chat-shaped request")
		}
		details := req.ChatRequest.Input[1].ChatAssistantMessage.ReasoningDetails
		if len(details) != 1 {
			t.Fatalf("expected the reasoning detail to survive, got %d", len(details))
		}
		if details[0].Signature != nil {
			t.Errorf("expected the signature to be cleared, got %q", *details[0].Signature)
		}
		if details[0].Text == nil || *details[0].Text != "planning the run" {
			t.Errorf("expected the reasoning text to survive, got %+v", details[0].Text)
		}
	})

	t.Run("clears encrypted reasoning data", func(t *testing.T) {
		req := newThinkingSignatureChatRequest("sig")
		details := req.ChatRequest.Input[1].ChatAssistantMessage.ReasoningDetails
		details[0].Type = schemas.BifrostReasoningDetailsTypeEncrypted
		details[0].Signature = nil
		details[0].Data = schemas.Ptr("ciphertext")

		if !stripUnverifiableReasoning(nil, req) {
			t.Fatal("expected the strip to report a change")
		}
		if got := req.ChatRequest.Input[1].ChatAssistantMessage.ReasoningDetails[0].Data; got != nil {
			t.Errorf("expected encrypted data to be cleared, got %q", *got)
		}
	})

	t.Run("drops a reasoning detail left with nothing to say", func(t *testing.T) {
		req := newThinkingSignatureChatRequest("sig")
		req.ChatRequest.Input[1].ChatAssistantMessage.ReasoningDetails[0].Text = nil

		if !stripUnverifiableReasoning(nil, req) {
			t.Fatal("expected the strip to report a change")
		}
		if got := len(req.ChatRequest.Input[1].ChatAssistantMessage.ReasoningDetails); got != 0 {
			t.Errorf("expected the empty reasoning detail to be dropped, got %d", got)
		}
	})

	// Claiming a change with nothing to strip buys a second identical upstream call.
	t.Run("reports no change when nothing is unverifiable", func(t *testing.T) {
		req := newThinkingSignatureChatRequest("sig")
		req.ChatRequest.Input[1].ChatAssistantMessage.ReasoningDetails[0].Signature = nil

		if stripUnverifiableReasoning(nil, req) {
			t.Error("expected no change when no signature or encrypted data is present")
		}
	})

	// The caller's slice and structs are shared with plugins and the transport layer,
	// so the rewrite must not mutate them in place.
	t.Run("does not mutate the caller's reasoning details", func(t *testing.T) {
		req := newThinkingSignatureChatRequest("keep-me")
		original := req.ChatRequest.Input[1].ChatAssistantMessage.ReasoningDetails

		if !stripUnverifiableReasoning(nil, req) {
			t.Fatal("expected the strip to report a change")
		}
		if original[0].Signature == nil || *original[0].Signature != "keep-me" {
			t.Error("expected the caller's original reasoning detail to be untouched")
		}
	})
}

// TestStripUnverifiableReasoning_ResponsesContentBlockSignature covers the second half
// of the gap: even on the Responses shape the strip only cleared
// ResponsesReasoning.EncryptedContent, never a reasoning content block's signature.
func TestStripUnverifiableReasoning_ResponsesContentBlockSignature(t *testing.T) {
	req := newEncryptedReasoningRequest("ciphertext")
	req.ResponsesRequest.Input[1].ResponsesReasoning.EncryptedContent = nil
	req.ResponsesRequest.Input[1].Content = &schemas.ResponsesMessageContent{
		ContentBlocks: []schemas.ResponsesMessageContentBlock{
			{
				Type:      schemas.ResponsesOutputMessageContentTypeReasoning,
				Text:      schemas.Ptr("planning the run"),
				Signature: schemas.Ptr("ErUBCkYIBRgCKkD..."),
			},
		},
	}

	if !stripUnverifiableReasoning(nil, req) {
		t.Fatal("expected the strip to report a change for a content-block signature")
	}
	blocks := req.ResponsesRequest.Input[1].Content.ContentBlocks
	if len(blocks) != 1 {
		t.Fatalf("expected the block to survive, got %d", len(blocks))
	}
	if blocks[0].Signature != nil {
		t.Errorf("expected the signature to be cleared, got %q", *blocks[0].Signature)
	}
	if blocks[0].Text == nil || *blocks[0].Text != "planning the run" {
		t.Errorf("expected the reasoning text to survive, got %+v", blocks[0].Text)
	}
}

// TestStripUnverifiableReasoning_ResponsesContentBlockEncryptedContent covers the other
// unverifiable field the content block models. ResponsesMessageContentBlock carries both
// Signature and EncryptedContent, so clearing only the signature leaves a block-only
// encrypted payload on the retry -- which earns the same refusal the strip exists to avoid.
func TestStripUnverifiableReasoning_ResponsesContentBlockEncryptedContent(t *testing.T) {
	req := newEncryptedReasoningRequest("ciphertext")
	req.ResponsesRequest.Input[1].ResponsesReasoning.EncryptedContent = nil
	req.ResponsesRequest.Input[1].Content = &schemas.ResponsesMessageContent{
		ContentBlocks: []schemas.ResponsesMessageContentBlock{
			{
				Type:             schemas.ResponsesOutputMessageContentTypeReasoning,
				Text:             schemas.Ptr("planning the run"),
				EncryptedContent: schemas.Ptr("gAAAAABn..."),
			},
		},
	}

	if !stripUnverifiableReasoning(nil, req) {
		t.Fatal("expected the strip to report a change for a content-block encrypted_content")
	}
	blocks := req.ResponsesRequest.Input[1].Content.ContentBlocks
	if len(blocks) != 1 {
		t.Fatalf("expected the block to survive, got %d", len(blocks))
	}
	if blocks[0].EncryptedContent != nil {
		t.Errorf("expected encrypted_content to be cleared, got %q", *blocks[0].EncryptedContent)
	}
	if blocks[0].Text == nil || *blocks[0].Text != "planning the run" {
		t.Errorf("expected the reasoning text to survive, got %+v", blocks[0].Text)
	}
}

// TestStripUnverifiableReasoning_ResponsesContentBlockBothFields pins that one pass clears
// both carriers. Clearing them in separate passes would still need two upstream calls to
// heal, and the strip only gets one retry.
func TestStripUnverifiableReasoning_ResponsesContentBlockBothFields(t *testing.T) {
	req := newEncryptedReasoningRequest("ciphertext")
	req.ResponsesRequest.Input[1].ResponsesReasoning.EncryptedContent = nil
	req.ResponsesRequest.Input[1].Content = &schemas.ResponsesMessageContent{
		ContentBlocks: []schemas.ResponsesMessageContentBlock{
			{
				Type:             schemas.ResponsesOutputMessageContentTypeReasoning,
				Text:             schemas.Ptr("planning the run"),
				Signature:        schemas.Ptr("ErUBCkYIBRgCKkD..."),
				EncryptedContent: schemas.Ptr("gAAAAABn..."),
			},
		},
	}

	if !stripUnverifiableReasoning(nil, req) {
		t.Fatal("expected the strip to report a change")
	}
	blocks := req.ResponsesRequest.Input[1].Content.ContentBlocks
	if len(blocks) != 1 {
		t.Fatalf("expected the block to survive, got %d", len(blocks))
	}
	if blocks[0].Signature != nil {
		t.Errorf("expected the signature to be cleared, got %q", *blocks[0].Signature)
	}
	if blocks[0].EncryptedContent != nil {
		t.Errorf("expected encrypted_content to be cleared, got %q", *blocks[0].EncryptedContent)
	}
}

// TestStripUnverifiableReasoning_RawContentBlockCarriers is the raw-body half of the same
// rule. The passthrough path used to key entirely off a top-level encrypted_content, so an
// item whose only carrier was a content-block signature -- replayed Anthropic thinking --
// was forwarded verbatim AND reported as no-change, which meant the one fail-soft retry
// never fired at all on this path even though the typed path heals the identical request.
func TestStripUnverifiableReasoning_RawContentBlockCarriers(t *testing.T) {
	t.Run("content block signature with no top-level encrypted_content", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		req := newEncryptedReasoningRequest("ciphertext")
		req.ResponsesRequest.RawRequestBody = []byte(`{"model":"claude-sonnet-5","input":[` +
			`{"type":"message","role":"user","content":"run the tests"},` +
			`{"type":"reasoning","id":"rs_1","summary":[],"content":[` +
			`{"type":"reasoning_text","text":"planning","signature":"ErUBCkYIBRgCKkD...","unmodeled_field":7}` +
			`]}` +
			`],"store":false}`)

		if !stripResponsesEncryptedContent(ctx, req) {
			t.Fatal("expected a content-block signature alone to be rewritten on the raw path")
		}

		body := string(req.ResponsesRequest.RawRequestBody)
		if strings.Contains(body, "signature") {
			t.Errorf("expected the content-block signature to be gone, got %s", body)
		}
		if !strings.Contains(body, `"planning"`) {
			t.Errorf("expected the reasoning text to survive, got %s", body)
		}
		if !strings.Contains(body, `"unmodeled_field":7`) {
			t.Errorf("expected fields Bifrost does not model to survive, got %s", body)
		}
		if !strings.Contains(body, `"store":false`) || !strings.Contains(body, `"run the tests"`) {
			t.Errorf("expected the rest of the body to be untouched, got %s", body)
		}
	})

	t.Run("content block encrypted_content with no top-level encrypted_content", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		req := newEncryptedReasoningRequest("ciphertext")
		req.ResponsesRequest.RawRequestBody = []byte(`{"model":"gpt-5.6-sol","input":[` +
			`{"type":"message","role":"user","content":"run the tests"},` +
			`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"planning"}],"content":[` +
			`{"type":"reasoning_text","text":"planning","encrypted_content":"gAAAAABn..."}` +
			`]}` +
			`],"store":false}`)

		if !stripResponsesEncryptedContent(ctx, req) {
			t.Fatal("expected a content-block encrypted_content alone to be rewritten on the raw path")
		}

		body := string(req.ResponsesRequest.RawRequestBody)
		if strings.Contains(body, "encrypted_content") {
			t.Errorf("expected the content-block encrypted_content to be gone, got %s", body)
		}
		if !strings.Contains(body, `"summary_text"`) {
			t.Errorf("expected the summary to survive, got %s", body)
		}
	})

	t.Run("leaves an item with no carrier untouched", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		req := newEncryptedReasoningRequest("ciphertext")
		req.ResponsesRequest.RawRequestBody = []byte(`{"model":"gpt-5.6-sol","input":[` +
			`{"type":"message","role":"user","content":"run the tests"},` +
			`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"planning"}],"content":[` +
			`{"type":"reasoning_text","text":"planning"}` +
			`]}` +
			`],"store":false}`)
		before := string(req.ResponsesRequest.RawRequestBody)

		if stripResponsesEncryptedContent(ctx, req) {
			t.Error("expected no change to be claimed when nothing unverifiable is present")
		}
		if body := string(req.ResponsesRequest.RawRequestBody); body != before {
			t.Errorf("expected the body to be untouched, got %s", body)
		}
	})
}

// TestStripChatUnverifiableReasoning_AnthropicRawBody covers the raw-passthrough half of
// the chat shape. /v1/messages is the drop-in route a signature-replaying router most
// often arrives on, and it sets BifrostContextKeyUseRawRequestBody -- so the typed strip
// never sees the payload, the strip reported no change, and the client got the same 400
// the fail-soft exists to absorb.
func TestStripChatUnverifiableReasoning_AnthropicRawBody(t *testing.T) {
	anthropicCtx := func() *schemas.BifrostContext {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
		ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
		return ctx
	}

	t.Run("blanks a thinking signature and keeps the reasoning text", func(t *testing.T) {
		req := newThinkingSignatureChatRequest("ErUBCkYIBRgCKkD...")
		req.ChatRequest.RawRequestBody = []byte(`{"model":"claude-sonnet-5","messages":[` +
			`{"role":"user","content":"run the tests"},` +
			`{"role":"assistant","content":[` +
			`{"type":"thinking","thinking":"planning the run","signature":"ErUBCkYIBRgCKkD...","unmodeled_field":7},` +
			`{"type":"text","text":"On it."}` +
			`]},` +
			// The later assistant turn matters: Anthropic protects the last message with
			// role assistant from any edit, so without a turn after it the payload under
			// test would sit on the protected turn and the rewrite would decline.
			`{"role":"user","content":"now ship it"},` +
			`{"role":"assistant","content":[{"type":"text","text":"Shipped."}]},` +
			`{"role":"user","content":"thanks"}` +
			`],"max_tokens":1024}`)

		if !stripUnverifiableReasoning(anthropicCtx(), req) {
			t.Fatal("expected the raw Anthropic body to be rewritten")
		}

		body := string(req.ChatRequest.RawRequestBody)
		if strings.Contains(body, "ErUBCkYIBRgCKkD") {
			t.Errorf("expected the foreign signature to be gone, got %s", body)
		}
		if !strings.Contains(body, `"signature":""`) {
			t.Errorf("expected an empty signature rather than an omitted field, got %s", body)
		}
		if !strings.Contains(body, `"planning the run"`) || !strings.Contains(body, `"On it."`) {
			t.Errorf("expected the reasoning text and the reply to survive, got %s", body)
		}
		if !strings.Contains(body, `"unmodeled_field":7`) {
			t.Errorf("expected fields Bifrost does not model to survive, got %s", body)
		}
		if !strings.Contains(body, `"max_tokens":1024`) || !strings.Contains(body, `"run the tests"`) {
			t.Errorf("expected the rest of the body to be untouched, got %s", body)
		}
	})

	t.Run("drops a redacted_thinking block whose data is the whole payload", func(t *testing.T) {
		req := newThinkingSignatureChatRequest("sig")
		req.ChatRequest.RawRequestBody = []byte(`{"model":"claude-sonnet-5","messages":[` +
			`{"role":"user","content":"run the tests"},` +
			`{"role":"assistant","content":[` +
			`{"type":"redacted_thinking","data":"EroBCkYIBRgCKkDdeadbeef"},` +
			`{"type":"text","text":"On it."}` +
			`]},` +
			// Earlier turn, not the protected latest assistant message -- see above.
			`{"role":"user","content":"now ship it"},` +
			`{"role":"assistant","content":[{"type":"text","text":"Shipped."}]},` +
			`{"role":"user","content":"thanks"}` +
			`],"max_tokens":1024}`)

		if !stripUnverifiableReasoning(anthropicCtx(), req) {
			t.Fatal("expected the redacted_thinking block to be removed")
		}

		body := string(req.ChatRequest.RawRequestBody)
		if strings.Contains(body, "redacted_thinking") || strings.Contains(body, "deadbeef") {
			t.Errorf("expected the redacted block and its data to be gone, got %s", body)
		}
		if !strings.Contains(body, `"On it."`) {
			t.Errorf("expected the assistant's own text to survive, got %s", body)
		}
	})

	t.Run("leaves a turn that would be emptied untouched", func(t *testing.T) {
		// A redacted-only assistant turn cannot be healed by removal: dropping the block
		// leaves an empty content array, which Anthropic rejects, and dropping the whole
		// message would break user/assistant alternation. Reporting no change is the
		// truthful answer -- the caller spends an upstream call on a true.
		req := newThinkingSignatureChatRequest("sig")
		raw := `{"model":"claude-sonnet-5","messages":[` +
			`{"role":"user","content":"run the tests"},` +
			`{"role":"assistant","content":[{"type":"redacted_thinking","data":"EroBdeadbeef"}]}` +
			`],"max_tokens":1024}`
		req.ChatRequest.RawRequestBody = []byte(raw)

		if stripUnverifiableReasoning(anthropicCtx(), req) {
			t.Error("expected no change to be claimed when the turn cannot be rewritten")
		}
		if string(req.ChatRequest.RawRequestBody) != raw {
			t.Errorf("expected the body to be untouched, got %s", req.ChatRequest.RawRequestBody)
		}
	})

	t.Run("reports no change when nothing is unverifiable", func(t *testing.T) {
		req := newThinkingSignatureChatRequest("sig")
		raw := `{"model":"claude-sonnet-5","messages":[` +
			`{"role":"user","content":"run the tests"},` +
			`{"role":"assistant","content":[{"type":"text","text":"On it."}]}` +
			`],"max_tokens":1024}`
		req.ChatRequest.RawRequestBody = []byte(raw)

		if stripUnverifiableReasoning(anthropicCtx(), req) {
			t.Error("expected no change for a body carrying no thinking blocks")
		}
		if string(req.ChatRequest.RawRequestBody) != raw {
			t.Errorf("expected the body to be untouched, got %s", req.ChatRequest.RawRequestBody)
		}
	})

	t.Run("declines a dialect it cannot parse", func(t *testing.T) {
		// Gemini also enables raw chat passthrough, but its thought_signature rides inside
		// parts[*] on a generateContent body. Rewriting it with the Anthropic field paths
		// would corrupt the request, so the strip keeps returning false there.
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
		ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "genai")

		req := newThinkingSignatureChatRequest("sig")
		raw := `{"contents":[{"role":"model","parts":[{"text":"planning","thoughtSignature":"abc"}]}]}`
		req.ChatRequest.RawRequestBody = []byte(raw)

		if stripUnverifiableReasoning(ctx, req) {
			t.Error("expected no change to be claimed for an unsupported raw dialect")
		}
		if string(req.ChatRequest.RawRequestBody) != raw {
			t.Errorf("expected the body to be untouched, got %s", req.ChatRequest.RawRequestBody)
		}
	})

	t.Run("declines large payload mode even on the anthropic dialect", func(t *testing.T) {
		ctx := anthropicCtx()
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)

		req := newThinkingSignatureChatRequest("sig")
		raw := `{"messages":[{"role":"assistant","content":[` +
			`{"type":"thinking","thinking":"x","signature":"sig"}]}]}`
		req.ChatRequest.RawRequestBody = []byte(raw)

		if stripUnverifiableReasoning(ctx, req) {
			t.Error("expected no change when the body streams past core unparsed")
		}
	})

	// Anthropic verifies that thinking and redacted_thinking blocks in the latest
	// assistant message arrive exactly as it minted them: "Within the latest assistant
	// message, the sequence of consecutive thinking blocks must match what the model
	// generated in the original request: you can't rearrange, edit, or partially drop
	// them." Editing there swaps the signature refusal for a second 400 -- "`thinking`
	// or `redacted_thinking` blocks in the latest assistant message cannot be modified"
	// -- after a wasted upstream call, so the retry has to leave that turn alone.
	// https://platform.claude.com/docs/en/build-with-claude/thinking#preserving-thinking-blocks

	t.Run("leaves the latest assistant message untouched", func(t *testing.T) {
		req := newThinkingSignatureChatRequest("ErUBCkYIBRgCKkD...")
		raw := `{"model":"claude-sonnet-5","messages":[` +
			`{"role":"user","content":"run the tests"},` +
			`{"role":"assistant","content":[` +
			`{"type":"thinking","thinking":"planning the run","signature":"ErUBCkYIBRgCKkD..."},` +
			`{"type":"text","text":"On it."}` +
			`]}` +
			`],"max_tokens":1024}`
		req.ChatRequest.RawRequestBody = []byte(raw)

		if stripUnverifiableReasoning(anthropicCtx(), req) {
			t.Error("expected no change to be claimed when only the protected turn carries a payload")
		}
		if string(req.ChatRequest.RawRequestBody) != raw {
			t.Errorf("expected the latest assistant message to survive verbatim, got %s", req.ChatRequest.RawRequestBody)
		}
	})

	t.Run("skips the latest assistant turn even when it is not the last message", func(t *testing.T) {
		// A tool-result round trip ends on a user message, so the protected turn sits at
		// index 1 rather than at the end. Treating the last element as the protected one
		// would rewrite it anyway and earn the "cannot be modified" refusal.
		req := newThinkingSignatureChatRequest("sig")
		raw := `{"model":"claude-sonnet-5","messages":[` +
			`{"role":"user","content":"what is the weather in Paris?"},` +
			`{"role":"assistant","content":[` +
			`{"type":"redacted_thinking","data":"EroBCkYIBRgCKkDdeadbeef"},` +
			`{"type":"tool_use","id":"toolu_01","name":"get_weather","input":{"city":"Paris"}}` +
			`]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"20C, sunny"}]}` +
			`],"max_tokens":1024}`
		req.ChatRequest.RawRequestBody = []byte(raw)

		if stripUnverifiableReasoning(anthropicCtx(), req) {
			t.Error("expected the protected turn to be located by role, not by array position")
		}
		if string(req.ChatRequest.RawRequestBody) != raw {
			t.Errorf("expected the tool-use turn to survive verbatim, got %s", req.ChatRequest.RawRequestBody)
		}
	})

	t.Run("still rewrites earlier assistant turns", func(t *testing.T) {
		// Only the latest assistant message is protected -- "Allowed: outside tool use,
		// omit prior turns' thinking" -- so an earlier turn is still healed, which is the
		// case the retry exists to fix.
		req := newThinkingSignatureChatRequest("ErUBCkYIBRgCKkD...")
		req.ChatRequest.RawRequestBody = []byte(`{"model":"claude-sonnet-5","messages":[` +
			`{"role":"user","content":"run the tests"},` +
			`{"role":"assistant","content":[` +
			`{"type":"thinking","thinking":"planning the run","signature":"ErUBCkYIBRgCKkD..."},` +
			`{"type":"redacted_thinking","data":"EroBCkYIBRgCKkDdeadbeef"},` +
			`{"type":"text","text":"On it."}` +
			`]},` +
			`{"role":"user","content":"now ship it"},` +
			`{"role":"assistant","content":[{"type":"text","text":"Shipped."}]},` +
			`{"role":"user","content":"thanks"}` +
			`],"max_tokens":1024}`)

		if !stripUnverifiableReasoning(anthropicCtx(), req) {
			t.Fatal("expected the earlier assistant turn to be rewritten")
		}

		body := string(req.ChatRequest.RawRequestBody)
		if strings.Contains(body, "ErUBCkYIBRgCKkD") || strings.Contains(body, "deadbeef") {
			t.Errorf("expected the earlier turn's unverifiable payloads to be gone, got %s", body)
		}
		if !strings.Contains(body, `"signature":""`) {
			t.Errorf("expected an empty signature rather than an omitted field, got %s", body)
		}
		if !strings.Contains(body, `"planning the run"`) || !strings.Contains(body, `"On it."`) {
			t.Errorf("expected the earlier turn's own text to survive, got %s", body)
		}
		if !strings.Contains(body, `{"type":"text","text":"Shipped."}`) {
			t.Errorf("expected the latest assistant message to survive verbatim, got %s", body)
		}
	})
}

// TestExecuteRequestWithRetries_HealsThinkingSignatureOnRawAnthropicChat is the
// consequence test for the raw path: the rewrite only matters if the retry it gates
// actually reaches the upstream with the signature blanked.
func TestExecuteRequestWithRetries_HealsThinkingSignatureOnRawAnthropicChat(t *testing.T) {
	config := createTestConfig(0, time.Millisecond, time.Millisecond)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
	logger := NewDefaultLogger(schemas.LogLevelError)

	req := newThinkingSignatureChatRequest("ErUBCkYIBRgCKkD...")
	req.ChatRequest.RawRequestBody = []byte(`{"model":"claude-sonnet-5","messages":[` +
		`{"role":"user","content":"run the tests"},` +
		`{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"planning the run","signature":"ErUBCkYIBRgCKkD..."}` +
		`]},` +
		// Earlier turn: Anthropic protects the last message with role assistant from
		// edits, so the payload has to sit behind a later assistant turn to reach a retry.
		`{"role":"user","content":"now ship it"},` +
		`{"role":"assistant","content":[{"type":"text","text":"Shipped."}]},` +
		`{"role":"user","content":"thanks"}` +
		`],"max_tokens":1024}`)

	callCount := 0
	var secondAttemptBody string
	handler := func(_ schemas.Key) (string, *schemas.BifrostError) {
		callCount++
		if callCount == 1 {
			return "", thinkingSignatureError()
		}
		secondAttemptBody = string(req.ChatRequest.RawRequestBody)
		return "success", nil
	}

	result, err := executeRequestWithRetries(ctx, config, handler, nil,
		schemas.ChatCompletionRequest, schemas.Anthropic, "claude-sonnet-5", req, logger)

	if err != nil {
		t.Fatalf("expected the stripped retry to succeed, got %v", err)
	}
	if result != "success" {
		t.Fatalf("expected 'success', got %q", result)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 attempts (original + stripped retry), got %d", callCount)
	}
	if strings.Contains(secondAttemptBody, "ErUBCkYIBRgCKkD") {
		t.Errorf("the retry still carried the foreign signature: %s", secondAttemptBody)
	}
	if !strings.Contains(secondAttemptBody, `"planning the run"`) {
		t.Errorf("expected the reasoning text to survive into the retry, got %s", secondAttemptBody)
	}
}

// TestExecuteRequestWithRetries_HealsThinkingSignatureOnChatShape is the consequence
// test: the predicate answering correctly only matters if the retry it gates actually
// reaches the upstream with the signature removed.
func TestExecuteRequestWithRetries_HealsThinkingSignatureOnChatShape(t *testing.T) {
	config := createTestConfig(0, time.Millisecond, time.Millisecond)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	logger := NewDefaultLogger(schemas.LogLevelError)

	req := newThinkingSignatureChatRequest("ErUBCkYIBRgCKkD...")

	callCount := 0
	var secondAttemptInput []schemas.ChatMessage
	handler := func(_ schemas.Key) (string, *schemas.BifrostError) {
		callCount++
		if callCount == 1 {
			return "", thinkingSignatureError()
		}
		secondAttemptInput = req.ChatRequest.Input
		return "success", nil
	}

	result, err := executeRequestWithRetries(ctx, config, handler, nil,
		schemas.ChatCompletionRequest, schemas.Anthropic, "claude-sonnet-5", req, logger)

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
		t.Fatalf("expected both messages to survive, got %d", len(secondAttemptInput))
	}
	details := secondAttemptInput[1].ChatAssistantMessage.ReasoningDetails
	if len(details) != 1 {
		t.Fatalf("expected the reasoning detail to survive, got %d", len(details))
	}
	if details[0].Signature != nil {
		t.Errorf("expected the signature to be gone from the retry, got %q", *details[0].Signature)
	}
}
