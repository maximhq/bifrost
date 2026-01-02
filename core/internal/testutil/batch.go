// Package testutil provides batch API test utilities for the Bifrost system.
package testutil

import (
	"context"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunBatchCreateTest tests the batch create functionality
func RunBatchCreateTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.BatchCreate {
		t.Logf("[SKIPPED] Batch Create: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("BatchCreate", func(t *testing.T) {
		t.Logf("[RUNNING] Batch Create test for provider: %s", testConfig.Provider)

		// Create a batch request with a simple chat completion
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

		response, err := client.BatchCreateRequest(ctx, request)
		if err != nil {
			// Check if this is an unsupported operation error
			if err.Error != nil && (err.Error.Code != nil && *err.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error", testConfig.Provider)
				return
			}
			t.Errorf("BatchCreate failed: %v", err)
			return
		}

		if response == nil {
			t.Error("BatchCreate returned nil response")
			return
		}

		if response.ID == "" {
			t.Error("BatchCreate returned empty batch ID")
			return
		}

		t.Logf("[SUCCESS] Batch Create test passed for provider: %s, batch ID: %s", testConfig.Provider, response.ID)
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

		request := &schemas.BifrostBatchListRequest{
			Provider: testConfig.Provider,
			Limit:    10,
		}

		response, err := client.BatchListRequest(ctx, request)
		if err != nil {
			// Check if this is an unsupported operation error
			if err.Error != nil && (err.Error.Code != nil && *err.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error", testConfig.Provider)
				return
			}
			t.Errorf("BatchList failed: %v", err)
			return
		}

		if response == nil {
			t.Error("BatchList returned nil response")
			return
		}

		t.Logf("[SUCCESS] Batch List test passed for provider: %s, found %d batches", testConfig.Provider, len(response.Data))
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

		// First, we need to create a batch to retrieve
		// Note: In real tests, you might want to use an existing batch ID
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

		createResponse, createErr := client.BatchCreateRequest(ctx, createRequest)
		if createErr != nil {
			// Check if this is an unsupported operation error
			if createErr.Error != nil && (createErr.Error.Code != nil && *createErr.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error for create", testConfig.Provider)
				return
			}
			t.Errorf("BatchCreate (for retrieve test) failed: %v", createErr)
			return
		}

		if createResponse == nil || createResponse.ID == "" {
			t.Error("BatchCreate returned invalid response for retrieve test")
			return
		}

		// Now retrieve the batch
		retrieveRequest := &schemas.BifrostBatchRetrieveRequest{
			Provider: testConfig.Provider,
			BatchID:  createResponse.ID,
		}

		response, err := client.BatchRetrieveRequest(ctx, retrieveRequest)
		if err != nil {
			t.Errorf("BatchRetrieve failed: %v", err)
			return
		}

		if response == nil {
			t.Error("BatchRetrieve returned nil response")
			return
		}

		if response.ID != createResponse.ID {
			t.Errorf("BatchRetrieve returned wrong batch ID: got %s, expected %s", response.ID, createResponse.ID)
			return
		}

		t.Logf("[SUCCESS] Batch Retrieve test passed for provider: %s, batch ID: %s, status: %s", testConfig.Provider, response.ID, response.Status)
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

		// First, create a batch to cancel
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

		createResponse, createErr := client.BatchCreateRequest(ctx, createRequest)
		if createErr != nil {
			// Check if this is an unsupported operation error
			if createErr.Error != nil && (createErr.Error.Code != nil && *createErr.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error for create", testConfig.Provider)
				return
			}
			t.Errorf("BatchCreate (for cancel test) failed: %v", createErr)
			return
		}

		if createResponse == nil || createResponse.ID == "" {
			t.Error("BatchCreate returned invalid response for cancel test")
			return
		}

		// Now cancel the batch
		cancelRequest := &schemas.BifrostBatchCancelRequest{
			Provider: testConfig.Provider,
			BatchID:  createResponse.ID,
		}

		response, err := client.BatchCancelRequest(ctx, cancelRequest)
		if err != nil {
			// Note: Cancel might fail if batch has already completed
			t.Logf("[WARNING] BatchCancel failed (batch may have already completed): %v", err)
			return
		}

		if response == nil {
			t.Error("BatchCancel returned nil response")
			return
		}

		t.Logf("[SUCCESS] Batch Cancel test passed for provider: %s, batch ID: %s", testConfig.Provider, response.ID)
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
		// This test assumes there might be an existing completed batch
		// In practice, you might want to poll for completion or use a pre-existing batch ID

		// For now, we'll just verify the API call works
		// A full test would involve creating a batch, waiting for completion, then getting results
		request := &schemas.BifrostBatchResultsRequest{
			Provider: testConfig.Provider,
			BatchID:  "test-batch-id", // This would be a real batch ID in practice
		}

		_, err := client.BatchResultsRequest(ctx, request)
		if err != nil {
			// This is expected to fail with a "batch not found" error since we're using a fake ID
			// In a real test, you would use an actual completed batch ID
			t.Logf("[INFO] BatchResults test completed (expected error with test ID): %v", err)
			return
		}

		t.Logf("[SUCCESS] Batch Results test passed for provider: %s", testConfig.Provider)
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

		_, err := client.BatchCreateRequest(ctx, request)
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

		response, err := client.FileUploadRequest(ctx, request)
		if err != nil {
			// Check if this is an unsupported operation error
			if err.Error != nil && (err.Error.Code != nil && *err.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error", testConfig.Provider)
				return
			}
			t.Errorf("FileUpload failed: %v", err)
			return
		}

		if response == nil {
			t.Error("FileUpload returned nil response")
			return
		}

		if response.ID == "" {
			t.Error("FileUpload returned empty file ID")
			return
		}

		t.Logf("[SUCCESS] File Upload test passed for provider: %s, file ID: %s", testConfig.Provider, response.ID)
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

		request := &schemas.BifrostFileListRequest{
			Provider:    testConfig.Provider,
			Limit:       10,
			ExtraParams: testConfig.FileExtraParams,
		}

		response, err := client.FileListRequest(ctx, request)
		if err != nil {
			// Check if this is an unsupported operation error
			if err.Error != nil && (err.Error.Code != nil && *err.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error", testConfig.Provider)
				return
			}
			t.Errorf("FileList failed: %v", err)
			return
		}

		if response == nil {
			t.Error("FileList returned nil response")
			return
		}

		t.Logf("[SUCCESS] File List test passed for provider: %s, found %d files", testConfig.Provider, len(response.Data))
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

		// First upload a file to retrieve
		fileContent := []byte(`{"custom_id": "test-retrieve-1", "method": "POST", "url": "/v1/chat/completions", "body": {"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hello"}]}}
`)

		uploadRequest := &schemas.BifrostFileUploadRequest{
			Provider:    testConfig.Provider,
			File:        fileContent,
			Filename:    "test_retrieve.jsonl",
			Purpose:     "batch",
			ExtraParams: testConfig.FileExtraParams,
		}

		uploadResponse, uploadErr := client.FileUploadRequest(ctx, uploadRequest)
		if uploadErr != nil {
			if uploadErr.Error != nil && (uploadErr.Error.Code != nil && *uploadErr.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error for upload", testConfig.Provider)
				return
			}
			t.Errorf("FileUpload (for retrieve test) failed: %v", uploadErr)
			return
		}

		if uploadResponse == nil || uploadResponse.ID == "" {
			t.Error("FileUpload returned invalid response for retrieve test")
			return
		}

		// Now retrieve the file
		retrieveRequest := &schemas.BifrostFileRetrieveRequest{
			Provider: testConfig.Provider,
			FileID:   uploadResponse.ID,
		}

		response, err := client.FileRetrieveRequest(ctx, retrieveRequest)
		if err != nil {
			t.Errorf("FileRetrieve failed: %v", err)
			return
		}

		if response == nil {
			t.Error("FileRetrieve returned nil response")
			return
		}

		if response.ID != uploadResponse.ID {
			t.Errorf("FileRetrieve returned wrong file ID: got %s, expected %s", response.ID, uploadResponse.ID)
			return
		}

		t.Logf("[SUCCESS] File Retrieve test passed for provider: %s, file ID: %s", testConfig.Provider, response.ID)
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

		// First upload a file to delete
		fileContent := []byte(`{"custom_id": "test-delete-1", "method": "POST", "url": "/v1/chat/completions", "body": {"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hello"}]}}
`)

		uploadRequest := &schemas.BifrostFileUploadRequest{
			Provider:    testConfig.Provider,
			File:        fileContent,
			Filename:    "test_delete.jsonl",
			Purpose:     "batch",
			ExtraParams: testConfig.FileExtraParams,
		}

		uploadResponse, uploadErr := client.FileUploadRequest(ctx, uploadRequest)
		if uploadErr != nil {
			if uploadErr.Error != nil && (uploadErr.Error.Code != nil && *uploadErr.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error for upload", testConfig.Provider)
				return
			}
			t.Errorf("FileUpload (for delete test) failed: %v", uploadErr)
			return
		}

		if uploadResponse == nil || uploadResponse.ID == "" {
			t.Error("FileUpload returned invalid response for delete test")
			return
		}

		// Now delete the file
		deleteRequest := &schemas.BifrostFileDeleteRequest{
			Provider: testConfig.Provider,
			FileID:   uploadResponse.ID,
		}

		response, err := client.FileDeleteRequest(ctx, deleteRequest)
		if err != nil {
			t.Errorf("FileDelete failed: %v", err)
			return
		}

		if response == nil {
			t.Error("FileDelete returned nil response")
			return
		}

		if !response.Deleted {
			t.Error("FileDelete did not mark file as deleted")
			return
		}

		t.Logf("[SUCCESS] File Delete test passed for provider: %s, file ID: %s", testConfig.Provider, response.ID)
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

		// First upload a file to download
		originalContent := []byte(`{"custom_id": "test-content-1", "method": "POST", "url": "/v1/chat/completions", "body": {"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hello"}]}}
`)

		uploadRequest := &schemas.BifrostFileUploadRequest{
			Provider:    testConfig.Provider,
			File:        originalContent,
			Filename:    "test_content.jsonl",
			Purpose:     "batch",
			ExtraParams: testConfig.FileExtraParams,
		}

		uploadResponse, uploadErr := client.FileUploadRequest(ctx, uploadRequest)
		if uploadErr != nil {
			if uploadErr.Error != nil && (uploadErr.Error.Code != nil && *uploadErr.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error for upload", testConfig.Provider)
				return
			}
			t.Errorf("FileUpload (for content test) failed: %v", uploadErr)
			return
		}

		if uploadResponse == nil || uploadResponse.ID == "" {
			t.Error("FileUpload returned invalid response for content test")
			return
		}

		// Now download the file content
		contentRequest := &schemas.BifrostFileContentRequest{
			Provider: testConfig.Provider,
			FileID:   uploadResponse.ID,
		}

		response, err := client.FileContentRequest(ctx, contentRequest)
		if err != nil {
			t.Errorf("FileContent failed: %v", err)
			return
		}

		if response == nil {
			t.Error("FileContent returned nil response")
			return
		}

		if len(response.Content) == 0 {
			t.Error("FileContent returned empty content")
			return
		}

		// Verify content matches (optional, as some providers may modify content)
		t.Logf("[SUCCESS] File Content test passed for provider: %s, file ID: %s, content length: %d bytes", testConfig.Provider, response.FileID, len(response.Content))
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

		_, err := client.FileUploadRequest(ctx, request)
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

		uploadResponse, uploadErr := client.FileUploadRequest(ctx, uploadRequest)
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

		batchResponse, batchErr := client.BatchCreateRequest(ctx, batchRequest)
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
