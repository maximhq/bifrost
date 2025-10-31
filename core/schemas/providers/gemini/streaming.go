package gemini

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func ProcessGeminiStreamChunk(jsonData string) (*GenerateContentResponse, error) {
	var errorCheck map[string]interface{}
	if err := sonic.Unmarshal([]byte(jsonData), &errorCheck); err != nil {
		return nil, fmt.Errorf("failed to parse stream data as JSON: %v", err)
	}

	if _, hasError := errorCheck["error"]; hasError {
		return nil, fmt.Errorf("gemini api error: %v", errorCheck["error"])
	}

	var geminiResponse GenerateContentResponse
	if err := sonic.Unmarshal([]byte(jsonData), &geminiResponse); err != nil {
		return nil, fmt.Errorf("failed to parse Gemini stream response: %v", err)
	}

	return &geminiResponse, nil
}

func ExtractGeminiUsageMetadata(geminiResponse *GenerateContentResponse) (int, int, int) {
	var inputTokens, outputTokens, totalTokens int
	if geminiResponse.UsageMetadata != nil {
		usageMetadata := geminiResponse.UsageMetadata
		inputTokens = int(usageMetadata.PromptTokenCount)
		outputTokens = int(usageMetadata.CandidatesTokenCount)
		totalTokens = int(usageMetadata.TotalTokenCount)
	}
	return inputTokens, outputTokens, totalTokens
}

func ParseStreamGeminiError(providerName schemas.ModelProvider, resp *http.Response) *schemas.BifrostError {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return newBifrostOperationError("failed to read error response body", err, providerName)
	}

	var errorResp GeminiGenerationError
	if err := sonic.Unmarshal(body, &errorResp); err == nil {
		bifrostErr := &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     schemas.Ptr(resp.StatusCode),
			Error: &schemas.ErrorField{
				Code:    schemas.Ptr(strconv.Itoa(errorResp.Error.Code)),
				Message: errorResp.Error.Message,
			},
		}
		return bifrostErr
	}

	var rawResponse interface{}
	if err := sonic.Unmarshal(body, &rawResponse); err != nil {
		return newBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	return newBifrostOperationError(fmt.Sprintf("Gemini streaming error (HTTP %d): %v", resp.StatusCode, rawResponse), fmt.Errorf("HTTP %d", resp.StatusCode), providerName)
}

func ParseGeminiError(providerName schemas.ModelProvider, resp *fasthttp.Response) *schemas.BifrostError {
	body := resp.Body()

	var errorResp GeminiGenerationError
	if err := sonic.Unmarshal(body, &errorResp); err == nil {
		bifrostErr := &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     schemas.Ptr(resp.StatusCode()),
			Error: &schemas.ErrorField{
				Code:    schemas.Ptr(strconv.Itoa(errorResp.Error.Code)),
				Message: errorResp.Error.Message,
			},
		}
		return bifrostErr
	}

	var rawResponse map[string]interface{}
	if err := sonic.Unmarshal(body, &rawResponse); err != nil {
		return newBifrostOperationError("failed to parse error response", err, providerName)
	}

	return newBifrostOperationError(fmt.Sprintf("Gemini error: %v", rawResponse), fmt.Errorf("HTTP %d", resp.StatusCode()), providerName)
}

func ConvertGeminiStreamChunkToBifrostResponsesStream(
	geminiResponse *GenerateContentResponse,
	providerName schemas.ModelProvider,
	model string,
	chunkIndex int,
	lastChunkTime time.Time,
	jsonData string,
	sendBackRawResponse bool,
) *schemas.BifrostResponsesStreamResponse {
	if len(geminiResponse.Candidates) == 0 {
		return nil
	}

	candidate := geminiResponse.Candidates[0]
	if candidate.Content == nil || len(candidate.Content.Parts) == 0 {
		return nil
	}

	outputMessages := convertGeminiCandidatesToResponsesOutput([]*Candidate{candidate})
	if len(outputMessages) == 0 {
		return nil
	}

	response := &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
		SequenceNumber: chunkIndex,
		Item:           &outputMessages[0],
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.ResponsesStreamRequest,
			Provider:       providerName,
			ModelRequested: model,
			ChunkIndex:     chunkIndex,
			Latency:        time.Since(lastChunkTime).Milliseconds(),
		},
	}

	if sendBackRawResponse {
		response.ExtraFields.RawResponse = jsonData
	}

	return response
}

func CreateGeminiResponsesStreamEndResponse(
	usage *schemas.ResponsesResponseUsage,
	providerName schemas.ModelProvider,
	model string,
	chunkIndex int,
	startTime time.Time,
) *schemas.BifrostResponsesStreamResponse {
	return &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeCompleted,
		SequenceNumber: chunkIndex + 1,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.ResponsesStreamRequest,
			Provider:       providerName,
			ModelRequested: model,
			ChunkIndex:     chunkIndex + 1,
			Latency:        time.Since(startTime).Milliseconds(),
		},
	}
}

func ConvertGeminiStreamChunkToBifrostChatResponse(
	geminiResponse *GenerateContentResponse,
	providerName schemas.ModelProvider,
	modelRequested string,
	chunkIndex int,
	lastChunkTime time.Time,
	jsonData string,
	sendBackRawResponse bool,
) *schemas.BifrostChatResponse {
	if len(geminiResponse.Candidates) == 0 {
		return nil
	}

	candidate := geminiResponse.Candidates[0]
	if candidate.Content == nil || len(candidate.Content.Parts) == 0 {
		return nil
	}

	var content *string
	var toolCalls []schemas.ChatAssistantMessageToolCall

	for _, part := range candidate.Content.Parts {
		if part.Text != "" {
			if content == nil {
				content = &part.Text
			} else {
				*content += part.Text
			}
		}

		if part.FunctionCall != nil {
			jsonArgs, err := sonic.Marshal(part.FunctionCall.Args)
			if err != nil {
				jsonArgs = []byte(fmt.Sprintf("%v", part.FunctionCall.Args))
			}
			callID := part.FunctionCall.Name
			if strings.TrimSpace(part.FunctionCall.ID) != "" {
				callID = part.FunctionCall.ID
			}
			toolCalls = append(toolCalls, schemas.ChatAssistantMessageToolCall{
				ID:   schemas.Ptr(callID),
				Type: schemas.Ptr(string(schemas.ChatToolChoiceTypeFunction)),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      &part.FunctionCall.Name,
					Arguments: string(jsonArgs),
				},
			})
		}
	}

	if content == nil && len(toolCalls) == 0 {
		return nil
	}

	response := &schemas.BifrostChatResponse{
		Object: "chat.completion.chunk",
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{
						Content:   content,
						ToolCalls: toolCalls,
					},
				},
				FinishReason: func() *string {
					if candidate.FinishReason != "" {
						fr := string(candidate.FinishReason)
						return &fr
					}
					return nil
				}(),
			},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.ChatCompletionStreamRequest,
			Provider:       providerName,
			ModelRequested: modelRequested,
			ChunkIndex:     chunkIndex,
			Latency:        time.Since(lastChunkTime).Milliseconds(),
		},
	}

	if sendBackRawResponse {
		response.ExtraFields.RawResponse = jsonData
	}

	return response
}

func ConvertGeminiStreamChunkToBifrostSpeechResponse(
	geminiResponse *GenerateContentResponse,
	providerName schemas.ModelProvider,
	modelRequested string,
	chunkIndex int,
	lastChunkTime time.Time,
	jsonData string,
	sendBackRawResponse bool,
) *schemas.BifrostSpeechStreamResponse {
	if len(geminiResponse.Candidates) == 0 {
		return nil
	}

	candidate := geminiResponse.Candidates[0]
	if candidate.Content == nil || len(candidate.Content.Parts) == 0 {
		return nil
	}

	var audioChunk []byte
	for _, part := range candidate.Content.Parts {
		if part.InlineData != nil && part.InlineData.Data != nil {
			audioChunk = append(audioChunk, part.InlineData.Data...)
		}
	}

	if len(audioChunk) == 0 {
		return nil
	}

	response := &schemas.BifrostSpeechStreamResponse{
		Type:  schemas.SpeechStreamResponseTypeDelta,
		Audio: audioChunk,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.SpeechStreamRequest,
			Provider:       providerName,
			ModelRequested: modelRequested,
			ChunkIndex:     chunkIndex,
			Latency:        time.Since(lastChunkTime).Milliseconds(),
		},
	}

	if sendBackRawResponse {
		response.ExtraFields.RawResponse = jsonData
	}

	return response
}

func ConvertGeminiStreamChunkToBifrostTranscriptionResponse(
	geminiResponse *GenerateContentResponse,
	providerName schemas.ModelProvider,
	modelRequested string,
	chunkIndex int,
	lastChunkTime time.Time,
	jsonData string,
	sendBackRawResponse bool,
) *schemas.BifrostTranscriptionStreamResponse {
	if len(geminiResponse.Candidates) == 0 {
		return nil
	}

	candidate := geminiResponse.Candidates[0]
	if candidate.Content == nil || len(candidate.Content.Parts) == 0 {
		return nil
	}

	var deltaText string
	for _, part := range candidate.Content.Parts {
		if part.Text != "" {
			deltaText += part.Text
		}
	}

	if deltaText == "" {
		return nil
	}

	response := &schemas.BifrostTranscriptionStreamResponse{
		Type:  schemas.TranscriptionStreamResponseTypeDelta,
		Delta: &deltaText,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.TranscriptionStreamRequest,
			Provider:       providerName,
			ModelRequested: modelRequested,
			ChunkIndex:     chunkIndex,
			Latency:        time.Since(lastChunkTime).Milliseconds(),
		},
	}

	if sendBackRawResponse {
		response.ExtraFields.RawResponse = jsonData
	}

	return response
}

func CreateGeminiStreamEndResponse(
	usage *schemas.BifrostLLMUsage,
	providerName schemas.ModelProvider,
	modelRequested string,
	chunkIndex int,
	startTime time.Time,
	requestType schemas.RequestType,
) *schemas.BifrostChatResponse {
	response := &schemas.BifrostChatResponse{
		Object: "chat.completion.chunk",
		Usage:  usage,
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{},
				},
				FinishReason: schemas.Ptr("stop"),
			},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    requestType,
			Provider:       providerName,
			ModelRequested: modelRequested,
			ChunkIndex:     chunkIndex + 1,
			Latency:        time.Since(startTime).Milliseconds(),
		},
	}

	return response
}

func CreateGeminiSpeechStreamEndResponse(
	usage *schemas.SpeechUsage,
	providerName schemas.ModelProvider,
	modelRequested string,
	chunkIndex int,
	startTime time.Time,
) *schemas.BifrostSpeechStreamResponse {
	response := &schemas.BifrostSpeechStreamResponse{
		Type:  schemas.SpeechStreamResponseTypeDone,
		Usage: usage,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.SpeechStreamRequest,
			Provider:       providerName,
			ModelRequested: modelRequested,
			ChunkIndex:     chunkIndex + 1,
			Latency:        time.Since(startTime).Milliseconds(),
		},
	}

	return response
}

func CreateGeminiTranscriptionStreamEndResponse(
	fullTranscriptionText string,
	usage *schemas.TranscriptionUsage,
	providerName schemas.ModelProvider,
	modelRequested string,
	chunkIndex int,
	startTime time.Time,
) *schemas.BifrostTranscriptionStreamResponse {
	response := &schemas.BifrostTranscriptionStreamResponse{
		Type: schemas.TranscriptionStreamResponseTypeDone,
		Text: fullTranscriptionText,
		Usage: &schemas.TranscriptionUsage{
			Type:         "tokens",
			InputTokens:  usage.InputTokens,
			OutputTokens: usage.OutputTokens,
			TotalTokens:  usage.TotalTokens,
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.TranscriptionStreamRequest,
			Provider:       providerName,
			ModelRequested: modelRequested,
			ChunkIndex:     chunkIndex + 1,
			Latency:        time.Since(startTime).Milliseconds(),
		},
	}

	return response
}

func newBifrostOperationError(message string, err error, provider schemas.ModelProvider) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		Error: &schemas.ErrorField{
			Message: message,
			Error:   err,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider: provider,
		},
	}
}
