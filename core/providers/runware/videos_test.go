package runware

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// Video requests keep the videoInference task type and the 16:9 1080p width/height defaults.
func TestToRunwareVideoGenerationRequest_VideoDefaults(t *testing.T) {
	req := &schemas.BifrostVideoGenerationRequest{
		Model: "klingai:kling-video@3-pro",
		Input: &schemas.VideoGenerationInput{Prompt: "a red bird flying"},
	}

	out, err := ToRunwareVideoGenerationRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.TaskType != taskTypeVideoInference {
		t.Fatalf("taskType = %q, want %q", out.TaskType, taskTypeVideoInference)
	}
	if out.Width == nil || out.Height == nil {
		t.Fatalf("video request must set width/height, got width=%v height=%v", out.Width, out.Height)
	}
	if *out.Width != defaultRunwareVideoWidth || *out.Height != defaultRunwareVideoHeight {
		t.Fatalf("default size = %dx%d, want %dx%d", *out.Width, *out.Height, defaultRunwareVideoWidth, defaultRunwareVideoHeight)
	}
}

// A 3dInference override (via extra_params) must switch the task type and drop the video-only
// width/height defaults, which Runware's 3D task type does not accept.
func TestToRunwareVideoGenerationRequest_3DOmitsWidthHeight(t *testing.T) {
	req := &schemas.BifrostVideoGenerationRequest{
		Model: "tripo:v3.1@0",
		Input: &schemas.VideoGenerationInput{Prompt: "a ceramic teapot"},
		Params: &schemas.VideoGenerationParameters{
			// Size is set to prove it is ignored for non-video task types.
			Size: "1024x1024",
			ExtraParams: map[string]any{
				"taskType":   taskType3DInference,
				"resolution": 1024,
			},
		},
	}

	out, err := ToRunwareVideoGenerationRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.TaskType != taskType3DInference {
		t.Fatalf("taskType = %q, want %q", out.TaskType, taskType3DInference)
	}
	if out.Width != nil || out.Height != nil {
		t.Fatalf("3D request must not set width/height, got width=%v height=%v", out.Width, out.Height)
	}
	if out.PositivePrompt == nil || *out.PositivePrompt != "a ceramic teapot" {
		t.Fatalf("positivePrompt not carried through: %v", out.PositivePrompt)
	}
}

// type="3d" selects the 3D task without the caller knowing Runware's task type names, and pulls
// settings out of extra params so model tuning reaches the wire as a nested object.
func TestToRunwareVideoGenerationRequest_3DTypeParam(t *testing.T) {
	req := &schemas.BifrostVideoGenerationRequest{
		Model: "tencent:hunyuan-3d@3.1-rapid",
		Input: &schemas.VideoGenerationInput{Prompt: "a ceramic teapot"},
		Params: &schemas.VideoGenerationParameters{
			Type:        new("3d"),
			ExtraParams: map[string]any{"settings": `{"pbr":true}`},
		},
	}

	out, err := ToRunwareVideoGenerationRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.TaskType != taskType3DInference {
		t.Fatalf("taskType = %q, want %q", out.TaskType, taskType3DInference)
	}
	if out.Settings["pbr"] != true {
		t.Fatalf("settings = %+v, want pbr=true", out.Settings)
	}
	if _, ok := out.ExtraParams["settings"]; ok {
		t.Fatalf("settings should be consumed from ExtraParams, got %+v", out.ExtraParams)
	}
	if out.IncludeCost == nil || !*out.IncludeCost {
		t.Fatalf("3D has no datasheet rate, so includeCost must be set")
	}
}

// Image-to-3D routes the reference image into the nested inputs object rather than frameImages,
// which the 3D task type does not accept. The singular/array form is per-model.
func TestToRunwareVideoGenerationRequest_3DInputArity(t *testing.T) {
	build := func(model string) *RunwareInferenceRequest {
		t.Helper()
		out, err := ToRunwareVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{
			Model: model,
			Input: &schemas.VideoGenerationInput{InputReference: new("https://assets.runware.ai/a.jpg")},
			Params: &schemas.VideoGenerationParameters{
				Type: new("3d"),
			},
		})
		if err != nil {
			t.Fatalf("unexpected error for %s: %v", model, err)
		}
		if len(out.FrameImages) != 0 {
			t.Fatalf("%s: 3D must not use frameImages, got %+v", model, out.FrameImages)
		}
		return out
	}

	singular := build("tencent:hunyuan-3d@3.1-rapid")
	if singular.Inputs == nil || singular.Inputs.Image == nil || len(singular.Inputs.Images) != 0 {
		t.Fatalf("rapid expects inputs.image, got %+v", singular.Inputs)
	}

	array := build("tencent:hunyuan-3d@3.1-pro")
	if array.Inputs == nil || len(array.Inputs.Images) != 1 || array.Inputs.Image != nil {
		t.Fatalf("pro expects inputs.images[], got %+v", array.Inputs)
	}
}

// Video generation is unaffected: no type means videoInference, and the reference image still
// anchors to the first frame.
func TestToRunwareVideoGenerationRequest_VideoInputUnchanged(t *testing.T) {
	out, err := ToRunwareVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{
		Model: "klingai:kling-video@3-pro",
		Input: &schemas.VideoGenerationInput{
			Prompt:         "a red bird flying",
			InputReference: new("https://assets.runware.ai/a.jpg"),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.TaskType != taskTypeVideoInference {
		t.Fatalf("taskType = %q, want %q", out.TaskType, taskTypeVideoInference)
	}
	if len(out.FrameImages) != 1 || out.FrameImages[0].Frame == nil || *out.FrameImages[0].Frame != "first" {
		t.Fatalf("video must keep frameImages anchoring, got %+v", out.FrameImages)
	}
	if out.Inputs != nil {
		t.Fatalf("video must not use the nested inputs object, got %+v", out.Inputs)
	}
	if out.IncludeCost == nil || !*out.IncludeCost {
		t.Fatalf("expected includeCost=true so the task cost is reported")
	}
}

// Video tool tasks (upscale, removeBackground) take their source from video_uri, nested under
// inputs.video, and never use frameImages or the video width/height defaults.
func TestToRunwareVideoGenerationRequest_VideoTools(t *testing.T) {
	cases := map[string]string{
		"upscale":            taskTypeUpscale,
		"background_removal": taskTypeRemoveBackground,
		"remove-bg":          taskTypeRemoveBackground,
	}
	for editType, wantTask := range cases {
		out, err := ToRunwareVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{
			Model: "bytedance:50@1",
			Input: &schemas.VideoGenerationInput{VideoURI: new("https://assets.runware.ai/clip.mp4")},
			Params: &schemas.VideoGenerationParameters{
				Type:          &editType,
				UpscaleFactor: new(2),
				ExtraParams: map[string]any{
					"providerSettings": `{"bria":{"preserveAlpha":true}}`,
				},
			},
		})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", editType, err)
		}
		if out.TaskType != wantTask {
			t.Fatalf("%s: taskType = %q, want %q", editType, out.TaskType, wantTask)
		}
		if out.Inputs == nil || out.Inputs.Video == nil || *out.Inputs.Video != "https://assets.runware.ai/clip.mp4" {
			t.Fatalf("%s: expected inputs.video, got %+v", editType, out.Inputs)
		}
		if len(out.FrameImages) != 0 || out.Width != nil || out.Height != nil {
			t.Fatalf("%s: video-generation fields must stay unset, got %+v", editType, out)
		}
		if out.UpscaleFactor == nil || *out.UpscaleFactor != 2 {
			t.Fatalf("%s: upscaleFactor = %v, want 2", editType, out.UpscaleFactor)
		}
		if out.ProviderSettings["bria"] == nil {
			t.Fatalf("%s: providerSettings = %+v, want a bria entry", editType, out.ProviderSettings)
		}
		if out.DeliveryMethod == nil || *out.DeliveryMethod != deliveryMethodAsync {
			t.Fatalf("%s: video tools are async-only, got %v", editType, out.DeliveryMethod)
		}
	}
}

// video_uri alone is a complete request for a tool task: no prompt and no reference image.
func TestToRunwareVideoGenerationRequest_VideoURIOnly(t *testing.T) {
	out, err := ToRunwareVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{
		Model: "bytedance:50@1",
		Input: &schemas.VideoGenerationInput{VideoURI: new("https://assets.runware.ai/clip.mp4")},
		Params: &schemas.VideoGenerationParameters{
			Type: new("upscale"),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Inputs == nil || out.Inputs.Video == nil {
		t.Fatalf("expected inputs.video, got %+v", out.Inputs)
	}

	// Generation still requires an input block.
	if _, err := ToRunwareVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{
		Model: "klingai:kling-video@3-pro",
	}); err == nil {
		t.Fatalf("expected video generation without input to fail")
	}
}

// The /videos schema has no output_format, so the container is selected through extra params and
// normalised to Runware's uppercase enum. Video background removal is rejected unless it gets an
// alpha-capable container, so this is the only way to reach it.
func TestToRunwareVideoGenerationRequest_OutputFormat(t *testing.T) {
	out, err := ToRunwareVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{
		Model: "bria:51@1",
		Input: &schemas.VideoGenerationInput{VideoURI: new("https://assets.runware.ai/clip.mp4")},
		Params: &schemas.VideoGenerationParameters{
			Type:        new("background_removal"),
			ExtraParams: map[string]any{"outputFormat": "webm"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.OutputFormat == nil || *out.OutputFormat != "WEBM" {
		t.Fatalf("outputFormat = %v, want WEBM", out.OutputFormat)
	}
	if _, ok := out.ExtraParams["outputFormat"]; ok {
		t.Fatalf("outputFormat should be consumed from ExtraParams, got %+v", out.ExtraParams)
	}
}

// outputFormat accepts MP4, WEBM and MOV, so the asset content type follows the URL rather than
// being assumed to be MP4. Extensionless URLs keep the MP4 fallback.
func TestToBifrostVideoGenerationResponse_ContentTypeFollowsFormat(t *testing.T) {
	cases := map[string]string{
		"https://vm.runware.ai/v/clip.mp4":  "video/mp4",
		"https://vm.runware.ai/v/clip.webm": "video/webm",
		"https://vm.runware.ai/v/clip.mov":  "video/quicktime",
		"https://vm.runware.ai/v/clip":      "video/mp4",
	}
	for url, want := range cases {
		resp := ToBifrostVideoGenerationResponse(&RunwareResult{Status: "success", VideoURL: url}, nil)
		if len(resp.Videos) != 1 || resp.Videos[0].ContentType != want {
			t.Errorf("%s: content type = %+v, want %q", url, resp.Videos, want)
		}
	}
}

// A 3D task result exposes its glb asset under outputs.files[]; the mapper surfaces it as a
// VideoOutput URL with the model/gltf-binary content type and a completed status.
func TestToBifrostVideoGenerationResponse_3DOutputs(t *testing.T) {
	result := &RunwareResult{
		TaskType: taskType3DInference,
		TaskUUID: "21f2d643-11b5-4ec3-8148-4e271571047a",
		Outputs: &RunwareOutputs{
			Files: []RunwareOutputFile{
				{UUID: "bddff57a", URL: "https://im.runware.ai/image/os/a04d20/ws/4/ii/bddff57a.glb"},
			},
		},
	}

	resp := ToBifrostVideoGenerationResponse(result, nil)
	if resp.Status != schemas.VideoStatusCompleted {
		t.Fatalf("status = %q, want %q", resp.Status, schemas.VideoStatusCompleted)
	}
	if len(resp.Videos) != 1 {
		t.Fatalf("expected 1 asset, got %d", len(resp.Videos))
	}
	got := resp.Videos[0]
	if got.URL == nil || *got.URL != result.Outputs.Files[0].URL {
		t.Fatalf("asset URL = %v, want %q", got.URL, result.Outputs.Files[0].URL)
	}
	if got.ContentType != "model/gltf-binary" {
		t.Fatalf("content type = %q, want model/gltf-binary", got.ContentType)
	}
}

// Runware reports the exact task cost; it is surfaced as the provider-reported cost so pricing
// uses it verbatim (critical for 3D, which has no datasheet rate).
func TestToBifrostVideoGenerationResponse_Cost(t *testing.T) {
	result := &RunwareResult{
		TaskType: taskType3DInference,
		TaskUUID: "cost-1",
		Cost:     0.5,
		Outputs: &RunwareOutputs{
			Files: []RunwareOutputFile{{URL: "https://im.runware.ai/x/model.glb"}},
		},
	}

	resp := ToBifrostVideoGenerationResponse(result, nil)
	if resp.Usage == nil || resp.Usage.Cost == nil {
		t.Fatalf("expected provider-reported cost to be surfaced, got Usage=%+v", resp.Usage)
	}
	if resp.Usage.Cost.TotalCost != 0.5 {
		t.Fatalf("cost = %v, want 0.5", resp.Usage.Cost.TotalCost)
	}
}

// When Runware does not report a cost (includeCost omitted), no Usage is attached so pricing can
// fall through to the datasheet.
func TestToBifrostVideoGenerationResponse_NoCost(t *testing.T) {
	resp := ToBifrostVideoGenerationResponse(&RunwareResult{TaskUUID: "no-cost", Status: "success", VideoURL: "https://x/clip"}, nil)
	if resp.Usage != nil {
		t.Fatalf("expected no Usage when cost is absent, got %+v", resp.Usage)
	}
}

// The existing video path must be unchanged: a videoURL maps to a video/mp4 asset.
func TestToBifrostVideoGenerationResponse_VideoUnchanged(t *testing.T) {
	result := &RunwareResult{
		TaskType: taskTypeVideoInference,
		TaskUUID: "abc-123",
		Status:   "success",
		VideoURL: "https://im.runware.ai/video/os/clip",
	}

	resp := ToBifrostVideoGenerationResponse(result, nil)
	if resp.Status != schemas.VideoStatusCompleted {
		t.Fatalf("status = %q, want completed", resp.Status)
	}
	if len(resp.Videos) != 1 || resp.Videos[0].ContentType != "video/mp4" {
		t.Fatalf("video output not preserved: %+v", resp.Videos)
	}
}

// Processing tasks with no assets yet stay in progress / queued rather than being marked complete.
func TestToBifrostVideoGenerationResponse_ProcessingNoAssets(t *testing.T) {
	resp := ToBifrostVideoGenerationResponse(&RunwareResult{Status: "processing"}, nil)
	if resp.Status != schemas.VideoStatusInProgress {
		t.Fatalf("status = %q, want in_progress", resp.Status)
	}
	if len(resp.Videos) != 0 {
		t.Fatalf("expected no assets, got %d", len(resp.Videos))
	}
}

func TestContentTypeForAssetURL(t *testing.T) {
	cases := map[string]string{
		"https://x/model.glb":         "model/gltf-binary",
		"https://x/model.gltf":        "model/gltf+json",
		"https://x/model.usdz":        "model/vnd.usdz+zip",
		"https://x/model.obj":         "model/obj",
		"https://x/model.stl":         "model/stl",
		"https://x/model.fbx":         "application/octet-stream",
		"https://x/clip.mp4":          "video/mp4",
		"https://x/model.glb?token=1": "model/gltf-binary",
		"https://x/no-extension":      "application/octet-stream",
		"https://x/dir.v2/asset":      "application/octet-stream",
	}
	for url, want := range cases {
		if got := contentTypeForAssetURL(url); got != want {
			t.Errorf("contentTypeForAssetURL(%q) = %q, want %q", url, got, want)
		}
	}
}

// A failed task carries its reason in the envelope's errors[], not on the result, so the mapper
// must be given the envelope or the caller is left with a generic failure.
func TestToBifrostVideoGenerationResponse_FailureReason(t *testing.T) {
	result := &RunwareResult{TaskUUID: "task-1", Status: "error"}

	t.Run("uses the matching envelope error", func(t *testing.T) {
		resp := ToBifrostVideoGenerationResponse(result, []RunwareError{
			{TaskUUID: "other", Code: "wrongOne", Message: "not this"},
			{TaskUUID: "task-1", Code: "inferenceError", Message: "inference error occurred"},
		})
		if resp.Status != schemas.VideoStatusFailed {
			t.Fatalf("status = %q, want failed", resp.Status)
		}
		if resp.Error == nil || resp.Error.Code != "inferenceError" || resp.Error.Message != "inference error occurred" {
			t.Fatalf("error not surfaced from envelope: %+v", resp.Error)
		}
	})

	t.Run("falls back to a generic message with no envelope errors", func(t *testing.T) {
		resp := ToBifrostVideoGenerationResponse(result, nil)
		if resp.Error == nil || resp.Error.Message == "" {
			t.Fatalf("expected a fallback message, got %+v", resp.Error)
		}
	})
}

// Runware accepts asset UUIDs as inputs, so output UUIDs are surfaced to allow chaining tasks.
func TestToBifrostVideoGenerationResponse_AssetIDs(t *testing.T) {
	video := ToBifrostVideoGenerationResponse(&RunwareResult{
		Status: "success", VideoUUID: "vid-1", VideoURL: "https://x/clip.mp4",
	}, nil)
	if len(video.Videos) != 1 || video.Videos[0].ID != "vid-1" {
		t.Fatalf("video asset id not surfaced: %+v", video.Videos)
	}

	model := ToBifrostVideoGenerationResponse(&RunwareResult{
		Outputs: &RunwareOutputs{Files: []RunwareOutputFile{{UUID: "glb-1", URL: "https://x/m.glb"}}},
	}, nil)
	if len(model.Videos) != 1 || model.Videos[0].ID != "glb-1" {
		t.Fatalf("3D asset id not surfaced: %+v", model.Videos)
	}
}
