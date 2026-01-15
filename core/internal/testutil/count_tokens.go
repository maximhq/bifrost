package testutil

import (
	"context"
	"fmt"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunCountTokenTest validates the CountTokens API for the configured provider/model.
// It sends a simple prompt as Responses messages and asserts token counts and metadata.
// This function now supports testing multiple chat models - the test passes only if ALL models pass
func RunCountTokenTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.CountTokens {
		t.Logf("Count tokens not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("CountTokens", func(t *testing.T) {
		WrapTestScenario(t, client, ctx, testConfig, "CountTokens", ModelTypeChat, runCountTokensTestForModel)
	})
}

// runCountTokensTestForModel runs the count tokens test for a specific model
// The config passed here will have only ONE model in ChatModels array
func runCountTokensTestForModel(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) error {
	// Get the single model from the config
	model := GetChatModelOrFirst(testConfig)

	messages := []schemas.ResponsesMessage{
		CreateBasicResponsesMessage("Hello! What's the capital of France?"),
	}

	countTokensReq := &schemas.BifrostResponsesRequest{
		Provider:  testConfig.Provider,
		Model:     model,
		Input:     messages,
		Params:    &schemas.ResponsesParameters{},
		Fallbacks: testConfig.Fallbacks,
	}

	retryConfig := GetTestRetryConfigForScenario("CountTokens", testConfig)
	retryContext := TestRetryContext{
		ScenarioName: "CountTokens",
		ExpectedBehavior: map[string]interface{}{
			"should_return_token_counts": true,
		},
		TestMetadata: map[string]interface{}{
			"provider": testConfig.Provider,
			"model":    model,
		},
	}

	expectations := GetExpectationsForScenario("CountTokens", testConfig, map[string]interface{}{})
	expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
	if expectations.ProviderSpecific == nil {
		expectations.ProviderSpecific = make(map[string]interface{})
	}
	expectations.ProviderSpecific["expected_provider"] = string(testConfig.Provider)

	// Create CountTokens retry config with default conditions preserved
	countTokensRetryConfig := CountTokensRetryConfig{
		MaxAttempts: retryConfig.MaxAttempts,
		BaseDelay:   retryConfig.BaseDelay,
		MaxDelay:    retryConfig.MaxDelay,
		Conditions:  []CountTokensRetryCondition{},
		OnRetry:     retryConfig.OnRetry,
		OnFinalFail: retryConfig.OnFinalFail,
	}

	countTokensResp, countTokensErr := WithCountTokensTestRetry(
		t,
		countTokensRetryConfig,
		retryContext,
		expectations,
		"CountTokens",
		func() (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.CountTokensRequest(bfCtx, countTokensReq)
		},
	)

	if countTokensErr != nil {
		return fmt.Errorf("CountTokens request failed: %s", GetErrorMessage(countTokensErr))
	}
	if countTokensResp == nil {
		return fmt.Errorf("CountTokens response is nil")
	}

	// Validations are handled inside WithCountTokensTestRetry via ValidateCountTokensResponse
	if countTokensResp.TotalTokens != nil {
		t.Logf("✅ CountTokens test passed: input=%d, total=%d", countTokensResp.InputTokens, *countTokensResp.TotalTokens)
	} else {
		t.Logf("✅ CountTokens test passed: input=%d", countTokensResp.InputTokens)
	}

	return nil
}
