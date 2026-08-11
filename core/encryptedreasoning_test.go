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
