package llmtests

import (
	"context"
	"os"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunListModelsTest executes the list models test scenario
func RunListModelsTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ListModels {
		t.Logf("List models not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("ListModels", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Create basic list models request
		request := &schemas.BifrostListModelsRequest{
			Provider: testConfig.Provider,
		}

		// Use retry framework - ALWAYS retries on any failure (errors, nil response, empty data, validation failures)
		retryConfig := GetTestRetryConfigForScenario("ListModels", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "ListModels",
			ExpectedBehavior: map[string]interface{}{
				"should_return_models":  true,
				"should_have_valid_ids": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
			},
		}

		// Create expectations for list models
		expectations := ResponseExpectations{
			ShouldHaveLatency: true,
			ProviderSpecific: map[string]interface{}{
				"expected_provider": string(testConfig.Provider),
				"min_model_count":   1, // At least one model should be returned
			},
		}

		// Create ListModels retry config
		listModelsRetryConfig := ListModelsRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []ListModelsRetryCondition{}, // Empty - we retry on ALL failures
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		response, bifrostErr := WithListModelsTestRetry(t, listModelsRetryConfig, retryContext, expectations, "ListModels", func() (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.ListModelsRequest(bfCtx, request)
		})

		if bifrostErr != nil {
			t.Fatalf("❌ List models request failed after retries: %v", GetErrorMessage(bifrostErr))
		}

		if response == nil {
			t.Fatal("❌ List models response is nil after retries")
		}

		if len(response.Data) == 0 {
			t.Fatal("❌ List models response contains no models after retries")
		}

		t.Logf("✅ List models returned %d models", len(response.Data))

		// Validate individual model entries (already validated in ValidateListModelsResponse, but log for visibility)
		validModels := 0
		for i, model := range response.Data {
			if model.ID == "" {
				t.Fatalf("❌ Model at index %d has empty ID", i)
				continue
			}

			// Log a few sample models for verification
			if i < 5 {
				t.Logf("   Model %d: ID=%s", i+1, model.ID)
			}

			validModels++
		}

		t.Logf("✅ Validated %d models with proper structure", validModels)

		// Validate latency is reasonable (non-negative and not absurdly high)
		if response.ExtraFields.Latency < 0 {
			t.Fatalf("❌ Invalid latency: %d ms (should be non-negative)", response.ExtraFields.Latency)
		} else if response.ExtraFields.Latency > 30000 {
			t.Logf("⚠️  Warning: High latency detected: %d ms", response.ExtraFields.Latency)
		} else {
			t.Logf("✅ Request latency: %d ms", response.ExtraFields.Latency)
		}

		t.Logf("🎉 List models test passed successfully!")
	})
}

// RunListModelsPaginationTest executes pagination test for list models
func RunListModelsPaginationTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ListModels {
		t.Logf("List models not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("ListModelsPagination", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Test pagination with page size
		pageSize := 5
		request := &schemas.BifrostListModelsRequest{
			Provider: testConfig.Provider,
			PageSize: pageSize,
		}

		// Use retry framework - ALWAYS retries on any failure (errors, nil response, empty data, validation failures)
		retryConfig := GetTestRetryConfigForScenario("ListModelsPagination", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "ListModelsPagination",
			ExpectedBehavior: map[string]interface{}{
				"should_return_paginated_models": true,
				"should_respect_page_size":       true,
			},
			TestMetadata: map[string]interface{}{
				"provider":  string(testConfig.Provider),
				"page_size": pageSize,
			},
		}

		// Create expectations for pagination test
		expectations := ResponseExpectations{
			ShouldHaveLatency: true,
			ProviderSpecific: map[string]interface{}{
				"expected_provider": string(testConfig.Provider),
				"min_model_count":   0, // Pagination might return 0 models if page size is larger than total
			},
		}

		// Create ListModels retry config
		listModelsRetryConfig := ListModelsRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []ListModelsRetryCondition{}, // Empty - we retry on ALL failures
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		response, bifrostErr := WithListModelsTestRetry(t, listModelsRetryConfig, retryContext, expectations, "ListModelsPagination", func() (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.ListModelsRequest(bfCtx, request)
		})

		if bifrostErr != nil {
			t.Fatalf("❌ List models pagination request failed after retries: %v", GetErrorMessage(bifrostErr))
		}

		if response == nil {
			t.Fatal("❌ List models pagination response is nil after retries")
		}

		// Check that pagination was applied
		if len(response.Data) > pageSize {
			t.Fatalf("❌ Expected at most %d models, got %d", pageSize, len(response.Data))
		} else {
			t.Logf("✅ Pagination working: returned %d models (page size: %d)", len(response.Data), pageSize)
		}

		// Test with page token if provided
		if response.NextPageToken != "" {
			t.Logf("✅ Next page token available: %s", response.NextPageToken)

			// Fetch next page - also use retry wrapper
			nextPageRequest := &schemas.BifrostListModelsRequest{
				Provider:  testConfig.Provider,
				PageSize:  pageSize,
				PageToken: response.NextPageToken,
			}

			nextPageRetryContext := TestRetryContext{
				ScenarioName: "ListModelsPagination_NextPage",
				ExpectedBehavior: map[string]interface{}{
					"should_return_next_page": true,
				},
				TestMetadata: map[string]interface{}{
					"provider":   testConfig.Provider,
					"page_size":  pageSize,
					"page_token": response.NextPageToken,
				},
			}

			nextPageResponse, nextPageErr := WithListModelsTestRetry(t, listModelsRetryConfig, nextPageRetryContext, expectations, "ListModelsPagination_NextPage", func() (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
				bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
				return client.ListModelsRequest(bfCtx, nextPageRequest)
			})

			if nextPageErr != nil {
				t.Fatalf("❌ Failed to fetch next page after retries: %v", GetErrorMessage(nextPageErr))
			} else if nextPageResponse != nil {
				t.Logf("✅ Successfully fetched next page with %d models", len(nextPageResponse.Data))

				// Verify that the next page contains different models
				if len(response.Data) > 0 && len(nextPageResponse.Data) > 0 {
					firstPageFirstModel := response.Data[0].ID
					secondPageFirstModel := nextPageResponse.Data[0].ID
					if firstPageFirstModel != secondPageFirstModel {
						t.Logf("✅ Pages contain different models (first page: %s, second page: %s)",
							firstPageFirstModel, secondPageFirstModel)
					}
				}
			}
		} else {
			t.Logf("ℹ️  No next page token - all models returned in single page")
		}

		t.Logf("🎉 List models pagination test completed!")
	})
}
