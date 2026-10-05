package openai

import (
	"fmt"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// LiveWebSocketURL returns the WSS URL for a GPT Live primary, sideband or fork connection.
// The model travels in session.start, never in the URL.
func (provider *OpenAIProvider) LiveWebSocketURL(_ schemas.Key, kind schemas.LiveConnectionKind, sessionID string) (string, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.LiveRequest); err != nil {
		return "", err
	}
	base := provider.networkConfig.BaseURL
	base = strings.Replace(base, "https://", "wss://", 1)
	base = strings.Replace(base, "http://", "ws://", 1)
	base += "/v1/live/sessions"

	if kind == schemas.LiveConnectionPrimary {
		return base, nil
	}
	if kind != schemas.LiveConnectionSideband && kind != schemas.LiveConnectionFork {
		return "", providerUtils.NewBifrostBadRequestError(fmt.Sprintf("unknown live connection kind %q", kind))
	}
	escapedID, err := providerUtils.EscapeResourceID(sessionID, "session_id")
	if err != nil {
		return "", err
	}
	if kind == schemas.LiveConnectionFork {
		return base + "/" + escapedID + "/fork", nil
	}
	return base + "/" + escapedID + "/attach", nil
}

// LiveHeaders returns the headers for a GPT Live WebSocket connection.
func (provider *OpenAIProvider) LiveHeaders(ctx *schemas.BifrostContext, key schemas.Key) (map[string]string, *schemas.BifrostError) {
	return provider.RealtimeHeaders(ctx, key)
}
