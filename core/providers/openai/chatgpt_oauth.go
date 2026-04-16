package openai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ChatGPTOAuthDefaultBaseURL is the default base URL for ChatGPT's backend API.
// When chatgpt_oauth is enabled and no custom base URL is set, this is used.
const ChatGPTOAuthDefaultBaseURL = "https://chatgpt.com/backend-api/codex"

// ChatGPT OAuth Route Map
//
// The ChatGPT backend API (chatgpt.com/backend-api/codex) uses different paths
// from the standard OpenAI API (api.openai.com/v1). When chatgpt_oauth is enabled,
// the /v1 prefix is stripped. Routes supported by the ChatGPT backend:
//
//   Standard OpenAI Path         → ChatGPT Backend Path          Method     Notes
//   ─────────────────────────────────────────────────────────────────────────────────
//   /v1/responses                → /responses                    POST(SSE)  Primary inference
//   /v1/responses (WS upgrade)   → /responses (WS upgrade)       WebSocket  Preferred transport, falls back to SSE
//   /v1/responses/compact        → /responses/compact             POST       Context compaction (OpenAI+Azure only)
//   /v1/responses/input_tokens   → /responses/input_tokens        POST       Token counting
//   /v1/models                   → /models?client_version=<ver>   GET        Returns {models:[{slug}]} format
//   /v1/realtime/calls           → /realtime/calls                POST       Voice/realtime (creates WebRTC call)
//   /v1/realtime                 → /realtime                      WebSocket  Voice/realtime session
//   N/A                          → /memories/trace_summarize      POST       Memory summarization
//   N/A                          → /files                         POST       File upload (note: NOT under /codex/)
//   N/A                          → /files/{id}/uploaded           POST       File upload completion
//
// Required headers on every request:
//   - Authorization: Bearer <access_token>       (handled by direct key passthrough)
//   - chatgpt-account-id: <account_id>           (extracted from JWT, added here)
//   - OpenAI-Beta: responses=experimental        (added here)
//
// Required body mutations for /responses:
//   - instructions: must exist (default "")
//   - store: must be false
//   - max_output_tokens: must be deleted
//   - stream: must be true (backend only accepts streaming)

// chatGPTOAuthPath maps a standard OpenAI /v1/... path to the ChatGPT backend path.
// Strips the /v1 prefix. Returns the path unchanged if it doesn't start with /v1.
func chatGPTOAuthPath(standardPath string) string {
	if standardPath == "/v1" {
		return "/"
	}
	if strings.HasPrefix(standardPath, "/v1/") {
		return standardPath[3:] // strip "/v1" prefix, keep the "/"
	}
	return standardPath
}

// chatGPTOAuthWebSocketURL builds the upstream WebSocket URL for the ChatGPT backend,
// stripping the /v1 prefix and converting http(s):// to ws(s)://.
func chatGPTOAuthWebSocketURL(baseURL, standardPath string) string {
	url := strings.Replace(baseURL, "https://", "wss://", 1)
	url = strings.Replace(url, "http://", "ws://", 1)
	return url + chatGPTOAuthPath(standardPath)
}

// chatGPTOAuthWebSocketHeaders builds the headers required for the ChatGPT OAuth
// upstream WebSocket connection: Authorization (from key) + chatgpt-account-id
// (extracted from JWT) + OpenAI-Beta, merged with any existing extra headers.
// Extra headers with case-insensitive Authorization are skipped.
func chatGPTOAuthWebSocketHeaders(key schemas.Key, existingExtraHeaders map[string]string, logger schemas.Logger) map[string]string {
	headers := map[string]string{
		"Authorization": "Bearer " + key.Value.GetValue(),
	}
	for k, v := range existingExtraHeaders {
		if strings.EqualFold(k, "Authorization") {
			continue
		}
		headers[k] = v
	}
	accountID, err := extractChatGPTAccountID(key.Value.GetValue())
	if err != nil {
		if logger != nil {
			logger.Warn("chatgpt_oauth: failed to extract account ID for WebSocket: %v", err)
		}
		return headers
	}
	for k, v := range chatGPTOAuthExtraHeaders(accountID) {
		headers[k] = v
	}
	return headers
}

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

	// Sanitize: reject account IDs containing newlines or carriage returns
	// to prevent HTTP header injection attacks via crafted JWTs.
	if strings.ContainsAny(accountID, "\r\n") {
		return "", fmt.Errorf("chatgpt_account_id contains invalid characters")
	}

	return accountID, nil
}

// transformChatGPTResponsesBody modifies the JSON request body for the ChatGPT backend API:
//   - ensures "instructions" field exists (defaults to "")
//   - sets "store" to false if not already present
//   - deletes "max_output_tokens"
//   - forces "stream" to true (the ChatGPT backend API only accepts streaming requests)
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

	// Force stream to true — the ChatGPT backend API only accepts streaming
	data["stream"] = true

	return json.Marshal(data)
}

// chatGPTOAuthExtraHeaders returns the extra headers required for ChatGPT OAuth requests.
func chatGPTOAuthExtraHeaders(accountID string) map[string]string {
	return map[string]string{
		"chatgpt-account-id": accountID,
		"OpenAI-Beta":        "responses=experimental",
	}
}

// chatGPTOAuthPrepare extracts the account ID from the bearer token, builds the
// merged extra headers (OAuth-specific headers merged with any existing headers),
// and maps the standard OpenAI path to the ChatGPT backend path.
// This is the single entry point for all ChatGPT OAuth header/path logic —
// openai.go calls this instead of duplicating the logic.
func chatGPTOAuthPrepare(key schemas.Key, existingExtraHeaders map[string]string, standardPath string, logger schemas.Logger) (extraHeaders map[string]string, path string, err error) {
	accountID, err := extractChatGPTAccountID(key.Value.GetValue())
	if err != nil {
		return nil, "", err
	}

	oauthHeaders := chatGPTOAuthExtraHeaders(accountID)
	merged := make(map[string]string, len(existingExtraHeaders)+len(oauthHeaders))
	for k, v := range existingExtraHeaders {
		merged[k] = v
	}
	for k, v := range oauthHeaders {
		merged[k] = v
	}

	return merged, chatGPTOAuthPath(standardPath), nil
}

// chatGPTOAuthMergeHeaders merges ChatGPT OAuth headers (chatgpt-account-id, OpenAI-Beta)
// into the existing extraHeaders. Safe to call unconditionally — returns existingExtraHeaders
// unchanged when enabled=false or when JWT extraction fails (logged).
// Use for request types that don't need body transformation (ListModels, ChatCompletion, etc).
func chatGPTOAuthMergeHeaders(enabled bool, key schemas.Key, existingExtraHeaders map[string]string, logger schemas.Logger) map[string]string {
	if !enabled {
		return existingExtraHeaders
	}
	accountID, err := extractChatGPTAccountID(key.Value.GetValue())
	if err != nil {
		if logger != nil {
			logger.Warn("chatgpt_oauth: failed to extract account ID: %v", err)
		}
		return existingExtraHeaders
	}
	oauthHeaders := chatGPTOAuthExtraHeaders(accountID)
	merged := make(map[string]string, len(existingExtraHeaders)+len(oauthHeaders))
	for k, v := range existingExtraHeaders {
		merged[k] = v
	}
	for k, v := range oauthHeaders {
		merged[k] = v
	}
	return merged
}

// chatGPTOAuthApplyRequest is a convenience wrapper that applies ChatGPT OAuth
// transformations for the Responses request: merged headers + body transformer.
// Path mapping is handled separately by buildRequestURL, which auto-strips /v1
// when chatgpt_oauth is enabled.
// If enabled is false, returns the inputs unchanged and nil bodyTransformer.
// If enabled is true and JWT extraction fails, logs and returns unchanged headers
// but still returns the bodyTransformer so stream=true is forced.
func chatGPTOAuthApplyRequest(enabled bool, key schemas.Key, existingExtraHeaders map[string]string, logger schemas.Logger) (headers map[string]string, bodyTransformer func([]byte) ([]byte, error)) {
	if !enabled {
		return existingExtraHeaders, nil
	}
	return chatGPTOAuthMergeHeaders(enabled, key, existingExtraHeaders, logger), transformChatGPTResponsesBody
}
