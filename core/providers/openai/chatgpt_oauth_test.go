package openai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildTestJWT creates a minimal JWT with the given payload for testing.
// No signature verification is needed — we only decode the payload.
func buildTestJWT(payload map[string]interface{}) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("buildTestJWT: failed to marshal test payload: %v", err))
	}
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadBytes)
	sig := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))
	return header + "." + payloadB64 + "." + sig
}

// captureLogger is a minimal schemas.Logger stub that records Warn calls for
// assertion in tests. All other methods are no-ops.
type captureLogger struct {
	warns []string
}

func (l *captureLogger) Debug(msg string, args ...any) {}
func (l *captureLogger) Info(msg string, args ...any)  {}
func (l *captureLogger) Warn(msg string, args ...any) {
	l.warns = append(l.warns, fmt.Sprintf(msg, args...))
}
func (l *captureLogger) Error(msg string, args ...any)                     {}
func (l *captureLogger) Fatal(msg string, args ...any)                     {}
func (l *captureLogger) SetLevel(level schemas.LogLevel)                   {}
func (l *captureLogger) SetOutputType(outputType schemas.LoggerOutputType) {}
func (l *captureLogger) LogHTTPRequest(level schemas.LogLevel, msg string) schemas.LogEventBuilder {
	return nil
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

	// Security: HTTP header injection prevention
	t.Run("rejects account_id with newline (header injection)", func(t *testing.T) {
		token := buildTestJWT(map[string]interface{}{
			"https://api.openai.com/auth": map[string]interface{}{
				"chatgpt_account_id": "legit-id\r\nX-Injected: malicious",
			},
		})
		_, err := extractChatGPTAccountID(token)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid characters")
	})

	t.Run("rejects account_id with bare newline", func(t *testing.T) {
		token := buildTestJWT(map[string]interface{}{
			"https://api.openai.com/auth": map[string]interface{}{
				"chatgpt_account_id": "legit-id\ninjection",
			},
		})
		_, err := extractChatGPTAccountID(token)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid characters")
	})

	t.Run("rejects account_id with bare carriage return", func(t *testing.T) {
		token := buildTestJWT(map[string]interface{}{
			"https://api.openai.com/auth": map[string]interface{}{
				"chatgpt_account_id": "legit-id\rinjection",
			},
		})
		_, err := extractChatGPTAccountID(token)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid characters")
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

	t.Run("forces store false even when caller sets true", func(t *testing.T) {
		input := []byte(`{"model":"gpt-5.4","store":true}`)
		output, err := transformChatGPTResponsesBody(input)
		require.NoError(t, err)

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(output, &result))

		assert.Equal(t, false, result["store"], "ChatGPT backend rejects store=true for OAuth callers; transformer must force false")
	})

	t.Run("keeps store false when caller already sets false", func(t *testing.T) {
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

		// Models — appends required client_version query param when not present
		{"models no query injects fallback", "/v1/models", "/models?client_version=" + ChatGPTOAuthClientVersionFallback},
		// Models — preserves caller-supplied client_version
		{"models preserves caller client_version", "/v1/models?client_version=1.2.3", "/models?client_version=1.2.3"},
		// Models — injects fallback alongside other caller query params
		{"models preserves other query, adds fallback", "/v1/models?foo=bar", "/models?foo=bar&client_version=" + ChatGPTOAuthClientVersionFallback},
		// Models — preserves multiple params including caller's client_version
		{"models preserves all when client_version present", "/v1/models?foo=bar&client_version=9.9.9", "/models?foo=bar&client_version=9.9.9"},

		// Realtime/voice
		{"realtime calls", "/v1/realtime/calls", "/realtime/calls"},
		{"realtime session", "/v1/realtime", "/realtime"},
		// Query-string preservation for non-/models routes
		{"preserves query for non-models", "/v1/responses?foo=bar", "/responses?foo=bar"},

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

	t.Run("maps /v1/models with client_version", func(t *testing.T) {
		key := schemas.Key{Value: schemas.EnvVar{Val: validToken}}

		_, path, err := chatGPTOAuthPrepare(key, nil, "/v1/models", nil)
		require.NoError(t, err)
		assert.Equal(t, "/models?client_version="+ChatGPTOAuthClientVersionFallback, path)
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

// ---------------------------------------------------------------------------
// ExtractChatGPTOAuthBearerToken (public helper used by core/bifrost.go)
// ---------------------------------------------------------------------------

func TestExtractChatGPTOAuthBearerToken(t *testing.T) {
	t.Run("extracts bearer token from lowercased key", func(t *testing.T) {
		headers := map[string]string{"authorization": "Bearer abc123"}
		assert.Equal(t, "abc123", ExtractChatGPTOAuthBearerToken(headers))
	})

	t.Run("extracts bearer token case-insensitively", func(t *testing.T) {
		headers := map[string]string{"Authorization": "Bearer abc123"}
		assert.Equal(t, "abc123", ExtractChatGPTOAuthBearerToken(headers))
	})

	t.Run("accepts mixed-case Bearer prefix", func(t *testing.T) {
		headers := map[string]string{"authorization": "bearer xyz"}
		assert.Equal(t, "xyz", ExtractChatGPTOAuthBearerToken(headers))
	})

	t.Run("trims whitespace", func(t *testing.T) {
		headers := map[string]string{"authorization": "Bearer   padded  "}
		assert.Equal(t, "padded", ExtractChatGPTOAuthBearerToken(headers))
	})

	t.Run("returns empty when no auth header", func(t *testing.T) {
		assert.Equal(t, "", ExtractChatGPTOAuthBearerToken(map[string]string{"x-other": "v"}))
	})

	t.Run("returns empty when auth is not Bearer", func(t *testing.T) {
		headers := map[string]string{"authorization": "Basic dXNlcjpwYXNz"}
		assert.Equal(t, "", ExtractChatGPTOAuthBearerToken(headers))
	})

	t.Run("returns empty when auth header is empty", func(t *testing.T) {
		headers := map[string]string{"authorization": ""}
		assert.Equal(t, "", ExtractChatGPTOAuthBearerToken(headers))
	})

	t.Run("returns empty when headers is nil", func(t *testing.T) {
		assert.Equal(t, "", ExtractChatGPTOAuthBearerToken(nil))
	})
}

// ---------------------------------------------------------------------------
// chatGPTOAuthMergeHeaders (non-request variant used for headers-only routes)
// ---------------------------------------------------------------------------

func TestChatGPTOAuthMergeHeaders(t *testing.T) {
	validToken := buildTestJWT(map[string]interface{}{
		"https://api.openai.com/auth": map[string]interface{}{"chatgpt_account_id": "acct-xyz"},
	})
	key := schemas.Key{Value: schemas.EnvVar{Val: validToken}}

	t.Run("disabled returns input unchanged", func(t *testing.T) {
		existing := map[string]string{"X-Custom": "v"}
		got := chatGPTOAuthMergeHeaders(false, key, existing, nil)
		assert.Equal(t, existing, got)
	})

	t.Run("enabled merges OAuth headers", func(t *testing.T) {
		existing := map[string]string{"X-Custom": "v"}
		got := chatGPTOAuthMergeHeaders(true, key, existing, nil)
		assert.Equal(t, "acct-xyz", got["chatgpt-account-id"])
		assert.Equal(t, "responses=experimental", got["OpenAI-Beta"])
		assert.Equal(t, "v", got["X-Custom"])
	})

	t.Run("enabled with invalid token returns unchanged headers", func(t *testing.T) {
		existing := map[string]string{"X-Custom": "v"}
		badKey := schemas.Key{Value: schemas.EnvVar{Val: "not-a-jwt"}}
		got := chatGPTOAuthMergeHeaders(true, badKey, existing, nil)
		assert.Equal(t, existing, got)
	})

	t.Run("case-insensitive override drops conflicting existing header", func(t *testing.T) {
		existing := map[string]string{
			"chatgpt-account-id": "stale-id",
			"OPENAI-BETA":        "stale-beta",
			"X-Keep":             "keep",
		}
		got := chatGPTOAuthMergeHeaders(true, key, existing, nil)
		// OAuth values win even when existing had different casing
		assert.Equal(t, "acct-xyz", got["chatgpt-account-id"])
		assert.Equal(t, "responses=experimental", got["OpenAI-Beta"])
		// Stale casing should NOT appear as a duplicate key
		_, hasStaleBeta := got["OPENAI-BETA"]
		assert.False(t, hasStaleBeta, "existing header with conflicting case must be dropped")
		assert.Equal(t, "keep", got["X-Keep"])
	})
}

// ---------------------------------------------------------------------------
// chatGPTOAuthApplyRequest — fail-fast on invalid token
// ---------------------------------------------------------------------------

func TestChatGPTOAuthApplyRequest(t *testing.T) {
	validToken := buildTestJWT(map[string]interface{}{
		"https://api.openai.com/auth": map[string]interface{}{"chatgpt_account_id": "acct-apply"},
	})

	t.Run("disabled returns unchanged headers and nil transformer", func(t *testing.T) {
		existing := map[string]string{"X-Custom": "v"}
		headers, transformer, err := chatGPTOAuthApplyRequest(false, schemas.Key{}, existing, nil)
		require.NoError(t, err)
		assert.Equal(t, existing, headers)
		assert.Nil(t, transformer)
	})

	t.Run("enabled with valid token returns merged headers + transformer", func(t *testing.T) {
		key := schemas.Key{Value: schemas.EnvVar{Val: validToken}}
		headers, transformer, err := chatGPTOAuthApplyRequest(true, key, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, "acct-apply", headers["chatgpt-account-id"])
		assert.NotNil(t, transformer)
	})

	t.Run("enabled with invalid token returns error (fail fast)", func(t *testing.T) {
		key := schemas.Key{Value: schemas.EnvVar{Val: "not-a-jwt"}}
		headers, transformer, err := chatGPTOAuthApplyRequest(true, key, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid ChatGPT OAuth token")
		assert.Nil(t, headers)
		assert.Nil(t, transformer)
	})
}

// ---------------------------------------------------------------------------
// chatGPTOAuthWebSocketURL / chatGPTOAuthWebSocketHeaders
// ---------------------------------------------------------------------------

func TestChatGPTOAuthWebSocketURL(t *testing.T) {
	t.Run("https with /v1 prefix", func(t *testing.T) {
		got := chatGPTOAuthWebSocketURL("https://chatgpt.com/backend-api/codex", "/v1/responses")
		assert.Equal(t, "wss://chatgpt.com/backend-api/codex/responses", got)
	})

	t.Run("http maps to ws", func(t *testing.T) {
		got := chatGPTOAuthWebSocketURL("http://localhost:8080", "/v1/responses")
		assert.Equal(t, "ws://localhost:8080/responses", got)
	})
}

func TestChatGPTOAuthWebSocketHeaders(t *testing.T) {
	validToken := buildTestJWT(map[string]interface{}{
		"https://api.openai.com/auth": map[string]interface{}{"chatgpt_account_id": "acct-ws"},
	})
	key := schemas.Key{Value: schemas.EnvVar{Val: validToken}}

	t.Run("sets Authorization + chatgpt headers", func(t *testing.T) {
		got := chatGPTOAuthWebSocketHeaders(key, nil, nil)
		assert.Equal(t, "Bearer "+validToken, got["Authorization"])
		assert.Equal(t, "acct-ws", got["chatgpt-account-id"])
		assert.Equal(t, "responses=experimental", got["OpenAI-Beta"])
	})

	t.Run("merges extra headers but skips Authorization override", func(t *testing.T) {
		extra := map[string]string{
			"authorization": "Bearer should-be-ignored",
			"X-Custom":      "kept",
		}
		got := chatGPTOAuthWebSocketHeaders(key, extra, nil)
		assert.Equal(t, "Bearer "+validToken, got["Authorization"])
		assert.Equal(t, "kept", got["X-Custom"])
	})

	t.Run("falls back to auth-only when JWT invalid", func(t *testing.T) {
		badKey := schemas.Key{Value: schemas.EnvVar{Val: "not-a-jwt"}}
		got := chatGPTOAuthWebSocketHeaders(badKey, nil, nil)
		assert.Equal(t, "Bearer not-a-jwt", got["Authorization"])
		_, hasAccountID := got["chatgpt-account-id"]
		assert.False(t, hasAccountID, "account-id should not be set when JWT extraction fails")
	})

	t.Run("logs warning when JWT invalid and logger provided", func(t *testing.T) {
		badKey := schemas.Key{Value: schemas.EnvVar{Val: "not-a-jwt"}}
		logger := &captureLogger{}
		got := chatGPTOAuthWebSocketHeaders(badKey, nil, logger)
		assert.Equal(t, "Bearer not-a-jwt", got["Authorization"])
		require.Len(t, logger.warns, 1)
		assert.Contains(t, logger.warns[0], "failed to extract account ID for WebSocket")
	})
}

// ---------------------------------------------------------------------------
// Logger-nil branches for chatGPTOAuthMergeHeaders
// ---------------------------------------------------------------------------

func TestChatGPTOAuthMergeHeaders_LoggerBranch(t *testing.T) {
	t.Run("invalid token with logger emits warning", func(t *testing.T) {
		badKey := schemas.Key{Value: schemas.EnvVar{Val: "not-a-jwt"}}
		logger := &captureLogger{}
		existing := map[string]string{"X-Keep": "v"}
		got := chatGPTOAuthMergeHeaders(true, badKey, existing, logger)
		assert.Equal(t, existing, got)
		require.Len(t, logger.warns, 1)
		assert.Contains(t, logger.warns[0], "failed to extract account ID")
	})
}

// ---------------------------------------------------------------------------
// OpenAIListModelsResponse.UnmarshalJSON — dual-shape handling
// ---------------------------------------------------------------------------

func TestOpenAIListModelsResponse_UnmarshalStandard(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"gpt-4","object":"model","owned_by":"openai"}]}`)
	var resp OpenAIListModelsResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	assert.Equal(t, "list", resp.Object)
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "gpt-4", resp.Data[0].ID)
	assert.Equal(t, "openai", resp.Data[0].OwnedBy)
}

func TestOpenAIListModelsResponse_UnmarshalChatGPT(t *testing.T) {
	body := []byte(`{"models":[{"slug":"gpt-5.3-codex"},{"slug":"gpt-5.4"}]}`)
	var resp OpenAIListModelsResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	assert.Equal(t, "list", resp.Object, "projected object must be list")
	require.Len(t, resp.Data, 2)
	assert.Equal(t, "gpt-5.3-codex", resp.Data[0].ID)
	assert.Equal(t, "model", resp.Data[0].Object)
	assert.Equal(t, "chatgpt-oauth", resp.Data[0].OwnedBy)
	assert.Equal(t, "gpt-5.4", resp.Data[1].ID)
}

func TestOpenAIListModelsResponse_UnmarshalChatGPT_SkipsEmptySlug(t *testing.T) {
	body := []byte(`{"models":[{"slug":"gpt-5.4"},{"slug":""},{"slug":"gpt-5.2"}]}`)
	var resp OpenAIListModelsResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	require.Len(t, resp.Data, 2)
	assert.Equal(t, "gpt-5.4", resp.Data[0].ID)
	assert.Equal(t, "gpt-5.2", resp.Data[1].ID)
}

func TestOpenAIListModelsResponse_UnmarshalEmpty(t *testing.T) {
	body := []byte(`{}`)
	var resp OpenAIListModelsResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	assert.Empty(t, resp.Data)
}

// ---------------------------------------------------------------------------
// Non-streaming Responses path rejects chatgpt_oauth cleanly via error sentinel
// ---------------------------------------------------------------------------

func TestChatGPTOAuthRequiresStreaming_Error(t *testing.T) {
	// The sentinel must be exported within the package so Responses() can reference it.
	assert.NotNil(t, errChatGPTOAuthRequiresStreaming)
	assert.Contains(t, errChatGPTOAuthRequiresStreaming.Error(), "streaming")
}
