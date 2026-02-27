// Package llmtests provides batch API test utilities for the Bifrost system.
package llmtests

import (
	"context"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// getFakeBatchID returns a provider-specific fake batch ID for testing
func getFakeBatchID(provider schemas.ModelProvider) string {
	switch provider {
	case schemas.Anthropic:
		return "msgbatch_test-batch-id"
	case schemas.OpenAI, schemas.Azure:
		return "batch_test-batch-id"
	case schemas.Bedrock:
		// Bedrock uses ARNs for batch IDs - job ID must be exactly 12 lowercase alphanumeric chars
		return "arn:aws:bedrock:us-east-1:791152688819:model-invocation-job/aaaaaaaaaaaa"
	case schemas.Gemini:
		// Gemini uses "batches/" prefix with alphanumeric batch IDs
		return "batches/aaaaaaaaaaaa"
	default:
		return "batch_test-batch-id"
	}
}

// RunBatchCreateTest tests the batch create functionality
func RunBatchCreateTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.BatchCreate {
		t.Logf("[SKIPPED] Batch Create: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("BatchCreate", func(t *testing.T) {
		t.Logf("[RUNNING] Batch Create test for provider: %s", testConfig.Provider)

		// Use retry framework
		retryConfig := GetTestRetryConfigForScenario("BatchCreate", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "BatchCreate",
			ExpectedBehavior: map[string]interface{}{
				"should_return_batch_id": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
			},
		}

		batchCreateRetryConfig := BatchCreateRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []BatchCreateRetryCondition{},
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		expectations := ResponseExpectations{
			ShouldHaveLatency: true,
			ProviderSpecific: map[string]interface{}{
				"expected_provider": string(testConfig.Provider),
			},
		}

		response, err := WithBatchCreateTestRetry(t, batchCreateRetryConfig, retryContext, expectations, "BatchCreate", func() (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
			request := &schemas.BifrostBatchCreateRequest{
				Provider: testConfig.Provider,
				Model:    schemas.Ptr(testConfig.ChatModel),
				Endpoint: schemas.BatchEndpointChatCompletions,
				Requests: []schemas.BatchRequestItem{
					{
						CustomID: "test-request-1",
						Body: map[string]interface{}{
							"model": testConfig.ChatModel,
							"messages": []map[string]string{
								{"role": "user", "content": "Say hello"},
							},
						},
					},
				},
				CompletionWindow: "24h",
				ExtraParams:      testConfig.BatchExtraParams,
			}
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.BatchCreateRequest(bfCtx, request)
		})

		if err != nil {
			// Check if this is an unsupported operation error
			if err.Error != nil && (err.Error.Code != nil && *err.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error", testConfig.Provider)
				return
			}
			t.Fatalf("❌ BatchCreate failed after retries: %v", GetErrorMessage(err))
		}

		if response == nil {
			t.Fatal("❌ BatchCreate returned nil response after retries")
		}

		if response.ID == "" {
			t.Fatal("❌ BatchCreate returned empty batch ID after retries")
		}

		t.Logf("✅ Batch Create test passed for provider: %s, batch ID: %s", testConfig.Provider, response.ID)
	})
}

// RunBatchListTest tests the batch list functionality
func RunBatchListTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.BatchList {
		t.Logf("[SKIPPED] Batch List: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("BatchList", func(t *testing.T) {
		t.Logf("[RUNNING] Batch List test for provider: %s", testConfig.Provider)

		// Use retry framework
		retryConfig := GetTestRetryConfigForScenario("BatchList", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "BatchList",
			ExpectedBehavior: map[string]interface{}{
				"should_return_list": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
			},
		}

		batchListRetryConfig := BatchListRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []BatchListRetryCondition{},
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		expectations := ResponseExpectations{
			ShouldHaveLatency: true,
			ProviderSpecific: map[string]interface{}{
				"expected_provider": string(testConfig.Provider),
			},
		}

		response, err := WithBatchListTestRetry(t, batchListRetryConfig, retryContext, expectations, "BatchList", func() (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
			request := &schemas.BifrostBatchListRequest{
				Provider: testConfig.Provider,
				Limit:    10,
			}
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.BatchListRequest(bfCtx, request)
		})

		if err != nil {
			// Check if this is an unsupported operation error
			if err.Error != nil && (err.Error.Code != nil && *err.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error", testConfig.Provider)
				return
			}
			t.Fatalf("❌ BatchList failed after retries: %v", GetErrorMessage(err))
		}

		if response == nil {
			t.Fatal("❌ BatchList returned nil response after retries")
		}

		t.Logf("✅ Batch List test passed for provider: %s, found %d batches", testConfig.Provider, len(response.Data))
	})
}

// RunBatchRetrieveTest tests the batch retrieve functionality
func RunBatchRetrieveTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.BatchRetrieve {
		t.Logf("[SKIPPED] Batch Retrieve: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("BatchRetrieve", func(t *testing.T) {
		t.Logf("[RUNNING] Batch Retrieve test for provider: %s", testConfig.Provider)

		// First, we need to create a batch to retrieve using retry framework
		retryConfig := GetTestRetryConfigForScenario("BatchRetrieve", testConfig)

		createRetryContext := TestRetryContext{
			ScenarioName: "BatchRetrieve_Create",
			ExpectedBehavior: map[string]interface{}{
				"should_return_batch_id": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
			},
		}

		batchCreateRetryConfig := BatchCreateRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []BatchCreateRetryCondition{},
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		createExpectations := ResponseExpectations{
			ShouldHaveLatency: true,
			ProviderSpecific: map[string]interface{}{
				"expected_provider": string(testConfig.Provider),
			},
		}

		createResponse, createErr := WithBatchCreateTestRetry(t, batchCreateRetryConfig, createRetryContext, createExpectations, "BatchRetrieve_Create", func() (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
			createRequest := &schemas.BifrostBatchCreateRequest{
				Provider: testConfig.Provider,
				Model:    schemas.Ptr(testConfig.ChatModel),
				Endpoint: schemas.BatchEndpointChatCompletions,
				Requests: []schemas.BatchRequestItem{
					{
						CustomID: "test-retrieve-1",
						Body: map[string]interface{}{
							"model": testConfig.ChatModel,
							"messages": []map[string]string{
								{"role": "user", "content": "Say hello"},
							},
						},
					},
				},
				CompletionWindow: "24h",
				ExtraParams:      testConfig.BatchExtraParams,
			}
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.BatchCreateRequest(bfCtx, createRequest)
		})

		if createErr != nil {
			if createErr.Error != nil && (createErr.Error.Code != nil && *createErr.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error for create", testConfig.Provider)
				return
			}
			t.Fatalf("❌ BatchCreate (for retrieve test) failed after retries: %v", GetErrorMessage(createErr))
		}

		if createResponse == nil || createResponse.ID == "" {
			t.Fatal("❌ BatchCreate returned invalid response for retrieve test after retries")
		}

		// Now retrieve the batch using retry framework
		retrieveRetryContext := TestRetryContext{
			ScenarioName: "BatchRetrieve",
			ExpectedBehavior: map[string]interface{}{
				"should_return_batch": true,
				"expected_batch_id":   createResponse.ID,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
			},
		}

		batchRetrieveRetryConfig := BatchRetrieveRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []BatchRetrieveRetryCondition{},
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		retrieveExpectations := ResponseExpectations{
			ShouldHaveLatency: true,
			ProviderSpecific: map[string]interface{}{
				"expected_provider": string(testConfig.Provider),
			},
		}

		response, err := WithBatchRetrieveTestRetry(t, batchRetrieveRetryConfig, retrieveRetryContext, retrieveExpectations, "BatchRetrieve", func() (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
			retrieveRequest := &schemas.BifrostBatchRetrieveRequest{
				Provider: testConfig.Provider,
				BatchID:  createResponse.ID,
			}
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.BatchRetrieveRequest(bfCtx, retrieveRequest)
		})

		if err != nil {
			t.Fatalf("❌ BatchRetrieve failed after retries: %v", GetErrorMessage(err))
		}

		if response == nil {
			t.Fatal("❌ BatchRetrieve returned nil response after retries")
		}

		if response.ID != createResponse.ID {
			t.Fatalf("❌ BatchRetrieve returned wrong batch ID: got %s, expected %s", response.ID, createResponse.ID)
		}

		t.Logf("✅ Batch Retrieve test passed for provider: %s, batch ID: %s, status: %s", testConfig.Provider, response.ID, response.Status)
	})
}

// RunBatchCancelTest tests the batch cancel functionality
func RunBatchCancelTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.BatchCancel {
		t.Logf("[SKIPPED] Batch Cancel: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("BatchCancel", func(t *testing.T) {
		t.Logf("[RUNNING] Batch Cancel test for provider: %s", testConfig.Provider)

		// First, create a batch to cancel using retry framework
		retryConfig := GetTestRetryConfigForScenario("BatchCancel", testConfig)

		createRetryContext := TestRetryContext{
			ScenarioName: "BatchCancel_Create",
			ExpectedBehavior: map[string]interface{}{
				"should_return_batch_id": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
			},
		}

		batchCreateRetryConfig := BatchCreateRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []BatchCreateRetryCondition{},
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		createExpectations := ResponseExpectations{
			ShouldHaveLatency: true,
			ProviderSpecific: map[string]interface{}{
				"expected_provider": string(testConfig.Provider),
			},
		}

		createResponse, createErr := WithBatchCreateTestRetry(t, batchCreateRetryConfig, createRetryContext, createExpectations, "BatchCancel_Create", func() (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
			createRequest := &schemas.BifrostBatchCreateRequest{
				Provider: testConfig.Provider,
				Model:    schemas.Ptr(testConfig.ChatModel),
				Endpoint: schemas.BatchEndpointChatCompletions,
				Requests: []schemas.BatchRequestItem{
					{
						CustomID: "test-cancel-1",
						Body: map[string]interface{}{
							"model": testConfig.ChatModel,
							"messages": []map[string]string{
								{"role": "user", "content": "Say hello"},
							},
						},
					},
				},
				CompletionWindow: "24h",
				ExtraParams:      testConfig.BatchExtraParams,
			}
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.BatchCreateRequest(bfCtx, createRequest)
		})

		if createErr != nil {
			if createErr.Error != nil && (createErr.Error.Code != nil && *createErr.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error for create", testConfig.Provider)
				return
			}
			t.Fatalf("❌ BatchCreate (for cancel test) failed after retries: %v", GetErrorMessage(createErr))
		}

		if createResponse == nil || createResponse.ID == "" {
			t.Fatal("❌ BatchCreate returned invalid response for cancel test after retries")
		}

		// Now cancel the batch using retry framework
		cancelRetryContext := TestRetryContext{
			ScenarioName: "BatchCancel",
			ExpectedBehavior: map[string]interface{}{
				"should_cancel_batch": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"batch_id": createResponse.ID,
			},
		}

		batchCancelRetryConfig := BatchCancelRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []BatchCancelRetryCondition{},
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		cancelExpectations := ResponseExpectations{
			ShouldHaveLatency: true,
			ProviderSpecific: map[string]interface{}{
				"expected_provider": string(testConfig.Provider),
			},
		}

		response, err := WithBatchCancelTestRetry(t, batchCancelRetryConfig, cancelRetryContext, cancelExpectations, "BatchCancel", func() (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
			cancelRequest := &schemas.BifrostBatchCancelRequest{
				Provider: testConfig.Provider,
				BatchID:  createResponse.ID,
			}
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.BatchCancelRequest(bfCtx, cancelRequest)
		})

		if err != nil {
			// Note: Cancel might fail if batch has already completed
			t.Logf("[WARNING] BatchCancel failed after retries (batch may have already completed): %v", GetErrorMessage(err))
			return
		}

		if response == nil {
			t.Fatal("❌ BatchCancel returned nil response after retries")
		}

		t.Logf("✅ Batch Cancel test passed for provider: %s, batch ID: %s", testConfig.Provider, response.ID)
	})
}

// RunBatchResultsTest tests the batch results functionality
func RunBatchResultsTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.BatchResults {
		t.Logf("[SKIPPED] Batch Results: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("BatchResults", func(t *testing.T) {
		t.Logf("[RUNNING] Batch Results test for provider: %s", testConfig.Provider)

		// Note: For a complete test, you would need a completed batch
		// This test uses a fake batch ID to verify the API endpoint is reachable
		// and returns the expected "not found" error format.
		// In practice, you would use an actual completed batch ID.

		// We intentionally don't use the retry framework here because we expect
		// an error with the fake batch ID - retrying would give the same result.
		request := &schemas.BifrostBatchResultsRequest{
			Provider: testConfig.Provider,
			BatchID:  getFakeBatchID(testConfig.Provider), // Fake batch ID with provider-specific prefix
		}
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		_, err := client.BatchResultsRequest(bfCtx, request)

		if err != nil {
			// Check for auth/configuration errors - these should fail the test
			if err.StatusCode != nil {
				switch *err.StatusCode {
				case 401, 403:
					t.Fatalf("❌ BatchResults failed with auth error (status %d): %v", *err.StatusCode, GetErrorMessage(err))
					return
				case 404:
					// Not found is expected for a fake batch ID
					t.Logf("[INFO] BatchResults test completed (expected 404 not found error): %v", GetErrorMessage(err))
					return
				}
			}

			// Check error type/message for "not found" or validation error indicators
			// Providers may return "not found" errors OR validation errors for fake batch IDs
			isExpectedError := false
			if err.Error != nil {
				if err.Error.Type != nil {
					errType := strings.ToLower(*err.Error.Type)
					if strings.Contains(errType, "not_found") || strings.Contains(errType, "notfound") {
						isExpectedError = true
					}
				}
				errMsg := strings.ToLower(err.Error.Message)
				// Check for "not found" style errors
				if strings.Contains(errMsg, "not found") || strings.Contains(errMsg, "does not exist") || strings.Contains(errMsg, "could not find") {
					isExpectedError = true
				}
				// Check for validation/parsing errors (providers validate batch ID format before lookup)
				if strings.Contains(errMsg, "could not parse") || strings.Contains(errMsg, "validation error") ||
					strings.Contains(errMsg, "failed to satisfy constraint") || strings.Contains(errMsg, "invalid") {
					isExpectedError = true
				}
			}

			if isExpectedError {
				t.Logf("[INFO] BatchResults test completed (expected error with fake ID): %v", GetErrorMessage(err))
				return
			}

			// Unexpected error type - fail the test to avoid masking real issues
			t.Fatalf("❌ BatchResults failed with unexpected error: %v", GetErrorMessage(err))
			return
		}

		t.Logf("✅ Batch Results test passed for provider: %s", testConfig.Provider)
	})
}

// RunBatchUnsupportedTest tests that unsupported providers return appropriate errors
func RunBatchUnsupportedTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	// Only run this test for providers that don't support batch
	if testConfig.Scenarios.BatchCreate || testConfig.Scenarios.BatchList ||
		testConfig.Scenarios.BatchRetrieve || testConfig.Scenarios.BatchCancel ||
		testConfig.Scenarios.BatchResults {
		return
	}

	t.Run("BatchUnsupported", func(t *testing.T) {
		t.Logf("[RUNNING] Batch Unsupported test for provider: %s", testConfig.Provider)

		// TODO remove this once azure is officially supported
		// We are skipping azure from this for now
		if testConfig.Provider == schemas.Azure {
			t.Skipf("Skipping batch unsupported test for provider: %s", testConfig.Provider)
			return
		}

		// Try to create a batch - should fail with unsupported error
		request := &schemas.BifrostBatchCreateRequest{
			Provider: testConfig.Provider,
			Model:    schemas.Ptr(testConfig.ChatModel),
			Endpoint: schemas.BatchEndpointChatCompletions,
			Requests: []schemas.BatchRequestItem{
				{
					CustomID: "test-unsupported-1",
					Body: map[string]interface{}{
						"model": testConfig.ChatModel,
						"messages": []map[string]string{
							{"role": "user", "content": "Say hello"},
						},
					},
				},
			},
		}

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		_, err := client.BatchCreateRequest(bfCtx, request)
		if err == nil {
			t.Error("BatchCreate should have failed for unsupported provider")
			return
		}

		// Verify it's an unsupported operation error
		if err.Error != nil && (err.Error.Code == nil || *err.Error.Code != "unsupported_operation") {
			t.Errorf("Expected unsupported_operation error, got: %v", err)
			return
		}

		t.Logf("[SUCCESS] Batch Unsupported test passed for provider: %s - correctly returned unsupported error", testConfig.Provider)
	})
}

// ============================================================================
// File API Tests
// ============================================================================

// RunFileUploadTest tests the file upload functionality
func RunFileUploadTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.FileUpload {
		t.Logf("[SKIPPED] File Upload: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("FileUpload", func(t *testing.T) {
		t.Logf("[RUNNING] File Upload test for provider: %s", testConfig.Provider)

		// Use retry framework
		retryConfig := GetTestRetryConfigForScenario("FileUpload", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "FileUpload",
			ExpectedBehavior: map[string]interface{}{
				"should_return_file_id": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
			},
		}

		fileUploadRetryConfig := FileUploadRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []FileUploadRetryCondition{},
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		expectations := ResponseExpectations{
			ShouldHaveLatency: true,
			ProviderSpecific: map[string]interface{}{
				"expected_provider": string(testConfig.Provider),
			},
		}

		response, err := WithFileUploadTestRetry(t, fileUploadRetryConfig, retryContext, expectations, "FileUpload", func() (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
			// Create a simple JSONL file content for batch processing
			fileContent := []byte(`{"custom_id": "test-1", "method": "POST", "url": "/v1/chat/completions", "body": {"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hello"}]}}
`)
			request := &schemas.BifrostFileUploadRequest{
				Provider:    testConfig.Provider,
				File:        fileContent,
				Filename:    "test_batch.jsonl",
				Purpose:     "batch",
				ExtraParams: testConfig.FileExtraParams,
			}
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.FileUploadRequest(bfCtx, request)
		})

		if err != nil {
			// Check if this is an unsupported operation error
			if err.Error != nil && (err.Error.Code != nil && *err.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error", testConfig.Provider)
				return
			}
			t.Fatalf("❌ FileUpload failed after retries: %v", GetErrorMessage(err))
		}

		if response == nil {
			t.Fatal("❌ FileUpload returned nil response after retries")
		}

		if response.ID == "" {
			t.Fatal("❌ FileUpload returned empty file ID after retries")
		}

		t.Logf("✅ File Upload test passed for provider: %s, file ID: %s", testConfig.Provider, response.ID)
	})
}

// RunFileListTest tests the file list functionality
func RunFileListTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.FileList {
		t.Logf("[SKIPPED] File List: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("FileList", func(t *testing.T) {
		t.Logf("[RUNNING] File List test for provider: %s", testConfig.Provider)

		// Use retry framework
		retryConfig := GetTestRetryConfigForScenario("FileList", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "FileList",
			ExpectedBehavior: map[string]interface{}{
				"should_return_list": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
			},
		}

		fileListRetryConfig := FileListRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []FileListRetryCondition{},
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		expectations := ResponseExpectations{
			ShouldHaveLatency: true,
			ProviderSpecific: map[string]interface{}{
				"expected_provider": string(testConfig.Provider),
			},
		}

		response, err := WithFileListTestRetry(t, fileListRetryConfig, retryContext, expectations, "FileList", func() (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
			request := &schemas.BifrostFileListRequest{
				Provider:    testConfig.Provider,
				Limit:       10,
				ExtraParams: testConfig.FileExtraParams,
			}
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.FileListRequest(bfCtx, request)
		})

		if err != nil {
			// Check if this is an unsupported operation error
			if err.Error != nil && (err.Error.Code != nil && *err.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error", testConfig.Provider)
				return
			}
			t.Fatalf("❌ FileList failed after retries: %v", GetErrorMessage(err))
		}

		if response == nil {
			t.Fatal("❌ FileList returned nil response after retries")
		}

		t.Logf("✅ File List test passed for provider: %s, found %d files", testConfig.Provider, len(response.Data))
	})
}

// RunFileRetrieveTest tests the file retrieve functionality
func RunFileRetrieveTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.FileRetrieve {
		t.Logf("[SKIPPED] File Retrieve: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("FileRetrieve", func(t *testing.T) {
		t.Logf("[RUNNING] File Retrieve test for provider: %s", testConfig.Provider)

		// First upload a file to retrieve using retry framework
		retryConfig := GetTestRetryConfigForScenario("FileRetrieve", testConfig)

		uploadRetryContext := TestRetryContext{
			ScenarioName: "FileRetrieve_Upload",
			ExpectedBehavior: map[string]interface{}{
				"should_return_file_id": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
			},
		}

		fileUploadRetryConfig := FileUploadRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []FileUploadRetryCondition{},
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		uploadExpectations := ResponseExpectations{
			ShouldHaveLatency: true,
			ProviderSpecific: map[string]interface{}{
				"expected_provider": string(testConfig.Provider),
			},
		}

		uploadResponse, uploadErr := WithFileUploadTestRetry(t, fileUploadRetryConfig, uploadRetryContext, uploadExpectations, "FileRetrieve_Upload", func() (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
			fileContent := []byte(`{"custom_id": "test-retrieve-1", "method": "POST", "url": "/v1/chat/completions", "body": {"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hello"}]}}
`)
			uploadRequest := &schemas.BifrostFileUploadRequest{
				Provider:    testConfig.Provider,
				File:        fileContent,
				Filename:    "test_retrieve.jsonl",
				Purpose:     "batch",
				ExtraParams: testConfig.FileExtraParams,
			}
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.FileUploadRequest(bfCtx, uploadRequest)
		})

		if uploadErr != nil {
			if uploadErr.Error != nil && (uploadErr.Error.Code != nil && *uploadErr.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error for upload", testConfig.Provider)
				return
			}
			t.Fatalf("❌ FileUpload (for retrieve test) failed after retries: %v", GetErrorMessage(uploadErr))
		}

		if uploadResponse == nil || uploadResponse.ID == "" {
			t.Fatal("❌ FileUpload returned invalid response for retrieve test after retries")
		}

		// Now retrieve the file using retry framework
		retrieveRetryContext := TestRetryContext{
			ScenarioName: "FileRetrieve",
			ExpectedBehavior: map[string]interface{}{
				"should_return_file": true,
				"expected_file_id":   uploadResponse.ID,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
			},
		}

		fileRetrieveRetryConfig := FileRetrieveRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []FileRetrieveRetryCondition{},
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		retrieveExpectations := ResponseExpectations{
			ShouldHaveLatency: true,
			ProviderSpecific: map[string]interface{}{
				"expected_provider": string(testConfig.Provider),
			},
		}

		response, err := WithFileRetrieveTestRetry(t, fileRetrieveRetryConfig, retrieveRetryContext, retrieveExpectations, "FileRetrieve", func() (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
			retrieveRequest := &schemas.BifrostFileRetrieveRequest{
				Provider: testConfig.Provider,
				FileID:   uploadResponse.ID,
			}
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.FileRetrieveRequest(bfCtx, retrieveRequest)
		})

		if err != nil {
			t.Fatalf("❌ FileRetrieve failed after retries: %v", GetErrorMessage(err))
		}

		if response == nil {
			t.Fatal("❌ FileRetrieve returned nil response after retries")
		}

		if response.ID != uploadResponse.ID {
			t.Fatalf("❌ FileRetrieve returned wrong file ID: got %s, expected %s", response.ID, uploadResponse.ID)
		}

		t.Logf("✅ File Retrieve test passed for provider: %s, file ID: %s", testConfig.Provider, response.ID)
	})
}

// RunFileDeleteTest tests the file delete functionality
func RunFileDeleteTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.FileDelete {
		t.Logf("[SKIPPED] File Delete: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("FileDelete", func(t *testing.T) {
		t.Logf("[RUNNING] File Delete test for provider: %s", testConfig.Provider)

		// First upload a file to delete using retry framework
		retryConfig := GetTestRetryConfigForScenario("FileDelete", testConfig)

		uploadRetryContext := TestRetryContext{
			ScenarioName: "FileDelete_Upload",
			ExpectedBehavior: map[string]interface{}{
				"should_return_file_id": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
			},
		}

		fileUploadRetryConfig := FileUploadRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []FileUploadRetryCondition{},
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		uploadExpectations := ResponseExpectations{
			ShouldHaveLatency: true,
			ProviderSpecific: map[string]interface{}{
				"expected_provider": string(testConfig.Provider),
			},
		}

		uploadResponse, uploadErr := WithFileUploadTestRetry(t, fileUploadRetryConfig, uploadRetryContext, uploadExpectations, "FileDelete_Upload", func() (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
			fileContent := []byte(`{"custom_id": "test-delete-1", "method": "POST", "url": "/v1/chat/completions", "body": {"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hello"}]}}
`)
			uploadRequest := &schemas.BifrostFileUploadRequest{
				Provider:    testConfig.Provider,
				File:        fileContent,
				Filename:    "test_delete.jsonl",
				Purpose:     "batch",
				ExtraParams: testConfig.FileExtraParams,
			}
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.FileUploadRequest(bfCtx, uploadRequest)
		})

		if uploadErr != nil {
			if uploadErr.Error != nil && (uploadErr.Error.Code != nil && *uploadErr.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error for upload", testConfig.Provider)
				return
			}
			t.Fatalf("❌ FileUpload (for delete test) failed after retries: %v", GetErrorMessage(uploadErr))
		}

		if uploadResponse == nil || uploadResponse.ID == "" {
			t.Fatal("❌ FileUpload returned invalid response for delete test after retries")
		}

		// Now delete the file using retry framework
		deleteRetryContext := TestRetryContext{
			ScenarioName: "FileDelete",
			ExpectedBehavior: map[string]interface{}{
				"should_delete_file": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"file_id":  uploadResponse.ID,
			},
		}

		fileDeleteRetryConfig := FileDeleteRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []FileDeleteRetryCondition{},
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		deleteExpectations := ResponseExpectations{
			ShouldHaveLatency: true,
			ProviderSpecific: map[string]interface{}{
				"expected_provider": string(testConfig.Provider),
			},
		}

		response, err := WithFileDeleteTestRetry(t, fileDeleteRetryConfig, deleteRetryContext, deleteExpectations, "FileDelete", func() (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
			deleteRequest := &schemas.BifrostFileDeleteRequest{
				Provider: testConfig.Provider,
				FileID:   uploadResponse.ID,
			}
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.FileDeleteRequest(bfCtx, deleteRequest)
		})

		if err != nil {
			t.Fatalf("❌ FileDelete failed after retries: %v", GetErrorMessage(err))
		}

		if response == nil {
			t.Fatal("❌ FileDelete returned nil response after retries")
		}

		if !response.Deleted {
			t.Fatal("❌ FileDelete did not mark file as deleted after retries")
		}

		t.Logf("✅ File Delete test passed for provider: %s, file ID: %s", testConfig.Provider, response.ID)
	})
}

// RunFileContentTest tests the file content download functionality
func RunFileContentTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.FileContent {
		t.Logf("[SKIPPED] File Content: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("FileContent", func(t *testing.T) {
		t.Logf("[RUNNING] File Content test for provider: %s", testConfig.Provider)

		// First upload a file to download using retry framework
		retryConfig := GetTestRetryConfigForScenario("FileContent", testConfig)

		uploadRetryContext := TestRetryContext{
			ScenarioName: "FileContent_Upload",
			ExpectedBehavior: map[string]interface{}{
				"should_return_file_id": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
			},
		}

		fileUploadRetryConfig := FileUploadRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []FileUploadRetryCondition{},
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		uploadExpectations := ResponseExpectations{
			ShouldHaveLatency: true,
			ProviderSpecific: map[string]interface{}{
				"expected_provider": string(testConfig.Provider),
			},
		}

		uploadResponse, uploadErr := WithFileUploadTestRetry(t, fileUploadRetryConfig, uploadRetryContext, uploadExpectations, "FileContent_Upload", func() (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
			originalContent := []byte(`{"custom_id": "test-content-1", "method": "POST", "url": "/v1/chat/completions", "body": {"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hello"}]}}
`)
			uploadRequest := &schemas.BifrostFileUploadRequest{
				Provider:    testConfig.Provider,
				File:        originalContent,
				Filename:    "test_content.jsonl",
				Purpose:     "batch",
				ExtraParams: testConfig.FileExtraParams,
			}
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.FileUploadRequest(bfCtx, uploadRequest)
		})

		if uploadErr != nil {
			if uploadErr.Error != nil && (uploadErr.Error.Code != nil && *uploadErr.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error for upload", testConfig.Provider)
				return
			}
			t.Fatalf("❌ FileUpload (for content test) failed after retries: %v", GetErrorMessage(uploadErr))
		}

		if uploadResponse == nil || uploadResponse.ID == "" {
			t.Fatal("❌ FileUpload returned invalid response for content test after retries")
		}

		// Now download the file content using retry framework
		contentRetryContext := TestRetryContext{
			ScenarioName: "FileContent",
			ExpectedBehavior: map[string]interface{}{
				"should_return_content": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"file_id":  uploadResponse.ID,
			},
		}

		fileContentRetryConfig := FileContentRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []FileContentRetryCondition{},
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		contentExpectations := ResponseExpectations{
			ShouldHaveLatency: true,
			ProviderSpecific: map[string]interface{}{
				"expected_provider": string(testConfig.Provider),
			},
		}

		response, err := WithFileContentTestRetry(t, fileContentRetryConfig, contentRetryContext, contentExpectations, "FileContent", func() (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
			contentRequest := &schemas.BifrostFileContentRequest{
				Provider:    testConfig.Provider,
				FileID:      uploadResponse.ID,
				ExtraParams: testConfig.FileExtraParams,
			}
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.FileContentRequest(bfCtx, contentRequest)
		})

		if err != nil {
			t.Fatalf("❌ FileContent failed after retries: %v", GetErrorMessage(err))
		}

		if response == nil {
			t.Fatal("❌ FileContent returned nil response after retries")
		}

		if len(response.Content) == 0 {
			t.Fatal("❌ FileContent returned empty content after retries")
		}

		// Verify content matches (optional, as some providers may modify content)
		t.Logf("✅ File Content test passed for provider: %s, file ID: %s, content length: %d bytes", testConfig.Provider, response.FileID, len(response.Content))
	})
}

// RunFileUnsupportedTest tests that unsupported providers return appropriate errors for file operations
func RunFileUnsupportedTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	// Only run this test for providers that don't support any file operations
	if testConfig.Scenarios.FileUpload || testConfig.Scenarios.FileList ||
		testConfig.Scenarios.FileRetrieve || testConfig.Scenarios.FileDelete ||
		testConfig.Scenarios.FileContent {
		return
	}

	t.Run("FileUnsupported", func(t *testing.T) {
		t.Logf("[RUNNING] File Unsupported test for provider: %s", testConfig.Provider)

		// TODO remove this once azure is officially supported
		// We are skipping azure from this for now
		if testConfig.Provider == schemas.Azure {
			t.Skipf("Skipping batch unsupported test for provider: %s", testConfig.Provider)
			return
		}

		// Try to upload a file - should fail with unsupported error
		request := &schemas.BifrostFileUploadRequest{
			Provider: testConfig.Provider,
			File:     []byte("test content"),
			Filename: "test.txt",
			Purpose:  "batch",
		}

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		_, err := client.FileUploadRequest(bfCtx, request)
		if err == nil {
			t.Error("FileUpload should have failed for unsupported provider")
			return
		}

		// Verify it's an unsupported operation error
		if err.Error != nil && (err.Error.Code == nil || *err.Error.Code != "unsupported_operation") {
			t.Errorf("Expected unsupported_operation error, got: %v", err)
			return
		}

		t.Logf("[SUCCESS] File Unsupported test passed for provider: %s - correctly returned unsupported error", testConfig.Provider)
	})
}

// RunFileAndBatchIntegrationTest tests the integration between file upload and batch create
func RunFileAndBatchIntegrationTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	// Skip if file-based batch input is not supported
	if !testConfig.Scenarios.FileBatchInput {
		t.Logf("[SKIPPED] File and Batch Integration: FileBatchInput=%v for provider %s",
			testConfig.Scenarios.FileBatchInput, testConfig.Provider)
		return
	}

	t.Run("FileAndBatchIntegration", func(t *testing.T) {
		t.Logf("[RUNNING] File and Batch Integration test for provider: %s", testConfig.Provider)

		// Step 1: Upload a batch input file
		fileContent := []byte(`{"custom_id": "integration-test-1", "method": "POST", "url": "/v1/chat/completions", "body": {"model": "` + testConfig.ChatModel + `", "messages": [{"role": "user", "content": "Say hello"}]}}
{"custom_id": "integration-test-2", "method": "POST", "url": "/v1/chat/completions", "body": {"model": "` + testConfig.ChatModel + `", "messages": [{"role": "user", "content": "Say goodbye"}]}}
`)

		uploadRequest := &schemas.BifrostFileUploadRequest{
			Provider:    testConfig.Provider,
			File:        fileContent,
			Filename:    "integration_test_batch.jsonl",
			Purpose:     "batch",
			ExtraParams: testConfig.FileExtraParams,
		}

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		uploadResponse, uploadErr := client.FileUploadRequest(bfCtx, uploadRequest)
		if uploadErr != nil {
			if uploadErr.Error != nil && (uploadErr.Error.Code != nil && *uploadErr.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error for upload", testConfig.Provider)
				return
			}
			t.Errorf("FileUpload failed: %v", uploadErr)
			return
		}

		if uploadResponse == nil || uploadResponse.ID == "" {
			t.Error("FileUpload returned invalid response")
			return
		}

		t.Logf("[INFO] File uploaded successfully, ID: %s", uploadResponse.ID)

		// Step 2: Create a batch using the uploaded file
		batchRequest := &schemas.BifrostBatchCreateRequest{
			Provider:         testConfig.Provider,
			Model:            schemas.Ptr(testConfig.ChatModel),
			InputFileID:      uploadResponse.ID,
			Endpoint:         schemas.BatchEndpointChatCompletions,
			CompletionWindow: "24h",
			ExtraParams:      testConfig.BatchExtraParams,
		}

		bfCtx2 := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		batchResponse, batchErr := client.BatchCreateRequest(bfCtx2, batchRequest)
		if batchErr != nil {
			if batchErr.Error != nil && (batchErr.Error.Code != nil && *batchErr.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error for batch create", testConfig.Provider)
				return
			}
			t.Errorf("BatchCreate with file ID failed: %v", batchErr)
			return
		}

		if batchResponse == nil || batchResponse.ID == "" {
			t.Error("BatchCreate returned invalid response")
			return
		}

		t.Logf("[SUCCESS] File and Batch Integration test passed for provider: %s, file ID: %s, batch ID: %s",
			testConfig.Provider, uploadResponse.ID, batchResponse.ID)
	})
}
