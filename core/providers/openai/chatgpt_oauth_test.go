package openai

import (
	"encoding/base64"
	"encoding/json"
	"testing"

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
	})

	t.Run("malformed JWT - no dots", func(t *testing.T) {
		_, err := extractChatGPTAccountID("not-a-jwt")
		assert.Error(t, err)
	})

	t.Run("malformed JWT - invalid base64", func(t *testing.T) {
		_, err := extractChatGPTAccountID("header.!!!invalid!!!.sig")
		assert.Error(t, err)
	})

	t.Run("missing auth claim", func(t *testing.T) {
		token := buildTestJWT(map[string]interface{}{
			"sub": "google-oauth2|12345",
		})
		_, err := extractChatGPTAccountID(token)
		assert.Error(t, err)
	})

	t.Run("missing account_id in auth claim", func(t *testing.T) {
		token := buildTestJWT(map[string]interface{}{
			"https://api.openai.com/auth": map[string]interface{}{
				"chatgpt_user_id": "user-FcJBIsPIye2kIwcIet4nIvx4",
			},
		})
		_, err := extractChatGPTAccountID(token)
		assert.Error(t, err)
	})

	t.Run("empty account_id", func(t *testing.T) {
		token := buildTestJWT(map[string]interface{}{
			"https://api.openai.com/auth": map[string]interface{}{
				"chatgpt_account_id": "",
			},
		})
		_, err := extractChatGPTAccountID(token)
		assert.Error(t, err)
	})
}

func TestTransformChatGPTResponsesBody(t *testing.T) {
	t.Run("adds instructions and store, removes max_output_tokens", func(t *testing.T) {
		input := []byte(`{"model":"gpt-5.4","input":"hello","max_output_tokens":4096}`)
		output, err := transformChatGPTResponsesBody(input)
		require.NoError(t, err)

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(output, &result))

		assert.Equal(t, "", result["instructions"])
		assert.Equal(t, false, result["store"])
		assert.Equal(t, "gpt-5.4", result["model"])
		assert.Equal(t, "hello", result["input"])
		_, hasMaxOutputTokens := result["max_output_tokens"]
		assert.False(t, hasMaxOutputTokens, "max_output_tokens should be removed")
	})

	t.Run("preserves existing instructions", func(t *testing.T) {
		input := []byte(`{"model":"gpt-5.4","instructions":"You are a coding assistant"}`)
		output, err := transformChatGPTResponsesBody(input)
		require.NoError(t, err)

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(output, &result))

		assert.Equal(t, "You are a coding assistant", result["instructions"])
	})

	t.Run("preserves existing store value", func(t *testing.T) {
		input := []byte(`{"model":"gpt-5.4","store":true}`)
		output, err := transformChatGPTResponsesBody(input)
		require.NoError(t, err)

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(output, &result))

		// store is preserved if already set
		assert.Equal(t, true, result["store"])
	})

	t.Run("preserves all other fields", func(t *testing.T) {
		input := []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"hi"}],"temperature":0.7,"stream":true}`)
		output, err := transformChatGPTResponsesBody(input)
		require.NoError(t, err)

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(output, &result))

		assert.Equal(t, "gpt-5.4", result["model"])
		assert.Equal(t, float64(0.7), result["temperature"])
		assert.Equal(t, true, result["stream"])
		assert.NotNil(t, result["input"])
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		_, err := transformChatGPTResponsesBody([]byte(`not json`))
		assert.Error(t, err)
	})
}

func TestChatGPTOAuthExtraHeaders(t *testing.T) {
	headers := chatGPTOAuthExtraHeaders("9774aee9-daa9-4327-afe5-3efbeed7e328")

	assert.Equal(t, "9774aee9-daa9-4327-afe5-3efbeed7e328", headers["chatgpt-account-id"])
	assert.Equal(t, "responses=experimental", headers["OpenAI-Beta"])
	assert.Len(t, headers, 2)
}

func TestChatGPTOAuthDefaultBaseURL(t *testing.T) {
	assert.Equal(t, "https://chatgpt.com/backend-api/codex", ChatGPTOAuthDefaultBaseURL)
}
