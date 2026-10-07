package integrations

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// PassthroughRouter is a catch-all router that forwards all requests directly
// to the provider without matching against known route patterns.
type PassthroughRouter struct {
	*GenericRouter
}

var genAIInferencePath = regexp.MustCompile(`^/(projects/[^/]+/locations/[^/]+/publishers/[^/]+/)?(models|tunedModels)/[^/:]+:(generateContent|streamGenerateContent|countTokens|embedContent|batchEmbedContents|predict|rawPredict|streamRawPredict)$`)

// genAIInteractionsPath admits only interaction creation; get/cancel/delete address stored interactions.
var genAIInteractionsPath = regexp.MustCompile(`^/(projects/[^/]+/locations/[^/]+/)?interactions$`)

var apiVersionSegment = regexp.MustCompile(`^v\d+((alpha|beta)\d*)?$`)

// passthroughInferenceRoute limits HTTP passthrough to inference operations.
// A provider key grants more capabilities than an inference credential; raw
// forwarding must not expose the provider's object or account management APIs.
func passthroughInferenceRoute(cfg *PassthroughConfig, method, path string) bool {
	if method != fasthttp.MethodPost || strings.ContainsAny(path, "%?#\\") {
		return false
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	if cfg.UpstreamURL != "" {
		return cfg.UpstreamURL == "https://chatgpt.com" && path == "/backend-api/codex/responses"
	}
	switch cfg.Provider {
	case schemas.OpenAI:
		return openAIInferencePath(strings.TrimPrefix(path, "/v1"))
	case schemas.Anthropic:
		return path == "/v1/messages" || path == "/v1/messages/count_tokens" || path == "/v1/complete"
	case schemas.Azure:
		if strings.HasPrefix(path, "/openai/v1/") {
			return openAIInferencePath(strings.TrimPrefix(path, "/openai/v1"))
		}
		parts := strings.SplitN(path, "/", 5)
		return len(parts) == 5 && parts[1] == "openai" && parts[2] == "deployments" && parts[3] != "" && openAIInferencePath("/"+parts[4])
	case schemas.Gemini, schemas.Vertex:
		return genAIInferencePath.MatchString(path) || genAIInteractionsPath.MatchString(path)
	case schemas.Runware:
		return path == "/v1"
	case schemas.Bedrock:
		return bedrock.IsPassthroughRoute(method, path)
	default:
		return false
	}
}

func openAIInferencePath(path string) bool {
	switch path {
	case "/chat/completions", "/completions", "/responses", "/embeddings", "/moderations",
		"/audio/speech", "/audio/transcriptions", "/audio/translations",
		"/images/generations", "/images/edits", "/images/variations":
		return true
	}
	return false
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

// validatePassthroughPath rejects passthrough remainders that could change how the upstream
// URL is interpreted once concatenated onto a provider base URL. The path must be rooted by
// exactly one "/", must not carry a userinfo separator in its first segment, and must not
// contain a scheme separator, backslashes, dot-dot segments, or control characters anywhere.
// The same rules are applied to the percent-decoded form so an encoded variant cannot slip
// through. Later segments may contain "@" because Vertex model versions use it
// ("models/claude-sonnet-4-5@20250929"); past the first segment it can no longer reach
// the authority, and the provider-side host check covers the remaining cases.
func validatePassthroughPath(p string) error {
	if err := checkPassthroughPathRules(p); err != nil {
		return err
	}
	decoded, err := url.PathUnescape(p)
	if err != nil {
		return fmt.Errorf("invalid passthrough path: %w", err)
	}
	if decoded != p {
		return checkPassthroughPathRules(decoded)
	}
	return nil
}

func checkPassthroughPathRules(p string) error {
	switch {
	case !strings.HasPrefix(p, "/"):
		return errors.New("invalid passthrough path: must start with /")
	case strings.HasPrefix(p, "//"):
		return errors.New("invalid passthrough path: must not start with //")
	case strings.Contains(p, "\\"):
		return errors.New("invalid passthrough path: must not contain backslashes")
	case strings.Contains(p, "://"):
		return errors.New("invalid passthrough path: must not contain a scheme")
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 0x20 || p[i] == 0x7f {
			return errors.New("invalid passthrough path: must not contain control characters")
		}
	}
	first, _, _ := strings.Cut(p[1:], "/")
	if strings.Contains(first, "@") {
		return errors.New("invalid passthrough path: first segment must not contain @")
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == ".." {
			return errors.New("invalid passthrough path: must not contain .. segments")
		}
	}
	return nil
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

// NewChatGPTPassthroughRouter creates a passthrough router for /chatgpt_passthrough.
// Restricted to the Codex responses endpoint only — this is not a general-purpose
// ChatGPT backend proxy.
func NewChatGPTPassthroughRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, accessResolver AccessResolver, logger schemas.Logger) *PassthroughRouter {
	return NewPassthroughRouter(client, handlerStore, accessResolver, logger, &PassthroughConfig{
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
