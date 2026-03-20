package integrations

import (
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

// NewChatGPTPassthroughRouter creates a passthrough router for /chatgpt.
// All requests matching /chatgpt/{path} are forwarded to the ChatGPT provider
// with the /chatgpt prefix stripped.
func NewChatGPTPassthroughRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *PassthroughRouter {
	return NewPassthroughRouter(client, handlerStore, logger, &PassthroughConfig{
		Provider: schemas.ChatGPT,
		StripPrefix: []string{
			"/chatgpt",
		},
		ForwardAllHeaders: true,
		SkipKeySelection:  true,
	})
}
