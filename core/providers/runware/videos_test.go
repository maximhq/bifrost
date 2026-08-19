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

	resp := ToBifrostVideoGenerationResponse(result)
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

	resp := ToBifrostVideoGenerationResponse(result)
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
	resp := ToBifrostVideoGenerationResponse(&RunwareResult{TaskUUID: "no-cost", Status: "success", VideoURL: "https://x/clip"})
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

	resp := ToBifrostVideoGenerationResponse(result)
	if resp.Status != schemas.VideoStatusCompleted {
		t.Fatalf("status = %q, want completed", resp.Status)
	}
	if len(resp.Videos) != 1 || resp.Videos[0].ContentType != "video/mp4" {
		t.Fatalf("video output not preserved: %+v", resp.Videos)
	}
}

// Processing tasks with no assets yet stay in progress / queued rather than being marked complete.
func TestToBifrostVideoGenerationResponse_ProcessingNoAssets(t *testing.T) {
	resp := ToBifrostVideoGenerationResponse(&RunwareResult{Status: "processing"})
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
