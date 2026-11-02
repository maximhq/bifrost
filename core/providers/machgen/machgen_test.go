package machgen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testNoopLogger struct{}

func (testNoopLogger) Debug(string, ...any)                   {}
func (testNoopLogger) Info(string, ...any)                    {}
func (testNoopLogger) Warn(string, ...any)                    {}
func (testNoopLogger) Error(string, ...any)                   {}
func (testNoopLogger) Fatal(string, ...any)                   {}
func (testNoopLogger) SetLevel(schemas.LogLevel)              {}
func (testNoopLogger) SetOutputType(schemas.LoggerOutputType) {}
func (testNoopLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func TestToMachGenVideoGenerationRequest_InfersTaskTypeFromInputs(t *testing.T) {
	cases := []struct {
		name     string
		input    *schemas.VideoGenerationInput
		extra    map[string]interface{}
		wantType string
	}{
		{"prompt only is text to video", &schemas.VideoGenerationInput{Prompt: "p"}, nil, taskTypeT2V},
		{"input reference is image to video", &schemas.VideoGenerationInput{Prompt: "p", InputReference: schemas.Ptr("https://x/a.png")}, nil, taskTypeI2V},
		{"video uri is reference to video", &schemas.VideoGenerationInput{Prompt: "p", VideoURI: schemas.Ptr("https://x/a.mp4")}, nil, taskTypeR2V},
		{"named subjects are reference to video", &schemas.VideoGenerationInput{Prompt: "@alice waves"}, map[string]interface{}{
			"src_image_urls":       []interface{}{"https://x/alice.png"},
			"subject_to_image_ids": map[string]interface{}{"alice": []interface{}{0}},
		}, taskTypeR2V},
		{"explicit task type wins", &schemas.VideoGenerationInput{Prompt: "p", InputReference: schemas.Ptr("https://x/a.png")}, map[string]interface{}{"task_type": "R2V"}, taskTypeR2V},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := ToMachGenVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{
				Model:  "MiniMax-H3",
				Input:  tc.input,
				Params: &schemas.VideoGenerationParameters{ExtraParams: tc.extra},
			})
			require.NoError(t, err)
			assert.Equal(t, tc.wantType, req.TaskType)
			assert.NotContains(t, req.ExtraParams, "task_type")
			assert.NotContains(t, req.ExtraParams, "subject_to_image_ids")
		})
	}
}

func TestToMachGenVideoGenerationRequest_MapsSizeAndDurationToVideoConfig(t *testing.T) {
	req, err := ToMachGenVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{
		Model: "Kling-v3",
		Input: &schemas.VideoGenerationInput{Prompt: "a red panda"},
		Params: &schemas.VideoGenerationParameters{
			Seconds:        schemas.Ptr("5"),
			Size:           "1080x1920",
			Seed:           schemas.Ptr(7),
			Audio:          schemas.Ptr(true),
			NegativePrompt: schemas.Ptr("blur"),
		},
	})
	require.NoError(t, err)

	body, err := json.Marshal(req)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"model": "Kling-v3",
		"task_type": "T2V",
		"prompt": "a red panda",
		"seed": 7,
		"video_config": {"duration_secs": 5, "height": 1920, "aspect_ratio": "9:16", "audio": true, "negative_prompt": "blur"}
	}`, string(body))
}

func TestParseSize_SnapsRoundedWidthsToNamedAspectRatios(t *testing.T) {
	for size, want := range map[string]string{
		"1280x720":  "16:9",
		"1366x768":  "16:9", // MachGen rounds 768 * 16/9 up to 1366
		"1080x1920": "9:16",
		"1024x1024": "1:1",
		"2560x1097": "21:9",
	} {
		height, aspect, err := parseSize(size)
		require.NoError(t, err, size)
		assert.Equal(t, want, *aspect, size)
		assert.Positive(t, *height)
	}
}

func TestToMachGenVideoGenerationRequest_RejectsMalformedParams(t *testing.T) {
	for name, params := range map[string]*schemas.VideoGenerationParameters{
		"non integer seconds": {Seconds: schemas.Ptr("4.5")},
		"size without x":      {Size: "720p"},
		"unsupported ratio":   {Size: "1000x700"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ToMachGenVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{
				Model:  "MiniMax-H3",
				Input:  &schemas.VideoGenerationInput{Prompt: "p"},
				Params: params,
			})
			require.Error(t, err)
		})
	}
}

func TestToMachGenImageEditRequest_RequiresImageURLs(t *testing.T) {
	_, err := ToMachGenImageEditRequest(&schemas.BifrostImageEditRequest{
		Model: "Nano-Banana-Pro",
		Input: &schemas.ImageEditInput{Prompt: "p", Images: []schemas.ImageInput{{Image: []byte{1, 2, 3}}}},
	})
	require.Error(t, err)

	req, err := ToMachGenImageEditRequest(&schemas.BifrostImageEditRequest{
		Model: "Nano-Banana-Pro",
		Input: &schemas.ImageEditInput{Prompt: "p", Images: []schemas.ImageInput{{URL: "https://x/a.png"}}},
	})
	require.NoError(t, err)
	assert.Equal(t, taskTypeI2I, req.TaskType)
	assert.Equal(t, []string{"https://x/a.png"}, req.SrcImageURLs)
}

func TestToBifrostVideoGenerationResponse_MapsTaskLifecycle(t *testing.T) {
	completed, bErr := ToBifrostVideoGenerationResponse(&MachGenTaskStatusResponse{
		TaskID:     "t-1",
		Status:     MachGenTaskStatusCompleted,
		Metadata:   &MachGenTaskMetadata{Prompt: "p", Model: "MiniMax-H3", Width: schemas.Ptr(1280), Height: schemas.Ptr(720), DurationSecs: schemas.Ptr(5.0)},
		TaskOutput: &MachGenTaskOutput{Video: "https://api.machgen.ai/api/v0/assets/t-1"},
	})
	require.Nil(t, bErr)
	assert.Equal(t, schemas.VideoStatusCompleted, completed.Status)
	assert.Equal(t, "1280x720", completed.Size)
	assert.Equal(t, "5", *completed.Seconds)
	require.Len(t, completed.Videos, 1)

	failed, bErr := ToBifrostVideoGenerationResponse(&MachGenTaskStatusResponse{
		TaskID:   "t-2",
		Status:   MachGenTaskStatusFailed,
		ErrorMsg: schemas.Ptr("The task was canceled before it completed."),
	})
	require.Nil(t, bErr)
	assert.Equal(t, schemas.VideoStatusFailed, failed.Status)
	assert.Equal(t, "The task was canceled before it completed.", failed.Error.Message)

	pending, _ := ToBifrostVideoGenerationResponse(&MachGenTaskStatusResponse{TaskID: "t-3", Status: MachGenTaskStatusPending})
	assert.Equal(t, schemas.VideoStatusQueued, pending.Status)
}

// fakeMachGen serves the generate -> tasks -> assets lifecycle; a task reports RUNNING on its
// first poll and COMPLETED afterwards.
func fakeMachGen(t *testing.T, apiKey string, asset []byte, assetType string) (*httptest.Server, *[]byte) {
	t.Helper()
	var mu sync.Mutex
	polls := 0
	var submitted []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":"Invalid API key"}`))
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == generatePath:
			submitted, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"task_id":"t-abc","metadata":{"prompt":"p"}}`))
		case r.Method == http.MethodGet && r.URL.Path == tasksPath+"t-abc":
			polls++
			if polls == 1 {
				_, _ = w.Write([]byte(`{"task_id":"t-abc","status":"RUNNING","metadata":{"prompt":"p"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"task_id":"t-abc","status":"COMPLETED","metadata":{"prompt":"p","model":"FLUX.2-dev","width":1024,"height":1024},"task_output":{"image":"x","video":"x"}}`))
		case r.Method == http.MethodGet && r.URL.Path == assetsPath+"t-abc":
			w.Header().Set("Content-Type", assetType)
			_, _ = w.Write(asset)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Task not found"}`))
		}
	}))
	t.Cleanup(server.Close)
	return server, &submitted
}

func newTestProvider(t *testing.T, baseURL string) *MachGenProvider {
	t.Helper()
	provider, err := NewMachGenProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL, DefaultRequestTimeoutInSeconds: 30, AllowPrivateNetwork: true},
	}, testNoopLogger{})
	require.NoError(t, err)
	return provider
}

func testKey(value string) schemas.Key {
	return schemas.Key{Value: *schemas.NewSecretVar(value), Models: []string{"*"}}
}

func TestMachGenImageGeneration_WaitsForTaskAndReturnsImageInline(t *testing.T) {
	image := []byte("\xff\xd8jpeg-bytes")
	server, submitted := fakeMachGen(t, "MGA_test", image, "image/jpeg")
	provider := newTestProvider(t, server.URL)

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, bErr := provider.ImageGeneration(ctx, testKey("MGA_test"), &schemas.BifrostImageGenerationRequest{
		Model:  "FLUX.2-dev",
		Input:  &schemas.ImageGenerationInput{Prompt: "a cozy reading nook"},
		Params: &schemas.ImageGenerationParameters{Size: schemas.Ptr("1024x1024")},
	})
	require.Nil(t, bErr)
	require.Len(t, resp.Data, 1)
	assert.Equal(t, base64.StdEncoding.EncodeToString(image), resp.Data[0].B64JSON)
	assert.Equal(t, "FLUX.2-dev", resp.Model)
	assert.JSONEq(t, `{"model":"FLUX.2-dev","task_type":"T2I","prompt":"a cozy reading nook","image_config":{"height":1024,"aspect_ratio":"1:1"}}`, string(*submitted))
}

func TestMachGenVideoLifecycle_SubmitRetrieveDownload(t *testing.T) {
	video := []byte("mp4-bytes")
	server, _ := fakeMachGen(t, "MGA_test", video, "video/mp4")
	provider := newTestProvider(t, server.URL)
	key := testKey("MGA_test")

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	created, bErr := provider.VideoGeneration(ctx, key, &schemas.BifrostVideoGenerationRequest{
		Model: "MiniMax-H3",
		Input: &schemas.VideoGenerationInput{Prompt: "p"},
	})
	require.Nil(t, bErr)
	assert.Equal(t, schemas.VideoStatusQueued, created.Status)
	assert.Equal(t, "t-abc:machgen", created.ID)

	running, bErr := provider.VideoRetrieve(ctx, key, &schemas.BifrostVideoRetrieveRequest{ID: created.ID})
	require.Nil(t, bErr)
	assert.Equal(t, schemas.VideoStatusInProgress, running.Status)

	downloaded, bErr := provider.VideoDownload(ctx, key, &schemas.BifrostVideoDownloadRequest{ID: created.ID})
	require.Nil(t, bErr)
	assert.Equal(t, video, downloaded.Content)
	assert.Equal(t, "video/mp4", downloaded.ContentType)
}

func TestMachGenSurfacesAPIErrorDetail(t *testing.T) {
	server, _ := fakeMachGen(t, "MGA_test", nil, "")
	provider := newTestProvider(t, server.URL)

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, bErr := provider.VideoGeneration(ctx, testKey("MGA_wrong"), &schemas.BifrostVideoGenerationRequest{
		Model: "MiniMax-H3",
		Input: &schemas.VideoGenerationInput{Prompt: "p"},
	})
	require.NotNil(t, bErr)
	require.NotNil(t, bErr.StatusCode)
	assert.Equal(t, http.StatusUnauthorized, *bErr.StatusCode)
	assert.Equal(t, "Invalid API key", bErr.Error.Message)
}

// TestMachGenLive runs a real text-to-image task when MACHGEN_API_KEY is set.
func TestMachGenLive(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("MACHGEN_API_KEY"))
	if apiKey == "" {
		t.Skip("Skipping MachGen live test because MACHGEN_API_KEY is not set")
	}
	provider, err := NewMachGenProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 300},
	}, testNoopLogger{})
	require.NoError(t, err)

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	resp, bErr := provider.ImageGeneration(ctx, testKey(apiKey), &schemas.BifrostImageGenerationRequest{
		Model:  "FLUX.2-dev",
		Input:  &schemas.ImageGenerationInput{Prompt: "an isometric illustration of a cozy reading nook"},
		Params: &schemas.ImageGenerationParameters{Size: schemas.Ptr("1024x1024")},
	})
	require.Nil(t, bErr, "%v", bErr)
	require.Len(t, resp.Data, 1)
	assert.NotEmpty(t, resp.Data[0].B64JSON)
}
