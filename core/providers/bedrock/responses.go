package bedrock

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// BedrockResponsesStreamState tracks state during streaming conversion for responses API
type BedrockResponsesStreamState struct {
	ContentIndexToOutputIndex map[int]int    // Maps Bedrock contentBlockIndex to OpenAI output_index
	ToolArgumentBuffers       map[int]string // Maps output_index to accumulated tool argument JSON
	ItemIDs                   map[int]string // Maps output_index to item ID for stable IDs
	ToolCallIDs               map[int]string // Maps output_index to tool call ID (callID)
	ToolCallNames             map[int]string // Maps output_index to tool call name
	CurrentOutputIndex        int            // Current output index counter
	MessageID                 *string        // Message ID (generated)
	Model                     *string        // Model name
	CreatedAt                 int            // Timestamp for created_at consistency
	HasEmittedCreated         bool           // Whether we've emitted response.created
	HasEmittedInProgress      bool           // Whether we've emitted response.in_progress
	TextItemClosed            bool           // Whether text item has been closed
}

// bedrockResponsesStreamStatePool provides a pool for Bedrock responses stream state objects.
var bedrockResponsesStreamStatePool = sync.Pool{
	New: func() interface{} {
		return &BedrockResponsesStreamState{
			ContentIndexToOutputIndex: make(map[int]int),
			ToolArgumentBuffers:       make(map[int]string),
			ItemIDs:                   make(map[int]string),
			ToolCallIDs:               make(map[int]string),
			ToolCallNames:             make(map[int]string),
			CurrentOutputIndex:        0,
			CreatedAt:                 int(time.Now().Unix()),
			HasEmittedCreated:         false,
			HasEmittedInProgress:      false,
			TextItemClosed:            false,
		}
	},
}

// acquireBedrockResponsesStreamState gets a Bedrock responses stream state from the pool.
func acquireBedrockResponsesStreamState() *BedrockResponsesStreamState {
	state := bedrockResponsesStreamStatePool.Get().(*BedrockResponsesStreamState)
	// Clear maps (they're already initialized from New or previous flush)
	// Only initialize if nil (shouldn't happen, but defensive)
	if state.ContentIndexToOutputIndex == nil {
		state.ContentIndexToOutputIndex = make(map[int]int)
	} else {
		clear(state.ContentIndexToOutputIndex)
	}
	if state.ToolArgumentBuffers == nil {
		state.ToolArgumentBuffers = make(map[int]string)
	} else {
		clear(state.ToolArgumentBuffers)
	}
	if state.ItemIDs == nil {
		state.ItemIDs = make(map[int]string)
	} else {
		clear(state.ItemIDs)
	}
	if state.ToolCallIDs == nil {
		state.ToolCallIDs = make(map[int]string)
	} else {
		clear(state.ToolCallIDs)
	}
	if state.ToolCallNames == nil {
		state.ToolCallNames = make(map[int]string)
	} else {
		clear(state.ToolCallNames)
	}
	// Reset other fields
	state.CurrentOutputIndex = 0
	state.MessageID = nil
	state.Model = nil
	state.CreatedAt = int(time.Now().Unix())
	state.HasEmittedCreated = false
	state.HasEmittedInProgress = false
	state.TextItemClosed = false
	return state
}

// releaseBedrockResponsesStreamState returns a Bedrock responses stream state to the pool.
func releaseBedrockResponsesStreamState(state *BedrockResponsesStreamState) {
	if state != nil {
		state.flush() // Clean before returning to pool
		bedrockResponsesStreamStatePool.Put(state)
	}
}

func (state *BedrockResponsesStreamState) flush() {
	// Clear maps (reuse if already initialized, otherwise initialize)
	if state.ContentIndexToOutputIndex == nil {
		state.ContentIndexToOutputIndex = make(map[int]int)
	} else {
		clear(state.ContentIndexToOutputIndex)
	}
	if state.ToolArgumentBuffers == nil {
		state.ToolArgumentBuffers = make(map[int]string)
	} else {
		clear(state.ToolArgumentBuffers)
	}
	if state.ItemIDs == nil {
		state.ItemIDs = make(map[int]string)
	} else {
		clear(state.ItemIDs)
	}
	if state.ToolCallIDs == nil {
		state.ToolCallIDs = make(map[int]string)
	} else {
		clear(state.ToolCallIDs)
	}
	if state.ToolCallNames == nil {
		state.ToolCallNames = make(map[int]string)
	} else {
		clear(state.ToolCallNames)
	}
	state.CurrentOutputIndex = 0
	state.MessageID = nil
	state.Model = nil
	state.CreatedAt = int(time.Now().Unix())
	state.HasEmittedCreated = false
	state.HasEmittedInProgress = false
	state.TextItemClosed = false
}

// ToBifrostResponsesRequest converts a BedrockConverseRequest to Bifrost Responses Request format
func (request *BedrockConverseRequest) ToBifrostResponsesRequest() (*schemas.BifrostResponsesRequest, error) {
	if request == nil {
		return nil, fmt.Errorf("bedrock request is nil")
	}

	// Extract provider from model ID (format: "bedrock/model-name")
	provider, model := schemas.ParseModelString(request.ModelID, schemas.Bedrock)

	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    model,
		Params:   &schemas.ResponsesParameters{},
	}

	// Convert system messages first (they should appear at the top)
	if len(request.System) > 0 {
		for _, sysMsg := range request.System {
			if sysMsg.Text != nil {
				systemRole := schemas.ResponsesInputMessageRoleSystem
				bifrostMsg := schemas.ResponsesMessage{
					Role: &systemRole,
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesInputMessageContentBlockTypeText,
								Text: sysMsg.Text,
							},
						},
					},
				}
				bifrostReq.Input = append(bifrostReq.Input, bifrostMsg)
			}
		}
	}

	// Convert regular messages
	for _, msg := range request.Messages {
		var role schemas.ResponsesMessageRoleType
		if msg.Role == BedrockMessageRoleUser {
			role = schemas.ResponsesInputMessageRoleUser
		} else if msg.Role == BedrockMessageRoleAssistant {
			role = schemas.ResponsesInputMessageRoleAssistant
		}

		bifrostMsg := schemas.ResponsesMessage{
			Role: &role,
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{},
			},
		}

		// Convert content blocks
		for _, content := range msg.Content {
			if content.Text != nil {
				bifrostMsg.Content.ContentBlocks = append(bifrostMsg.Content.ContentBlocks, schemas.ResponsesMessageContentBlock{
					Type: schemas.ResponsesInputMessageContentBlockTypeText,
					Text: content.Text,
				})
			}
			if content.Image != nil {
				imageBlock := schemas.ResponsesMessageContentBlock{
					Type: schemas.ResponsesInputMessageContentBlockTypeImage,
				}
				if content.Image != nil && content.Image.Source.Bytes != nil {
					imageBlock.ResponsesInputMessageContentBlockImage = &schemas.ResponsesInputMessageContentBlockImage{
						ImageURL: content.Image.Source.Bytes,
					}
				}
				bifrostMsg.Content.ContentBlocks = append(bifrostMsg.Content.ContentBlocks, imageBlock)
			}
			if content.ToolUse != nil {
				callType := schemas.ResponsesMessageTypeFunctionCall
				callStatus := "in_progress"
				bifrostMsg.Type = &callType
				bifrostMsg.Status = &callStatus
				bifrostMsg.ResponsesToolMessage = &schemas.ResponsesToolMessage{
					CallID:    &content.ToolUse.ToolUseID,
					Name:      &content.ToolUse.Name,
					Arguments: schemas.Ptr(schemas.JsonifyInput(content.ToolUse.Input)),
				}
			}
			if content.ToolResult != nil {
				resultType := schemas.ResponsesMessageTypeFunctionCallOutput
				resultStatus := "completed"
				bifrostMsg.Type = &resultType
				bifrostMsg.Status = &resultStatus
				var toolResultContent []schemas.ResponsesMessageContentBlock
				for _, resultContent := range content.ToolResult.Content {
					if resultContent.Text != nil {
						toolResultContent = append(toolResultContent, schemas.ResponsesMessageContentBlock{
							Type: schemas.ResponsesInputMessageContentBlockTypeText,
							Text: resultContent.Text,
						})
					}
				}
				bifrostMsg.ResponsesToolMessage = &schemas.ResponsesToolMessage{
					CallID: &content.ToolResult.ToolUseID,
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesFunctionToolCallOutputBlocks: toolResultContent,
					},
				}
			}
		}

		bifrostReq.Input = append(bifrostReq.Input, bifrostMsg)
	}

	// Convert inference config to parameters
	if request.InferenceConfig != nil {
		if request.InferenceConfig.MaxTokens != nil {
			bifrostReq.Params.MaxOutputTokens = request.InferenceConfig.MaxTokens
		}
		if request.InferenceConfig.Temperature != nil {
			bifrostReq.Params.Temperature = request.InferenceConfig.Temperature
		}
		if request.InferenceConfig.TopP != nil {
			bifrostReq.Params.TopP = request.InferenceConfig.TopP
		}
		if len(request.InferenceConfig.StopSequences) > 0 {
			if bifrostReq.Params.ExtraParams == nil {
				bifrostReq.Params.ExtraParams = make(map[string]interface{})
			}
			bifrostReq.Params.ExtraParams["stop"] = request.InferenceConfig.StopSequences
		}
	}

	// Convert tool config
	if request.ToolConfig != nil && len(request.ToolConfig.Tools) > 0 {
		for _, tool := range request.ToolConfig.Tools {
			if tool.ToolSpec != nil {
				bifrostTool := schemas.ResponsesTool{
					Type:                  schemas.ResponsesToolTypeFunction,
					Name:                  &tool.ToolSpec.Name,
					Description:           tool.ToolSpec.Description,
					ResponsesToolFunction: &schemas.ResponsesToolFunction{},
				}

				// Handle different types for InputSchema.JSON
				if params, ok := tool.ToolSpec.InputSchema.JSON.(*schemas.ToolFunctionParameters); ok {
					bifrostTool.ResponsesToolFunction.Parameters = params
				} else if paramsMap, ok := tool.ToolSpec.InputSchema.JSON.(map[string]interface{}); ok {
					// Convert map to ToolFunctionParameters
					params := &schemas.ToolFunctionParameters{}
					if typeVal, ok := paramsMap["type"].(string); ok {
						params.Type = typeVal
					}
					// Handle both pointer and non-pointer properties
					if propsPtr, ok := paramsMap["properties"].(*map[string]interface{}); ok {
						params.Properties = propsPtr
					} else if props, ok := paramsMap["properties"].(map[string]interface{}); ok {
						params.Properties = &props
					}
					if required, ok := paramsMap["required"].([]interface{}); ok {
						reqStrings := make([]string, 0, len(required))
						for _, r := range required {
							if rStr, ok := r.(string); ok {
								reqStrings = append(reqStrings, rStr)
							}
						}
						params.Required = reqStrings
					} else if required, ok := paramsMap["required"].([]string); ok {
						params.Required = required
					}
					bifrostTool.ResponsesToolFunction.Parameters = params
				}

				bifrostReq.Params.Tools = append(bifrostReq.Params.Tools, bifrostTool)
			}
		}
	}

	// Convert guardrail config to extra params
	if request.GuardrailConfig != nil {
		if bifrostReq.Params.ExtraParams == nil {
			bifrostReq.Params.ExtraParams = make(map[string]interface{})
		}

		guardrailMap := map[string]interface{}{
			"guardrailIdentifier": request.GuardrailConfig.GuardrailIdentifier,
			"guardrailVersion":    request.GuardrailConfig.GuardrailVersion,
		}
		if request.GuardrailConfig.Trace != nil {
			guardrailMap["trace"] = *request.GuardrailConfig.Trace
		}
		bifrostReq.Params.ExtraParams["guardrailConfig"] = guardrailMap
	}

	// Convert additional model request fields to extra params
	if len(request.AdditionalModelRequestFields) > 0 {
		if bifrostReq.Params.ExtraParams == nil {
			bifrostReq.Params.ExtraParams = make(map[string]interface{})
		}
		bifrostReq.Params.ExtraParams["additionalModelRequestFieldPaths"] = request.AdditionalModelRequestFields
	}

	// Convert additional model response field paths to extra params
	if len(request.AdditionalModelResponseFieldPaths) > 0 {
		if bifrostReq.Params.ExtraParams == nil {
			bifrostReq.Params.ExtraParams = make(map[string]interface{})
		}
		bifrostReq.Params.ExtraParams["additionalModelResponseFieldPaths"] = request.AdditionalModelResponseFieldPaths
	}

	// Convert performance config to extra params
	if request.PerformanceConfig != nil {
		if bifrostReq.Params.ExtraParams == nil {
			bifrostReq.Params.ExtraParams = make(map[string]interface{})
		}

		perfConfigMap := map[string]interface{}{}
		if request.PerformanceConfig.Latency != nil {
			perfConfigMap["latency"] = *request.PerformanceConfig.Latency
		}
		if len(perfConfigMap) > 0 {
			bifrostReq.Params.ExtraParams["performanceConfig"] = perfConfigMap
		}
	}

	// Convert prompt variables to extra params
	if len(request.PromptVariables) > 0 {
		if bifrostReq.Params.ExtraParams == nil {
			bifrostReq.Params.ExtraParams = make(map[string]interface{})
		}

		promptVarsMap := make(map[string]interface{})
		for key, value := range request.PromptVariables {
			varMap := map[string]interface{}{}
			if value.Text != nil {
				varMap["text"] = *value.Text
			}
			if len(varMap) > 0 {
				promptVarsMap[key] = varMap
			}
		}
		if len(promptVarsMap) > 0 {
			bifrostReq.Params.ExtraParams["promptVariables"] = promptVarsMap
		}
	}

	// Convert request metadata to extra params
	if len(request.RequestMetadata) > 0 {
		if bifrostReq.Params.ExtraParams == nil {
			bifrostReq.Params.ExtraParams = make(map[string]interface{})
		}
		bifrostReq.Params.ExtraParams["requestMetadata"] = request.RequestMetadata
	}

	return bifrostReq, nil
}

// ToBedrockResponsesRequest converts a BifrostRequest (Responses structure) back to BedrockConverseRequest
func ToBedrockResponsesRequest(bifrostReq *schemas.BifrostResponsesRequest) (*BedrockConverseRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost request is nil")
	}

	bedrockReq := &BedrockConverseRequest{
		ModelID: bifrostReq.Model,
	}

	// map bifrost messages to bedrock messages
	if bifrostReq.Input != nil {
		messages, systemMessages, err := convertResponsesItemsToBedrockMessages(bifrostReq.Input)
		if err != nil {
			return nil, fmt.Errorf("failed to convert Responses messages: %w", err)
		}
		bedrockReq.Messages = messages
		if len(systemMessages) > 0 {
			bedrockReq.System = systemMessages
		}
	}

	// Map basic parameters to inference config
	if bifrostReq.Params != nil {
		inferenceConfig := &BedrockInferenceConfig{}

		if bifrostReq.Params.MaxOutputTokens != nil {
			inferenceConfig.MaxTokens = bifrostReq.Params.MaxOutputTokens
		}
		if bifrostReq.Params.Temperature != nil {
			inferenceConfig.Temperature = bifrostReq.Params.Temperature
		}
		if bifrostReq.Params.TopP != nil {
			inferenceConfig.TopP = bifrostReq.Params.TopP
		}
		if bifrostReq.Params.ExtraParams != nil {
			if stop, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["stop"]); ok {
				inferenceConfig.StopSequences = stop
			}
		}

		bedrockReq.InferenceConfig = inferenceConfig
	}

	// Convert tools
	if bifrostReq.Params != nil && bifrostReq.Params.Tools != nil {
		var bedrockTools []BedrockTool
		for _, tool := range bifrostReq.Params.Tools {
			if tool.ResponsesToolFunction != nil {
				// Create the complete schema object that Bedrock expects
				var schemaObject interface{}
				if tool.ResponsesToolFunction.Parameters != nil {
					schemaObject = tool.ResponsesToolFunction.Parameters
				} else {
					// Fallback to empty object schema if no parameters
					schemaObject = map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{},
					}
				}

				if tool.Name == nil || *tool.Name == "" {
					return nil, fmt.Errorf("responses tool is missing required name for Bedrock function conversion")
				}
				name := *tool.Name

				// Use the tool description if available, otherwise use a generic description
				description := "Function tool"
				if tool.Description != nil {
					description = *tool.Description
				}

				bedrockTool := BedrockTool{
					ToolSpec: &BedrockToolSpec{
						Name:        name,
						Description: &description,
						InputSchema: BedrockToolInputSchema{
							JSON: schemaObject,
						},
					},
				}
				bedrockTools = append(bedrockTools, bedrockTool)
			}
		}

		if len(bedrockTools) > 0 {
			bedrockReq.ToolConfig = &BedrockToolConfig{
				Tools: bedrockTools,
			}
		}
	}

	// Convert tool choice
	if bifrostReq.Params != nil && bifrostReq.Params.ToolChoice != nil {
		bedrockToolChoice := convertResponsesToolChoice(*bifrostReq.Params.ToolChoice)
		if bedrockToolChoice != nil {
			if bedrockReq.ToolConfig == nil {
				bedrockReq.ToolConfig = &BedrockToolConfig{}
			}
			bedrockReq.ToolConfig.ToolChoice = bedrockToolChoice
		}
	}

	// Ensure tool config is present when tool content exists (similar to Chat Completions)
	ensureResponsesToolConfigForConversation(bifrostReq, bedrockReq)

	return bedrockReq, nil
}

// ensureResponsesToolConfigForConversation ensures toolConfig is present when tool content exists
func ensureResponsesToolConfigForConversation(bifrostReq *schemas.BifrostResponsesRequest, bedrockReq *BedrockConverseRequest) {
	if bedrockReq.ToolConfig != nil {
		return // Already has tool config
	}

	hasToolContent, tools := extractToolsFromResponsesConversationHistory(bifrostReq.Input)
	if hasToolContent && len(tools) > 0 {
		bedrockReq.ToolConfig = &BedrockToolConfig{Tools: tools}
	}
}

// extractToolsFromResponsesConversationHistory extracts tools from Responses conversation history
func extractToolsFromResponsesConversationHistory(messages []schemas.ResponsesMessage) (bool, []BedrockTool) {
	var hasToolContent bool
	toolMap := make(map[string]*schemas.ResponsesTool) // Use map to deduplicate by name

	for _, msg := range messages {
		// Check if message contains tool use or tool result
		if msg.Type != nil {
			switch *msg.Type {
			case schemas.ResponsesMessageTypeFunctionCall, schemas.ResponsesMessageTypeFunctionCallOutput:
				hasToolContent = true
				// Try to infer tool definition from tool call/result
				if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.Name != nil {
					toolName := *msg.ResponsesToolMessage.Name
					if _, exists := toolMap[toolName]; !exists {
						// Create a minimal tool definition
						toolMap[toolName] = &schemas.ResponsesTool{
							Type: "function",
							Name: &toolName,
							ResponsesToolFunction: &schemas.ResponsesToolFunction{
								Parameters: &schemas.ToolFunctionParameters{
									Type:       "object",
									Properties: &map[string]interface{}{},
								},
							},
						}
					}
				}
			}
		}
	}

	// Convert map to slice
	var tools []BedrockTool
	for _, tool := range toolMap {
		if tool.Name != nil && tool.ResponsesToolFunction != nil {
			schemaObject := tool.ResponsesToolFunction.Parameters
			if schemaObject == nil {
				schemaObject = &schemas.ToolFunctionParameters{
					Type:       "object",
					Properties: &map[string]interface{}{},
				}
			}

			description := "Function tool"
			if tool.Description != nil {
				description = *tool.Description
			}

			bedrockTool := BedrockTool{
				ToolSpec: &BedrockToolSpec{
					Name:        *tool.Name,
					Description: &description,
					InputSchema: BedrockToolInputSchema{
						JSON: schemaObject,
					},
				},
			}
			tools = append(tools, bedrockTool)
		}
	}

	return hasToolContent, tools
}

// ToBifrostResponsesResponse converts BedrockConverseResponse to BifrostResponsesResponse
func (response *BedrockConverseResponse) ToBifrostResponsesResponse() (*schemas.BifrostResponsesResponse, error) {
	if response == nil {
		return nil, fmt.Errorf("bedrock response is nil")
	}

	bifrostResp := &schemas.BifrostResponsesResponse{
		CreatedAt: int(time.Now().Unix()),
	}

	if response.Usage != nil {
		// Convert usage information
		bifrostResp.Usage = &schemas.ResponsesResponseUsage{
			InputTokens:  response.Usage.InputTokens,
			OutputTokens: response.Usage.OutputTokens,
			TotalTokens:  response.Usage.TotalTokens,
		}
		// Handle cached tokens if present
		if response.Usage.CacheReadInputTokens > 0 {
			bifrostResp.Usage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{
				CachedTokens: response.Usage.CacheReadInputTokens,
			}
		}
		if response.Usage.CacheWriteInputTokens > 0 {
			bifrostResp.Usage.OutputTokensDetails = &schemas.ResponsesResponseOutputTokens{
				CachedTokens: response.Usage.CacheWriteInputTokens,
			}
		}
	}

	// Convert output message to Responses format
	if response.Output != nil && response.Output.Message != nil {
		outputMessages := convertBedrockMessageToResponsesMessages(*response.Output.Message)
		bifrostResp.Output = outputMessages
	}

	return bifrostResp, nil
}

// Helper functions

func convertResponsesToolChoice(toolChoice schemas.ResponsesToolChoice) *BedrockToolChoice {
	// Check if it's a string choice
	if toolChoice.ResponsesToolChoiceStr != nil {
		switch schemas.ResponsesToolChoiceType(*toolChoice.ResponsesToolChoiceStr) {
		case schemas.ResponsesToolChoiceTypeAny, schemas.ResponsesToolChoiceTypeRequired:
			return &BedrockToolChoice{
				Any: &BedrockToolChoiceAny{},
			}
		case schemas.ResponsesToolChoiceTypeNone:
			// Bedrock doesn't have explicit "none" - just don't include tools
			return nil
		}
	}

	// Check if it's a struct choice
	if toolChoice.ResponsesToolChoiceStruct != nil {
		switch toolChoice.ResponsesToolChoiceStruct.Type {
		case schemas.ResponsesToolChoiceTypeFunction:
			// Extract the actual function name from the struct
			if toolChoice.ResponsesToolChoiceStruct.Name != nil && *toolChoice.ResponsesToolChoiceStruct.Name != "" {
				return &BedrockToolChoice{
					Tool: &BedrockToolChoiceTool{
						Name: *toolChoice.ResponsesToolChoiceStruct.Name,
					},
				}
			}
			// If Name is nil or empty, return nil as we can't construct a valid tool choice
			return nil
		case schemas.ResponsesToolChoiceTypeAuto, schemas.ResponsesToolChoiceTypeAny, schemas.ResponsesToolChoiceTypeRequired:
			return &BedrockToolChoice{
				Any: &BedrockToolChoiceAny{},
			}
		case schemas.ResponsesToolChoiceTypeNone:
			return nil
		}
	}

	return nil
}

// convertResponsesItemsToBedrockMessages converts Responses items back to Bedrock messages
func convertResponsesItemsToBedrockMessages(messages []schemas.ResponsesMessage) ([]BedrockMessage, []BedrockSystemMessage, error) {
	var bedrockMessages []BedrockMessage
	var systemMessages []BedrockSystemMessage

	for _, msg := range messages {
		// Handle Responses items
		msgType := schemas.ResponsesMessageTypeMessage
		if msg.Type != nil {
			msgType = *msg.Type
		}
		switch msgType {
		case schemas.ResponsesMessageTypeMessage:
			// Check if Role is present, skip message if not
			if msg.Role == nil {
				continue
			}

			// Extract role from the Responses message structure
			role := *msg.Role

			if role == schemas.ResponsesInputMessageRoleSystem {
				// Convert to system message
				// Ensure Content and ContentStr are present
				if msg.Content != nil {
					if msg.Content.ContentStr != nil {
						systemMessages = append(systemMessages, BedrockSystemMessage{
							Text: msg.Content.ContentStr,
						})
					} else if msg.Content.ContentBlocks != nil {
						for _, block := range msg.Content.ContentBlocks {
							if block.Text != nil {
								systemMessages = append(systemMessages, BedrockSystemMessage{
									Text: block.Text,
								})
							}
						}
					}
				}
				// Skip system messages with no content
			} else {
				// Convert regular message
				// Ensure Content is present
				if msg.Content == nil {
					// Skip messages without content or create with empty content
					continue
				}

				bedrockMsg := BedrockMessage{
					Role: BedrockMessageRole(role),
				}

				// Convert content
				contentBlocks, err := convertBifrostResponsesMessageContentBlocksToBedrockContentBlocks(*msg.Content)
				if err != nil {
					return nil, nil, fmt.Errorf("failed to convert content blocks: %w", err)
				}
				bedrockMsg.Content = contentBlocks

				bedrockMessages = append(bedrockMessages, bedrockMsg)
			}

		case schemas.ResponsesMessageTypeFunctionCall:
			// Handle function calls from Responses
			if msg.ResponsesToolMessage != nil {
				// Create tool use content block
				var toolUseID string
				if msg.ResponsesToolMessage.CallID != nil {
					toolUseID = *msg.ResponsesToolMessage.CallID
				}

				// Get function name from ToolMessage
				var functionName string
				if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.Name != nil {
					functionName = *msg.ResponsesToolMessage.Name
				}

				// Parse JSON arguments into interface{}
				var input interface{} = map[string]interface{}{}
				if msg.ResponsesToolMessage.Arguments != nil {
					var parsedInput interface{}
					if err := json.Unmarshal([]byte(*msg.ResponsesToolMessage.Arguments), &parsedInput); err != nil {
						return nil, nil, fmt.Errorf("failed to parse tool arguments JSON: %w", err)
					}
					input = parsedInput
				}

				toolUseBlock := BedrockContentBlock{
					ToolUse: &BedrockToolUse{
						ToolUseID: toolUseID,
						Name:      functionName,
						Input:     input,
					},
				}

				// Create assistant message with tool use
				assistantMsg := BedrockMessage{
					Role:    BedrockMessageRoleAssistant,
					Content: []BedrockContentBlock{toolUseBlock},
				}
				bedrockMessages = append(bedrockMessages, assistantMsg)

			}

		case schemas.ResponsesMessageTypeFunctionCallOutput:
			// Handle function call outputs from Responses
			if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.Output != nil &&
				(msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil ||
					msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks != nil) {
				var toolUseID string
				if msg.ResponsesToolMessage.CallID != nil {
					toolUseID = *msg.ResponsesToolMessage.CallID
				}
				toolResultBlock := BedrockContentBlock{
					ToolResult: &BedrockToolResult{
						ToolUseID: toolUseID,
						Status:    schemas.Ptr("success"), // Default to success
					},
				}
				// Set content based on available data
				if msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
					raw := *msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
					var parsed interface{}
					if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
						toolResultBlock.ToolResult.Content = []BedrockContentBlock{
							{JSON: parsed},
						}
					} else {
						toolResultBlock.ToolResult.Content = []BedrockContentBlock{
							{Text: &raw},
						}
					}
				} else if msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks != nil {
					toolResultContent, err := convertBifrostResponsesMessageContentBlocksToBedrockContentBlocks(schemas.ResponsesMessageContent{
						ContentBlocks: msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks,
					})
					if err != nil {
						return nil, nil, fmt.Errorf("failed to convert tool result content blocks: %w", err)
					}
					toolResultBlock.ToolResult.Content = toolResultContent
				}

				// Create user message with tool result
				userMsg := BedrockMessage{
					Role:    BedrockMessageRoleUser,
					Content: []BedrockContentBlock{toolResultBlock},
				}
				bedrockMessages = append(bedrockMessages, userMsg)
			}
		}
	}

	return bedrockMessages, systemMessages, nil
}

// convertBifrostResponsesMessageContentBlocksToBedrockContentBlocks converts Bifrost content to Bedrock content blocks
func convertBifrostResponsesMessageContentBlocksToBedrockContentBlocks(content schemas.ResponsesMessageContent) ([]BedrockContentBlock, error) {
	var blocks []BedrockContentBlock

	if content.ContentStr != nil {
		blocks = append(blocks, BedrockContentBlock{
			Text: content.ContentStr,
		})
	} else if content.ContentBlocks != nil {
		for _, block := range content.ContentBlocks {

			bedrockBlock := BedrockContentBlock{}

			switch block.Type {
			case schemas.ResponsesInputMessageContentBlockTypeText, schemas.ResponsesOutputMessageContentTypeText:
				bedrockBlock.Text = block.Text
			case schemas.ResponsesInputMessageContentBlockTypeImage:
				if block.ResponsesInputMessageContentBlockImage != nil && block.ResponsesInputMessageContentBlockImage.ImageURL != nil {
					imageSource, err := convertImageToBedrockSource(*block.ResponsesInputMessageContentBlockImage.ImageURL)
					if err != nil {
						return nil, fmt.Errorf("failed to convert image in responses content block: %w", err)
					}
					bedrockBlock.Image = imageSource
				}
			default:
				// Don't add anything
			}

			blocks = append(blocks, bedrockBlock)
		}
	}

	return blocks, nil
}

// convertBedrockMessageToResponsesMessages converts Bedrock message to ChatMessage output format
func convertBedrockMessageToResponsesMessages(bedrockMsg BedrockMessage) []schemas.ResponsesMessage {
	var outputMessages []schemas.ResponsesMessage

	for _, block := range bedrockMsg.Content {
		if block.Text != nil {
			// Text content
			outputMessages = append(outputMessages, schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeText,
							Text: block.Text,
						},
					},
				},
			})
		} else if block.ToolUse != nil {
			// Tool use content
			// Create copies of the values to avoid range loop variable capture
			toolUseID := block.ToolUse.ToolUseID
			toolUseName := block.ToolUse.Name

			toolMsg := schemas.ResponsesMessage{
				Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    &toolUseID,
					Name:      &toolUseName,
					Arguments: schemas.Ptr(schemas.JsonifyInput(block.ToolUse.Input)),
				},
			}
			outputMessages = append(outputMessages, toolMsg)
		} else if block.ToolResult != nil {
			// Tool result content - typically not in assistant output but handled for completeness
			// Prefer JSON payloads without unmarshalling; fallback to text
			var resultContent string
			if len(block.ToolResult.Content) > 0 {
				// JSON first (no unmarshal; just one marshal to string when present)
				for _, c := range block.ToolResult.Content {
					if c.JSON != nil {
						resultContent = schemas.JsonifyInput(c.JSON)
						break
					}
				}
				// Fallback to first available text block
				if resultContent == "" {
					for _, c := range block.ToolResult.Content {
						if c.Text != nil {
							resultContent = *c.Text
							break
						}
					}
				}
			}

			// Create a copy of the value to avoid range loop variable capture
			toolResultID := block.ToolResult.ToolUseID

			resultMsg := schemas.ResponsesMessage{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeText,
							Text: &resultContent,
						},
					},
				},
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: &toolResultID,
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesToolCallOutputStr: &resultContent,
					},
				},
			}
			outputMessages = append(outputMessages, resultMsg)
		}
	}

	return outputMessages
}

// ToBedrockConverseResponse converts Bifrost Responses response to Bedrock Converse response
func ToBedrockConverseResponse(bifrostResp *schemas.BifrostResponsesResponse) (*BedrockConverseResponse, error) {
	if bifrostResp == nil {
		return nil, fmt.Errorf("bifrost response is nil")
	}

	bedrockResp := &BedrockConverseResponse{
		Output:  &BedrockConverseOutput{},
		Usage:   &BedrockTokenUsage{},
		Metrics: &BedrockConverseMetrics{},
	}

	var hasToolUse bool
	message := &BedrockMessage{
		Role:    BedrockMessageRoleAssistant,
		Content: []BedrockContentBlock{},
	}

	if len(bifrostResp.Output) > 0 {
		for _, outputMsg := range bifrostResp.Output {
			// Check if this output message contains a tool use
			if outputMsg.Type != nil && *outputMsg.Type == schemas.ResponsesMessageTypeFunctionCall {
				hasToolUse = true
			}
			// Handle content blocks
			if outputMsg.Content != nil && outputMsg.Content.ContentBlocks != nil {
				for _, content := range outputMsg.Content.ContentBlocks {
					switch content.Type {
					case schemas.ResponsesOutputMessageContentTypeText:
						if content.Text != nil {
							message.Content = append(message.Content, BedrockContentBlock{
								Text: content.Text,
							})
						}
					}
				}
			}

			// Handle tool calls and tool results
			if outputMsg.ResponsesToolMessage != nil {
				// Check if this is a tool use (function_call type)
				if outputMsg.Type != nil && *outputMsg.Type == schemas.ResponsesMessageTypeFunctionCall {
					// This is a tool use - ensure we have required fields
					if outputMsg.ResponsesToolMessage.Name != nil && outputMsg.ResponsesToolMessage.CallID != nil {
						var input interface{} = map[string]interface{}{}
						if outputMsg.ResponsesToolMessage.Arguments != nil {
							var parsed interface{}
							if err := json.Unmarshal([]byte(*outputMsg.ResponsesToolMessage.Arguments), &parsed); err == nil {
								input = parsed
							} else {
								// Fallback to raw string if it's not valid JSON
								input = *outputMsg.ResponsesToolMessage.Arguments
							}
						}
						message.Content = append(message.Content, BedrockContentBlock{
							ToolUse: &BedrockToolUse{
								ToolUseID: *outputMsg.ResponsesToolMessage.CallID,
								Name:      *outputMsg.ResponsesToolMessage.Name,
								Input:     input,
							},
						})
					}
				} else if outputMsg.Type != nil && *outputMsg.Type == schemas.ResponsesMessageTypeFunctionCallOutput {
					// This is a tool result - ensure we have required fields
					if outputMsg.ResponsesToolMessage.CallID != nil && outputMsg.ResponsesToolMessage.Output != nil {
						resultBlock := BedrockContentBlock{
							ToolResult: &BedrockToolResult{
								ToolUseID: *outputMsg.ResponsesToolMessage.CallID,
								Status:    schemas.Ptr("success"),
							},
						}
						var resultContent []BedrockContentBlock
						if outputMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
							raw := *outputMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
							var parsed interface{}
							if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
								resultContent = append(resultContent, BedrockContentBlock{
									JSON: parsed,
								})
							} else {
								// Fallback to raw string if it's not valid JSON
								resultContent = append(resultContent, BedrockContentBlock{
									Text: &raw,
								})
							}
						} else if outputMsg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks != nil {
							converted, err := convertBifrostResponsesMessageContentBlocksToBedrockContentBlocks(schemas.ResponsesMessageContent{
								ContentBlocks: outputMsg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks,
							})
							if err == nil {
								resultContent = append(resultContent, converted...)
							} else {
								// Fallback to JSON string if conversion fails
								fallback := schemas.JsonifyInput(outputMsg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks)
								resultContent = append(resultContent, BedrockContentBlock{
									Text: &fallback,
								})
							}
						}
						if len(resultContent) > 0 {
							resultBlock.ToolResult.Content = resultContent
							message.Content = append(message.Content, resultBlock)
						}
					}
				}
			}
		}

		// Also check the final message content for tool use blocks (more robust)
		if !hasToolUse {
			for _, block := range message.Content {
				if block.ToolUse != nil {
					hasToolUse = true
					break
				}
			}
		}
	}

	bedrockResp.Output.Message = message

	// Find stop reason from incomplete details or derive from response
	// Priority: IncompleteDetails > tool_use detection > end_turn
	stopReason := "end_turn"
	if bifrostResp.IncompleteDetails != nil {
		stopReason = bifrostResp.IncompleteDetails.Reason
	} else if hasToolUse {
		stopReason = "tool_use"
	}
	bedrockResp.StopReason = stopReason

	// Convert usage stats
	if bifrostResp.Usage != nil {
		bedrockResp.Usage.InputTokens = bifrostResp.Usage.InputTokens
		bedrockResp.Usage.OutputTokens = bifrostResp.Usage.OutputTokens
		bedrockResp.Usage.TotalTokens = bifrostResp.Usage.TotalTokens
	}

	// Set metrics
	if bifrostResp.ExtraFields.Latency > 0 {
		bedrockResp.Metrics.LatencyMs = bifrostResp.ExtraFields.Latency
	}

	return bedrockResp, nil
}

// ToBifrostResponsesStream converts a Bedrock stream event to a Bifrost Responses Stream response
// Returns a slice of responses to support cases where a single event produces multiple responses
func (chunk *BedrockStreamEvent) ToBifrostResponsesStream(sequenceNumber int, state *BedrockResponsesStreamState) ([]*schemas.BifrostResponsesStreamResponse, *schemas.BifrostError, bool) {
	switch {
	case chunk.Role != nil:
		// Message start - emit response.created and response.in_progress (OpenAI-style lifecycle)
		var responses []*schemas.BifrostResponsesStreamResponse

		// Generate message ID if not already set
		if state.MessageID == nil {
			messageID := fmt.Sprintf("msg_%d", state.CreatedAt)
			state.MessageID = &messageID
		}

		// Emit response.created
		if !state.HasEmittedCreated {
			response := &schemas.BifrostResponsesResponse{
				ID:        state.MessageID,
				CreatedAt: state.CreatedAt,
			}
			if state.Model != nil {
				// Note: Model field doesn't exist in BifrostResponsesResponse schema
			}
			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeCreated,
				SequenceNumber: sequenceNumber,
				Response:       response,
			})
			state.HasEmittedCreated = true
		}

		// Emit response.in_progress
		if !state.HasEmittedInProgress {
			response := &schemas.BifrostResponsesResponse{
				ID:        state.MessageID,
				CreatedAt: state.CreatedAt, // Use same timestamp
			}
			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeInProgress,
				SequenceNumber: sequenceNumber + len(responses),
				Response:       response,
			})
			state.HasEmittedInProgress = true
		}

		// Emit output_item.added for text message
		outputIndex := 0
		state.ContentIndexToOutputIndex[0] = outputIndex // Text is at content index 0

		// Generate stable ID for text item
		var itemID string
		if state.MessageID == nil {
			itemID = fmt.Sprintf("item_%d", outputIndex)
		} else {
			itemID = fmt.Sprintf("msg_%s_item_%d", *state.MessageID, outputIndex)
		}
		state.ItemIDs[outputIndex] = itemID

		messageType := schemas.ResponsesMessageTypeMessage
		role := schemas.ResponsesInputMessageRoleAssistant

		item := &schemas.ResponsesMessage{
			ID:   &itemID,
			Type: &messageType,
			Role: &role,
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{}, // Empty blocks slice for mutation support
			},
		}

		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    schemas.Ptr(outputIndex),
			ContentIndex:   schemas.Ptr(0),
			Item:           item,
		})

		// Emit content_part.added with empty output_text part
		emptyText := ""
		part := &schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesOutputMessageContentTypeText,
			Text: &emptyText,
		}
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeContentPartAdded,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    schemas.Ptr(outputIndex),
			ContentIndex:   schemas.Ptr(0),
			ItemID:         &itemID,
			Part:           part,
		})

		if len(responses) > 0 {
			return responses, nil, false
		}

	case chunk.Start != nil:
		// Handle content block start (text content or tool use)
		contentBlockIndex := 0
		if chunk.ContentBlockIndex != nil {
			contentBlockIndex = *chunk.ContentBlockIndex
		}

		// Check if this is a tool use start
		if chunk.Start.ToolUse != nil {
			// Close text item if it's still open
			var responses []*schemas.BifrostResponsesStreamResponse
			if !state.TextItemClosed {
				outputIndex := 0
				itemID := state.ItemIDs[outputIndex]

				// Emit output_text.done (without accumulated text, just the event)
				emptyText := ""
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputTextDone,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   schemas.Ptr(0),
					ItemID:         &itemID,
					Text:           &emptyText,
				})

				// Emit content_part.done
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   schemas.Ptr(0),
					ItemID:         &itemID,
				})

				// Emit output_item.done
				statusCompleted := "completed"
				doneItem := &schemas.ResponsesMessage{
					Status: &statusCompleted,
				}
				if itemID != "" {
					doneItem.ID = &itemID
				}
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   schemas.Ptr(0),
					Item:           doneItem,
				})
				state.TextItemClosed = true
			}

			// This is a function call starting - use output_index 1
			outputIndex := 1
			state.ContentIndexToOutputIndex[contentBlockIndex] = outputIndex
			state.CurrentOutputIndex = 2 // Next available index

			// Store tool use ID as item ID and call ID
			toolUseID := chunk.Start.ToolUse.ToolUseID
			toolName := chunk.Start.ToolUse.Name
			state.ItemIDs[outputIndex] = toolUseID
			state.ToolCallIDs[outputIndex] = toolUseID
			state.ToolCallNames[outputIndex] = toolName

			statusInProgress := "in_progress"
			item := &schemas.ResponsesMessage{
				ID:     &toolUseID,
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				Status: &statusInProgress,
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    &toolUseID,
					Name:      &toolName,
					Arguments: schemas.Ptr(""), // Arguments will be filled by deltas
				},
			}

			// Initialize argument buffer for this tool call
			state.ToolArgumentBuffers[outputIndex] = ""

			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
				SequenceNumber: sequenceNumber + len(responses),
				OutputIndex:    schemas.Ptr(outputIndex),
				ContentIndex:   schemas.Ptr(contentBlockIndex),
				Item:           item,
			})

			return responses, nil, false
		}
		// Text content start is handled by Role event, so we can ignore Start for text

	case chunk.ContentBlockIndex != nil && chunk.Delta != nil:
		// Handle contentBlockDelta event
		contentBlockIndex := *chunk.ContentBlockIndex
		outputIndex, exists := state.ContentIndexToOutputIndex[contentBlockIndex]
		if !exists {
			// Default to 0 for text if not mapped
			outputIndex = 0
			state.ContentIndexToOutputIndex[contentBlockIndex] = outputIndex
		}

		switch {
		case chunk.Delta.Text != nil:
			// Handle text delta
			text := *chunk.Delta.Text
			if text != "" {
				itemID := state.ItemIDs[outputIndex]
				response := &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   &contentBlockIndex,
					Delta:          &text,
				}
				if itemID != "" {
					response.ItemID = &itemID
				}
				return []*schemas.BifrostResponsesStreamResponse{response}, nil, false
			}

		case chunk.Delta.ToolUse != nil:
			// Handle tool use delta - function call arguments
			toolUseDelta := chunk.Delta.ToolUse

			if toolUseDelta.Input != "" {
				// Accumulate argument deltas
				state.ToolArgumentBuffers[outputIndex] += toolUseDelta.Input

				itemID := state.ItemIDs[outputIndex]
				response := &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   &contentBlockIndex,
					Delta:          &toolUseDelta.Input,
				}
				if itemID != "" {
					response.ItemID = &itemID
				}
				return []*schemas.BifrostResponsesStreamResponse{response}, nil, false
			}
		}

	case chunk.StopReason != nil:
		// Stop reason - don't use it to close items, just return nil
		// Items should be closed explicitly when content blocks end
		return nil, nil, false
	}

	return nil, nil, false
}

// FinalizeBedrockStream finalizes the stream by closing any open items and emitting completed event
func FinalizeBedrockStream(state *BedrockResponsesStreamState, sequenceNumber int, usage *schemas.ResponsesResponseUsage) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	// Close text item if still open
	if !state.TextItemClosed {
		outputIndex := 0
		itemID := state.ItemIDs[outputIndex]

		// Emit output_text.done (without accumulated text, just the event)
		emptyText := ""
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputTextDone,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    schemas.Ptr(outputIndex),
			ContentIndex:   schemas.Ptr(0),
			ItemID:         &itemID,
			Text:           &emptyText,
		})

		// Emit content_part.done
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    schemas.Ptr(outputIndex),
			ContentIndex:   schemas.Ptr(0),
			ItemID:         &itemID,
		})

		// Emit output_item.done
		statusCompleted := "completed"
		doneItem := &schemas.ResponsesMessage{
			Status: &statusCompleted,
		}
		if itemID != "" {
			doneItem.ID = &itemID
		}
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    schemas.Ptr(outputIndex),
			ContentIndex:   schemas.Ptr(0),
			Item:           doneItem,
		})
		state.TextItemClosed = true
	}

	// Close any open tool call items and emit function_call_arguments.done
	for outputIndex, args := range state.ToolArgumentBuffers {
		if args != "" {
			itemID := state.ItemIDs[outputIndex]
			callID := state.ToolCallIDs[outputIndex]
			toolName := state.ToolCallNames[outputIndex]

			// Create item with tool message info for the done event
			var doneItem *schemas.ResponsesMessage
			if callID != "" || toolName != "" {
				doneItem = &schemas.ResponsesMessage{
					ResponsesToolMessage: &schemas.ResponsesToolMessage{},
				}
				if callID != "" {
					doneItem.ResponsesToolMessage.CallID = &callID
				}
				if toolName != "" {
					doneItem.ResponsesToolMessage.Name = &toolName
				}
			}

			// Emit function_call_arguments.done with full arguments
			response := &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone,
				SequenceNumber: sequenceNumber + len(responses),
				OutputIndex:    schemas.Ptr(outputIndex),
				Arguments:      &args,
			}
			if itemID != "" {
				response.ItemID = &itemID
			}
			if doneItem != nil {
				response.Item = doneItem
			}
			responses = append(responses, response)

			// Emit output_item.done for function call
			statusCompleted := "completed"
			outputItemDone := &schemas.ResponsesMessage{
				Status: &statusCompleted,
			}
			if itemID != "" {
				outputItemDone.ID = &itemID
			}
			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
				SequenceNumber: sequenceNumber + len(responses),
				OutputIndex:    schemas.Ptr(outputIndex),
				Item:           outputItemDone,
			})
		}
	}

	// Emit response.completed
	response := &schemas.BifrostResponsesResponse{
		ID:        state.MessageID,
		CreatedAt: state.CreatedAt,
		Usage:     usage,
	}

	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeCompleted,
		SequenceNumber: sequenceNumber + len(responses),
		Response:       response,
	})

	return responses
}
