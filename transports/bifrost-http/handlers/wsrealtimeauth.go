package handlers

import (
	"fmt"
	"github.com/valyala/fasthttp"
	"strings"
)

// extractRealtimeSubprotocolAPIKey accepts native OpenAI and xAI browser credentials without treating unfamiliar protocol names as authentication.
func extractRealtimeSubprotocolAPIKey(ctx *fasthttp.RequestCtx) (string, error) {
	var credential string
	for _, protocol := range strings.Split(string(ctx.Request.Header.Peek("Sec-WebSocket-Protocol")), ",") {
		protocol = strings.TrimSpace(protocol)
		for _, prefix := range []string{"openai-insecure-api-key.", "xai-client-secret."} {
			if !strings.HasPrefix(protocol, prefix) {
				continue
			}
			token := strings.TrimPrefix(protocol, prefix)
			if token == "" || strings.ContainsAny(token, " \t\r\n") {
				return "", fmt.Errorf("invalid realtime subprotocol credential")
			}
			if credential != "" && credential != token {
				return "", fmt.Errorf("conflicting realtime subprotocol credentials")
			}
			credential = token
		}
	}
	return credential, nil
}
