package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// RunToolCallsTest executes the tool calls test scenario using dual API testing framework
// This function now supports testing multiple chat models - the test passes only if ALL models pass
func RunToolCallsTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ToolCalls {
		t.Logf("Tool calls not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("ToolCalls", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Use the generic multi-model test wrapper
		result := RunMultiModelTest(t, client, ctx, testConfig, "ToolCalls", ModelTypeChat, runToolCallsTestForModel)

		// Assert all models passed - this will fail the test if any model failed
		AssertAllModelsPassed(t, result)
	})
}

// runToolCallsTestForModel runs the tool calls test for a specific model
// The config passed here will have only ONE model in ChatModels array
func runToolCallsTestForModel(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) error {
	// Get the single model from the config
	model := GetChatModelOrFirst(testConfig)
	chatMessages := []schemas.ChatMessage{
		CreateBasicChatMessage("What's the weather like in New York? answer in celsius"),
	}
	responsesMessages := []schemas.ResponsesMessage{
		CreateBasicResponsesMessage("What's the weather like in New York? answer in celsius"),
	}

	// Get tools for both APIs using the new GetSampleTool function
	chatTool := GetSampleChatTool(SampleToolTypeWeather)           // Chat Completions API
	responsesTool := GetSampleResponsesTool(SampleToolTypeWeather) // Responses API

	// Use specialized tool call retry configuration
	retryConfig := ToolCallRetryConfig(string(SampleToolTypeWeather))
	retryContext := TestRetryContext{
		ScenarioName: "ToolCalls",
		ExpectedBehavior: map[string]interface{}{
			"expected_tool_name": string(SampleToolTypeWeather),
			"required_location":  "new york",
		},
		TestMetadata: map[string]interface{}{
			"provider": testConfig.Provider,
			"model":    model,
		},
	}

	// Enhanced tool call validation (same for both APIs)
	expectations := ToolCallExpectations(string(SampleToolTypeWeather), []string{"location"})
	expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)

	// Add additional tool-specific validations
	expectations.ExpectedToolCalls[0].ArgumentTypes = map[string]string{
		"location": "string",
	}

	// Create operations for both Chat Completions and Responses API
	chatOperation := func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		chatReq := &schemas.BifrostChatRequest{
			Provider: testConfig.Provider,
			Model:    model,
			Input:    chatMessages,
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: bifrost.Ptr(150),
				Tools:               []schemas.ChatTool{*chatTool},
			},
			Fallbacks: testConfig.Fallbacks,
		}
		return client.ChatCompletionRequest(bfCtx, chatReq)
	}

	responsesOperation := func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		responsesReq := &schemas.BifrostResponsesRequest{
			Provider: testConfig.Provider,
			Model:    model,
			Input:    responsesMessages,
			Params: &schemas.ResponsesParameters{
				Tools: []schemas.ResponsesTool{*responsesTool},
			},
		}
		return client.ResponsesRequest(bfCtx, responsesReq)
	}

	// Execute dual API test - passes only if BOTH APIs succeed
	dualResult := WithDualAPITestRetry(t,
		retryConfig,
		retryContext,
		expectations,
		"ToolCalls",
		chatOperation,
		responsesOperation)

	// Validate both APIs succeeded
	if !dualResult.BothSucceeded {
		var errors []string
		if dualResult.ChatCompletionsError != nil {
			errors = append(errors, "Chat Completions: "+GetErrorMessage(dualResult.ChatCompletionsError))
		}
		if dualResult.ResponsesAPIError != nil {
			errors = append(errors, "Responses API: "+GetErrorMessage(dualResult.ResponsesAPIError))
		}
		if len(errors) == 0 {
			errors = append(errors, "One or both APIs failed validation (see logs above)")
		}
		return fmt.Errorf("ToolCalls dual API test failed: %v", errors)
	}

	// Verify location argument mentions New York using universal tool extraction
	validateLocationInChatToolCalls := func(response *schemas.BifrostChatResponse, apiName string) error {
		toolCalls := ExtractChatToolCalls(response)
		return validateLocationInToolCallsWithError(t, toolCalls, apiName)
	}

	validateLocationInResponsesToolCalls := func(response *schemas.BifrostResponsesResponse, apiName string) error {
		toolCalls := ExtractResponsesToolCalls(response)
		return validateLocationInToolCallsWithError(t, toolCalls, apiName)
	}

	// Validate both API responses
	if dualResult.ChatCompletionsResponse != nil {
		if err := validateLocationInChatToolCalls(dualResult.ChatCompletionsResponse, "Chat Completions"); err != nil {
			return err
		}
	}

	if dualResult.ResponsesAPIResponse != nil {
		if err := validateLocationInResponsesToolCalls(dualResult.ResponsesAPIResponse, "Responses"); err != nil {
			return err
		}
	}

	t.Logf("🎉 Both Chat Completions and Responses APIs passed ToolCalls test for model: %s!", model)
	return nil
}

func validateLocationInToolCalls(t *testing.T, toolCalls []ToolCallInfo, apiName string) {
	locationFound := false

	for _, toolCall := range toolCalls {
		if toolCall.Name == string(SampleToolTypeWeather) {
			var args map[string]interface{}
			if json.Unmarshal([]byte(toolCall.Arguments), &args) == nil {
				if location, exists := args["location"].(string); exists {
					lowerLocation := strings.ToLower(location)
					if strings.Contains(lowerLocation, "new york") || strings.Contains(lowerLocation, "nyc") {
						locationFound = true
						t.Logf("✅ %s tool call has correct location: %s", apiName, location)
						break
					}
				}
			}
		}
	}

	require.True(t, locationFound, "%s API tool call should specify New York as the location", apiName)
}

func validateLocationInToolCallsWithError(t *testing.T, toolCalls []ToolCallInfo, apiName string) error {
	locationFound := false

	for _, toolCall := range toolCalls {
		if toolCall.Name == string(SampleToolTypeWeather) {
			var args map[string]interface{}
			if json.Unmarshal([]byte(toolCall.Arguments), &args) == nil {
				if location, exists := args["location"].(string); exists {
					lowerLocation := strings.ToLower(location)
					if strings.Contains(lowerLocation, "new york") || strings.Contains(lowerLocation, "nyc") {
						locationFound = true
						t.Logf("✅ %s tool call has correct location: %s", apiName, location)
						break
					}
				}
			}
		}
	}

	if !locationFound {
		return fmt.Errorf("%s API tool call should specify New York as the location", apiName)
	}
	return nil
}
