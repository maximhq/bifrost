package runware_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/runware"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestRunware(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("RUNWARE_API_KEY")) == "" {
		t.Skip("Skipping Runware tests because RUNWARE_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider: schemas.Runware,
		// Text inference runs through Runware's OpenAI-compatible /v1/chat/completions endpoint.
		ChatModel:      "anthropic:claude@haiku-4.5",
		VisionModel:    "google:gemini@3-flash", // Anthropic models reject image parts through Runware
		ReasoningModel: "minimax:m2.7@0",
		// Runware rejects a function schema whose properties object is empty or absent, unlike
		// OpenAI, so the empty/nil-schema tool tests cannot pass against it.
		SkipEmptyToolSchemas: true,
		ImageGenerationModel: "runware:101@1",
		ImageEditModel:       "runware:102@1",             // FLUX Fill: supports seedImage + maskImage (inpainting)
		VideoGenerationModel: "klingai:kling-video@3-pro", // set a video model; flip the scenarios below on to exercise it
		Scenarios: llmtests.TestScenarios{
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			// Runware returns several tool calls on the Chat Completions stream, but the
			// chat-to-responses stream conversion only reads delta.ToolCalls[0], so the Responses
			// stream surfaces one. Same reason Wafer leaves this off.
			MultipleToolCallsStreaming: false,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			ImageURL:                   true,
			ImageBase64:                true,
			MultipleImages:             true,
			CompleteEnd2End:            true,
			Reasoning:                  true,
			ListModels:                 true,
			ImageGeneration:            true,
			ImageEdit:                  true,
			VideoGeneration:            false,
			VideoRetrieve:              false,
			VideoDownload:              false,
		},
	}

	t.Run("RunwareTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// runwareTask is the subset of a task object the fake server inspects.
type runwareTask struct {
	TaskType string `json:"taskType"`
	TaskUUID string `json:"taskUUID"`
}

// newFakeRunware serves one canned reply per request, in order, substituting the taskUUID the first
// task carried for {{uuid}}. It records every task it received.
func newFakeRunware(t *testing.T, replies ...string) (*httptest.Server, func() []runwareTask) {
	t.Helper()
	var mu sync.Mutex
	var received []runwareTask
	var taskUUID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var tasks []runwareTask
		if err := json.NewDecoder(r.Body).Decode(&tasks); err != nil || len(tasks) != 1 {
			t.Errorf("expected a single-task array, got %v (err %v)", tasks, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if taskUUID == "" {
			taskUUID = tasks[0].TaskUUID
		}
		received = append(received, tasks[0])
		if len(received) > len(replies) {
			t.Errorf("unexpected request %d: %+v", len(received), tasks[0])
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.ReplaceAll(replies[len(received)-1], "{{uuid}}", taskUUID)))
	}))
	t.Cleanup(server.Close)
	return server, func() []runwareTask {
		mu.Lock()
		defer mu.Unlock()
		return append([]runwareTask(nil), received...)
	}
}

func newTestRunwareProvider(t *testing.T, baseURL string, timeoutSeconds int) *runware.RunwareProvider {
	t.Helper()
	provider, err := runware.NewRunwareProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        baseURL,
			DefaultRequestTimeoutInSeconds: timeoutSeconds,
			AllowPrivateNetwork:            true,
		},
	}, bifrost.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewRunwareProvider: %v", err)
	}
	return provider
}

func runUpscale(t *testing.T, provider *runware.RunwareProvider, n *int) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	t.Helper()
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return provider.ImageEdit(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostImageEditRequest{
		Provider: schemas.Runware,
		Model:    "topazlabs:wonder@3.5",
		Input:    &schemas.ImageEditInput{Images: []schemas.ImageInput{{URL: "https://example.com/in.png"}}},
		Params:   &schemas.ImageEditParameters{Type: new("upscale"), N: n},
	})
}

// A sync task still running at Runware's 120s cutoff comes back as a processing row and continues
// as an async task. The provider polls it with getResponse under the same taskUUID and returns the
// finished image, instead of a 200 with an empty data[0].
func TestImageEdit_PollsLongRunningSyncTask(t *testing.T) {
	t.Parallel()
	server, received := newFakeRunware(t,
		`{"data":[{"taskType":"upscale","taskUUID":"{{uuid}}","status":"processing"}]}`,
		`{"data":[{"taskType":"upscale","taskUUID":"{{uuid}}","status":"processing","progress":47}]}`,
		`{"data":[{"taskType":"upscale","taskUUID":"{{uuid}}","status":"success","imageUUID":"img-1","imageURL":"https://im.runware.ai/out.png","cost":0.08}]}`,
	)

	resp, bifrostErr := runUpscale(t, newTestRunwareProvider(t, server.URL, 30), nil)
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %+v", bifrostErr.Error)
	}
	if len(resp.Data) != 1 || resp.Data[0].URL != "https://im.runware.ai/out.png" || resp.Data[0].ID != "img-1" {
		t.Fatalf("expected the polled image, got %+v", resp.Data)
	}
	if resp.Usage == nil || resp.Usage.Cost == nil || resp.Usage.Cost.TotalCost != 0.08 {
		t.Fatalf("expected the polled task cost, got %+v", resp.Usage)
	}

	tasks := received()
	if len(tasks) != 3 {
		t.Fatalf("expected the upscale task then two getResponse polls, got %+v", tasks)
	}
	if tasks[0].TaskType != "upscale" {
		t.Fatalf("first task = %q, want upscale", tasks[0].TaskType)
	}
	for _, poll := range tasks[1:] {
		if poll.TaskType != "getResponse" || poll.TaskUUID != tasks[0].TaskUUID {
			t.Fatalf("poll = %+v, want getResponse for taskUUID %q", poll, tasks[0].TaskUUID)
		}
	}
}

// With n=2 the sync reply carries the finished image plus a processing row for the other one;
// getResponse returns the complete set, so both images come back exactly once.
func TestImageEdit_PollsPartialSyncResults(t *testing.T) {
	t.Parallel()
	server, received := newFakeRunware(t,
		`{"data":[{"taskType":"upscale","taskUUID":"{{uuid}}","imageURL":"https://im.runware.ai/1.png","cost":0.04},{"taskType":"upscale","taskUUID":"{{uuid}}","status":"processing"}]}`,
		`{"data":[{"taskType":"upscale","taskUUID":"{{uuid}}","status":"success","imageURL":"https://im.runware.ai/1.png","cost":0.04},{"taskType":"upscale","taskUUID":"{{uuid}}","status":"success","imageURL":"https://im.runware.ai/2.png","cost":0.04}]}`,
	)

	resp, bifrostErr := runUpscale(t, newTestRunwareProvider(t, server.URL, 30), new(2))
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %+v", bifrostErr.Error)
	}
	if len(resp.Data) != 2 || resp.Data[0].URL != "https://im.runware.ai/1.png" || resp.Data[1].URL != "https://im.runware.ai/2.png" {
		t.Fatalf("expected both images once, got %+v", resp.Data)
	}
	if resp.Usage == nil || resp.Usage.Cost == nil || resp.Usage.Cost.TotalCost != 0.08 {
		t.Fatalf("expected the cost of both images, got %+v", resp.Usage)
	}
	if got := len(received()); got != 2 {
		t.Fatalf("expected one getResponse poll, got %d requests", got)
	}
}

// A task that fails after the cutoff moves to errors on getResponse; that reason is surfaced.
func TestImageEdit_PolledTaskFailure(t *testing.T) {
	t.Parallel()
	server, _ := newFakeRunware(t,
		`{"data":[{"taskType":"upscale","taskUUID":"{{uuid}}","status":"processing"}]}`,
		`{"errors":[{"code":"timeoutProvider","status":"error","message":"The external provider did not respond within the timeout window.","taskType":"upscale","taskUUID":"{{uuid}}"}]}`,
	)

	resp, bifrostErr := runUpscale(t, newTestRunwareProvider(t, server.URL, 30), nil)
	if bifrostErr == nil {
		t.Fatalf("expected the polled failure, got %+v", resp)
	}
	if bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "did not respond within the timeout window") {
		t.Fatalf("expected Runware's failure reason, got %+v", bifrostErr.Error)
	}
}

// Polling is bounded by the provider's request timeout; a task still processing past it fails with
// a 504 rather than an empty image, and no further requests are sent.
func TestImageEdit_PollingTimesOut(t *testing.T) {
	t.Parallel()
	server, received := newFakeRunware(t,
		`{"data":[{"taskType":"upscale","taskUUID":"{{uuid}}","status":"processing"}]}`,
	)

	resp, bifrostErr := runUpscale(t, newTestRunwareProvider(t, server.URL, 1), nil)
	if bifrostErr == nil {
		t.Fatalf("expected a timeout, got %+v", resp)
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("StatusCode = %v, want %d", bifrostErr.StatusCode, http.StatusGatewayTimeout)
	}
	tasks := received()
	if len(tasks) != 1 {
		t.Fatalf("expected only the upscale task, got %+v", tasks)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Error == nil || !strings.Contains(bifrostErr.Error.Error.Error(), tasks[0].TaskUUID) {
		t.Fatalf("timeout should name the still-processing task, got %+v", bifrostErr.Error)
	}
}
