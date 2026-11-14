package integrations

import (
	"context"
	"errors"
	"strconv"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/elevenlabs"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// ElevenlabsRouter serves an ElevenLabs client under /elevenlabs, the base URL its SDK is given, for the endpoints
// Bifrost has a native method for. The rest of the ElevenLabs API is on /elevenlabs_passthrough.
type ElevenlabsRouter struct {
	*GenericRouter
}

// NewElevenlabsRouter creates the router for the /elevenlabs integration.
func NewElevenlabsRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, accessResolver AccessResolver, logger schemas.Logger) *ElevenlabsRouter {
	return &ElevenlabsRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, accessResolver, CreateElevenlabsRouteConfigs("/elevenlabs"), nil, logger),
	}
}

// elevenlabsSpeechCall is a text-to-speech call: the body the client sent, and what its path and query add to it.
type elevenlabsSpeechCall struct {
	elevenlabs.ElevenlabsSpeechRequest
	voice         string
	outputFormat  string
	enableLogging *bool
}

// isElevenlabsCall reports whether a call goes to ElevenLabs: its model names the provider, or has no prefix and is
// an ElevenLabs model by name, which is how the Anthropic route tells a Claude model before the catalog resolves it.
func isElevenlabsCall(provider schemas.ModelProvider, model string) bool {
	return provider == schemas.Elevenlabs || provider == "" && strings.HasPrefix(model, "eleven_")
}

// elevenlabsRawPassthrough has the call and its answer cross between the client and ElevenLabs as they are, so every
// field and option of ElevenLabs reaches it and comes back, while Bifrost still reads the typed request and response
// for governance, logs and cost.
func elevenlabsRawPassthrough(bifrostCtx *schemas.BifrostContext) {
	bifrostCtx.SetValue(schemas.BifrostContextKeyPassthroughOverridesPresent, true)
	bifrostCtx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	bifrostCtx.SetValue(schemas.BifrostContextKeySendBackRawResponse, true)
}

// elevenlabsSpeechPreCallback reads the voice from the path and the options from the query, which are not in the body.
func elevenlabsSpeechPreCallback(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	call, ok := req.(*elevenlabsSpeechCall)
	if !ok {
		return errors.New("invalid request type")
	}
	call.voice, _ = ctx.UserValue("voice_id").(string)
	// The voice is a path segment of the upstream call, so one that names another endpoint is no voice.
	if strings.ContainsAny(call.voice, `/\?#%`) || strings.Contains(call.voice, "..") {
		return errors.New("invalid voice_id")
	}
	call.outputFormat = string(ctx.QueryArgs().Peek("output_format"))
	if enable, err := strconv.ParseBool(string(ctx.QueryArgs().Peek("enable_logging"))); err == nil {
		call.enableLogging = &enable
	}
	if provider, model := schemas.ParseModelString(call.ModelID, ""); isElevenlabsCall(provider, model) {
		elevenlabsRawPassthrough(bifrostCtx)
	}
	return nil
}

// elevenlabsSpeechRoute is the text-to-speech endpoint of a path suffix: "" for the audio, "/with-timestamps" for
// the audio with the alignment of its characters.
func elevenlabsSpeechRoute(pathPrefix, suffix string, withTimestamps bool) RouteConfig {
	route := RouteConfig{
		Type:        RouteConfigTypeElevenlabs,
		Path:        pathPrefix + "/v1/text-to-speech/{voice_id}" + suffix,
		Method:      fasthttp.MethodPost,
		PreCallback: elevenlabsSpeechPreCallback,
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.SpeechRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &elevenlabsSpeechCall{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			call, ok := req.(*elevenlabsSpeechCall)
			if !ok {
				return nil, errors.New("invalid request type")
			}
			speech := call.ToBifrostSpeechRequest(call.voice)
			speech.Params.ResponseFormat = call.outputFormat
			speech.Params.EnableLogging = call.enableLogging
			if withTimestamps {
				speech.Params.WithTimestamps = &withTimestamps
			}
			return &schemas.BifrostRequest{SpeechRequest: speech}, nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return elevenlabs.ToElevenlabsError(err)
		},
	}
	// Without a converter the router writes the audio itself, which is what the plain endpoint answers.
	if withTimestamps {
		route.SpeechResponseConverter = func(ctx *schemas.BifrostContext, resp *schemas.BifrostSpeechResponse) (interface{}, error) {
			if resp.ExtraFields.Provider == schemas.Elevenlabs && resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return resp, nil
		}
	}
	return route
}

// CreateElevenlabsRouteConfigs creates the route configurations for the ElevenLabs endpoints Bifrost serves natively.
func CreateElevenlabsRouteConfigs(pathPrefix string) []RouteConfig {
	return []RouteConfig{
		elevenlabsSpeechRoute(pathPrefix, "", false),
		elevenlabsSpeechRoute(pathPrefix, "/with-timestamps", true),
		{
			Type:   RouteConfigTypeElevenlabs,
			Path:   pathPrefix + "/v1/models",
			Method: fasthttp.MethodGet,
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.ListModelsRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &schemas.BifrostListModelsRequest{}
			},
			PreCallback: func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
				listModelsReq, ok := req.(*schemas.BifrostListModelsRequest)
				if !ok {
					return errors.New("invalid request type")
				}
				listModelsReq.Provider = schemas.Elevenlabs
				return nil
			},
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				if listModelsReq, ok := req.(*schemas.BifrostListModelsRequest); ok {
					return &schemas.BifrostRequest{ListModelsRequest: listModelsReq}, nil
				}
				return nil, errors.New("invalid request type")
			},
			ListModelsResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostListModelsResponse) (interface{}, error) {
				return elevenlabs.ToElevenlabsListModelsResponse(resp), nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return elevenlabs.ToElevenlabsError(err)
			},
		},
	}
}
