package requestguard

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// RequestFeatures is a compact, request-owned snapshot. It contains only
// scalar values and never retains the pooled BifrostRequest or any of its
// nested pointers.
type RequestFeatures struct {
	RequestType                       schemas.RequestType   `json:"request_type"`
	Provider                          schemas.ModelProvider `json:"provider"`
	Model                             string                `json:"model"`
	IsStreaming                       bool                  `json:"is_streaming"`
	RequiresTools                     bool                  `json:"requires_tools"`
	ToolCount                         int                   `json:"tool_count"`
	RequiresParallelTools             bool                  `json:"requires_parallel_tools"`
	RequiresToolChoice                bool                  `json:"requires_tool_choice"`
	RequiresForcedToolChoice          bool                  `json:"requires_forced_tool_choice"`
	RequiresStructuredOutput          bool                  `json:"requires_structured_output"`
	RequiresStructuredOutputWithTools bool                  `json:"requires_structured_output_with_tools"`
	RequiresVision                    bool                  `json:"requires_vision"`
	ImageCount                        int                   `json:"image_count"`
	RequestedMaxOutputTokens          int                   `json:"requested_max_output_tokens"`
	Supported                         bool                  `json:"supported"`
}

// ExtractRequestFeatures returns normalized guard inputs for chat and
// Responses requests. Other request types are represented as unsupported.
func ExtractRequestFeatures(req *schemas.BifrostRequest) RequestFeatures {
	if req == nil {
		return RequestFeatures{}
	}

	requestType := req.RequestType
	if requestType == "" {
		switch {
		case req.ChatRequest != nil:
			requestType = schemas.ChatCompletionRequest
		case req.ResponsesRequest != nil:
			requestType = schemas.ResponsesRequest
		}
	}

	features := RequestFeatures{RequestType: requestType}
	switch requestType {
	case schemas.ChatCompletionStreamRequest, schemas.ResponsesStreamRequest:
		features.IsStreaming = true
	}

	switch requestType {
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		features.Supported = true
		if req.ChatRequest != nil {
			features.Provider = req.ChatRequest.Provider
			features.Model = req.ChatRequest.Model
			extractChatFeatures(&features, req.ChatRequest)
		}
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		features.Supported = true
		if req.ResponsesRequest != nil {
			features.Provider = req.ResponsesRequest.Provider
			features.Model = req.ResponsesRequest.Model
			extractResponsesFeatures(&features, req.ResponsesRequest)
		}
	}
	return features
}

func extractChatFeatures(features *RequestFeatures, req *schemas.BifrostChatRequest) {
	if features == nil || req == nil || req.Params == nil {
		return
	}
	params := req.Params
	features.ToolCount = len(params.Tools)
	features.RequiresTools = features.ToolCount > 0
	features.RequiresParallelTools = features.RequiresTools && params.ParallelToolCalls != nil && *params.ParallelToolCalls
	features.RequiresToolChoice = params.ToolChoice != nil
	features.RequiresForcedToolChoice = params.ToolChoice.IsForced()
	features.RequiresStructuredOutput = isStructuredChatFormat(params.ResponseFormat)
	features.RequiresStructuredOutputWithTools = features.RequiresStructuredOutput && features.RequiresTools
	if params.MaxCompletionTokens != nil && *params.MaxCompletionTokens > 0 {
		features.RequestedMaxOutputTokens = *params.MaxCompletionTokens
	}

	for i := range req.Input {
		content := req.Input[i].Content
		if content == nil {
			continue
		}
		for j := range content.ContentBlocks {
			if content.ContentBlocks[j].ImageURLStruct != nil {
				features.ImageCount++
			}
		}
	}
	features.RequiresVision = features.ImageCount > 0
}

func extractResponsesFeatures(features *RequestFeatures, req *schemas.BifrostResponsesRequest) {
	if features == nil || req == nil || req.Params == nil {
		return
	}
	params := req.Params
	features.ToolCount = len(params.Tools)
	features.RequiresTools = features.ToolCount > 0
	features.RequiresParallelTools = features.RequiresTools && params.ParallelToolCalls != nil && *params.ParallelToolCalls
	features.RequiresToolChoice = params.ToolChoice != nil
	features.RequiresForcedToolChoice = params.ToolChoice.IsForced()
	features.RequiresStructuredOutput = isStructuredResponsesFormat(params.Text)
	features.RequiresStructuredOutputWithTools = features.RequiresStructuredOutput && features.RequiresTools
	if params.MaxOutputTokens != nil && *params.MaxOutputTokens > 0 {
		features.RequestedMaxOutputTokens = *params.MaxOutputTokens
	}

	for i := range req.Input {
		content := req.Input[i].Content
		if content == nil {
			continue
		}
		for j := range content.ContentBlocks {
			block := &content.ContentBlocks[j]
			if block.ResponsesInputMessageContentBlockImage != nil || block.Type == schemas.ResponsesInputMessageContentBlockTypeImage {
				features.ImageCount++
			}
		}
	}
	features.RequiresVision = features.ImageCount > 0
}

func isStructuredChatFormat(responseFormat *interface{}) bool {
	if responseFormat == nil || *responseFormat == nil {
		return false
	}
	if format, ok := schemas.ParseChatResponseFormat(responseFormat); ok {
		return isStructuredFormatType(format.Type)
	}
	switch format := (*responseFormat).(type) {
	case schemas.ChatResponseFormat:
		return isStructuredFormatType(format.Type)
	case *schemas.ChatResponseFormat:
		return format != nil && isStructuredFormatType(format.Type)
	default:
		return false
	}
}

func isStructuredResponsesFormat(text *schemas.ResponsesTextConfig) bool {
	if text == nil || text.Format == nil {
		return false
	}
	return isStructuredFormatType(text.Format.Type)
}

func isStructuredFormatType(formatType string) bool {
	switch strings.ToLower(strings.TrimSpace(formatType)) {
	case "json_schema", "json_object":
		return true
	default:
		return false
	}
}
