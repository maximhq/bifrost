package handlers

import (
	"fmt"
	"github.com/valyala/fasthttp"
	"strings"
)

// azureVoiceLiveRootPath is where the JavaScript Voice Live SDK connects whatever path its endpoint has.
const azureVoiceLiveRootPath = "/voice-live/realtime"

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

// extractRealtimeAzureAPIKey reads the api-key Azure Voice Live and Azure OpenAI clients send on an Azure
// path: a header, or the query string from a browser, whose WebSocket API cannot set headers.
func extractRealtimeAzureAPIKey(ctx *fasthttp.RequestCtx, path string) string {
	if realtimeIntegrationTypeForPath(path) != "azure" {
		return ""
	}
	if key := strings.TrimSpace(string(ctx.Request.Header.Peek("api-key"))); key != "" {
		return key
	}
	return strings.TrimSpace(string(ctx.QueryArgs().Peek("api-key")))
}
