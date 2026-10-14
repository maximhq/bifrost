// Package passthroughparse decodes the raw request and response bodies of a passthrough call into
// Bifrost's typed request/response schemas, so passthrough traffic can be logged and rendered like
// any other request. Routing is shared with the providers' passthrough usage extractors (each
// provider's ClassifyPassthroughPath and vertex.PassthroughWireFormat), so a path is classified
// the same way for billing and for logging.
//
// Everything here is a pure transformation of the bytes it is handed: no network, no logging,
// no shared state. It is meant to run off the request path (the logging plugin calls it from its
// batch writer).
package passthroughparse

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/providers/vertex"
	"github.com/maximhq/bifrost/core/schemas"
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
)

// Parse decodes a passthrough call. It returns (nil, nil) for routes with no typed equivalent
// (unsupported providers, list/retrieve calls, Bedrock agent routes), so the caller keeps the raw
// bodies only. A non-nil error describes a body that matched a known route but failed to decode;
// the Result alongside it still carries whichever side did decode.
func Parse(in Input) (*Result, error) {
	format, requestType := classify(in)
	if format == wireUnknown || requestType == "" {
		return nil, nil
	}

	result := &Result{RequestType: requestType}
	var errs []error

	if len(in.RequestBody) > 0 {
		req, err := parseRequest(format, requestType, in.RequestBody)
		if err != nil {
			errs = append(errs, fmt.Errorf("request: %w", err))
		} else if req != nil {
			req.SetProvider(in.Provider)
			if in.Model != "" {
				req.SetModel(in.Model)
			}
			result.Request = req
		}
	}

	if len(in.ResponseBody) > 0 && (in.StatusCode == 0 || in.StatusCode < 400) && !in.Stream {
		resp, err := parseResponse(format, requestType, in.Model, in.ResponseBody)
		if err != nil {
			errs = append(errs, fmt.Errorf("response: %w", err))
		} else {
			result.Response = resp
		}
	}

	if result.Request == nil && result.Response == nil {
		return nil, errors.Join(errs...)
	}
	return result, errors.Join(errs...)
}

// classify picks the wire format and inner request type for a call. It mirrors the dispatch each
// provider's Passthrough method uses for usage extraction. Only POST generates on any of these APIs,
// so every other method is no typed request; OpenAI's classifier applies the same rule (and keeps
// GET /containers as a list), and this gate covers the classifiers that take no method.
func classify(in Input) (wireFormat, schemas.RequestType) {
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
	}
	return wireUnknown, ""
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
		case schemas.TextCompletionRequest:
			var native openai.OpenAITextCompletionRequest
			if err := sonic.Unmarshal(body, &native); err != nil {
				return nil, err
			}
			return &schemas.BifrostRequest{RequestType: requestType, TextCompletionRequest: native.ToBifrostTextCompletionRequest(ctx)}, nil
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
		}
	}
	return nil, nil
}

func parseResponse(format wireFormat, requestType schemas.RequestType, model string, body []byte) (*schemas.BifrostResponse, error) {
	ctx := newConverterContext()
	switch format {
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
		}
	}
	return nil, nil
}
