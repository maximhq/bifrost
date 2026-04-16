package openai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// ChatGPTOAuthDefaultBaseURL is the default base URL for ChatGPT's backend API.
const ChatGPTOAuthDefaultBaseURL = "https://chatgpt.com/backend-api/codex"

// extractChatGPTAccountID decodes the JWT access token payload and extracts
// the chatgpt_account_id from the "https://api.openai.com/auth" claim.
// No signature verification is performed — we only need the claim value.
func extractChatGPTAccountID(accessToken string) (string, error) {
	if accessToken == "" {
		return "", fmt.Errorf("empty access token")
	}

	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid JWT format: expected 3 parts, got %d", len(parts))
	}

	// base64url decode the payload (second segment)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("failed to parse JWT claims: %w", err)
	}

	authClaim, ok := claims["https://api.openai.com/auth"]
	if !ok {
		return "", fmt.Errorf("missing https://api.openai.com/auth claim in JWT")
	}

	authMap, ok := authClaim.(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("https://api.openai.com/auth claim is not an object")
	}

	accountID, ok := authMap["chatgpt_account_id"].(string)
	if !ok || accountID == "" {
		return "", fmt.Errorf("chatgpt_account_id not found or empty in JWT auth claim")
	}

	return accountID, nil
}

// transformChatGPTResponsesBody modifies the JSON request body for the ChatGPT backend API:
//   - ensures "instructions" field exists (defaults to "")
//   - sets "store" to false if not already present
//   - deletes "max_output_tokens"
func transformChatGPTResponsesBody(body []byte) ([]byte, error) {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("failed to parse request body: %w", err)
	}

	// Ensure instructions field exists
	if _, ok := data["instructions"]; !ok {
		data["instructions"] = ""
	}

	// Set store to false if not already set
	if _, ok := data["store"]; !ok {
		data["store"] = false
	}

	// Remove max_output_tokens
	delete(data, "max_output_tokens")

	return json.Marshal(data)
}

// chatGPTOAuthExtraHeaders returns the extra headers required for ChatGPT OAuth requests.
func chatGPTOAuthExtraHeaders(accountID string) map[string]string {
	return map[string]string{
		"chatgpt-account-id": accountID,
		"OpenAI-Beta":        "responses=experimental",
	}
}
