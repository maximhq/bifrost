package openai

import (
	"context"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// SupportsWebSocketMode returns true since OpenAI natively supports the Responses API WebSocket Mode.
func (provider *OpenAIProvider) SupportsWebSocketMode() bool {
	return true
}

// WebSocketResponsesURL returns the WebSocket URL for the OpenAI Responses API.
// Converts the HTTP base URL to a WSS URL: https://api.openai.com -> wss://api.openai.com/v1/responses
func (provider *OpenAIProvider) WebSocketResponsesURL(key schemas.Key) string {
	base := provider.networkConfig.BaseURL
	base = strings.Replace(base, "https://", "wss://", 1)
	base = strings.Replace(base, "http://", "ws://", 1)
	return base + "/v1/responses"
}

// WebSocketHeaders returns the headers required for the upstream WebSocket connection to OpenAI.
// An oauth_key_config key is resolved through the token cache first; the interface carries no
// context or error, so the mint runs under a background context bounded by the cache's exchange
// timeout, and a key that cannot mint sends no Authorization header, which leaves the transport
// its existing fall back to the HTTP bridge.
func (provider *OpenAIProvider) WebSocketHeaders(key schemas.Key) map[string]string {
	headers := map[string]string{}
	if resolved, bErr := provider.resolveKey(context.Background(), key); bErr == nil && resolved.Value.GetValue() != "" {
		headers["Authorization"] = "Bearer " + resolved.Value.GetValue()
	}
	for k, v := range provider.networkConfig.ExtraHeaders {
		if strings.EqualFold(k, "Authorization") {
			continue
		}
		headers[k] = v
	}
	return headers
}
