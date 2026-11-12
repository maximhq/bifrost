package integrations

import (
	"errors"
	"net/url"
	"regexp"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/providers/sagemaker"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/passthroughparse"
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

const (
	chatGPTPassthroughPrefix  = "/chatgpt_passthrough"
	chatGPTCodexModelsPath    = "/backend-api/codex/models"
	chatGPTCodexResponsesPath = "/backend-api/codex/responses"

	// ChatGPTCodexResponsesRoute is the Codex responses route. A POST streams over HTTP; a
	// WebSocket upgrade on the same path is served by the Codex WebSocket relay.
	ChatGPTCodexResponsesRoute = chatGPTPassthroughPrefix + chatGPTCodexResponsesPath
	// ChatGPTCodexResponsesWebSocketURL is the upstream the Codex WebSocket relay dials.
	ChatGPTCodexResponsesWebSocketURL = "wss://chatgpt.com" + chatGPTCodexResponsesPath
)

// IsChatGPTBearer reports whether an Authorization header value carries a ChatGPT sign-in
// token, the only credential the Codex routes forward to chatgpt.com.
func IsChatGPTBearer(authHeader string) bool {
	return isChatGPTBearer(authHeader)
}

// ResolveChatGPTPassthroughProvider applies the provider rule of the HTTP Codex routes to
// another transport: x-model-provider may only be absent or name OpenAI. It returns the HTTP
// status to refuse with when the header names anything else.
func ResolveChatGPTPassthroughProvider(ctx *fasthttp.RequestCtx, store lib.HandlerStore) (schemas.ModelProvider, int, error) {
	provider, err := getPassthroughProvider(ctx, store, schemas.OpenAI, false)
	if err != nil {
		if errors.Is(err, errPassthroughNotEnabled) {
			return "", fasthttp.StatusForbidden, err
		}
		return "", fasthttp.StatusBadRequest, err
	}
	return provider, 0, nil
}

// NewChatGPTCodexTurn builds the passthrough request for one Codex responses turn that arrived
// as a WebSocket response.create frame, under the rules the HTTP route applies to a POST: the
// model is read strictly from the frame, the caller's ChatGPT token is the upstream credential
// so key selection is skipped, and plugins that rewrite text get the Responses body rewriter.
// The frame is the request body as sent; nothing is re-encoded.
func NewChatGPTCodexTurn(bifrostCtx *schemas.BifrostContext, frame []byte, safeHeaders map[string]string) (*schemas.BifrostPassthroughRequest, error) {
	model, _, err := parsePassthroughBody("application/json", frame)
	if err != nil {
		return nil, err
	}
	bifrostCtx.SetValue(schemas.BifrostContextKeySkipKeySelection, true)
	req := &schemas.BifrostPassthroughRequest{
		Method:       fasthttp.MethodPost,
		Path:         chatGPTCodexResponsesPath,
		UpstreamURL:  chatGPTUpstreamURL,
		Body:         frame,
		SafeHeaders:  safeHeaders,
		Provider:     schemas.OpenAI,
		BaseProvider: schemas.OpenAI,
		Model:        model,
	}
	registerPassthroughTextRewriters(bifrostCtx, passthroughparse.Classify(schemas.OpenAI, model, req.Method, req.Path))
	return req, nil
}

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
			return method == fasthttp.MethodPost && path == chatGPTCodexResponsesPath
		},
		// The model list names and runs no model; each responses turn is still checked against the model allowlist.
		ModelFreePath: func(method, path string) bool {
			return method == fasthttp.MethodGet && path == chatGPTCodexModelsPath
		},
		StripPrefix: []string{
			chatGPTPassthroughPrefix,
		},
		// The GET upgrade on the responses path is registered by the Codex WebSocket relay.
		AllowedRoutes: []PassthroughRoute{
			{Method: fasthttp.MethodGet, Path: chatGPTPassthroughPrefix + chatGPTCodexModelsPath},
			{Method: fasthttp.MethodPost, Path: ChatGPTCodexResponsesRoute},
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
		// An Interactions create names an agent (Deep Research) instead of a model; the agent is
		// what runs, so it is what key selection, model allowlists and pricing see.
		BodyModelKey: func(method, path string) string {
			if gemini.IsInteractionsCreatePath(method, path) {
				return "agent"
			}
			return ""
		},
		// Polling, deleting or cancelling an interaction runs nothing new: the model or agent was
		// checked when it was created, and the request names none.
		ModelFreePath: gemini.IsInteractionsResourcePath,
		// An Interactions path names no model, only a project, location and run id. A project named
		// "models" or "deployments" would otherwise read as a model segment.
		NoPathModel: func(method, path string) bool {
			// The substring gate keeps model calls to one scan; the exact checks run only on Interactions paths.
			return gemini.IsInteractionsPassthroughPath(path) &&
				(gemini.IsInteractionsCreatePath(method, path) || gemini.IsInteractionsResourcePath(method, path))
		},
		StripPrefix: []string{
			"/genai_passthrough/v1beta1",
			"/genai_passthrough/v1beta",
			"/genai_passthrough/v1",
			"/genai_passthrough",
		},
	})
}

// registerPassthroughTextRewriters lets plugins that change a passthrough call's text keep its raw bytes in step,
// using the same integration-owned field rules as the native route for the API the call speaks. A passthrough body
// is forwarded as received, so a route with no rewriter registers nothing and a plugin that must change its text has
// to refuse the call instead of forwarding the original bytes.
func registerPassthroughTextRewriters(ctx *schemas.BifrostContext, route passthroughparse.Route) {
	var rewriter schemas.RawRequestBodyTextRewriter
	switch route.Wire {
	case schemas.Anthropic:
		rewriter = rewriteAnthropicRawRequestBody
	case schemas.Gemini:
		rewriter = rewriteGenAIRawRequestBody
	case schemas.OpenAI:
		rewriter = openAIRawRequestBodyRewriter(route.RequestType)
	}
	if rewriter != nil {
		ctx.SetValue(schemas.BifrostContextKeyRawRequestBodyTextRewriter, rewriter)
	}
}
