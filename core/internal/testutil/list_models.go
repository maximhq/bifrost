package testutil

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

		// Execute list models request
		response, bifrostErr := client.ListModelsRequest(ctx, request)
		if bifrostErr != nil {
			t.Fatalf("❌ List models request failed: %v", GetErrorMessage(bifrostErr))
		}

		// Validate response structure
		if response == nil {
			t.Fatal("❌ List models response is nil")
		}

		// Validate that we have models in the response
		if len(response.Data) == 0 {
			t.Fatal("❌ List models response contains no models")
		}

		t.Logf("✅ List models returned %d models", len(response.Data))

		// Validate individual model entries
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

		if validModels == 0 {
			t.Fatal("❌ No valid models found in response")
		}

		t.Logf("✅ Validated %d models with proper structure", validModels)

		// Validate extra fields
		if response.ExtraFields.Provider != testConfig.Provider {
			t.Fatalf("❌ Provider mismatch: expected %s, got %s", testConfig.Provider, response.ExtraFields.Provider)
		}

		if response.ExtraFields.RequestType != schemas.ListModelsRequest {
			t.Fatalf("❌ Request type mismatch: expected %s, got %s", schemas.ListModelsRequest, response.ExtraFields.RequestType)
		}

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

		response, bifrostErr := client.ListModelsRequest(ctx, request)
		if bifrostErr != nil {
			t.Fatalf("❌ List models pagination request failed: %v", GetErrorMessage(bifrostErr))
		}

		if response == nil {
			t.Fatal("❌ List models pagination response is nil")
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

			// Fetch next page
			nextPageRequest := &schemas.BifrostListModelsRequest{
				Provider:  testConfig.Provider,
				PageSize:  pageSize,
				PageToken: response.NextPageToken,
			}

			nextPageResponse, nextPageErr := client.ListModelsRequest(ctx, nextPageRequest)
			if nextPageErr != nil {
				t.Fatalf("❌ Failed to fetch next page: %v", GetErrorMessage(nextPageErr))
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
