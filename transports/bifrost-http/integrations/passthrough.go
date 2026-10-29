package integrations

import (
	"net/url"
	"regexp"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/providers/sagemaker"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// PassthroughRouter is a catch-all router that forwards all requests directly
// to the provider without matching against known route patterns.
type PassthroughRouter struct {
	*GenericRouter
}

var apiVersionSegment = regexp.MustCompile(`^v\d+((alpha|beta)\d*)?$`)

// passthroughQueryStreams reports a GET that asks for SSE in its query, such as resuming an interaction with ?stream=true.
func passthroughQueryStreams(method, rawQuery string) bool {
	if method != fasthttp.MethodGet {
		return false
	}
	q, err := url.ParseQuery(rawQuery)
	return err == nil && strings.EqualFold(q.Get("stream"), "true")
}

// stripPassthroughPrefix removes the first configured prefix that matches path at a segment
// boundary and returns the remainder as a rooted path. A prefix only matches when it ends the
// path or is followed by "/", so "/genai_passthrough/v1" never matches
// "/genai_passthrough/v1@host/x" or "/genai_passthrough/v1beta1foo/x"; the shorter
// "/genai_passthrough" prefix is tried in turn. ok is false when nothing matches, which the
// router treats as no route rather than forwarding an unanchored remainder. The second result
// is the version segment the matched prefix ends with ("v1beta1"), empty when it ends with none.
func stripPassthroughPrefix(path string, prefixes []string) (string, string, bool) {
	for _, prefix := range prefixes {
		rest, found := strings.CutPrefix(path, prefix)
		if !found {
			continue
		}
		apiVersion := prefix[strings.LastIndexByte(prefix, '/')+1:]
		if !apiVersionSegment.MatchString(apiVersion) {
			apiVersion = ""
		}
		if rest == "" {
			return "/", apiVersion, true
		}
		if strings.HasPrefix(rest, "/") {
			return rest, apiVersion, true
		}
	}
	return "", "", false
}

// validatePassthroughPath rejects a path a provider could misread; the rules are shared with the
// relay data plane in lib.ValidatePassthroughPath.
func validatePassthroughPath(p string) error {
	return lib.ValidatePassthroughPath(p)
}

// NewPassthroughRouter creates a passthrough-only router for any prefix/provider combo.
func NewPassthroughRouter(
	client *bifrost.Bifrost,
	handlerStore lib.HandlerStore,
	accessResolver AccessResolver,
	logger schemas.Logger,
	cfg *PassthroughConfig,
) *PassthroughRouter {
	if cfg == nil {
		cfg = &PassthroughConfig{}
	}
	return &PassthroughRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, accessResolver, nil, cfg, logger),
	}
}

// NewAnthropicPassthroughRouter creates a passthrough router for /anthropic_passthrough.
func NewAnthropicPassthroughRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, accessResolver AccessResolver, logger schemas.Logger) *PassthroughRouter {
	return NewPassthroughRouter(client, handlerStore, accessResolver, logger, &PassthroughConfig{
		Provider: schemas.Anthropic,
		StripPrefix: []string{
			"/anthropic_passthrough",
		},
	})
}

// NewOpenAIPassthroughRouter creates a passthrough router for /openai_passthrough.
func NewOpenAIPassthroughRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, accessResolver AccessResolver, logger schemas.Logger) *PassthroughRouter {
	return NewPassthroughRouter(client, handlerStore, accessResolver, logger, &PassthroughConfig{
		Provider: schemas.OpenAI,
		StripPrefix: []string{
			"/openai_passthrough",
		},
	})
}

// chatGPTUpstreamURL is the ChatGPT backend, the only OpenAI upstream that accepts a caller's ChatGPT sign-in token.
const chatGPTUpstreamURL = "https://chatgpt.com"

// NewChatGPTPassthroughRouter creates a passthrough router for /chatgpt_passthrough.
// Restricted to the Codex model list and responses endpoints, the two routes Codex calls
// through a gateway when users sign in with ChatGPT; this is not a general-purpose
// ChatGPT backend proxy.
func NewChatGPTPassthroughRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, accessResolver AccessResolver, logger schemas.Logger) *PassthroughRouter {
	return NewPassthroughRouter(client, handlerStore, accessResolver, logger, &PassthroughConfig{
		Provider:    schemas.OpenAI,
		UpstreamURL: chatGPTUpstreamURL,
		// The Codex endpoint owns an event stream even when a WebSocket bridge
		// omits the HTTP stream flag. Relay incrementally without changing its body.
		StreamingPath: func(method, path string) bool {
			return method == fasthttp.MethodPost && path == "/backend-api/codex/responses"
		},
		// The model list names and runs no model; each responses turn is still checked against the model allowlist.
		ModelFreePath: func(method, path string) bool {
			return method == fasthttp.MethodGet && path == "/backend-api/codex/models"
		},
		StripPrefix: []string{
			"/chatgpt_passthrough",
		},
		AllowedRoutes: []PassthroughRoute{
			{Method: fasthttp.MethodGet, Path: "/chatgpt_passthrough/backend-api/codex/models"},
			{Method: fasthttp.MethodPost, Path: "/chatgpt_passthrough/backend-api/codex/responses"},
		},
	})
}

// NewAzurePassthroughRouter creates a passthrough router for /azure_passthrough.
func NewAzurePassthroughRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, accessResolver AccessResolver, logger schemas.Logger) *PassthroughRouter {
	return NewPassthroughRouter(client, handlerStore, accessResolver, logger, &PassthroughConfig{
		Provider: schemas.Azure,
		StripPrefix: []string{
			"/azure_passthrough",
		},
	})
}

// NewBedrockPassthroughRouter creates a passthrough router for /bedrock_passthrough. Unlike the
// other passthrough routers it is not a general proxy: the Bedrock provider forwards only
// InvokeAgent, knowledge-base Retrieve and ApplyGuardrail, the operations Bifrost has no native
// route for, and refuses every other path with a 400 before any request is built.
func NewBedrockPassthroughRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, accessResolver AccessResolver, logger schemas.Logger) *PassthroughRouter {
	return NewPassthroughRouter(client, handlerStore, accessResolver, logger, &PassthroughConfig{
		Provider: schemas.Bedrock,
		StripPrefix: []string{
			"/bedrock_passthrough",
		},
		// InvokeAgent answers with an event stream; nothing in its path or body says so.
		StreamingPath: bedrock.IsPassthroughStreamPath,
	})
}

// NewSageMakerPassthroughRouter creates a passthrough router for /sagemaker_passthrough. It forwards only
// InvokeEndpoint and InvokeEndpointWithResponseStream, for containers Bifrost has no native route for
// (TEI, LMI's own format, custom images); the endpoint in the path is the model keys and virtual keys allow.
func NewSageMakerPassthroughRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, accessResolver AccessResolver, logger schemas.Logger) *PassthroughRouter {
	return NewPassthroughRouter(client, handlerStore, accessResolver, logger, &PassthroughConfig{
		Provider: schemas.SageMaker,
		StripPrefix: []string{
			"/sagemaker_passthrough",
		},
		StreamingPath: sagemaker.IsPassthroughStreamPath,
		PathModel:     sagemaker.PassthroughModel,
	})
}

// NewRunwarePassthroughRouter creates a passthrough router for /runware_passthrough. Runware exposes
// a single task-based endpoint, so this forwards raw task arrays and unlocks any Runware task type
// (3D, upscaling, background removal, ...) that Bifrost does not model natively.
func NewRunwarePassthroughRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, accessResolver AccessResolver, logger schemas.Logger) *PassthroughRouter {
	return NewPassthroughRouter(client, handlerStore, accessResolver, logger, &PassthroughConfig{
		Provider: schemas.Runware,
		StripPrefix: []string{
			"/runware_passthrough",
		},
	})
}

// NewGenAIPassthroughRouter creates a passthrough router for /genai_passthrough.
func NewGenAIPassthroughRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, accessResolver AccessResolver, logger schemas.Logger) *PassthroughRouter {
	return NewPassthroughRouter(client, handlerStore, accessResolver, logger, &PassthroughConfig{
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
