package sapaicore

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// sapaicoreAuthorizationTokenKey is the context key for passing a pre-fetched SAP AI Core token.
const sapaicoreAuthorizationTokenKey schemas.BifrostContextKey = "sapaicore-authorization-token"

// defaultCleanupInterval is the interval at which the background goroutine
// prunes expired entries from the token and deployment caches.
const defaultCleanupInterval = 5 * time.Minute

// defaultDeploymentCacheTTL is the default TTL for deployment cache entries
const defaultDeploymentCacheTTL = 1 * time.Hour

// minDeploymentCacheTTL is the minimum allowed TTL for deployment cache entries
const minDeploymentCacheTTL = 1 * time.Minute

// openaiReasoningAndGpt5Models is the list of OpenAI models that require special parameter handling.
// These models don't support max_tokens and temperature parameters when accessed via SAP AI Core.
var openaiReasoningAndGpt5Models = []string{
	"o1",
	"o3-mini",
	"o3",
	"o4-mini",
	"gpt-5",
}

// isOpenaiReasoningOrGpt5Model checks if the model requires special parameter handling.
// These models don't support max_tokens and temperature parameters when accessed via SAP AI Core.
func isOpenaiReasoningOrGpt5Model(model string) bool {
	modelLower := strings.ToLower(model)
	for _, rm := range openaiReasoningAndGpt5Models {
		if strings.Contains(modelLower, rm) {
			return true
		}
	}
	return false
}

// releaseStreamingResponseNoDrain releases a streaming response without draining the body stream.
// Use this for binary EventStream protocols (like AWS EventStream) where:
// 1. The stream has been fully consumed up to io.EOF
// 2. Draining would block because the protocol doesn't send additional data after the final event
// This skips the drain step that can cause the connection to hang on certain streaming protocols.
func releaseStreamingResponseNoDrain(resp *fasthttp.Response, logger schemas.Logger) {
	if bodyStream := resp.BodyStream(); bodyStream != nil {
		if closer, ok := bodyStream.(io.Closer); ok {
			if err := closer.Close(); err != nil {
				logger.Warn("failed to close streaming response body: %v", err)
			}
		}
	}
	fasthttp.ReleaseResponse(resp)
}

// deploymentCacheKey generates a unique key for deployment cache.
// Includes clientID and authURL so that different credential sets sharing the
// same baseURL and resourceGroup are isolated in the cache and singleflight.
// Uses length-prefixed format to avoid collisions when values contain ":"
// The baseURL is normalized before use so that "https://host", "https://host/",
// and "https://host/v2" all map to the same cache entry.
func deploymentCacheKey(clientID, authURL, baseURL, resourceGroup string) string {
	normalizedBase := normalizeBaseURL(baseURL)
	normalizedAuth := normalizeAuthURL(authURL)
	return fmt.Sprintf("%d:%s:%d:%s:%d:%s:%s",
		len(clientID), clientID,
		len(normalizedAuth), normalizedAuth,
		len(normalizedBase), normalizedBase,
		resourceGroup)
}

// determineBackend determines the backend type based on model name prefix
func determineBackend(modelName string) SAPAICoreBackendType {
	if strings.HasPrefix(modelName, "anthropic--") || strings.HasPrefix(modelName, "amazon--") {
		return SAPAICoreBackendBedrock
	}
	if strings.HasPrefix(modelName, "gemini-") {
		return SAPAICoreBackendVertex
	}
	return SAPAICoreBackendOpenAI
}

// normalizeBaseURL ensures the base URL has the /v2 suffix
func normalizeBaseURL(baseURL string) string {
	trimmed := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(trimmed, "/v2") {
		return trimmed
	}
	return trimmed + "/v2"
}

// buildRequestURL constructs the URL for a SAP AI Core API request
func buildRequestURL(baseURL, deploymentID, path string) string {
	normalizedURL := normalizeBaseURL(baseURL)
	return fmt.Sprintf("%s/inference/deployments/%s%s", normalizedURL, deploymentID, path)
}

// processVertexSSEStream processes Vertex SSE stream and sends chunks to the channel
func processVertexSSEStream(
	ctx *schemas.BifrostContext,
	bodyStream io.Reader,
	responseChan chan *schemas.BifrostStreamChunk,
	postHookRunner schemas.PostHookRunner,
	providerName schemas.ModelProvider,
	model string,
	logger schemas.Logger,
) {
	sseReader := providerUtils.GetSSEDataReader(ctx, bodyStream)

	chatCmplID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	chunkIndex := -1
	usage := &schemas.BifrostLLMUsage{}
	var finishReason *string
	startTime := time.Now()
	toolCallIndex := 0

	for {
		if ctx.Err() != nil {
			return
		}

		jsonData, readErr := sseReader.ReadDataLine()
		if readErr != nil {
			if readErr != io.EOF {
				logger.Warn("vertex SSE reader error: %v", readErr)
			}
			break
		}

		var vertexResp VertexGenerateContentResponse
		if err := sonic.Unmarshal(jsonData, &vertexResp); err != nil {
			logger.Warn("failed to parse Vertex stream event: %v", err)
			continue
		}

		// Convert to Bifrost response
		if len(vertexResp.Candidates) > 0 && len(vertexResp.Candidates[0].Content.Parts) > 0 {
			chunkIndex++

			for _, part := range vertexResp.Candidates[0].Content.Parts {
				// Handle text content
				if part.Text != "" {
					text := part.Text
					response := &schemas.BifrostChatResponse{
						ID:      chatCmplID,
						Object:  "chat.completion.chunk",
						Created: int(time.Now().Unix()),
						Model:   model,
						Choices: []schemas.BifrostResponseChoice{
							{
								Index: 0,
								ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
									Delta: &schemas.ChatStreamResponseChoiceDelta{
										Content: &text,
									},
								},
							},
						},
					}

					response.ExtraFields.Provider = providerName
					response.ExtraFields.ModelRequested = model
					response.ExtraFields.RequestType = schemas.ChatCompletionStreamRequest
					response.ExtraFields.ChunkIndex = chunkIndex

					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan)
				}

				// Handle function calls
				if part.FunctionCall != nil {
					// Serialize args to JSON string
					argsJSON := "{}"
					if part.FunctionCall.Args != nil {
						if argsBytes, err := sonic.Marshal(part.FunctionCall.Args); err == nil {
							argsJSON = string(argsBytes)
						}
					}

					// Generate a tool call ID
					toolCallID := fmt.Sprintf("call_%s_%d", model, toolCallIndex)
					toolCallType := "function"
					funcName := part.FunctionCall.Name
					idx := uint16(toolCallIndex)

					response := &schemas.BifrostChatResponse{
						ID:      chatCmplID,
						Object:  "chat.completion.chunk",
						Created: int(time.Now().Unix()),
						Model:   model,
						Choices: []schemas.BifrostResponseChoice{
							{
								Index: 0,
								ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
									Delta: &schemas.ChatStreamResponseChoiceDelta{
										ToolCalls: []schemas.ChatAssistantMessageToolCall{
											{
												Index: idx,
												Type:  &toolCallType,
												ID:    &toolCallID,
												Function: schemas.ChatAssistantMessageToolCallFunction{
													Name:      &funcName,
													Arguments: argsJSON,
												},
											},
										},
									},
								},
							},
						},
					}

					response.ExtraFields.Provider = providerName
					response.ExtraFields.ModelRequested = model
					response.ExtraFields.RequestType = schemas.ChatCompletionStreamRequest
					response.ExtraFields.ChunkIndex = chunkIndex

					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan)

					toolCallIndex++
				}
			}
		}

		// Handle finish reason — outside the Content.Parts check so that
		// metadata-only final events (no parts) still capture the terminal reason.
		if len(vertexResp.Candidates) > 0 && vertexResp.Candidates[0].FinishReason != "" {
			fr := vertexResp.Candidates[0].FinishReason
			// If there were tool calls, override finish reason
			if toolCallIndex > 0 {
				fr = "tool_calls"
			} else {
				fr = mapVertexFinishReason(fr)
			}
			finishReason = &fr
		}

		// Handle usage metadata
		if vertexResp.UsageMetadata != nil {
			usage.PromptTokens = vertexResp.UsageMetadata.PromptTokenCount
			usage.CompletionTokens = vertexResp.UsageMetadata.CandidatesTokenCount
			usage.TotalTokens = vertexResp.UsageMetadata.TotalTokenCount
		}
	}

	// Send final chunk with usage
	if finishReason != nil || usage.TotalTokens > 0 {
		finalResponse := providerUtils.CreateBifrostChatCompletionChunkResponse(chatCmplID, usage, finishReason, chunkIndex, schemas.ChatCompletionStreamRequest, providerName, model)
		finalResponse.ExtraFields.Latency = time.Since(startTime).Milliseconds()
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, finalResponse, nil, nil, nil, nil), responseChan)
	}
}
