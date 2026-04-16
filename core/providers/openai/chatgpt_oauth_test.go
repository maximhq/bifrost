package openai

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildTestJWT creates a minimal JWT with the given payload for testing.
// No signature verification is needed — we only decode the payload.
func buildTestJWT(payload map[string]interface{}) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payloadBytes, _ := json.Marshal(payload)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadBytes)
	sig := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))
	return header + "." + payloadB64 + "." + sig
}

// buildTestJWTRaw creates a JWT with a raw base64url-encoded payload string.
// Useful for testing invalid JSON payloads.
func buildTestJWTRaw(payloadB64 string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	sig := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))
	return header + "." + payloadB64 + "." + sig
}

// ---------------------------------------------------------------------------
// extractChatGPTAccountID
// ---------------------------------------------------------------------------

func TestExtractChatGPTAccountID(t *testing.T) {
	t.Run("valid token with account ID", func(t *testing.T) {
		token := buildTestJWT(map[string]interface{}{
			"sub": "google-oauth2|12345",
			"https://api.openai.com/auth": map[string]interface{}{
				"chatgpt_account_id": "9774aee9-daa9-4327-afe5-3efbeed7e328",
				"chatgpt_user_id":    "user-FcJBIsPIye2kIwcIet4nIvx4",
			},
		})
		accountID, err := extractChatGPTAccountID(token)
		require.NoError(t, err)
		assert.Equal(t, "9774aee9-daa9-4327-afe5-3efbeed7e328", accountID)
	})

	t.Run("empty token", func(t *testing.T) {
		_, err := extractChatGPTAccountID("")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "empty access token")
	})

	t.Run("malformed JWT - no dots", func(t *testing.T) {
		_, err := extractChatGPTAccountID("not-a-jwt")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid JWT format")
	})

	t.Run("malformed JWT - two parts only", func(t *testing.T) {
		_, err := extractChatGPTAccountID("header.payload")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "expected 3 parts, got 2")
	})

	t.Run("malformed JWT - four parts", func(t *testing.T) {
		_, err := extractChatGPTAccountID("a.b.c.d")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "expected 3 parts, got 4")
	})

	t.Run("malformed JWT - invalid base64 payload", func(t *testing.T) {
		_, err := extractChatGPTAccountID("header.!!!invalid!!!.sig")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode JWT payload")
	})

	t.Run("malformed JWT - payload is not valid JSON", func(t *testing.T) {
		notJSON := base64.RawURLEncoding.EncodeToString([]byte("this is not json"))
		token := buildTestJWTRaw(notJSON)
		_, err := extractChatGPTAccountID(token)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse JWT claims")
	})

	t.Run("missing auth claim", func(t *testing.T) {
		token := buildTestJWT(map[string]interface{}{
			"sub": "google-oauth2|12345",
		})
		_, err := extractChatGPTAccountID(token)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "missing https://api.openai.com/auth claim")
	})

	t.Run("auth claim is not an object", func(t *testing.T) {
		token := buildTestJWT(map[string]interface{}{
			"https://api.openai.com/auth": "not-an-object",
		})
		_, err := extractChatGPTAccountID(token)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "claim is not an object")
	})

	t.Run("auth claim is an array", func(t *testing.T) {
		token := buildTestJWT(map[string]interface{}{
			"https://api.openai.com/auth": []string{"a", "b"},
		})
		_, err := extractChatGPTAccountID(token)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "claim is not an object")
	})

	t.Run("missing account_id in auth claim", func(t *testing.T) {
		token := buildTestJWT(map[string]interface{}{
			"https://api.openai.com/auth": map[string]interface{}{
				"chatgpt_user_id": "user-FcJBIsPIye2kIwcIet4nIvx4",
			},
		})
		_, err := extractChatGPTAccountID(token)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "chatgpt_account_id not found or empty")
	})

	t.Run("account_id is not a string", func(t *testing.T) {
		token := buildTestJWT(map[string]interface{}{
			"https://api.openai.com/auth": map[string]interface{}{
				"chatgpt_account_id": 12345,
			},
		})
		_, err := extractChatGPTAccountID(token)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "chatgpt_account_id not found or empty")
	})

	t.Run("empty account_id", func(t *testing.T) {
		token := buildTestJWT(map[string]interface{}{
			"https://api.openai.com/auth": map[string]interface{}{
				"chatgpt_account_id": "",
			},
		})
		_, err := extractChatGPTAccountID(token)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "chatgpt_account_id not found or empty")
	})
}

// ---------------------------------------------------------------------------
// transformChatGPTResponsesBody
// ---------------------------------------------------------------------------

func TestTransformChatGPTResponsesBody(t *testing.T) {
	t.Run("adds instructions, store, stream and removes max_output_tokens", func(t *testing.T) {
		input := []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"hello"}],"max_output_tokens":4096}`)
		output, err := transformChatGPTResponsesBody(input)
		require.NoError(t, err)

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(output, &result))

		assert.Equal(t, "", result["instructions"], "instructions should default to empty string")
		assert.Equal(t, false, result["store"], "store should default to false")
		assert.Equal(t, true, result["stream"], "stream must be forced to true")
		assert.Equal(t, "gpt-5.4", result["model"], "model should be preserved")
		assert.NotNil(t, result["input"], "input should be preserved")
		_, hasMaxOutputTokens := result["max_output_tokens"]
		assert.False(t, hasMaxOutputTokens, "max_output_tokens must be removed")
	})

	t.Run("preserves existing instructions", func(t *testing.T) {
		input := []byte(`{"model":"gpt-5.4","instructions":"You are a coding assistant"}`)
		output, err := transformChatGPTResponsesBody(input)
		require.NoError(t, err)

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(output, &result))

		assert.Equal(t, "You are a coding assistant", result["instructions"])
	})

	t.Run("preserves existing store value when set to true", func(t *testing.T) {
		input := []byte(`{"model":"gpt-5.4","store":true}`)
		output, err := transformChatGPTResponsesBody(input)
		require.NoError(t, err)

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(output, &result))

		assert.Equal(t, true, result["store"])
	})

	t.Run("preserves existing store value when explicitly false", func(t *testing.T) {
		input := []byte(`{"model":"gpt-5.4","store":false}`)
		output, err := transformChatGPTResponsesBody(input)
		require.NoError(t, err)

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(output, &result))

		assert.Equal(t, false, result["store"])
	})

	t.Run("forces stream true when not present", func(t *testing.T) {
		input := []byte(`{"model":"gpt-5.4","input":"hello"}`)
		output, err := transformChatGPTResponsesBody(input)
		require.NoError(t, err)

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(output, &result))

		assert.Equal(t, true, result["stream"])
	})

	t.Run("overrides stream false with true", func(t *testing.T) {
		input := []byte(`{"model":"gpt-5.4","input":"hello","stream":false}`)
		output, err := transformChatGPTResponsesBody(input)
		require.NoError(t, err)

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(output, &result))

		assert.Equal(t, true, result["stream"], "stream must be forced to true even if caller set false")
	})

	t.Run("preserves stream true", func(t *testing.T) {
		input := []byte(`{"model":"gpt-5.4","stream":true}`)
		output, err := transformChatGPTResponsesBody(input)
		require.NoError(t, err)

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(output, &result))

		assert.Equal(t, true, result["stream"])
	})

	t.Run("preserves all other fields", func(t *testing.T) {
		input := []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"hi"}],"temperature":0.7,"top_p":0.9,"tools":[{"type":"function"}]}`)
		output, err := transformChatGPTResponsesBody(input)
		require.NoError(t, err)

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(output, &result))

		assert.Equal(t, "gpt-5.4", result["model"])
		assert.Equal(t, float64(0.7), result["temperature"])
		assert.Equal(t, float64(0.9), result["top_p"])
		assert.NotNil(t, result["input"])
		assert.NotNil(t, result["tools"])
	})

	t.Run("handles empty JSON object", func(t *testing.T) {
		input := []byte(`{}`)
		output, err := transformChatGPTResponsesBody(input)
		require.NoError(t, err)

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(output, &result))

		assert.Equal(t, "", result["instructions"])
		assert.Equal(t, false, result["store"])
		assert.Equal(t, true, result["stream"])
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		_, err := transformChatGPTResponsesBody([]byte(`not json`))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse request body")
	})

	t.Run("empty input returns error", func(t *testing.T) {
		_, err := transformChatGPTResponsesBody([]byte(``))
		assert.Error(t, err)
	})
}

// ---------------------------------------------------------------------------
// chatGPTOAuthExtraHeaders
// ---------------------------------------------------------------------------

func TestChatGPTOAuthExtraHeaders(t *testing.T) {
	t.Run("returns correct headers", func(t *testing.T) {
		headers := chatGPTOAuthExtraHeaders("9774aee9-daa9-4327-afe5-3efbeed7e328")

		assert.Equal(t, "9774aee9-daa9-4327-afe5-3efbeed7e328", headers["chatgpt-account-id"])
		assert.Equal(t, "responses=experimental", headers["OpenAI-Beta"])
		assert.Len(t, headers, 2)
	})

	t.Run("works with any account ID format", func(t *testing.T) {
		headers := chatGPTOAuthExtraHeaders("simple-id")
		assert.Equal(t, "simple-id", headers["chatgpt-account-id"])
	})
}

// ---------------------------------------------------------------------------
// ChatGPTOAuthDefaultBaseURL constant
// ---------------------------------------------------------------------------

func TestChatGPTOAuthDefaultBaseURL(t *testing.T) {
	assert.Equal(t, "https://chatgpt.com/backend-api/codex", ChatGPTOAuthDefaultBaseURL)
}

// ---------------------------------------------------------------------------
// chatGPTOAuthPath — route mapping for all documented ChatGPT backend routes
// ---------------------------------------------------------------------------

func TestChatGPTOAuthPath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// Core inference routes
		{"responses POST/SSE", "/v1/responses", "/responses"},
		{"responses WebSocket", "/v1/responses", "/responses"}, // same path, different upgrade
		{"responses compact", "/v1/responses/compact", "/responses/compact"},
		{"responses input_tokens", "/v1/responses/input_tokens", "/responses/input_tokens"},

		// Models
		{"models listing", "/v1/models", "/models"},

		// Realtime/voice
		{"realtime calls", "/v1/realtime/calls", "/realtime/calls"},
		{"realtime session", "/v1/realtime", "/realtime"},

		// Edge cases
		{"bare /v1", "/v1", "/"},
		{"already stripped path", "/responses", "/responses"},
		{"non-v1 path passthrough", "/custom/path", "/custom/path"},
		{"empty path", "", ""},
		{"root path", "/", "/"},
		{"v1 without slash", "/v1files", "/v1files"}, // must not strip partial match

		// Files (note: in ChatGPT backend these are NOT under /codex/)
		{"files upload", "/v1/files", "/files"},
		{"files uploaded", "/v1/files/file-abc123/uploaded", "/files/file-abc123/uploaded"},

		// Memory
		{"memories trace", "/v1/memories/trace_summarize", "/memories/trace_summarize"},

		// Batches (standard OpenAI, may not exist on ChatGPT backend but path should still map)
		{"batches", "/v1/batches", "/batches"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, chatGPTOAuthPath(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// chatGPTOAuthPrepare — integration of all helpers
// ---------------------------------------------------------------------------

func TestChatGPTOAuthPrepare(t *testing.T) {
	validToken := buildTestJWT(map[string]interface{}{
		"https://api.openai.com/auth": map[string]interface{}{
			"chatgpt_account_id": "acct-123",
		},
	})

	t.Run("returns mapped path and merged headers for /v1/responses", func(t *testing.T) {
		key := schemas.Key{Value: schemas.EnvVar{Val: validToken}}
		existing := map[string]string{"X-Custom": "value"}

		headers, path, err := chatGPTOAuthPrepare(key, existing, "/v1/responses", nil)

		require.NoError(t, err)
		assert.Equal(t, "/responses", path)
		assert.Equal(t, "acct-123", headers["chatgpt-account-id"])
		assert.Equal(t, "responses=experimental", headers["OpenAI-Beta"])
		assert.Equal(t, "value", headers["X-Custom"], "existing headers must be preserved")
	})

	t.Run("maps /v1/models", func(t *testing.T) {
		key := schemas.Key{Value: schemas.EnvVar{Val: validToken}}

		_, path, err := chatGPTOAuthPrepare(key, nil, "/v1/models", nil)
		require.NoError(t, err)
		assert.Equal(t, "/models", path)
	})

	t.Run("maps /v1/responses/compact", func(t *testing.T) {
		key := schemas.Key{Value: schemas.EnvVar{Val: validToken}}

		_, path, err := chatGPTOAuthPrepare(key, nil, "/v1/responses/compact", nil)
		require.NoError(t, err)
		assert.Equal(t, "/responses/compact", path)
	})

	t.Run("maps /v1/responses/input_tokens", func(t *testing.T) {
		key := schemas.Key{Value: schemas.EnvVar{Val: validToken}}

		_, path, err := chatGPTOAuthPrepare(key, nil, "/v1/responses/input_tokens", nil)
		require.NoError(t, err)
		assert.Equal(t, "/responses/input_tokens", path)
	})

	t.Run("maps /v1/realtime/calls", func(t *testing.T) {
		key := schemas.Key{Value: schemas.EnvVar{Val: validToken}}

		_, path, err := chatGPTOAuthPrepare(key, nil, "/v1/realtime/calls", nil)
		require.NoError(t, err)
		assert.Equal(t, "/realtime/calls", path)
	})

	t.Run("maps /v1/files", func(t *testing.T) {
		key := schemas.Key{Value: schemas.EnvVar{Val: validToken}}

		_, path, err := chatGPTOAuthPrepare(key, nil, "/v1/files", nil)
		require.NoError(t, err)
		assert.Equal(t, "/files", path)
	})

	t.Run("oauth headers override existing conflicting headers", func(t *testing.T) {
		key := schemas.Key{Value: schemas.EnvVar{Val: validToken}}
		existing := map[string]string{
			"chatgpt-account-id": "old-id",
			"OpenAI-Beta":        "old-beta",
			"X-Keep":             "keep",
		}

		headers, _, err := chatGPTOAuthPrepare(key, existing, "/v1/responses", nil)

		require.NoError(t, err)
		assert.Equal(t, "acct-123", headers["chatgpt-account-id"], "OAuth header must override existing")
		assert.Equal(t, "responses=experimental", headers["OpenAI-Beta"], "OAuth header must override existing")
		assert.Equal(t, "keep", headers["X-Keep"], "non-conflicting headers preserved")
	})

	t.Run("nil existing headers does not panic", func(t *testing.T) {
		key := schemas.Key{Value: schemas.EnvVar{Val: validToken}}

		headers, path, err := chatGPTOAuthPrepare(key, nil, "/v1/responses", nil)

		require.NoError(t, err)
		assert.Equal(t, "/responses", path)
		assert.Equal(t, "acct-123", headers["chatgpt-account-id"])
		assert.Equal(t, "responses=experimental", headers["OpenAI-Beta"])
		assert.Len(t, headers, 2)
	})

	t.Run("empty existing headers map", func(t *testing.T) {
		key := schemas.Key{Value: schemas.EnvVar{Val: validToken}}

		headers, _, err := chatGPTOAuthPrepare(key, map[string]string{}, "/v1/responses", nil)

		require.NoError(t, err)
		assert.Len(t, headers, 2)
	})

	t.Run("returns error on invalid token", func(t *testing.T) {
		key := schemas.Key{Value: schemas.EnvVar{Val: "not-a-jwt"}}

		_, _, err := chatGPTOAuthPrepare(key, nil, "/v1/responses", nil)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid JWT format")
	})

	t.Run("returns error on empty token", func(t *testing.T) {
		key := schemas.Key{Value: schemas.EnvVar{Val: ""}}

		_, _, err := chatGPTOAuthPrepare(key, nil, "/v1/responses", nil)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "empty access token")
	})
}
