package openai

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// errChatGPTOAuthRequiresStreaming is returned when a non-streaming Responses
// request is issued against a chatgpt_oauth-enabled provider. The ChatGPT
// backend only accepts stream=true so the non-streaming path is unsupported.
var errChatGPTOAuthRequiresStreaming = errors.New("chatgpt_oauth requires streaming /responses")

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

// ChatGPTOAuthClientVersionFallback is injected on outbound /models requests
// when the inbound caller didn't supply a ?client_version= query param. The
// ChatGPT backend requires the parameter to exist on /models but is tolerant of
// the actual value. If the inbound /v1/models?client_version=... query string
// IS forwarded by the transport (i.e. reaches chatGPTOAuthPath with a query),
// the caller's value is preserved and the fallback is not used.
// Matches the openai-oauth proxy fallback.
const ChatGPTOAuthClientVersionFallback = "0.111.0"

// ChatGPTOAuthDirectKeyID is the key ID used when Bifrost auto-injects a Bearer
// token from the inbound Authorization header as a direct key.
const ChatGPTOAuthDirectKeyID = "chatgpt-oauth"

// ExtractChatGPTOAuthBearerToken extracts a Bearer token from a request headers
// map (case-insensitive "authorization" lookup). Returns "" if no Bearer token
// is present. Public helper used by core/bifrost.go for the auto-inject path.
func ExtractChatGPTOAuthBearerToken(headers map[string]string) string {
	if headers == nil {
		return ""
	}
	authHeader, ok := headers["authorization"]
	if !ok {
		// Try case-insensitive fallback since the caller may not lowercase.
		for k, v := range headers {
			if strings.EqualFold(k, "authorization") {
				authHeader = v
				ok = true
				break
			}
		}
	}
	if !ok || authHeader == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return ""
	}
	return strings.TrimSpace(authHeader[7:])
}

// chatGPTOAuthPath maps a standard OpenAI /v1/... path to the ChatGPT backend path.
// Strips the /v1 prefix and appends required query parameters for routes that need them
// (e.g. /models requires ?client_version). Returns the path unchanged if it doesn't start with /v1.
//
// For /models: if the incoming path already carries a client_version (e.g. Codex sends
// /v1/models?client_version=0.121.0), that value is preserved. Only when the param is
// absent do we inject the fallback so the ChatGPT backend doesn't reject the request.
func chatGPTOAuthPath(standardPath string) string {
	// Split path and query so we can inspect and preserve caller-supplied query params.
	pathOnly, query, hasQuery := strings.Cut(standardPath, "?")

	mapped := pathOnly
	if pathOnly == "/v1" {
		mapped = "/"
	} else if strings.HasPrefix(pathOnly, "/v1/") {
		mapped = pathOnly[3:] // strip "/v1" prefix, keep the "/"
	}

	// /models requires a client_version query parameter on the ChatGPT backend.
	if mapped == "/models" {
		if !hasQuery {
			return mapped + "?client_version=" + ChatGPTOAuthClientVersionFallback
		}
		// Preserve caller query; inject fallback only if client_version is absent.
		if !queryContainsKey(query, "client_version") {
			return mapped + "?" + query + "&client_version=" + ChatGPTOAuthClientVersionFallback
		}
		return mapped + "?" + query
	}

	if hasQuery {
		return mapped + "?" + query
	}
	return mapped
}

// queryContainsKey reports whether the given raw query string contains the named key.
// Does not URL-decode — callers pass already-valid query strings.
func queryContainsKey(rawQuery, key string) bool {
	for _, pair := range strings.Split(rawQuery, "&") {
		k, _, _ := strings.Cut(pair, "=")
		if k == key {
			return true
		}
	}
	return false
}

// chatGPTOAuthWebSocketURL builds the upstream WebSocket URL for the ChatGPT backend,
// stripping the /v1 prefix and converting http(s):// to ws(s)://.
func chatGPTOAuthWebSocketURL(baseURL, standardPath string) string {
	url := strings.Replace(baseURL, "https://", "wss://", 1)
	url = strings.Replace(url, "http://", "ws://", 1)
	return url + chatGPTOAuthPath(standardPath)
}

// chatGPTOAuthWebSocketHeaders builds the OAuth-specific headers required for the
// ChatGPT upstream WebSocket connection: Authorization (Bearer from key),
// chatgpt-account-id (extracted from JWT), and OpenAI-Beta.
//
// It does NOT inject identity defaults (originator, version).  Those defaults are
// the responsibility of the caller — mergeClientWSHeaders in wsresponses.go fills
// them in only when the client-sent headers do not already carry those values.
// This keeps the two concerns separate: this function owns OAuth credentials;
// mergeClientWSHeaders owns the identity-fallback policy.
//
// Merge order (lowest → highest priority):
//  1. existingExtraHeaders  — provider-level static extra headers from config
//  2. OAuth headers         — Authorization (Bearer from key), chatgpt-account-id
//     (extracted from JWT), OpenAI-Beta.  These always win.
//
// forwardedHeaders is accepted for API compatibility but is no longer used here;
// pass nil or an empty map.
func chatGPTOAuthWebSocketHeaders(key schemas.Key, existingExtraHeaders map[string]string, forwardedHeaders map[string]string, logger schemas.Logger) map[string]string {
	authHeader := map[string]string{"Authorization": "Bearer " + key.Value.GetValue()}
	accountID, err := extractChatGPTAccountID(key.Value.GetValue())
	if err != nil {
		if logger != nil {
			logger.Warn("chatgpt_oauth: failed to extract account ID for WebSocket: %v", err)
		}
		// OAuth Authorization still wins; skip chatgpt-account-id and OpenAI-Beta.
		return mergeHeadersCaseInsensitive(existingExtraHeaders, authHeader)
	}
	oauth := chatGPTOAuthExtraHeaders(accountID)
	oauth["Authorization"] = authHeader["Authorization"]

	// Merge order: existingExtraHeaders → oauth (oauth always wins)
	return mergeHeadersCaseInsensitive(existingExtraHeaders, oauth)
}

// mapContainsKeyCI reports whether m contains a key that matches target
// case-insensitively.
func mapContainsKeyCI(m map[string]string, target string) bool {
	for k := range m {
		if strings.EqualFold(k, target) {
			return true
		}
	}
	return false
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
	if err := sonic.Unmarshal(payload, &claims); err != nil {
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
//   - forces "store" to false (the backend rejects store=true for OAuth callers)
//   - deletes "max_output_tokens"
//   - forces "stream" to true (the ChatGPT backend API only accepts streaming requests)
func transformChatGPTResponsesBody(body []byte) ([]byte, error) {
	var data map[string]interface{}
	if err := sonic.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("failed to parse request body: %w", err)
	}

	// Ensure instructions field exists
	if _, ok := data["instructions"]; !ok {
		data["instructions"] = ""
	}

	// Force store to false — the ChatGPT backend API rejects store=true for OAuth
	// callers regardless of caller intent.
	data["store"] = false

	// Remove max_output_tokens
	delete(data, "max_output_tokens")

	// Force stream to true — the ChatGPT backend API only accepts streaming
	data["stream"] = true

	return sonic.Marshal(data)
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
	return mergeHeadersCaseInsensitive(existingExtraHeaders, chatGPTOAuthExtraHeaders(accountID)), chatGPTOAuthPath(standardPath), nil
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
	return mergeHeadersCaseInsensitive(existingExtraHeaders, chatGPTOAuthExtraHeaders(accountID))
}

// chatGPTOAuthApplyRequest is a convenience wrapper that applies ChatGPT OAuth
// transformations for the Responses request: merged headers + body transformer.
// Path mapping is handled separately by buildRequestURL, which auto-strips /v1
// when chatgpt_oauth is enabled.
// If enabled is false, returns the inputs unchanged and nil bodyTransformer.
// If enabled is true and JWT extraction fails, returns an error so the caller
// can surface a structured "invalid ChatGPT OAuth token" error rather than
// letting the upstream reject a mutated body with no account-id header.
func chatGPTOAuthApplyRequest(enabled bool, key schemas.Key, existingExtraHeaders map[string]string, logger schemas.Logger) (headers map[string]string, bodyTransformer func([]byte) ([]byte, error), err error) {
	if !enabled {
		return existingExtraHeaders, nil, nil
	}
	accountID, extractErr := extractChatGPTAccountID(key.Value.GetValue())
	if extractErr != nil {
		return nil, nil, fmt.Errorf("invalid ChatGPT OAuth token: %w", extractErr)
	}
	oauthHeaders := chatGPTOAuthExtraHeaders(accountID)
	merged := mergeHeadersCaseInsensitive(existingExtraHeaders, oauthHeaders)
	return merged, transformChatGPTResponsesBody, nil
}

// mergeHeadersCaseInsensitive merges two header maps, treating header names
// case-insensitively. OAuth overrides always win. Keys from the OAuth map are
// preserved as-is; duplicates from existingHeaders (by case-insensitive match)
// are dropped. This prevents both "openai-beta" and "OpenAI-Beta" from ending
// up in the result map where Go's unordered iteration would cause intermittent
// behavior in SetExtraHeaders.
func mergeHeadersCaseInsensitive(existingHeaders, oauthHeaders map[string]string) map[string]string {
	// Build case-insensitive lookup of OAuth keys so we can skip duplicates from existingHeaders.
	oauthKeysLower := make(map[string]bool, len(oauthHeaders))
	for k := range oauthHeaders {
		oauthKeysLower[strings.ToLower(k)] = true
	}
	merged := make(map[string]string, len(existingHeaders)+len(oauthHeaders))
	for k, v := range existingHeaders {
		if oauthKeysLower[strings.ToLower(k)] {
			continue // OAuth override wins
		}
		merged[k] = v
	}
	for k, v := range oauthHeaders {
		merged[k] = v
	}
	return merged
}
