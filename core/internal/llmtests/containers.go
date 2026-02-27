// Package llmtests provides container API test utilities for the Bifrost system.
package llmtests

import (
	"context"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunContainerCreateTest tests the container create functionality
func RunContainerCreateTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ContainerCreate {
		t.Logf("[SKIPPED] Container Create: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("ContainerCreate", func(t *testing.T) {
		t.Logf("[RUNNING] Container Create test for provider: %s", testConfig.Provider)

		request := &schemas.BifrostContainerCreateRequest{
			Provider: testConfig.Provider,
			Name:     "bifrost-test-container",
		}

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		response, err := client.ContainerCreateRequest(bfCtx, request)

		if err != nil {
			// Check if this is an unsupported operation error
			if err.Error != nil && (err.Error.Code != nil && *err.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error", testConfig.Provider)
				return
			}
			t.Fatalf("❌ ContainerCreate failed: %v", GetErrorMessage(err))
		}

		if response == nil {
			t.Fatal("❌ ContainerCreate returned nil response")
		}

		if response.ID == "" {
			t.Fatal("❌ ContainerCreate returned empty container ID")
		}

		t.Logf("✅ Container Create test passed for provider: %s, container ID: %s", testConfig.Provider, response.ID)

		// Clean up: delete the created container
		deleteRequest := &schemas.BifrostContainerDeleteRequest{
			Provider:    testConfig.Provider,
			ContainerID: response.ID,
		}
		_, deleteErr := client.ContainerDeleteRequest(bfCtx, deleteRequest)
		if deleteErr != nil {
			t.Logf("[WARNING] Failed to clean up container %s: %v", response.ID, GetErrorMessage(deleteErr))
		}
	})
}

// RunContainerListTest tests the container list functionality
func RunContainerListTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ContainerList {
		t.Logf("[SKIPPED] Container List: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("ContainerList", func(t *testing.T) {
		t.Logf("[RUNNING] Container List test for provider: %s", testConfig.Provider)

		request := &schemas.BifrostContainerListRequest{
			Provider: testConfig.Provider,
			Limit:    10,
		}

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		response, err := client.ContainerListRequest(bfCtx, request)

		if err != nil {
			// Check if this is an unsupported operation error
			if err.Error != nil && (err.Error.Code != nil && *err.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error", testConfig.Provider)
				return
			}
			t.Fatalf("❌ ContainerList failed: %v", GetErrorMessage(err))
		}

		if response == nil {
			t.Fatal("❌ ContainerList returned nil response")
		}

		t.Logf("✅ Container List test passed for provider: %s, found %d containers", testConfig.Provider, len(response.Data))
	})
}

// RunContainerRetrieveTest tests the container retrieve functionality
func RunContainerRetrieveTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ContainerRetrieve {
		t.Logf("[SKIPPED] Container Retrieve: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("ContainerRetrieve", func(t *testing.T) {
		t.Logf("[RUNNING] Container Retrieve test for provider: %s", testConfig.Provider)

		// First, create a container to retrieve
		createRequest := &schemas.BifrostContainerCreateRequest{
			Provider: testConfig.Provider,
			Name:     "bifrost-test-container-retrieve",
		}

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		createResponse, createErr := client.ContainerCreateRequest(bfCtx, createRequest)

		if createErr != nil {
			if createErr.Error != nil && (createErr.Error.Code != nil && *createErr.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error", testConfig.Provider)
				return
			}
			t.Fatalf("❌ ContainerCreate (setup) failed: %v", GetErrorMessage(createErr))
		}

		if createResponse == nil || createResponse.ID == "" {
			t.Fatal("❌ ContainerCreate (setup) returned nil or empty response")
		}

		containerID := createResponse.ID
		defer func() {
			// Clean up
			deleteRequest := &schemas.BifrostContainerDeleteRequest{
				Provider:    testConfig.Provider,
				ContainerID: containerID,
			}
			_, _ = client.ContainerDeleteRequest(bfCtx, deleteRequest)
		}()

		// Now retrieve the container
		retrieveRequest := &schemas.BifrostContainerRetrieveRequest{
			Provider:    testConfig.Provider,
			ContainerID: containerID,
		}

		response, err := client.ContainerRetrieveRequest(bfCtx, retrieveRequest)

		if err != nil {
			t.Fatalf("❌ ContainerRetrieve failed: %v", GetErrorMessage(err))
		}

		if response == nil {
			t.Fatal("❌ ContainerRetrieve returned nil response")
		}

		if response.ID != containerID {
			t.Fatalf("❌ ContainerRetrieve returned wrong container ID: expected %s, got %s", containerID, response.ID)
		}

		t.Logf("✅ Container Retrieve test passed for provider: %s, container ID: %s", testConfig.Provider, response.ID)
	})
}

// RunContainerDeleteTest tests the container delete functionality
func RunContainerDeleteTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ContainerDelete {
		t.Logf("[SKIPPED] Container Delete: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("ContainerDelete", func(t *testing.T) {
		t.Logf("[RUNNING] Container Delete test for provider: %s", testConfig.Provider)

		// First, create a container to delete
		createRequest := &schemas.BifrostContainerCreateRequest{
			Provider: testConfig.Provider,
			Name:     "bifrost-test-container-delete",
		}

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		createResponse, createErr := client.ContainerCreateRequest(bfCtx, createRequest)

		if createErr != nil {
			if createErr.Error != nil && (createErr.Error.Code != nil && *createErr.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error", testConfig.Provider)
				return
			}
			t.Fatalf("❌ ContainerCreate (setup) failed: %v", GetErrorMessage(createErr))
		}

		if createResponse == nil || createResponse.ID == "" {
			t.Fatal("❌ ContainerCreate (setup) returned nil or empty response")
		}

		containerID := createResponse.ID

		// Now delete the container
		deleteRequest := &schemas.BifrostContainerDeleteRequest{
			Provider:    testConfig.Provider,
			ContainerID: containerID,
		}

		response, err := client.ContainerDeleteRequest(bfCtx, deleteRequest)

		if err != nil {
			t.Fatalf("❌ ContainerDelete failed: %v", GetErrorMessage(err))
		}

		if response == nil {
			t.Fatal("❌ ContainerDelete returned nil response")
		}

		if !response.Deleted {
			t.Fatal("❌ ContainerDelete returned deleted=false")
		}

		t.Logf("✅ Container Delete test passed for provider: %s, container ID: %s", testConfig.Provider, containerID)
	})
}

// RunContainerUnsupportedTest tests that providers correctly return unsupported operation errors
func RunContainerUnsupportedTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	// Only run this test if none of the container operations are supported
	if testConfig.Scenarios.ContainerCreate || testConfig.Scenarios.ContainerList ||
		testConfig.Scenarios.ContainerRetrieve || testConfig.Scenarios.ContainerDelete {
		t.Logf("[SKIPPED] Container Unsupported: Provider %s supports container operations", testConfig.Provider)
		return
	}

	t.Run("ContainerUnsupported", func(t *testing.T) {
		t.Logf("[RUNNING] Container Unsupported test for provider: %s", testConfig.Provider)

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)

		// Test ContainerCreate returns unsupported
		createRequest := &schemas.BifrostContainerCreateRequest{
			Provider: testConfig.Provider,
			Name:     "test-container",
		}
		_, createErr := client.ContainerCreateRequest(bfCtx, createRequest)

		if createErr == nil {
			t.Fatal("❌ Expected unsupported operation error for ContainerCreate, got nil")
		}

		if createErr.Error == nil || createErr.Error.Code == nil || *createErr.Error.Code != "unsupported_operation" {
			t.Fatalf("❌ Expected unsupported_operation error code, got: %v", createErr)
		}

		t.Logf("✅ Container Unsupported test passed for provider: %s", testConfig.Provider)
	})
}

// =============================================================================
// CONTAINER FILES API TESTS
// =============================================================================

// RunContainerFileCreateTest tests the container file create functionality
func RunContainerFileCreateTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ContainerFileCreate {
		t.Logf("[SKIPPED] Container File Create: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("ContainerFileCreate", func(t *testing.T) {
		t.Logf("[RUNNING] Container File Create test for provider: %s", testConfig.Provider)

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)

		// First, create a container to hold the file
		containerRequest := &schemas.BifrostContainerCreateRequest{
			Provider: testConfig.Provider,
			Name:     "bifrost-test-container-file-create",
		}

		containerResponse, containerErr := client.ContainerCreateRequest(bfCtx, containerRequest)
		if containerErr != nil {
			if containerErr.Error != nil && (containerErr.Error.Code != nil && *containerErr.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error for container creation", testConfig.Provider)
				return
			}
			t.Fatalf("❌ ContainerCreate (setup) failed: %v", GetErrorMessage(containerErr))
		}

		if containerResponse == nil || containerResponse.ID == "" {
			t.Fatal("❌ ContainerCreate (setup) returned nil or empty response")
		}

		containerID := containerResponse.ID
		defer func() {
			// Clean up container
			deleteRequest := &schemas.BifrostContainerDeleteRequest{
				Provider:    testConfig.Provider,
				ContainerID: containerID,
			}
			_, _ = client.ContainerDeleteRequest(bfCtx, deleteRequest)
		}()

		// Create a file in the container
		testContent := []byte("Hello, Bifrost! This is a test file for container file operations.")
		filePath := "/test-file.txt"

		fileCreateRequest := &schemas.BifrostContainerFileCreateRequest{
			Provider:    testConfig.Provider,
			ContainerID: containerID,
			File:        testContent,
			Path:        &filePath,
		}

		response, err := client.ContainerFileCreateRequest(bfCtx, fileCreateRequest)

		if err != nil {
			if err.Error != nil && (err.Error.Code != nil && *err.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error", testConfig.Provider)
				return
			}
			t.Fatalf("❌ ContainerFileCreate failed: %v", GetErrorMessage(err))
		}

		if response == nil {
			t.Fatal("❌ ContainerFileCreate returned nil response")
		}

		if response.ID == "" {
			t.Fatal("❌ ContainerFileCreate returned empty file ID")
		}

		if response.ContainerID != containerID {
			t.Fatalf("❌ ContainerFileCreate returned wrong container ID: expected %s, got %s", containerID, response.ContainerID)
		}

		t.Logf("✅ Container File Create test passed for provider: %s, file ID: %s", testConfig.Provider, response.ID)

		// Clean up file
		fileDeleteRequest := &schemas.BifrostContainerFileDeleteRequest{
			Provider:    testConfig.Provider,
			ContainerID: containerID,
			FileID:      response.ID,
		}
		_, _ = client.ContainerFileDeleteRequest(bfCtx, fileDeleteRequest)
	})
}

// RunContainerFileListTest tests the container file list functionality
func RunContainerFileListTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ContainerFileList {
		t.Logf("[SKIPPED] Container File List: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("ContainerFileList", func(t *testing.T) {
		t.Logf("[RUNNING] Container File List test for provider: %s", testConfig.Provider)

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)

		// First, create a container
		containerRequest := &schemas.BifrostContainerCreateRequest{
			Provider: testConfig.Provider,
			Name:     "bifrost-test-container-file-list",
		}

		containerResponse, containerErr := client.ContainerCreateRequest(bfCtx, containerRequest)
		if containerErr != nil {
			if containerErr.Error != nil && (containerErr.Error.Code != nil && *containerErr.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error for container creation", testConfig.Provider)
				return
			}
			t.Fatalf("❌ ContainerCreate (setup) failed: %v", GetErrorMessage(containerErr))
		}

		if containerResponse == nil || containerResponse.ID == "" {
			t.Fatal("❌ ContainerCreate (setup) returned nil or empty response")
		}

		containerID := containerResponse.ID
		defer func() {
			// Clean up container
			deleteRequest := &schemas.BifrostContainerDeleteRequest{
				Provider:    testConfig.Provider,
				ContainerID: containerID,
			}
			_, _ = client.ContainerDeleteRequest(bfCtx, deleteRequest)
		}()

		// Create a file in the container first
		testContent := []byte("Test content for file list")
		filePath := "/test-file-list.txt"

		fileCreateRequest := &schemas.BifrostContainerFileCreateRequest{
			Provider:    testConfig.Provider,
			ContainerID: containerID,
			File:        testContent,
			Path:        &filePath,
		}

	fileCreateResponse, fileCreateErr := client.ContainerFileCreateRequest(bfCtx, fileCreateRequest)
	if fileCreateErr != nil {
		if fileCreateErr.Error != nil && (fileCreateErr.Error.Code != nil && *fileCreateErr.Error.Code == "unsupported_operation") {
			t.Logf("[EXPECTED] Provider %s returned unsupported operation error for file creation", testConfig.Provider)
			return
		}
		t.Fatalf("❌ ContainerFileCreate (setup) failed: %v", GetErrorMessage(fileCreateErr))
	}

	if fileCreateResponse == nil {
		t.Fatal("❌ ContainerFileCreate (setup) returned nil response with no error")
	}

	fileID := fileCreateResponse.ID
	defer func() {
		// Clean up file
		fileDeleteRequest := &schemas.BifrostContainerFileDeleteRequest{
			Provider:    testConfig.Provider,
			ContainerID: containerID,
			FileID:      fileID,
		}
		_, _ = client.ContainerFileDeleteRequest(bfCtx, fileDeleteRequest)
	}()

	// Now list files in the container
		listRequest := &schemas.BifrostContainerFileListRequest{
			Provider:    testConfig.Provider,
			ContainerID: containerID,
			Limit:       10,
		}

		response, err := client.ContainerFileListRequest(bfCtx, listRequest)

		if err != nil {
			if err.Error != nil && (err.Error.Code != nil && *err.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error", testConfig.Provider)
				return
			}
			t.Fatalf("❌ ContainerFileList failed: %v", GetErrorMessage(err))
		}

		if response == nil {
			t.Fatal("❌ ContainerFileList returned nil response")
		}

		if len(response.Data) == 0 {
			t.Fatal("❌ ContainerFileList returned empty list, expected at least one file")
		}

		t.Logf("✅ Container File List test passed for provider: %s, found %d files", testConfig.Provider, len(response.Data))
	})
}

// RunContainerFileRetrieveTest tests the container file retrieve functionality
func RunContainerFileRetrieveTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ContainerFileRetrieve {
		t.Logf("[SKIPPED] Container File Retrieve: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("ContainerFileRetrieve", func(t *testing.T) {
		t.Logf("[RUNNING] Container File Retrieve test for provider: %s", testConfig.Provider)

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)

		// First, create a container
		containerRequest := &schemas.BifrostContainerCreateRequest{
			Provider: testConfig.Provider,
			Name:     "bifrost-test-container-file-retrieve",
		}

		containerResponse, containerErr := client.ContainerCreateRequest(bfCtx, containerRequest)
		if containerErr != nil {
			if containerErr.Error != nil && (containerErr.Error.Code != nil && *containerErr.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error for container creation", testConfig.Provider)
				return
			}
			t.Fatalf("❌ ContainerCreate (setup) failed: %v", GetErrorMessage(containerErr))
		}

		if containerResponse == nil || containerResponse.ID == "" {
			t.Fatal("❌ ContainerCreate (setup) returned nil or empty response")
		}

		containerID := containerResponse.ID
		defer func() {
			// Clean up container
			deleteRequest := &schemas.BifrostContainerDeleteRequest{
				Provider:    testConfig.Provider,
				ContainerID: containerID,
			}
			_, _ = client.ContainerDeleteRequest(bfCtx, deleteRequest)
		}()

	// Create a file in the container
	testContent := []byte("Test content for file retrieve")
	filePath := "/test-file-retrieve.txt"

	fileCreateRequest := &schemas.BifrostContainerFileCreateRequest{
		Provider:    testConfig.Provider,
		ContainerID: containerID,
		File:        testContent,
		Path:        &filePath,
	}

	fileCreateResponse, fileCreateErr := client.ContainerFileCreateRequest(bfCtx, fileCreateRequest)
	if fileCreateErr != nil {
		if fileCreateErr.Error != nil && (fileCreateErr.Error.Code != nil && *fileCreateErr.Error.Code == "unsupported_operation") {
			t.Logf("[EXPECTED] Provider %s returned unsupported operation error for file creation", testConfig.Provider)
			return
		}
		t.Fatalf("❌ ContainerFileCreate (setup) failed: %v", GetErrorMessage(fileCreateErr))
	}

	if fileCreateResponse == nil {
		t.Fatal("❌ ContainerFileCreate (setup) returned nil response with no error")
	}

	fileID := fileCreateResponse.ID
		defer func() {
			// Clean up file
			fileDeleteRequest := &schemas.BifrostContainerFileDeleteRequest{
				Provider:    testConfig.Provider,
				ContainerID: containerID,
				FileID:      fileID,
			}
			_, _ = client.ContainerFileDeleteRequest(bfCtx, fileDeleteRequest)
		}()

		// Now retrieve the file
		retrieveRequest := &schemas.BifrostContainerFileRetrieveRequest{
			Provider:    testConfig.Provider,
			ContainerID: containerID,
			FileID:      fileID,
		}

		response, err := client.ContainerFileRetrieveRequest(bfCtx, retrieveRequest)

		if err != nil {
			if err.Error != nil && (err.Error.Code != nil && *err.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error", testConfig.Provider)
				return
			}
			t.Fatalf("❌ ContainerFileRetrieve failed: %v", GetErrorMessage(err))
		}

		if response == nil {
			t.Fatal("❌ ContainerFileRetrieve returned nil response")
		}

		if response.ID != fileID {
			t.Fatalf("❌ ContainerFileRetrieve returned wrong file ID: expected %s, got %s", fileID, response.ID)
		}

		if response.ContainerID != containerID {
			t.Fatalf("❌ ContainerFileRetrieve returned wrong container ID: expected %s, got %s", containerID, response.ContainerID)
		}

		t.Logf("✅ Container File Retrieve test passed for provider: %s, file ID: %s", testConfig.Provider, response.ID)
	})
}

// RunContainerFileContentTest tests the container file content functionality
func RunContainerFileContentTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ContainerFileContent {
		t.Logf("[SKIPPED] Container File Content: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("ContainerFileContent", func(t *testing.T) {
		t.Logf("[RUNNING] Container File Content test for provider: %s", testConfig.Provider)

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)

		// First, create a container
		containerRequest := &schemas.BifrostContainerCreateRequest{
			Provider: testConfig.Provider,
			Name:     "bifrost-test-container-file-content",
		}

		containerResponse, containerErr := client.ContainerCreateRequest(bfCtx, containerRequest)
		if containerErr != nil {
			if containerErr.Error != nil && (containerErr.Error.Code != nil && *containerErr.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error for container creation", testConfig.Provider)
				return
			}
			t.Fatalf("❌ ContainerCreate (setup) failed: %v", GetErrorMessage(containerErr))
		}

		if containerResponse == nil || containerResponse.ID == "" {
			t.Fatal("❌ ContainerCreate (setup) returned nil or empty response")
		}

		containerID := containerResponse.ID
		defer func() {
			// Clean up container
			deleteRequest := &schemas.BifrostContainerDeleteRequest{
				Provider:    testConfig.Provider,
				ContainerID: containerID,
			}
			_, _ = client.ContainerDeleteRequest(bfCtx, deleteRequest)
		}()

	// Create a file in the container with known content
	testContent := []byte("Hello, Bifrost! This is test content for file content retrieval.")
	filePath := "/test-file-content.txt"

	fileCreateRequest := &schemas.BifrostContainerFileCreateRequest{
		Provider:    testConfig.Provider,
		ContainerID: containerID,
		File:        testContent,
		Path:        &filePath,
	}

	fileCreateResponse, fileCreateErr := client.ContainerFileCreateRequest(bfCtx, fileCreateRequest)
	if fileCreateErr != nil {
		if fileCreateErr.Error != nil && (fileCreateErr.Error.Code != nil && *fileCreateErr.Error.Code == "unsupported_operation") {
			t.Logf("[EXPECTED] Provider %s returned unsupported operation error for file creation", testConfig.Provider)
			return
		}
		t.Fatalf("❌ ContainerFileCreate (setup) failed: %v", GetErrorMessage(fileCreateErr))
	}

	if fileCreateResponse == nil {
		t.Fatal("❌ ContainerFileCreate (setup) returned nil response with no error")
	}

	fileID := fileCreateResponse.ID
		defer func() {
			// Clean up file
			fileDeleteRequest := &schemas.BifrostContainerFileDeleteRequest{
				Provider:    testConfig.Provider,
				ContainerID: containerID,
				FileID:      fileID,
			}
			_, _ = client.ContainerFileDeleteRequest(bfCtx, fileDeleteRequest)
		}()

		// Now retrieve the file content
		contentRequest := &schemas.BifrostContainerFileContentRequest{
			Provider:    testConfig.Provider,
			ContainerID: containerID,
			FileID:      fileID,
		}

		response, err := client.ContainerFileContentRequest(bfCtx, contentRequest)

		if err != nil {
			if err.Error != nil && (err.Error.Code != nil && *err.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error", testConfig.Provider)
				return
			}
			t.Fatalf("❌ ContainerFileContent failed: %v", GetErrorMessage(err))
		}

		if response == nil {
			t.Fatal("❌ ContainerFileContent returned nil response")
		}

		if len(response.Content) == 0 {
			t.Fatal("❌ ContainerFileContent returned empty content")
		}

		// Verify content matches what we uploaded
		if string(response.Content) != string(testContent) {
			t.Fatalf("❌ ContainerFileContent returned wrong content: expected %q, got %q", string(testContent), string(response.Content))
		}

		t.Logf("✅ Container File Content test passed for provider: %s, content length: %d bytes", testConfig.Provider, len(response.Content))
	})
}

// RunContainerFileDeleteTest tests the container file delete functionality
func RunContainerFileDeleteTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ContainerFileDelete {
		t.Logf("[SKIPPED] Container File Delete: Not supported by provider %s", testConfig.Provider)
		return
	}

	t.Run("ContainerFileDelete", func(t *testing.T) {
		t.Logf("[RUNNING] Container File Delete test for provider: %s", testConfig.Provider)

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)

		// First, create a container
		containerRequest := &schemas.BifrostContainerCreateRequest{
			Provider: testConfig.Provider,
			Name:     "bifrost-test-container-file-delete",
		}

		containerResponse, containerErr := client.ContainerCreateRequest(bfCtx, containerRequest)
		if containerErr != nil {
			if containerErr.Error != nil && (containerErr.Error.Code != nil && *containerErr.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error for container creation", testConfig.Provider)
				return
			}
			t.Fatalf("❌ ContainerCreate (setup) failed: %v", GetErrorMessage(containerErr))
		}

		if containerResponse == nil || containerResponse.ID == "" {
			t.Fatal("❌ ContainerCreate (setup) returned nil or empty response")
		}

		containerID := containerResponse.ID
		defer func() {
			// Clean up container
			deleteRequest := &schemas.BifrostContainerDeleteRequest{
				Provider:    testConfig.Provider,
				ContainerID: containerID,
			}
			_, _ = client.ContainerDeleteRequest(bfCtx, deleteRequest)
		}()

	// Create a file in the container
	testContent := []byte("Test content for file delete")
	filePath := "/test-file-delete.txt"

	fileCreateRequest := &schemas.BifrostContainerFileCreateRequest{
		Provider:    testConfig.Provider,
		ContainerID: containerID,
		File:        testContent,
		Path:        &filePath,
	}

	fileCreateResponse, fileCreateErr := client.ContainerFileCreateRequest(bfCtx, fileCreateRequest)
	if fileCreateErr != nil {
		if fileCreateErr.Error != nil && (fileCreateErr.Error.Code != nil && *fileCreateErr.Error.Code == "unsupported_operation") {
			t.Logf("[EXPECTED] Provider %s returned unsupported operation error for file creation", testConfig.Provider)
			return
		}
		t.Fatalf("❌ ContainerFileCreate (setup) failed: %v", GetErrorMessage(fileCreateErr))
	}

	if fileCreateResponse == nil {
		t.Fatal("❌ ContainerFileCreate (setup) returned nil response with no error")
	}

	fileID := fileCreateResponse.ID

	// Now delete the file
		deleteRequest := &schemas.BifrostContainerFileDeleteRequest{
			Provider:    testConfig.Provider,
			ContainerID: containerID,
			FileID:      fileID,
		}

		response, err := client.ContainerFileDeleteRequest(bfCtx, deleteRequest)

		if err != nil {
			if err.Error != nil && (err.Error.Code != nil && *err.Error.Code == "unsupported_operation") {
				t.Logf("[EXPECTED] Provider %s returned unsupported operation error", testConfig.Provider)
				return
			}
			t.Fatalf("❌ ContainerFileDelete failed: %v", GetErrorMessage(err))
		}

		if response == nil {
			t.Fatal("❌ ContainerFileDelete returned nil response")
		}

		if !response.Deleted {
			t.Fatal("❌ ContainerFileDelete returned deleted=false")
		}

		t.Logf("✅ Container File Delete test passed for provider: %s, file ID: %s", testConfig.Provider, fileID)
	})
}

// RunContainerFileUnsupportedTest tests that providers correctly return unsupported operation errors for container file operations
func RunContainerFileUnsupportedTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	// Only run this test if none of the container file operations are supported
	if testConfig.Scenarios.ContainerFileCreate || testConfig.Scenarios.ContainerFileList ||
		testConfig.Scenarios.ContainerFileRetrieve || testConfig.Scenarios.ContainerFileContent ||
		testConfig.Scenarios.ContainerFileDelete {
		t.Logf("[SKIPPED] Container File Unsupported: Provider %s supports container file operations", testConfig.Provider)
		return
	}

	// Also skip if container operations themselves are not supported (can't test file ops without containers)
	if !testConfig.Scenarios.ContainerCreate {
		t.Logf("[SKIPPED] Container File Unsupported: Provider %s does not support container operations", testConfig.Provider)
		return
	}

	t.Run("ContainerFileUnsupported", func(t *testing.T) {
		t.Logf("[RUNNING] Container File Unsupported test for provider: %s", testConfig.Provider)

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)

		// Test ContainerFileCreate returns unsupported
		testContent := []byte("Test content")
		filePath := "/test.txt"
		createRequest := &schemas.BifrostContainerFileCreateRequest{
			Provider:    testConfig.Provider,
			ContainerID: "test-container-id",
			File:        testContent,
			Path:        &filePath,
		}
		_, createErr := client.ContainerFileCreateRequest(bfCtx, createRequest)

		if createErr == nil {
			t.Fatal("❌ Expected unsupported operation error for ContainerFileCreate, got nil")
		}

		if createErr.Error == nil || createErr.Error.Code == nil || *createErr.Error.Code != "unsupported_operation" {
			t.Fatalf("❌ Expected unsupported_operation error code, got: %v", createErr)
		}

		t.Logf("✅ Container File Unsupported test passed for provider: %s", testConfig.Provider)
	})
}
