// Package passthroughparse decodes the raw request and response bodies of a passthrough call into
// Bifrost's typed request/response schemas, so passthrough traffic can be logged and rendered like
// any other request. Routing is shared with the providers' passthrough usage extractors (each
// provider's ClassifyPassthroughPath and vertex.PassthroughWireFormat), so a path is classified
// the same way for billing and for logging.
//
// Everything here is a pure transformation of the bytes it is handed: no network, no logging,
// no shared state. Classify and InferRequestType only read the route and suit the request path.
// Parse and RedactStreamBody read whole bodies: the logging plugin parses from its batch writer,
// and request-path callers use them on a complete body (a response, or a held stream's final chunk).
package passthroughparse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/providers/sagemaker"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/providers/vertex"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// Input is one passthrough call as the logging plugin recorded it.
type Input struct {
	Provider     schemas.ModelProvider // base provider type; custom providers pass their BaseProviderType
	Model        string                // resolved model (from the path or the body's "model" field)
	Method       string
	Path         string // stripped request path, as in PassthroughLogParams.Path
	StatusCode   int    // upstream status; error bodies (>= 400) are never parsed as responses
	RequestBody  []byte
	ResponseBody []byte
	Stream       bool
}

// Result is the typed view of a passthrough call. Request and Response are independent: either can
// be nil when its body was absent or did not decode, and the other is still usable.
type Result struct {
	RequestType schemas.RequestType // the inner kind the path addresses, e.g. ChatCompletionRequest
	Request     *schemas.BifrostRequest
	Response    *schemas.BifrostResponse
}

// wireFormat is the API shape a passthrough call speaks, which can differ from the provider that
// served it (Claude on Vertex speaks Anthropic, Vertex MaaS speaks OpenAI).
type wireFormat int

const (
	wireUnknown wireFormat = iota
	wireOpenAI
	wireAnthropic
	wireGemini
	wireGeminiInteractions
	wireRunware
	wireSageMaker
	wireBedrockAgent
)

// WireGeminiInteractions names the Gemini Interactions API in Route.Wire. It is an API shape, not a provider:
// Interactions calls go to Gemini or Vertex, but their bodies and stream events differ from generateContent's.
const WireGeminiInteractions schemas.ModelProvider = "gemini_interactions"

// provider names the API a wire format is, as Route.Wire carries it, or "" for an unknown format.
func (format wireFormat) provider() schemas.ModelProvider {
	switch format {
	case wireOpenAI:
		return schemas.OpenAI
	case wireAnthropic:
		return schemas.Anthropic
	case wireGemini:
		return schemas.Gemini
	case wireGeminiInteractions:
		return WireGeminiInteractions
	case wireRunware:
		return schemas.Runware
	case wireSageMaker:
		return schemas.SageMaker
	case wireBedrockAgent:
		return schemas.Bedrock
	}
	return ""
}

// typedByBody reports whether the route alone leaves the request type open, so Parse names it from the body:
// Runware's task endpoint and SageMaker invocations take whatever their tasks or container accept, and a Bedrock
// agent turn is no typed request of its own, so its log row stays a passthrough one.
func (format wireFormat) typedByBody() bool {
	return format == wireRunware || format == wireSageMaker || format == wireBedrockAgent
}

// Route is the API shape and inner request kind a passthrough call addresses.
type Route struct {
	Wire        schemas.ModelProvider // API the bodies are written in, e.g. OpenAI, Anthropic, Gemini or WireGeminiInteractions
	RequestType schemas.RequestType   // inner request kind, e.g. ChatCompletionRequest; never a streaming variant; "" when the body names it
}

// Classify returns the API shape and request kind a passthrough call addresses from its route alone (base
// provider, model, method, path), without reading either body. The zero Route means the route has no typed
// equivalent; a route whose request type is left to the body has an empty RequestType, which Parse's Result names.
// Like InferRequestType it is a few string checks, cheap enough for the request path.
func Classify(provider schemas.ModelProvider, model, method, path string) Route {
	format, requestType := classify(Input{Provider: provider, Model: model, Method: method, Path: path})
	if format == wireUnknown || (requestType == "" && !format.typedByBody()) {
		return Route{}
	}
	return Route{Wire: format.provider(), RequestType: requestType}
}

// RequestTypeFor returns the route's request type, as its streaming variant when stream is set.
func (route Route) RequestTypeFor(stream bool) schemas.RequestType {
	if stream {
		return streamVariant(route.RequestType)
	}
	return route.RequestType
}

// StreamFormat names the declared format of the route's stream events, as RedactStreamBody and
// schemas.RedactRawResponseText read it, or "" when Parse cannot assemble the route's stream.
func (route Route) StreamFormat() schemas.RawResponseFormat {
	switch route.Wire {
	case schemas.SageMaker:
		return schemas.RawResponseFormatSageMakerStreamEvent
	case WireGeminiInteractions:
		return schemas.RawResponseFormatGeminiInteractionsStreamEvent
	}
	switch route {
	case Route{Wire: schemas.OpenAI, RequestType: schemas.ChatCompletionRequest}:
		return schemas.RawResponseFormatOpenAIChatCompletionChunk
	case Route{Wire: schemas.OpenAI, RequestType: schemas.TextCompletionRequest}:
		return schemas.RawResponseFormatOpenAITextCompletionChunk
	case Route{Wire: schemas.OpenAI, RequestType: schemas.ResponsesRequest}:
		return schemas.RawResponseFormatOpenAIResponsesStreamEvent
	case Route{Wire: schemas.Anthropic, RequestType: schemas.ResponsesRequest}:
		return schemas.RawResponseFormatAnthropicMessagesStreamEvent
	case Route{Wire: schemas.Gemini, RequestType: schemas.ResponsesRequest}:
		return schemas.RawResponseFormatGeminiStreamChunk
	}
	return ""
}

// Parse decodes a passthrough call on a best-effort basis. It returns (nil, nil) for routes with no
// typed equivalent (unsupported providers, list/retrieve calls, Bedrock knowledge-base and guardrail
// routes), so the caller keeps the raw bodies only. A non-nil error describes a body that matched a
// known route but failed to decode; the Result alongside it still carries whichever side did decode.
// On a route whose type the body names, Result.RequestType is the one the request body named. Parse
// never panics: a converter that panics on an unexpected body is reported as an error for that side.
func Parse(in Input) (*Result, error) {
	format, requestType := classify(in)
	if format == wireUnknown || (requestType == "" && !format.typedByBody()) {
		return nil, nil
	}

	result := &Result{RequestType: requestType}
	var errs []error

	if len(in.RequestBody) > 0 {
		req, err := safeParse(func() (*schemas.BifrostRequest, error) {
			return parseRequest(format, requestType, in.RequestBody)
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("request: %w", err))
		} else if req != nil {
			req.SetProvider(in.Provider)
			if in.Model != "" {
				req.SetModel(in.Model)
			}
			result.Request = req
			if result.RequestType == "" {
				result.RequestType = req.RequestType
			}
		}
	}

	if len(in.ResponseBody) > 0 && (in.StatusCode == 0 || in.StatusCode < 400) {
		var (
			resp *schemas.BifrostResponse
			err  error
		)
		resp, err = safeParse(func() (*schemas.BifrostResponse, error) {
			if in.Stream {
				return parseStreamResponse(format, requestType, in.ResponseBody)
			}
			return parseResponse(format, requestType, in.Model, in.ResponseBody)
		})
		if resp != nil {
			result.Response = resp
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("response: %w", err))
		}
	}

	if result.Request == nil && result.Response == nil {
		return nil, errors.Join(errs...)
	}
	return result, errors.Join(errs...)
}

// InferRequestType returns the request type a passthrough call addresses from its route alone
// (base provider, model, method, path) without reading either body, or "" when the route has no
// typed equivalent. stream selects the streaming variant where one exists. It is cheap enough for
// the request path: a few string suffix checks.
func InferRequestType(provider schemas.ModelProvider, model, method, path string, stream bool) schemas.RequestType {
	format, requestType := classify(Input{Provider: provider, Model: model, Method: method, Path: path})
	if format == wireUnknown || requestType == "" {
		return ""
	}
	if stream {
		return streamVariant(requestType)
	}
	return requestType
}

// streamVariant maps a request type to its streaming counterpart; types without one are returned
// unchanged.
func streamVariant(requestType schemas.RequestType) schemas.RequestType {
	switch requestType {
	case schemas.ChatCompletionRequest:
		return schemas.ChatCompletionStreamRequest
	case schemas.TextCompletionRequest:
		return schemas.TextCompletionStreamRequest
	case schemas.ResponsesRequest:
		return schemas.ResponsesStreamRequest
	case schemas.ResponsesRetrieveRequest:
		return schemas.ResponsesRetrieveStreamRequest
	case schemas.SpeechRequest:
		return schemas.SpeechStreamRequest
	case schemas.TranscriptionRequest:
		return schemas.TranscriptionStreamRequest
	case schemas.ImageGenerationRequest:
		return schemas.ImageGenerationStreamRequest
	case schemas.ImageEditRequest:
		return schemas.ImageEditStreamRequest
	}
	return requestType
}

// classify picks the wire format and inner request type for a call. It mirrors the dispatch each
// provider's Passthrough method uses for usage extraction. Only POST generates on any of these APIs,
// so every other method is no typed request; OpenAI's classifier applies the same rule (and keeps
// GET /containers as a list), and this gate covers the classifiers that take no method.
func classify(in Input) (wireFormat, schemas.RequestType) {
	if (in.Provider == schemas.Gemini || in.Provider == schemas.Vertex) && gemini.IsInteractionsPassthroughPath(in.Path) {
		return classifyInteractions(in.Method, in.Path)
	}
	if !strings.EqualFold(in.Method, "POST") {
		if in.Provider == schemas.OpenAI || in.Provider == schemas.Azure {
			if requestType := openai.ClassifyPassthroughPath(in.Method, in.Path); requestType == schemas.ContainerListRequest {
				return wireOpenAI, requestType
			}
		}
		return wireUnknown, ""
	}
	switch in.Provider {
	case schemas.OpenAI:
		return wireOpenAI, openai.ClassifyPassthroughPath(in.Method, in.Path)
	case schemas.Anthropic:
		return wireAnthropic, anthropic.ClassifyPassthroughPath(in.Path)
	case schemas.Gemini:
		return wireGemini, gemini.ClassifyPassthroughPath(in.Path)
	case schemas.Azure:
		// Azure fronts both OpenAI and Anthropic (Foundry) models; the Anthropic ones are served
		// under /anthropic/ and carry Anthropic model names.
		if strings.HasPrefix(in.Path, "/anthropic/") || schemas.IsAnthropicModel(in.Model) {
			return wireAnthropic, anthropic.ClassifyPassthroughPath(in.Path)
		}
		return wireOpenAI, openai.ClassifyPassthroughPath(in.Method, in.Path)
	case schemas.Vertex:
		switch vertex.PassthroughWireFormat(in.Path) {
		case schemas.Anthropic:
			// :rawPredict / :streamRawPredict carry a Messages body; :countTokens a count request.
			if strings.HasSuffix(pathWithoutQuery(in.Path), ":countTokens") || strings.Contains(in.Path, "/count-tokens") {
				return wireAnthropic, schemas.CountTokensRequest
			}
			return wireAnthropic, schemas.ResponsesRequest
		case schemas.Mistral:
			// Mistral on Vertex serves OpenAI chat-completion shapes from :rawPredict.
			return wireOpenAI, schemas.ChatCompletionRequest
		case schemas.OpenAI:
			return wireOpenAI, openai.ClassifyPassthroughPath(in.Method, in.Path)
		}
		return wireGemini, gemini.ClassifyPassthroughPath(in.Path)
	case schemas.Runware:
		return classifyRunware(in.Method, in.Path)
	case schemas.SageMaker:
		if sagemaker.PassthroughModel(pathWithoutQuery(in.Path)) != "" {
			return wireSageMaker, ""
		}
	case schemas.Bedrock:
		if bedrock.IsPassthroughAgentPath(in.Method, pathWithoutQuery(in.Path)) {
			return wireBedrockAgent, ""
		}
	}
	return wireUnknown, ""
}

// safeParse runs one side of the parse, turning a converter panic into an error so a malformed or
// unexpected body can never take down the caller (the logging writer).
func safeParse[T any](fn func() (*T, error)) (out *T, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("converter panicked: %v", r)
		}
	}()
	return fn()
}

func pathWithoutQuery(path string) string {
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		return path[:idx]
	}
	return path
}

// newConverterContext returns a private context for the provider converters. Some of them record
// routing hints on the context they are given; a throwaway one keeps that local to this parse.
func newConverterContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

func parseRequest(format wireFormat, requestType schemas.RequestType, body []byte) (*schemas.BifrostRequest, error) {
	ctx := newConverterContext()
	switch format {
	case wireGeminiInteractions:
		if requestType == schemas.ResponsesRequest {
			return parseInteractionsRequest(body)
		}
	case wireRunware:
		return parseRunwareRequest(body)
	case wireSageMaker:
		return parseSageMakerRequest(ctx, body)
	case wireBedrockAgent:
		return parseBedrockAgentRequest(body)
	case wireOpenAI:
		switch requestType {
		case schemas.ChatCompletionRequest:
			var native openai.OpenAIChatRequest
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			return &schemas.BifrostRequest{RequestType: requestType, ChatRequest: native.ToBifrostChatRequest(ctx)}, nil
		case schemas.ResponsesRequest:
			var native openai.OpenAIResponsesRequest
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			return &schemas.BifrostRequest{RequestType: requestType, ResponsesRequest: native.ToBifrostResponsesRequest(ctx)}, nil
		case schemas.CompactionRequest:
			var native openai.OpenAICompactionRequest
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			return &schemas.BifrostRequest{RequestType: requestType, CompactionRequest: native.ToBifrostCompactionRequest(ctx)}, nil
		case schemas.TextCompletionRequest:
			var native openai.OpenAITextCompletionRequest
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			return &schemas.BifrostRequest{RequestType: requestType, TextCompletionRequest: native.ToBifrostTextCompletionRequest(ctx)}, nil
		case schemas.EmbeddingRequest:
			var native openai.OpenAIEmbeddingRequest
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			return &schemas.BifrostRequest{RequestType: requestType, EmbeddingRequest: native.ToBifrostEmbeddingRequest(ctx)}, nil
		case schemas.SpeechRequest:
			var native openai.OpenAISpeechRequest
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			return &schemas.BifrostRequest{RequestType: requestType, SpeechRequest: native.ToBifrostSpeechRequest(ctx)}, nil
		case schemas.ImageGenerationRequest:
			var native openai.OpenAIImageGenerationRequest
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			return &schemas.BifrostRequest{RequestType: requestType, ImageGenerationRequest: native.ToBifrostImageGenerationRequest(ctx)}, nil
		case schemas.TranscriptionRequest, schemas.ImageEditRequest, schemas.ImageVariationRequest:
			// Multipart uploads are logged as a SummarizeMultipart summary, not the raw body.
			return parseOpenAIMultipartRequest(requestType, body), nil
		}
	case wireAnthropic:
		switch requestType {
		case schemas.ResponsesRequest, schemas.CountTokensRequest:
			var native anthropic.AnthropicMessageRequest
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			converted := native.ToBifrostResponsesRequest(ctx)
			if requestType == schemas.CountTokensRequest {
				return &schemas.BifrostRequest{RequestType: requestType, CountTokensRequest: converted}, nil
			}
			return &schemas.BifrostRequest{RequestType: requestType, ResponsesRequest: converted}, nil
		case schemas.TextCompletionRequest:
			var native anthropic.AnthropicTextRequest
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			return &schemas.BifrostRequest{RequestType: requestType, TextCompletionRequest: native.ToBifrostTextCompletionRequest(ctx)}, nil
		}
	case wireGemini:
		switch requestType {
		case schemas.ResponsesRequest:
			var native gemini.GeminiGenerationRequest
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			return &schemas.BifrostRequest{RequestType: requestType, ResponsesRequest: native.ToBifrostResponsesRequest(ctx)}, nil
		case schemas.CountTokensRequest:
			var native gemini.GeminiCountTokensRequest
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			return &schemas.BifrostRequest{RequestType: requestType, CountTokensRequest: native.ToGeminiGenerationRequest().ToBifrostResponsesRequest(ctx)}, nil
		case schemas.CachedContentCreateRequest:
			// A cached content holds the same contents, systemInstruction and tools as a generateContent body. Its own
			// request type keeps them untyped, so it is read through the Responses shape, as countTokens is.
			var native gemini.GeminiGenerationRequest
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			return &schemas.BifrostRequest{RequestType: requestType, ResponsesRequest: native.ToBifrostResponsesRequest(ctx)}, nil
		case schemas.EmbeddingRequest:
			return parseGeminiEmbeddingRequest(ctx, body)
		case schemas.ImageGenerationRequest:
			// :predict on an Imagen model; the request carries the prompt per instance.
			var native gemini.GeminiImagenRequest
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			if len(native.Instances) == 0 || native.Instances[0].Prompt == "" {
				return nil, nil
			}
			return &schemas.BifrostRequest{RequestType: requestType, ImageGenerationRequest: &schemas.BifrostImageGenerationRequest{
				Input: &schemas.ImageGenerationInput{Prompt: native.Instances[0].Prompt},
			}}, nil
		}
	}
	return nil, nil
}

// parseGeminiEmbeddingRequest decodes :embedContent (one content), :batchEmbedContents (a
// requests array) and Vertex :predict (an instances array) embedding bodies.
func parseGeminiEmbeddingRequest(ctx *schemas.BifrostContext, body []byte) (*schemas.BifrostRequest, error) {
	if providerUtils.JSONFieldExists(body, "instances") {
		return parseVertexPredictEmbeddingRequest(body)
	}
	var (
		converted *schemas.BifrostEmbeddingRequest
		err       error
	)
	if providerUtils.JSONFieldExists(body, "requests") {
		var native gemini.GeminiBatchEmbeddingRequest
		if err := sonic.Unmarshal(body, &native); err != nil {
			return nil, err
		}
		converted, err = native.ToBifrostEmbeddingRequest(ctx)
	} else {
		var native gemini.GeminiEmbeddingRequest
		if err := sonic.Unmarshal(body, &native); err != nil {
			return nil, err
		}
		converted, err = native.ToBifrostEmbeddingRequest(ctx)
	}
	if err != nil || converted == nil {
		return nil, err
	}
	return &schemas.BifrostRequest{RequestType: schemas.EmbeddingRequest, EmbeddingRequest: converted}, nil
}

func parseResponse(format wireFormat, requestType schemas.RequestType, model string, body []byte) (*schemas.BifrostResponse, error) {
	ctx := newConverterContext()
	switch format {
	case wireGeminiInteractions:
		return parseInteractionsResponse(body)
	case wireRunware:
		return parseRunwareResponse(body)
	case wireSageMaker:
		return parseSageMakerResponse(body)
	case wireOpenAI:
		switch requestType {
		case schemas.ChatCompletionRequest:
			var resp schemas.BifrostChatResponse
			if err := sonic.Unmarshal(body, &resp); err != nil {
				return nil, err
			}
			return &schemas.BifrostResponse{ChatResponse: &resp}, nil
		case schemas.ResponsesRequest:
			var resp schemas.BifrostResponsesResponse
			if err := sonic.Unmarshal(body, &resp); err != nil {
				return nil, err
			}
			return &schemas.BifrostResponse{ResponsesResponse: &resp}, nil
		case schemas.TextCompletionRequest:
			var resp schemas.BifrostTextCompletionResponse
			if err := sonic.Unmarshal(body, &resp); err != nil {
				return nil, err
			}
			return &schemas.BifrostResponse{TextCompletionResponse: &resp}, nil
		case schemas.EmbeddingRequest:
			var resp schemas.BifrostEmbeddingResponse
			if err := sonic.Unmarshal(body, &resp); err != nil {
				return nil, err
			}
			return &schemas.BifrostResponse{EmbeddingResponse: &resp}, nil
		case schemas.ImageGenerationRequest, schemas.ImageEditRequest, schemas.ImageVariationRequest:
			var resp schemas.BifrostImageGenerationResponse
			if err := sonic.Unmarshal(body, &resp); err != nil {
				return nil, err
			}
			return &schemas.BifrostResponse{ImageGenerationResponse: &resp}, nil
		case schemas.TranscriptionRequest:
			// response_format text/srt/vtt returns plain text, which carries nothing to decode.
			if !looksLikeJSONObject(body) {
				return nil, nil
			}
			var resp schemas.BifrostTranscriptionResponse
			if err := sonic.Unmarshal(body, &resp); err != nil {
				return nil, err
			}
			return &schemas.BifrostResponse{TranscriptionResponse: &resp}, nil
		}
	case wireAnthropic:
		switch requestType {
		case schemas.ResponsesRequest:
			var native anthropic.AnthropicMessageResponse
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			return &schemas.BifrostResponse{ResponsesResponse: native.ToBifrostResponsesResponse(ctx)}, nil
		case schemas.TextCompletionRequest:
			var native anthropic.AnthropicTextResponse
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			return &schemas.BifrostResponse{TextCompletionResponse: native.ToBifrostTextCompletionResponse()}, nil
		case schemas.CountTokensRequest:
			var native anthropic.AnthropicCountTokensResponse
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			return &schemas.BifrostResponse{CountTokensResponse: native.ToBifrostCountTokensResponse(model)}, nil
		}
	case wireGemini:
		switch requestType {
		case schemas.ResponsesRequest:
			var native gemini.GenerateContentResponse
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			return &schemas.BifrostResponse{ResponsesResponse: native.ToResponsesBifrostResponsesResponse()}, nil
		case schemas.CountTokensRequest:
			var native gemini.GeminiCountTokensResponse
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			return &schemas.BifrostResponse{CountTokensResponse: native.ToBifrostCountTokensResponse(model)}, nil
		case schemas.EmbeddingRequest:
			if providerUtils.JSONFieldExists(body, "predictions") {
				// Vertex embedding model on :predict.
				var native vertex.VertexEmbeddingResponse
				if err := sonic.Unmarshal(body, &native); err != nil {
					return nil, err
				}
				converted := native.ToBifrostEmbeddingResponse()
				if converted == nil || len(converted.Data) == 0 {
					return nil, nil
				}
				return &schemas.BifrostResponse{EmbeddingResponse: converted}, nil
			}
			var native gemini.GeminiEmbeddingResponse
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			converted := gemini.ToBifrostEmbeddingResponse(&native, model)
			if converted == nil {
				return nil, nil
			}
			return &schemas.BifrostResponse{EmbeddingResponse: converted}, nil
		case schemas.ImageGenerationRequest:
			var native gemini.GeminiImagenResponse
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			// :predict also serves non-Imagen models; only a response that carried images is one.
			converted := native.ToBifrostImageGenerationResponse()
			if converted == nil || !slices.ContainsFunc(converted.Data, func(d schemas.ImageData) bool { return d.B64JSON != "" || d.URL != "" }) {
				return nil, nil
			}
			return &schemas.BifrostResponse{ImageGenerationResponse: converted}, nil
		}
	}
	return nil, nil
}

// parseVertexPredictEmbeddingRequest decodes a Vertex embedding :predict body: each instance's text
// (content for text-embedding-*, text for multimodalembedding@*) becomes one text input. Image and
// video instances carry media the log does not need to repeat, so they are left out.
func parseVertexPredictEmbeddingRequest(body []byte) (*schemas.BifrostRequest, error) {
	var native vertex.VertexEmbeddingRequest
	if err := sonic.Unmarshal(body, &native); err != nil {
		return nil, err
	}
	var input []schemas.EmbeddingInputItem
	for _, instance := range native.Instances {
		text := instance.Content
		if text == "" && instance.Text != nil {
			text = *instance.Text
		}
		if text == "" {
			continue
		}
		input = append(input, schemas.EmbeddingInputItem{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}})
	}
	if len(input) == 0 {
		return nil, nil
	}
	return &schemas.BifrostRequest{RequestType: schemas.EmbeddingRequest, EmbeddingRequest: &schemas.BifrostEmbeddingRequest{Input: input}}, nil
}

func looksLikeJSONObject(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// Runware task types whose text a passthrough call is read for. Other task types (upscaling, background removal,
// 3D, audio, ...) are not read.
const (
	runwareTextTask          = "textInference"
	runwareCaptionTask       = "caption"
	runwareImageCaptionTask  = "imageCaption" // the caption task's label in some of Runware's examples
	runwarePromptEnhanceTask = "promptEnhance"
	runwareImageTask         = "imageInference"
	runwareVideoTask         = "videoInference"
)

// RunwareTaskTexts calls visit with each text a Runware task's model acts on, its path within the task, and the chat
// role it reads as: a text task's system prompt and string message contents, a caption or prompt-enhance task's
// prompt, and an image or video task's prompts (its positive prompt, an image model's system prompt, an avatar
// video's speech, Kling's per-shot prompts), which have no role. Negative prompts name what to leave out and are not
// visited. Only string values are visited. The request reader and the raw-body rewriter both walk this, so they read
// and rewrite the same fields.
func RunwareTaskTexts(task gjson.Result, visit func(path string, role schemas.ChatMessageRole, text gjson.Result)) {
	field := func(path string, role schemas.ChatMessageRole) {
		if text := task.Get(path); text.Type == gjson.String {
			visit(path, role, text)
		}
	}
	switch task.Get("taskType").String() {
	case runwareTextTask:
		field("settings.systemPrompt", schemas.ChatMessageRoleSystem)
		index := 0
		task.Get("messages").ForEach(func(_, message gjson.Result) bool {
			role := schemas.ChatMessageRoleUser
			if message.Get("role").String() == string(schemas.ChatMessageRoleAssistant) {
				role = schemas.ChatMessageRoleAssistant
			}
			field(fmt.Sprintf("messages.%d.content", index), role)
			index++
			return true
		})
	case runwareCaptionTask, runwareImageCaptionTask, runwarePromptEnhanceTask:
		field("prompt", schemas.ChatMessageRoleUser)
	case runwareImageTask:
		field("positivePrompt", "")
		field("settings.systemPrompt", "")
	case runwareVideoTask:
		field("positivePrompt", "")
		field("speech.text", "")
		index := 0
		task.Get("providerSettings.klingai.multiPrompt").ForEach(func(_, _ gjson.Result) bool {
			field(fmt.Sprintf("providerSettings.klingai.multiPrompt.%d.prompt", index), "")
			index++
			return true
		})
	}
}

// classifyRunware classifies a Runware call. Its OpenAI- and Anthropic-compatible endpoints speak those APIs; its task
// endpoint, the API root, takes an array of tasks whose types name the request, so the body decides it.
func classifyRunware(method, path string) (wireFormat, schemas.RequestType) {
	switch strings.TrimSuffix(pathWithoutQuery(path), "/") {
	case "", "/v1":
		return wireRunware, ""
	}
	if requestType := anthropic.ClassifyPassthroughPath(path); requestType != "" {
		return wireAnthropic, requestType
	}
	return wireOpenAI, openai.ClassifyPassthroughPath(method, path)
}

// runwareTasks returns the tasks of a Runware request body: its array, or a single task object.
func runwareTasks(body []byte) ([]gjson.Result, error) {
	if !gjson.ValidBytes(body) {
		return nil, errors.New("runware request body is not valid JSON")
	}
	root := gjson.ParseBytes(body)
	if root.IsObject() {
		return []gjson.Result{root}, nil
	}
	if !root.IsArray() {
		return nil, errors.New("runware request body is neither a task array nor a task")
	}
	return root.Array(), nil
}

// parseRunwareRequest reads a Runware task request into the request its tasks make, named by their types: a chat of
// the text, caption and prompt-enhance tasks' texts, with any image or video prompts as user turns; without one of
// those, an image generation (or a video generation when every prompt is a video's) of the prompts. The texts are the
// ones RunwareTaskTexts visits. A request with none of them has nothing to read.
func parseRunwareRequest(body []byte) (*schemas.BifrostRequest, error) {
	tasks, err := runwareTasks(body)
	if err != nil {
		return nil, err
	}
	var (
		messages            []schemas.ChatMessage
		prompts             []string
		textTask, imageTask bool
	)
	for _, task := range tasks {
		switch task.Get("taskType").String() {
		case runwareTextTask, runwareCaptionTask, runwareImageCaptionTask, runwarePromptEnhanceTask:
			textTask = true
		case runwareImageTask:
			imageTask = true
		}
		RunwareTaskTexts(task, func(_ string, role schemas.ChatMessageRole, text gjson.Result) {
			if role == "" {
				prompts = append(prompts, strings.Clone(text.Str))
				return
			}
			messages = append(messages, chatTextMessage(role, strings.Clone(text.Str)))
		})
	}
	switch {
	case textTask:
		for _, prompt := range prompts {
			messages = append(messages, chatTextMessage(schemas.ChatMessageRoleUser, prompt))
		}
		return &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Input: messages}}, nil
	case len(prompts) == 0:
		return nil, nil
	case imageTask:
		return &schemas.BifrostRequest{RequestType: schemas.ImageGenerationRequest, ImageGenerationRequest: &schemas.BifrostImageGenerationRequest{
			Input: &schemas.ImageGenerationInput{Prompt: strings.Join(prompts, "\n")},
		}}, nil
	default:
		return &schemas.BifrostRequest{RequestType: schemas.VideoGenerationRequest, VideoGenerationRequest: &schemas.BifrostVideoGenerationRequest{
			Input: &schemas.VideoGenerationInput{Prompt: strings.Join(prompts, "\n")},
		}}, nil
	}
}

// parseRunwareResponse reads the text results of a Runware task response as chat choices, one per text, caption or
// prompt-enhance result, in order. Reasoning content is not read. A response with no text result (images, videos,
// errors) has no text form.
func parseRunwareResponse(body []byte) (*schemas.BifrostResponse, error) {
	if !gjson.ValidBytes(body) {
		return nil, errors.New("runware response body is not valid JSON")
	}
	var choices []schemas.BifrostResponseChoice
	gjson.GetBytes(body, "data").ForEach(func(_, result gjson.Result) bool {
		switch result.Get("taskType").String() {
		case runwareTextTask, runwareCaptionTask, runwareImageCaptionTask, runwarePromptEnhanceTask:
		default:
			return true
		}
		if text := result.Get("text"); text.Type == gjson.String {
			message := chatTextMessage(schemas.ChatMessageRoleAssistant, strings.Clone(text.Str))
			choices = append(choices, schemas.BifrostResponseChoice{
				Index:                       len(choices),
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{Message: &message},
			})
		}
		return true
	})
	if len(choices) == 0 {
		return nil, nil
	}
	return &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{Object: "chat.completion", Choices: choices}}, nil
}

// chatTextMessage returns a chat message of role carrying text.
func chatTextMessage(role schemas.ChatMessageRole, text string) schemas.ChatMessage {
	return schemas.ChatMessage{Role: role, Content: &schemas.ChatMessageContent{ContentStr: &text}}
}

// classifyInteractions classifies a Gemini Interactions call: a create runs a turn, and a GET of an interaction
// returns its steps, when polling a background run or resuming its stream. Deletes and cancels carry no text.
func classifyInteractions(method, path string) (wireFormat, schemas.RequestType) {
	switch {
	case gemini.IsInteractionsCreatePath(method, path):
		return wireGeminiInteractions, schemas.ResponsesRequest
	case method == "GET" && gemini.IsInteractionsResourcePath(method, path):
		return wireGeminiInteractions, schemas.ResponsesRetrieveRequest
	}
	return wireUnknown, ""
}

// parseInteractionsRequest reads an Interactions create body as a Responses request: its system instruction, and its
// input, which is a string, one content part or step, or a list of either.
func parseInteractionsRequest(body []byte) (*schemas.BifrostRequest, error) {
	if !looksLikeJSONObject(body) || !gjson.ValidBytes(body) {
		return nil, errors.New("interactions request body is not a JSON object")
	}
	root := gjson.ParseBytes(body)
	var messages []schemas.ResponsesMessage
	switch input := root.Get("input"); {
	case input.Type == gjson.String:
		messages = append(messages, responsesTextMessage(schemas.ResponsesInputMessageRoleUser, strings.Clone(input.Str)))
	case input.IsObject():
		messages = appendInteractionsInput(messages, input)
	case input.IsArray():
		input.ForEach(func(_, item gjson.Result) bool {
			messages = appendInteractionsInput(messages, item)
			return true
		})
	}
	req := &schemas.BifrostResponsesRequest{Input: messages}
	if instruction := root.Get("system_instruction"); instruction.Type == gjson.String {
		instructions := strings.Clone(instruction.Str)
		req.Params = &schemas.ResponsesParameters{Instructions: &instructions}
	}
	return &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest, ResponsesRequest: req}, nil
}

// appendInteractionsInput appends one input item: a text part as a user turn, a user or model step as that role's
// turns, one per text part, a replayed function call with its arguments, or a function result as a tool output of its
// text (see functionResultTexts). Other items (media, thoughts, server tool steps) carry no text to read.
func appendInteractionsInput(messages []schemas.ResponsesMessage, item gjson.Result) []schemas.ResponsesMessage {
	switch item.Get("type").String() {
	case "text":
		if text := item.Get("text"); text.Type == gjson.String {
			messages = append(messages, responsesTextMessage(schemas.ResponsesInputMessageRoleUser, strings.Clone(text.Str)))
		}
	case "user_input", "model_output":
		role := schemas.ResponsesInputMessageRoleUser
		if item.Get("type").String() == "model_output" {
			role = schemas.ResponsesInputMessageRoleAssistant
		}
		item.Get("content").ForEach(func(_, part gjson.Result) bool {
			if text := part.Get("text"); part.Get("type").String() == "text" && text.Type == gjson.String {
				messages = append(messages, responsesTextMessage(role, strings.Clone(text.Str)))
			}
			return true
		})
	case "function_call":
		messages = append(messages, interactionsFunctionCall(item))
	case "function_result":
		output := strings.Join(functionResultTexts(item.Get("result")), "\n")
		callID := strings.Clone(item.Get("call_id").String())
		outputType := schemas.ResponsesMessageTypeFunctionCallOutput
		messages = append(messages, schemas.ResponsesMessage{
			Type: &outputType,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: &callID,
				Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &output},
			},
		})
	}
	return messages
}

// parseInteractionsResponse reads an interaction (a create response, or a GET of one) as a Responses response of its
// model output text and function calls. Thoughts, the user input a GET repeats, function results and server tool
// steps are not read; a run still in progress has no steps yet and reads as an empty response.
func parseInteractionsResponse(body []byte) (*schemas.BifrostResponse, error) {
	if !looksLikeJSONObject(body) || !gjson.ValidBytes(body) {
		return nil, nil
	}
	var output []schemas.ResponsesMessage
	gjson.GetBytes(body, "steps").ForEach(func(_, step gjson.Result) bool {
		switch step.Get("type").String() {
		case "model_output":
			var texts []string
			step.Get("content").ForEach(func(_, part gjson.Result) bool {
				if text := part.Get("text"); part.Get("type").String() == "text" && text.Type == gjson.String {
					texts = append(texts, strings.Clone(text.Str))
				}
				return true
			})
			if len(texts) > 0 {
				output = append(output, responsesOutputMessage(texts))
			}
		case "function_call":
			output = append(output, interactionsFunctionCall(step))
		}
		return true
	})
	return &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{Output: output}}, nil
}

// interactionsFunctionCall reads an Interactions function call step, whose arguments are a JSON object (or a string
// holding one), as a function call item.
func interactionsFunctionCall(step gjson.Result) schemas.ResponsesMessage {
	arguments := step.Get("arguments")
	raw := arguments.Raw
	if arguments.Type == gjson.String {
		raw = arguments.Str
	}
	return responsesFunctionCall(strings.Clone(step.Get("id").String()), strings.Clone(step.Get("name").String()), strings.Clone(raw))
}

// functionResultTexts returns the text of an Interactions function result: a string result, the text parts of a
// content list (media parts carry no text), or a free-form JSON object, read as its JSON the way a Gemini function
// response is.
func functionResultTexts(result gjson.Result) []string {
	switch {
	case result.Type == gjson.String:
		return []string{strings.Clone(result.Str)}
	case result.IsObject():
		return []string{strings.Clone(result.Raw)}
	}
	var texts []string
	result.ForEach(func(_, part gjson.Result) bool {
		if text := part.Get("text"); part.Get("type").String() == "text" && text.Type == gjson.String {
			texts = append(texts, strings.Clone(text.Str))
		}
		return true
	})
	return texts
}

// responsesTextMessage returns a Responses message of role carrying text.
func responsesTextMessage(role schemas.ResponsesMessageRoleType, text string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{Role: &role, Content: &schemas.ResponsesMessageContent{ContentStr: &text}}
}

// responsesOutputMessage returns an assistant output message with one output_text block per text.
func responsesOutputMessage(texts []string) schemas.ResponsesMessage {
	role, messageType := schemas.ResponsesInputMessageRoleAssistant, schemas.ResponsesMessageTypeMessage
	blocks := make([]schemas.ResponsesMessageContentBlock, len(texts))
	for i := range texts {
		blocks[i] = schemas.ResponsesMessageContentBlock{Type: schemas.ResponsesOutputMessageContentTypeText, Text: &texts[i]}
	}
	return schemas.ResponsesMessage{Type: &messageType, Role: &role, Content: &schemas.ResponsesMessageContent{ContentBlocks: blocks}}
}

// responsesFunctionCall returns a function call item with the given call id, name and JSON arguments.
func responsesFunctionCall(callID, name, arguments string) schemas.ResponsesMessage {
	callType := schemas.ResponsesMessageTypeFunctionCall
	return schemas.ResponsesMessage{
		Type:                 &callType,
		ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: &callID, Name: &name, Arguments: &arguments},
	}
}

// parseSageMakerRequest reads a SageMaker invocation body for every text its container could run: OpenAI chat messages
// (the TGI Messages API, LMI chat), an OpenAI completion prompt, and the "inputs" of a TGI or LMI generate call (a
// string) or a TEI embedding call (a string or string array). Containers choose among these fields in different orders
// (TGI 3 tries inputs first, LMI's vLLM engine the prompt, LMI's rolling batch the messages), so a body carrying more
// than one is read for all of them, as a chat with the prompt and inputs as user turns; a field alone reads as its own
// request. A repeated inputs key reads as its last value, as LMI's Python decoder takes it. Any other body (a rerank,
// custom JSON, CSV) has no text form.
func parseSageMakerRequest(ctx *schemas.BifrostContext, body []byte) (*schemas.BifrostRequest, error) {
	if !looksLikeJSONObject(body) {
		return nil, nil
	}
	root := gjson.ParseBytes(body)
	var fields struct {
		Inputs json.RawMessage `json:"inputs"`
	}
	if err := sonic.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	inputs := sageMakerStrings(gjson.ParseBytes(fields.Inputs))
	hasMessages := root.Get("messages").IsArray()
	prompt := root.Get("prompt")
	hasPrompt := prompt.Exists() && prompt.Type != gjson.Null
	switch {
	case hasMessages && !hasPrompt && inputs == nil:
		var native openai.OpenAIChatRequest
		if err := sonic.Unmarshal(body, &native); err != nil {
			return nil, err
		}
		return &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: native.ToBifrostChatRequest(ctx)}, nil
	case hasPrompt && !hasMessages && inputs == nil:
		var native openai.OpenAITextCompletionRequest
		if err := sonic.Unmarshal(body, &native); err != nil {
			return nil, err
		}
		return &schemas.BifrostRequest{RequestType: schemas.TextCompletionRequest, TextCompletionRequest: native.ToBifrostTextCompletionRequest(ctx)}, nil
	case !hasMessages && !hasPrompt:
		if inputs == nil {
			return nil, nil
		}
		input := &schemas.TextCompletionInput{}
		if text := gjson.ParseBytes(fields.Inputs); text.Type == gjson.String {
			input.PromptStr = &inputs[0]
		} else {
			input.PromptArray = inputs
		}
		return &schemas.BifrostRequest{RequestType: schemas.TextCompletionRequest, TextCompletionRequest: &schemas.BifrostTextCompletionRequest{Input: input}}, nil
	}
	chat := &schemas.BifrostChatRequest{}
	if hasMessages {
		var native openai.OpenAIChatRequest
		if err := sonic.Unmarshal(body, &native); err != nil {
			return nil, err
		}
		chat = native.ToBifrostChatRequest(ctx)
	}
	for _, text := range append(sageMakerStrings(prompt), inputs...) {
		chat.Input = append(chat.Input, chatTextMessage(schemas.ChatMessageRoleUser, text))
	}
	return &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: chat}, nil
}

// sageMakerStrings returns the texts of a prompt or inputs value: the string itself, or the strings of a list (whose
// token-id items carry no text). Any other value, such as an object, has no text this reader reads; the raw-body
// rewriter selects the same strings. It returns nil when the value has no text.
func sageMakerStrings(value gjson.Result) []string {
	if value.Type == gjson.String {
		return []string{strings.Clone(value.Str)}
	}
	if !value.IsArray() {
		return nil
	}
	var texts []string
	value.ForEach(func(_, item gjson.Result) bool {
		if item.Type == gjson.String {
			texts = append(texts, strings.Clone(item.Str))
		}
		return true
	})
	return texts
}

// parseSageMakerResponse reads a SageMaker container's reply by its shape: an OpenAI chat or completion response, a
// TGI or LMI generation ({"generated_text"}, with any best_of alternatives, an array of them, or LMI's legacy
// {"outputs"}), or TEI's embedding vectors, which carry no text. Any other shape has no text form.
func parseSageMakerResponse(body []byte) (*schemas.BifrostResponse, error) {
	if !gjson.ValidBytes(body) {
		return nil, nil
	}
	root := gjson.ParseBytes(body)
	if root.Get("choices").IsArray() {
		if schemas.IsTextCompletionChoice(root, "message") {
			var resp schemas.BifrostTextCompletionResponse
			if err := sonic.Unmarshal(body, &resp); err != nil {
				return nil, err
			}
			return &schemas.BifrostResponse{TextCompletionResponse: &resp}, nil
		}
		var resp schemas.BifrostChatResponse
		if err := sonic.Unmarshal(body, &resp); err != nil {
			return nil, err
		}
		return &schemas.BifrostResponse{ChatResponse: &resp}, nil
	}
	var texts []string
	// A generation's text, then the alternative answers best_of or n returns beside it.
	generation := func(item gjson.Result) {
		if text := item.Get("generated_text"); text.Type == gjson.String {
			texts = append(texts, strings.Clone(text.Str))
		}
		item.Get("details.best_of_sequences").ForEach(func(_, sequence gjson.Result) bool {
			if text := sequence.Get("generated_text"); text.Type == gjson.String {
				texts = append(texts, strings.Clone(text.Str))
			}
			return true
		})
	}
	switch {
	case root.Get("generated_text").Type == gjson.String:
		generation(root)
	case root.IsArray() && root.Get("0.generated_text").Exists():
		root.ForEach(func(_, item gjson.Result) bool {
			generation(item)
			return true
		})
	case root.Get("outputs").IsArray():
		root.Get("outputs").ForEach(func(_, item gjson.Result) bool {
			if item.Type == gjson.String {
				texts = append(texts, strings.Clone(item.Str))
			}
			return true
		})
	case root.IsArray() && (root.Get("0.0").Type == gjson.Number || root.Get("0").Type == gjson.Number):
		return &schemas.BifrostResponse{EmbeddingResponse: &schemas.BifrostEmbeddingResponse{}}, nil
	default:
		return nil, nil
	}
	return textCompletionResponse(texts), nil
}

// textCompletionResponse returns a completion response with one choice per text.
func textCompletionResponse(texts []string) *schemas.BifrostResponse {
	resp := &schemas.BifrostTextCompletionResponse{Object: "text_completion"}
	for i := range texts {
		resp.Choices = append(resp.Choices, schemas.BifrostResponseChoice{
			Index:                        i,
			TextCompletionResponseChoice: &schemas.TextCompletionResponseChoice{Text: &texts[i]},
		})
	}
	return &schemas.BifrostResponse{TextCompletionResponse: resp}
}

// parseBedrockAgentRequest reads a Bedrock InvokeAgent body as a chat turn: the prompt session attributes, which the
// agent puts into its orchestration prompt, as system text, then the session's conversation history, the results of
// tools the agent asked the caller to run, and the input text, in that order. Session attributes go to the caller's
// Lambda functions, not the model, and are not read. The agent's answer arrives as a binary AWS event stream and has
// no text form.
func parseBedrockAgentRequest(body []byte) (*schemas.BifrostRequest, error) {
	if !looksLikeJSONObject(body) || !gjson.ValidBytes(body) {
		return nil, errors.New("bedrock agent request body is not a JSON object")
	}
	root := gjson.ParseBytes(body)
	var messages []schemas.ChatMessage
	root.Get("sessionState.promptSessionAttributes").ForEach(func(_, value gjson.Result) bool {
		if value.Type == gjson.String {
			messages = append(messages, chatTextMessage(schemas.ChatMessageRoleSystem, strings.Clone(value.Str)))
		}
		return true
	})
	root.Get("sessionState.conversationHistory.messages").ForEach(func(_, message gjson.Result) bool {
		role := schemas.ChatMessageRoleUser
		if message.Get("role").String() == string(schemas.ChatMessageRoleAssistant) {
			role = schemas.ChatMessageRoleAssistant
		}
		message.Get("content").ForEach(func(_, block gjson.Result) bool {
			if text := block.Get("text"); text.Type == gjson.String {
				messages = append(messages, chatTextMessage(role, strings.Clone(text.Str)))
			}
			return true
		})
		return true
	})
	root.Get("sessionState.returnControlInvocationResults").ForEach(func(_, result gjson.Result) bool {
		for _, kind := range []string{"functionResult", "apiResult"} {
			result.Get(kind + ".responseBody").ForEach(func(_, content gjson.Result) bool {
				if text := content.Get("body"); text.Type == gjson.String {
					messages = append(messages, chatTextMessage(schemas.ChatMessageRoleTool, strings.Clone(text.Str)))
				}
				return true
			})
		}
		return true
	})
	if input := root.Get("inputText"); input.Type == gjson.String {
		messages = append(messages, chatTextMessage(schemas.ChatMessageRoleUser, strings.Clone(input.Str)))
	}
	return &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Input: messages}}, nil
}
