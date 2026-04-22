package openai

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// SupportsWebSocketMode returns true since OpenAI natively supports the Responses API WebSocket Mode.
// This applies to both the standard OpenAI path (api.openai.com) and the ChatGPT OAuth path
// (chatgpt.com/backend-api/codex). The ChatGPT backend does speak WebSocket but requires
// first-party Codex identity headers (originator, version, user-agent) which are forwarded
// from the incoming client request — see chatGPTOAuthWebSocketHeaders for details.
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

// WebSocketHeaders returns the base headers required for the upstream WebSocket connection to OpenAI.
// For chatgpt_oauth, the caller (wsresponses.go / tryNativeWSUpstream) is expected to merge
// first-party client headers BEFORE this call and then merge the result of this call on top so
// that OAuth headers (Authorization, chatgpt-account-id, OpenAI-Beta) always win — see
// mergeClientWSHeaders in wsresponses.go.
// When chatgpt_oauth is enabled, forwardedHeaders (captured from the incoming Codex WS upgrade)
// are threaded through to chatGPTOAuthWebSocketHeaders so that originator/version/user-agent
// reach the ChatGPT backend.
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
