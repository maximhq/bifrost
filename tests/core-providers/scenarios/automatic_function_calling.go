package scenarios

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunAutomaticFunctionCallingTest executes the automatic function calling test scenario using dual API testing framework
func RunAutomaticFunctionCallingTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.AutomaticFunctionCall {
		t.Logf("Automatic function calling not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("AutomaticFunctionCalling", func(t *testing.T) {
		chatMessages := []schemas.ChatMessage{
			CreateBasicChatMessage("Get the current time in UTC timezone"),
		}
		responsesMessages := []schemas.ResponsesMessage{
			CreateBasicResponsesMessage("Get the current time in UTC timezone"),
		}

		// Get tools for both APIs using the new GetSampleTool function
		chatTool := GetSampleTool(SampleToolTypeTime, false)     // Chat Completions API
		responsesTool := GetSampleTool(SampleToolTypeTime, true) // Responses API

		// Base request configuration (tools and tool choice will be set per API)
		baseRequest := &schemas.BifrostRequest{
			Provider:  testConfig.Provider,
			Model:     testConfig.ChatModel,
			Params:    testConfig.CustomParams,
			Fallbacks: testConfig.Fallbacks,
		}

		// Use specialized tool call retry configuration
		retryConfig := ToolCallRetryConfig("get_current_time")
		retryContext := TestRetryContext{
			ScenarioName: "AutomaticFunctionCalling",
			ExpectedBehavior: map[string]interface{}{
				"expected_tool_name": "get_current_time",
				"is_forced_call":     true,
				"timezone":           "UTC",
			},
			TestMetadata: map[string]interface{}{
				"provider":    testConfig.Provider,
				"model":       testConfig.ChatModel,
				"tool_choice": "forced",
			},
		}

		// Enhanced tool call validation for automatic/forced function calls (same for both APIs)
		expectations := ToolCallExpectations("get_current_time", []string{"timezone"})
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
		expectations.ExpectedToolCalls[0].ArgumentTypes = map[string]string{
			"timezone": "string",
		}

		// Create operations for both Chat Completions and Responses API
		chatOperation := func() (*schemas.BifrostResponse, *schemas.BifrostError) {
			chatReq := *baseRequest
			chatReq.Input = schemas.RequestInput{
				ChatCompletionInput: &chatMessages,
			}
			chatReq.Params = MergeModelParameters(chatReq.Params, &schemas.ModelParameters{
				CommonParameters: schemas.CommonParameters{
					Tools: &schemas.BifrostTool{
						ChatTools: []schemas.ChatTool{
							*chatTool,
						},
					},
					ToolChoice: &schemas.BifrostToolChoice{
						ChatToolChoice: &schemas.ChatToolChoice{
							Type: schemas.ChatToolChoiceTypeFunction,
							Function: schemas.ChatToolChoiceFunction{
								Name: "get_current_time",
							},
						},
					},
				},
			})
			return client.ChatCompletionRequest(ctx, &chatReq)
		}

		responsesOperation := func() (*schemas.BifrostResponse, *schemas.BifrostError) {
			responsesReq := *baseRequest
			responsesReq.Input = schemas.RequestInput{
				ResponsesInput: &responsesMessages,
			}
			responsesReq.Params = MergeModelParameters(responsesReq.Params, &schemas.ModelParameters{
				Tools: &[]schemas.Tool{*responsesTool},
				ToolChoice: &schemas.ToolChoice{
					ToolChoiceStruct: &schemas.ToolChoiceStruct{
						Type: bifrost.Ptr(schemas.ToolChoiceTypeFunction),
						ResponsesAPIExtendedToolChoice: &schemas.ResponsesAPIExtendedToolChoice{
							Name: bifrost.Ptr("get_current_time"),
						},
					},
				},
			})
			return client.ResponsesRequest(ctx, &responsesReq)
		}

		// Execute dual API test - passes only if BOTH APIs succeed
		result := WithDualAPITestRetry(t,
			retryConfig,
			retryContext,
			expectations,
			"AutomaticFunctionCalling",
			chatOperation,
			responsesOperation)

		// Validate both APIs succeeded
		if !result.BothSucceeded {
			var errors []string
			if result.ChatCompletionsError != nil {
				errors = append(errors, "Chat Completions: "+GetErrorMessage(result.ChatCompletionsError))
			}
			if result.ResponsesAPIError != nil {
				errors = append(errors, "Responses API: "+GetErrorMessage(result.ResponsesAPIError))
			}
			if len(errors) == 0 {
				errors = append(errors, "One or both APIs failed validation (see logs above)")
			}
			t.Fatalf("❌ AutomaticFunctionCalling dual API test failed: %v", errors)
		}

		// Additional validation specific to automatic function calling using universal tool extraction
		validateAutomaticToolCall := func(response *schemas.BifrostResponse, apiName string) {
			toolCalls := ExtractToolCalls(response)
			foundValidToolCall := false

			for _, toolCall := range toolCalls {
				if toolCall.Name == "get_current_time" {
					foundValidToolCall = true
					t.Logf("✅ %s automatic function call: %s", apiName, toolCall.Arguments)

					// Additional validation for timezone argument
					lowerArgs := strings.ToLower(toolCall.Arguments)
					if strings.Contains(lowerArgs, "utc") || strings.Contains(lowerArgs, "timezone") {
						t.Logf("✅ %s tool call correctly includes timezone information", apiName)
					} else {
						t.Logf("⚠️ %s tool call may be missing timezone specification: %s", apiName, toolCall.Arguments)
					}
					break
				}
			}

			if !foundValidToolCall {
				t.Fatalf("Expected %s API to have automatic tool call for 'time'", apiName)
			}
		}

		// Validate both API responses
		if result.ChatCompletionsResponse != nil {
			validateAutomaticToolCall(result.ChatCompletionsResponse, "Chat Completions")
		}

		if result.ResponsesAPIResponse != nil {
			validateAutomaticToolCall(result.ResponsesAPIResponse, "Responses")
		}

		t.Logf("🎉 Both Chat Completions and Responses APIs passed AutomaticFunctionCalling test!")
	})
}
