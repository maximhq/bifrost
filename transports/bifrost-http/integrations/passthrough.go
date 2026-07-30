package integrations

import (
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// PassthroughRouter is a catch-all router that forwards all requests directly
// to the provider without matching against known route patterns.
type PassthroughRouter struct {
	*GenericRouter
}

// NewPassthroughRouter creates a passthrough-only router for any prefix/provider combo.
func NewPassthroughRouter(
	client *bifrost.Bifrost,
	handlerStore lib.HandlerStore,
	logger schemas.Logger,
	cfg *PassthroughConfig,
) *PassthroughRouter {
	if cfg == nil {
		cfg = &PassthroughConfig{}
	}
	return &PassthroughRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, nil, cfg, logger),
	}
}

// NewAnthropicPassthroughRouter creates a passthrough router for /anthropic_passthrough.
func NewAnthropicPassthroughRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *PassthroughRouter {
	return NewPassthroughRouter(client, handlerStore, logger, &PassthroughConfig{
		Provider: schemas.Anthropic,
		StripPrefix: []string{
			"/anthropic_passthrough",
		},
	})
}

// NewOpenAIPassthroughRouter creates a passthrough router for /openai_passthrough.
func NewOpenAIPassthroughRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *PassthroughRouter {
	return NewPassthroughRouter(client, handlerStore, logger, &PassthroughConfig{
		Provider: schemas.OpenAI,
		StripPrefix: []string{
			"/openai_passthrough",
		},
	})
}

// NewChatGPTPassthroughRouter creates a passthrough router for /chatgpt_passthrough.
// Restricted to the Codex responses endpoint only — this is not a general-purpose
// ChatGPT backend proxy.
func NewChatGPTPassthroughRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *PassthroughRouter {
	return NewPassthroughRouter(client, handlerStore, logger, &PassthroughConfig{
		Provider:    schemas.OpenAI,
		UpstreamURL: "https://chatgpt.com",
		StripPrefix: []string{
			"/chatgpt_passthrough",
		},
		AllowedRoutes: []PassthroughRoute{
			{Method: fasthttp.MethodPost, Path: "/chatgpt_passthrough/backend-api/codex/responses"},
		},
	})
}

// NewAzurePassthroughRouter creates a passthrough router for /azure_passthrough.
func NewAzurePassthroughRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *PassthroughRouter {
	return NewPassthroughRouter(client, handlerStore, logger, &PassthroughConfig{
		Provider: schemas.Azure,
		StripPrefix: []string{
			"/azure_passthrough",
		},
	})
}

// NewGenAIPassthroughRouter creates a passthrough router for /genai_passthrough.
func NewGenAIPassthroughRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *PassthroughRouter {
	return NewPassthroughRouter(client, handlerStore, logger, &PassthroughConfig{
		Provider:         schemas.Gemini,
		ProviderDetector: detectProviderFromGenAIRequest,
		StripPrefix: []string{
			"/genai_passthrough/v1beta1",
			"/genai_passthrough/v1beta",
			"/genai_passthrough/v1",
			"/genai_passthrough",
		},
	})
}
