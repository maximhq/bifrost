package llmtests

import (
	"context"
	"fmt"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	videoTestPrompt         = "A cinematic aerial shot of mountains at sunrise with soft clouds"
	videoRetrievePollDelay  = 5 * time.Second
	videoCompletionTimeout  = 6 * time.Minute
	videoRetrieveMaxRetries = 6
)

func RunVideoGenerationTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.VideoGeneration {
		t.Logf("Video generation not supported for provider %s", testConfig.Provider)
		return
	}
	if testConfig.VideoGenerationModel == "" {
		t.Logf("Video generation model not configured for provider %s", testConfig.Provider)
		return
	}

	t.Run("VideoGeneration", func(t *testing.T) {
		ShouldRunParallel(t, testConfig, "VideoGeneration")

		resp, err := createVideoJob(client, ctx, testConfig)
		if err != nil {
			t.Fatalf("❌ Video generation failed: %s", GetErrorMessage(err))
		}
		if resp == nil {
			t.Fatal("❌ Video generation response is nil")
		}
		if resp.ID == "" {
			t.Fatal("❌ Video generation returned empty ID")
		}
		if !isValidVideoStatus(resp.Status) {
			t.Fatalf("❌ Video generation returned invalid status: %s", resp.Status)
		}

		if resp.ExtraFields.Provider == "" {
			t.Fatal("❌ Video generation extra_fields.provider is empty")
		}
		if resp.ExtraFields.ModelRequested == "" {
			t.Fatal("❌ Video generation extra_fields.model_requested is empty")
		}

		t.Logf("✅ Video generation created job: id=%s status=%s", resp.ID, resp.Status)
	})
}

func RunVideoRetrieveTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.VideoRetrieve {
		t.Logf("Video retrieve not supported for provider %s", testConfig.Provider)
		return
	}
	if testConfig.VideoGenerationModel == "" {
		t.Logf("Video retrieve skipped: video model not configured for provider %s", testConfig.Provider)
		return
	}

	t.Run("VideoRetrieve", func(t *testing.T) {
		ShouldRunParallel(t, testConfig, "VideoRetrieve")

		created, err := createVideoJob(client, ctx, testConfig)
		if err != nil {
			t.Fatalf("❌ Video generation (for retrieve test) failed: %s", GetErrorMessage(err))
		}
		if created == nil || created.ID == "" {
			t.Fatal("❌ Video generation (for retrieve test) returned invalid response")
		}

		retrieved, err := retrieveVideoWithRetries(client, ctx, testConfig, created.ID)
		if err != nil {
			t.Fatalf("❌ Video retrieve failed: %s", GetErrorMessage(err))
		}
		if retrieved == nil {
			t.Fatal("❌ Video retrieve returned nil response")
		}
		if retrieved.ID == "" {
			t.Fatal("❌ Video retrieve returned empty ID")
		}
		if !isValidVideoStatus(retrieved.Status) {
			t.Fatalf("❌ Video retrieve returned invalid status: %s", retrieved.Status)
		}

		t.Logf("✅ Video retrieve successful: id=%s status=%s", retrieved.ID, retrieved.Status)
	})
}

func RunVideoDownloadTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.VideoDownload {
		t.Logf("Video download not supported for provider %s", testConfig.Provider)
		return
	}
	if testConfig.VideoGenerationModel == "" {
		t.Logf("Video download skipped: video model not configured for provider %s", testConfig.Provider)
		return
	}

	t.Run("VideoDownload", func(t *testing.T) {
		ShouldRunParallel(t, testConfig, "VideoDownload")

		created, err := createVideoJob(client, ctx, testConfig)
		if err != nil {
			t.Fatalf("❌ Video generation (for download test) failed: %s", GetErrorMessage(err))
		}
		if created == nil || created.ID == "" {
			t.Fatal("❌ Video generation (for download test) returned invalid response")
		}

		requireURL := testConfig.Provider == schemas.Runway
		completed, pollErr := waitForVideoCompletion(client, ctx, testConfig, created.ID, requireURL)
		if pollErr != nil {
			t.Fatalf("❌ Video completion polling failed: %s", GetErrorMessage(pollErr))
		}
		if completed == nil {
			t.Fatal("❌ Video completion polling returned nil response")
		}
		if completed.Status != schemas.VideoStatusCompleted {
			t.Fatalf("❌ Video did not complete successfully: status=%s, error=%s", completed.Status, completed.Error.Message)
		}

		downloadReq := &schemas.BifrostVideoDownloadRequest{
			Provider: testConfig.Provider,
			ID:       created.ID,
		}
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		downloadResp, downloadErr := client.VideoDownloadRequest(bfCtx, downloadReq)
		if downloadErr != nil {
			t.Fatalf("❌ Video download failed: %s", GetErrorMessage(downloadErr))
		}
		if downloadResp == nil {
			t.Fatal("❌ Video download returned nil response")
		}
		if len(downloadResp.Content) == 0 {
			t.Fatal("❌ Video download returned empty content")
		}
		if downloadResp.ContentType == "" {
			t.Fatal("❌ Video download returned empty content type")
		}

		t.Logf("✅ Video download successful: id=%s bytes=%d content_type=%s", created.ID, len(downloadResp.Content), downloadResp.ContentType)
	})
}

func RunVideoListTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.VideoList {
		t.Logf("Video list not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("VideoList", func(t *testing.T) {
		ShouldRunParallel(t, testConfig, "VideoList")

		order := "desc"
		limit := 5
		req := &schemas.BifrostVideoListRequest{
			Provider: testConfig.Provider,
			Order:    bifrost.Ptr(order),
			Limit:    bifrost.Ptr(limit),
		}
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		resp, err := client.VideoListRequest(bfCtx, req)
		if err != nil {
			t.Fatalf("❌ Video list failed: %s", GetErrorMessage(err))
		}
		if resp == nil {
			t.Fatal("❌ Video list returned nil response")
		}
		if resp.Object == "" {
			t.Fatal("❌ Video list returned empty object")
		}

		t.Logf("✅ Video list successful: object=%s items=%d", resp.Object, len(resp.Data))
	})
}

func RunVideoDeleteTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.VideoDelete {
		t.Logf("Video delete not supported for provider %s", testConfig.Provider)
		return
	}
	if testConfig.VideoGenerationModel == "" {
		t.Logf("Video delete skipped: video model not configured for provider %s", testConfig.Provider)
		return
	}

	t.Run("VideoDelete", func(t *testing.T) {
		ShouldRunParallel(t, testConfig, "VideoDelete")

		created, err := createVideoJob(client, ctx, testConfig)
		if err != nil {
			t.Fatalf("❌ Video generation (for delete test) failed: %s", GetErrorMessage(err))
		}
		if created == nil || created.ID == "" {
			t.Fatal("❌ Video generation (for delete test) returned invalid response")
		}

		// OpenAI video jobs cannot be deleted while still processing.
		// Wait until the job reaches a terminal state before delete.
		terminalResp, terminalErr := waitForVideoCompletion(client, ctx, testConfig, created.ID, false)
		if terminalErr != nil {
			t.Fatalf("❌ Video terminal-state polling failed before delete: %s", GetErrorMessage(terminalErr))
		}
		if terminalResp == nil {
			t.Fatal("❌ Video terminal-state polling returned nil response")
		}
		if terminalResp.Status == schemas.VideoStatusQueued || terminalResp.Status == schemas.VideoStatusInProgress {
			t.Fatalf("❌ Video is not in terminal state before delete: status=%s", terminalResp.Status)
		}

		deleteReq := &schemas.BifrostVideoDeleteRequest{
			Provider: testConfig.Provider,
			ID:       created.ID,
		}
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		deleteResp, deleteErr := client.VideoDeleteRequest(bfCtx, deleteReq)
		if deleteErr != nil {
			t.Fatalf("❌ Video delete failed: %s", GetErrorMessage(deleteErr))
		}
		if deleteResp == nil {
			t.Fatal("❌ Video delete returned nil response")
		}
		if !deleteResp.Deleted {
			t.Fatal("❌ Video delete returned deleted=false")
		}
		if deleteResp.ID == "" {
			t.Fatal("❌ Video delete returned empty ID")
		}

		t.Logf("✅ Video delete successful: id=%s", deleteResp.ID)
	})
}

func RunVideoUnsupportedTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if testConfig.Scenarios.VideoList || testConfig.Scenarios.VideoDelete {
		return
	}

	t.Run("VideoUnsupported", func(t *testing.T) {
		ShouldRunParallel(t, testConfig, "VideoUnsupported")

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)

		_, listErr := client.VideoListRequest(bfCtx, &schemas.BifrostVideoListRequest{
			Provider: testConfig.Provider,
		})
		if !isUnsupportedOperationError(listErr) {
			t.Fatalf("❌ Expected unsupported_operation for VideoList, got: %s", GetErrorMessage(listErr))
		}

		_, deleteErr := client.VideoDeleteRequest(bfCtx, &schemas.BifrostVideoDeleteRequest{
			Provider: testConfig.Provider,
			ID:       "video_test_id",
		})
		if !isUnsupportedOperationError(deleteErr) {
			t.Fatalf("❌ Expected unsupported_operation for VideoDelete, got: %s", GetErrorMessage(deleteErr))
		}

		t.Logf("✅ Video unsupported behavior verified for provider %s", testConfig.Provider)
	})
}

func createVideoJob(client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	req := &schemas.BifrostVideoGenerationRequest{
		Provider: testConfig.Provider,
		Model:    testConfig.VideoGenerationModel,
		Input: &schemas.VideoGenerationInput{
			Prompt: videoTestPrompt,
		},
		Params: &schemas.VideoGenerationParameters{
			Seconds: bifrost.Ptr("4"),
		},
		Fallbacks: testConfig.Fallbacks,
	}
	bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	return client.VideoGenerationRequest(bfCtx, req)
}

func retrieveVideoWithRetries(client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig, videoID string) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	var lastErr *schemas.BifrostError
	for attempt := 0; attempt < videoRetrieveMaxRetries; attempt++ {
		req := &schemas.BifrostVideoRetrieveRequest{
			Provider: testConfig.Provider,
			ID:       videoID,
		}
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		resp, err := client.VideoRetrieveRequest(bfCtx, req)
		if err == nil && resp != nil {
			return resp, nil
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, &schemas.BifrostError{
		IsBifrostError: true,
		Error: &schemas.ErrorField{
			Message: "video retrieve failed after retries",
		},
	}
}

func waitForVideoCompletion(
	client *bifrost.Bifrost,
	ctx context.Context,
	testConfig ComprehensiveTestConfig,
	videoID string,
	requireURL bool,
) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	deadline := time.Now().Add(videoCompletionTimeout)
	var lastResp *schemas.BifrostVideoGenerationResponse
	var lastErr *schemas.BifrostError

	for time.Now().Before(deadline) {
		req := &schemas.BifrostVideoRetrieveRequest{
			Provider: testConfig.Provider,
			ID:       videoID,
		}
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		resp, err := client.VideoRetrieveRequest(bfCtx, req)
		if err != nil {
			lastErr = err
			time.Sleep(videoRetrievePollDelay)
			continue
		}
		if resp == nil {
			time.Sleep(videoRetrievePollDelay)
			continue
		}

		lastResp = resp
		if resp.Status == schemas.VideoStatusFailed {
			return resp, nil
		}

		if resp.Status == schemas.VideoStatusCompleted {
			if !requireURL || (len(resp.Videos) > 0) {
				return resp, nil
			}
		}

		time.Sleep(videoRetrievePollDelay)
	}

	if lastErr != nil {
		return nil, lastErr
	}
	if lastResp != nil {
		return lastResp, nil
	}

	return nil, &schemas.BifrostError{
		IsBifrostError: true,
		Error: &schemas.ErrorField{
			Message: fmt.Sprintf("timed out waiting for video completion for id %s", videoID),
		},
	}
}

func isValidVideoStatus(status schemas.VideoStatus) bool {
	switch status {
	case schemas.VideoStatusQueued, schemas.VideoStatusInProgress, schemas.VideoStatusCompleted, schemas.VideoStatusFailed:
		return true
	default:
		return false
	}
}

func isUnsupportedOperationError(err *schemas.BifrostError) bool {
	return err != nil && err.Error != nil && err.Error.Code != nil && *err.Error.Code == "unsupported_operation"
}
