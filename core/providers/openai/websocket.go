package openai

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// SupportsWebSocketMode returns true since OpenAI natively supports the Responses API WebSocket Mode.
// This applies to both the standard OpenAI path (api.openai.com) and the ChatGPT OAuth path
// (chatgpt.com/backend-api/codex).
func (provider *OpenAIProvider) SupportsWebSocketMode() bool {
	return true
}

// WebSocketResponsesURL returns the WebSocket URL for the OpenAI Responses API.
// Converts the HTTP base URL to a WSS URL: https://api.openai.com -> wss://api.openai.com/v1/responses.
// When chatgpt_oauth is enabled, routes to chatgpt.com/backend-api/codex/responses (no /v1 prefix).
func (provider *OpenAIProvider) WebSocketResponsesURL(key schemas.Key) string {
	if provider.chatgptOAuth {
		return chatGPTOAuthWebSocketURL(provider.networkConfig.BaseURL, "/v1/responses")
	}
	base := provider.networkConfig.BaseURL
	base = strings.Replace(base, "https://", "wss://", 1)
	base = strings.Replace(base, "http://", "ws://", 1)
	return base + "/v1/responses"
}

// WebSocketHeaders returns the OAuth-specific headers for the upstream WebSocket connection.
// For chatgpt_oauth, it returns Authorization, chatgpt-account-id, and OpenAI-Beta only;
// it does NOT inject Codex identity headers (originator, version).
//
// The caller (tryNativeWSUpstream in wsresponses.go) then passes these provider headers to
// mergeClientWSHeaders, which layers the real client headers on top (so a Codex client's own
// originator and version always win) and injects identity defaults only as a last resort if
// neither the client nor the provider supplied them.
func (provider *OpenAIProvider) WebSocketHeaders(key schemas.Key) map[string]string {
	if provider.chatgptOAuth {
		return chatGPTOAuthWebSocketHeaders(key, provider.networkConfig.ExtraHeaders, nil, provider.logger)
	}
	headers := map[string]string{
		"Authorization": "Bearer " + key.Value.GetValue(),
	}
	for k, v := range provider.networkConfig.ExtraHeaders {
		if strings.EqualFold(k, "Authorization") {
			continue
		}
		headers[k] = v
	}
	return headers
}
