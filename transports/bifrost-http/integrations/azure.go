package integrations

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/azure"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// AzureRouter serves an Azure AI Services resource under one base URL: the Azure AI Speech REST
// paths and the Azure OpenAI paths of the OpenAI integration. The key or alias decides which Azure
// surface answers (use_speech_endpoints), not the route.
type AzureRouter struct {
	*GenericRouter
}

// NewAzureRouter creates a new AzureRouter with the given bifrost client.
func NewAzureRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, accessResolver AccessResolver, logger schemas.Logger) *AzureRouter {
	routes := CreateAzureSpeechRouteConfigs("/azure")
	routes = append(routes, createAzureOpenAIRouteConfigs("/azure", handlerStore)...)
	return &AzureRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, accessResolver, routes, nil, logger),
	}
}

// azureOpenAIMountPrefix is where NewAzureRouter serves the Azure OpenAI paths of a resource base URL.
var azureOpenAIMountPrefix = []byte("/azure/openai/")

// createAzureOpenAIRouteConfigs mounts the OpenAI integration's Azure OpenAI paths
// ({prefix}/openai/deployments/..., /openai/files, /openai/batches and the like) on the resource base URL.
func createAzureOpenAIRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	configs := CreateOpenAIRouteConfigs(pathPrefix, handlerStore)
	configs = append(configs, CreateOpenAIListModelsRouteConfigs(pathPrefix, handlerStore)...)
	configs = append(configs, CreateOpenAIModelRetrieveRouteConfigs(pathPrefix, handlerStore)...)
	configs = append(configs, CreateOpenAIBatchRouteConfigs(pathPrefix, handlerStore)...)
	configs = append(configs, CreateOpenAIFileRouteConfigs(pathPrefix, handlerStore)...)
	var routes []RouteConfig
	for _, route := range configs {
		if strings.HasPrefix(route.Path, pathPrefix+"/openai/") {
			routes = append(routes, route)
		}
	}
	return routes
}

// AzureRealtimePaths returns the realtime WebSocket paths Azure clients build from a resource endpoint
// under pathPrefix: Voice Live, and Azure OpenAI realtime in its deployment and v1 forms.
func AzureRealtimePaths(pathPrefix string) []string {
	return []string{
		pathPrefix + "/voice-live/realtime",
		pathPrefix + "/openai/realtime",
		pathPrefix + "/openai/v1/realtime",
	}
}

// CreateAzureSpeechRouteConfigs creates route configurations for the Azure AI Speech REST API. Each
// path is mounted in its regional form ({region}.tts.speech.microsoft.com) and its resource form
// ({resource}.cognitiveservices.azure.com, under /tts or /stt).
func CreateAzureSpeechRouteConfigs(pathPrefix string) []RouteConfig {
	var routes []RouteConfig

	for _, path := range []string{"/cognitiveservices/v1", "/tts/cognitiveservices/v1"} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeAzure,
			Path:   pathPrefix + path,
			Method: "POST",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.SpeechRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &azure.AzureSpeechSynthesisRequest{}
			},
			RequestParser: parseAzureSpeechSynthesisRequest,
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				if synthesisReq, ok := req.(*azure.AzureSpeechSynthesisRequest); ok {
					return &schemas.BifrostRequest{SpeechRequest: synthesisReq.ToBifrostSpeechRequest()}, nil
				}
				return nil, errors.New("invalid request type")
			},
			// Returning the bytes writes the audio as is, under the Content-Type the PostCallback set.
			SpeechResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostSpeechResponse) (interface{}, error) {
				return resp.Audio, nil
			},
			PostCallback:   azureSpeechSynthesisPostCallback,
			ErrorConverter: azureSpeechErrorConverter,
		})
	}

	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeAzure,
		Path:   pathPrefix + "/speechtotext/transcriptions:transcribe",
		Method: "POST",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.TranscriptionRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &azure.AzureSpeechFastTranscriptionRequest{}
		},
		RequestParser: parseAzureSpeechFastTranscriptionRequest,
		PreCallback:   azureSpeechRawResponsePreCallback,
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			if transcriptionReq, ok := req.(*azure.AzureSpeechFastTranscriptionRequest); ok {
				return &schemas.BifrostRequest{TranscriptionRequest: transcriptionReq.ToBifrostTranscriptionRequest()}, nil
			}
			return nil, errors.New("invalid request type")
		},
		TranscriptionResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostTranscriptionResponse) (interface{}, error) {
			if raw, ok := azureSpeechRawResponse(&resp.ExtraFields, "combinedPhrases"); ok {
				return raw, nil
			}
			return azure.ToAzureSpeechTranscriptionResponse(resp), nil
		},
		ErrorConverter: azureSpeechErrorConverter,
	})

	// The recognition mode is a path segment, so each one is its own route rather than a parameter.
	for _, base := range []string{"/speech/recognition/", "/stt/speech/recognition/"} {
		for _, mode := range []string{"conversation", "interactive", "dictation"} {
			routes = append(routes, RouteConfig{
				Type:   RouteConfigTypeAzure,
				Path:   pathPrefix + base + mode + "/cognitiveservices/v1",
				Method: "POST",
				GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
					return schemas.TranscriptionRequest
				},
				GetRequestTypeInstance: func(ctx context.Context) interface{} {
					return &azure.AzureSpeechShortAudioRequest{Mode: mode}
				},
				RequestParser: parseAzureSpeechShortAudioRequest,
				PreCallback:   azureSpeechRawResponsePreCallback,
				RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
					if shortAudioReq, ok := req.(*azure.AzureSpeechShortAudioRequest); ok {
						return &schemas.BifrostRequest{TranscriptionRequest: shortAudioReq.ToBifrostTranscriptionRequest()}, nil
					}
					return nil, errors.New("invalid request type")
				},
				TranscriptionResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostTranscriptionResponse) (interface{}, error) {
					if raw, ok := azureSpeechRawResponse(&resp.ExtraFields, "RecognitionStatus"); ok {
						return raw, nil
					}
					return azure.ToAzureSpeechShortAudioResponse(resp), nil
				},
				ErrorConverter: azureSpeechErrorConverter,
			})
		}
	}

	return routes
}

// parseAzureSpeechSynthesisRequest reads the SSML body and the X-Microsoft-OutputFormat header.
func parseAzureSpeechSynthesisRequest(ctx *fasthttp.RequestCtx, req interface{}) error {
	synthesisReq, ok := req.(*azure.AzureSpeechSynthesisRequest)
	if !ok {
		return errors.New("invalid request type for azure speech synthesis")
	}
	synthesisReq.SSML = string(ctx.Request.Body())
	synthesisReq.OutputFormat = string(ctx.Request.Header.Peek("X-Microsoft-OutputFormat"))
	return nil
}

// parseAzureSpeechFastTranscriptionRequest reads the "audio" and "definition" form parts.
func parseAzureSpeechFastTranscriptionRequest(ctx *fasthttp.RequestCtx, req interface{}) error {
	transcriptionReq, ok := req.(*azure.AzureSpeechFastTranscriptionRequest)
	if !ok {
		return errors.New("invalid request type for azure fast transcription")
	}
	form, err := ctx.MultipartForm()
	if err != nil {
		return err
	}
	fileHeaders := form.File["audio"]
	if len(fileHeaders) == 0 {
		return errors.New("audio field is required")
	}
	file, err := fileHeaders[0].Open()
	if err != nil {
		return err
	}
	defer file.Close()
	if transcriptionReq.Audio, err = io.ReadAll(file); err != nil {
		return err
	}
	transcriptionReq.Filename = fileHeaders[0].Filename
	if definition := form.Value["definition"]; len(definition) > 0 && definition[0] != "" {
		if err := sonic.Unmarshal([]byte(definition[0]), &transcriptionReq.Definition); err != nil {
			return fmt.Errorf("invalid definition: %w", err)
		}
	}
	return nil
}

// parseAzureSpeechShortAudioRequest reads the audio body, the query options and the
// Pronunciation-Assessment header; the mode comes from the route.
func parseAzureSpeechShortAudioRequest(ctx *fasthttp.RequestCtx, req interface{}) error {
	shortAudioReq, ok := req.(*azure.AzureSpeechShortAudioRequest)
	if !ok {
		return errors.New("invalid request type for azure short audio recognition")
	}
	args := ctx.QueryArgs()
	shortAudioReq.Audio = bytes.Clone(ctx.Request.Body())
	shortAudioReq.Language = string(args.Peek("language"))
	shortAudioReq.Format = string(args.Peek("format"))
	shortAudioReq.Profanity = string(args.Peek("profanity"))
	shortAudioReq.EndpointID = string(args.Peek("cid"))
	shortAudioReq.PronunciationAssessment = string(ctx.Request.Header.Peek("Pronunciation-Assessment"))
	return nil
}

// azureSpeechRawResponsePreCallback asks the provider for Azure's own JSON, which the route returns
// unchanged when Azure AI Speech answered. The route's body is only ever that JSON, so the raw
// response override does not depend on the per-request override setting. Opening that setting would
// also honor a client's raw request header, which this body never carries, so that one is dropped.
func azureSpeechRawResponsePreCallback(_ *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, _ interface{}) error {
	bifrostCtx.SetValue(schemas.BifrostContextKeyAllowPerRequestRawOverride, true)
	bifrostCtx.ClearValue(schemas.BifrostContextKeySendBackRawRequest)
	bifrostCtx.SetValue(schemas.BifrostContextKeySendBackRawResponse, true)
	return nil
}

// azureSpeechRawResponse returns Azure's JSON when it has the field that marks this route's shape.
// An alias that sends the request to Azure OpenAI or another provider gets its result converted instead.
func azureSpeechRawResponse(extra *schemas.BifrostResponseExtraFields, field string) (interface{}, bool) {
	if extra.Provider != schemas.Azure {
		return nil, false
	}
	raw, ok := extra.RawResponse.(map[string]interface{})
	if !ok {
		return nil, false
	}
	if _, ok := raw[field]; !ok {
		return nil, false
	}
	return raw, true
}

// azureSpeechSynthesisPostCallback labels the audio with the Content-Type Azure sends for the
// requested output format.
func azureSpeechSynthesisPostCallback(ctx *fasthttp.RequestCtx, req interface{}, _ interface{}) error {
	if synthesisReq, ok := req.(*azure.AzureSpeechSynthesisRequest); ok {
		ctx.Response.Header.SetContentType(azure.AzureSpeechContentType(synthesisReq.OutputFormat))
	}
	return nil
}

func azureSpeechErrorConverter(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
	return azure.ToAzureSpeechError(err)
}
