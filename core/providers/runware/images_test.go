package runware

import (
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func upscaleEditRequest(extraParams map[string]interface{}) *schemas.BifrostImageEditRequest {
	return &schemas.BifrostImageEditRequest{
		Model: "topazlabs:wonder@3.5",
		Input: &schemas.ImageEditInput{Images: []schemas.ImageInput{{Image: []byte("fake-image-bytes")}}},
		Params: &schemas.ImageEditParameters{
			Type:        new("upscale"),
			ExtraParams: extraParams,
		},
	}
}

// type=upscale switches the edit path to the upscale task: the image moves under inputs, and the
// imageInference-only fields (prompt, dimensions, steps) are left unset since Runware rejects them.
func TestToRunwareImageEditRequest_Upscale(t *testing.T) {
	out, err := ToRunwareImageEditRequest(upscaleEditRequest(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.TaskType != taskTypeUpscale {
		t.Fatalf("taskType = %q, want %q", out.TaskType, taskTypeUpscale)
	}
	if out.Inputs == nil || out.Inputs.Image == nil || !strings.HasPrefix(*out.Inputs.Image, "data:") {
		t.Fatalf("expected inputs.image data URI, got %+v", out.Inputs)
	}
	if out.SeedImage != nil {
		t.Fatalf("seedImage must stay unset on upscale, got %q", *out.SeedImage)
	}
	if out.PositivePrompt != nil || out.Width != nil || out.Height != nil || out.Steps != nil {
		t.Fatalf("prompt/width/height/steps must stay unset on upscale, got %+v", out)
	}
	if out.IncludeCost == nil || !*out.IncludeCost {
		t.Fatalf("expected includeCost=true so the task cost is reported")
	}
}

// Upscaler fields arrive as multipart form strings; they are coerced to typed properties and
// removed from ExtraParams so passthrough does not also emit them verbatim.
func TestToRunwareImageEditRequest_UpscaleExtraParams(t *testing.T) {
	req := upscaleEditRequest(map[string]interface{}{
		"settings":  `{"enhancementStrength":"high"}`,
		"unrelated": "keep-me",
	})
	req.Params.UpscaleFactor = new(4)
	req.Params.TargetMegapixels = new(16)

	out, err := ToRunwareImageEditRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.UpscaleFactor == nil || *out.UpscaleFactor != 4 {
		t.Fatalf("upscaleFactor = %v, want 4", out.UpscaleFactor)
	}
	if out.TargetMegapixels == nil || *out.TargetMegapixels != 16 {
		t.Fatalf("targetMegapixels = %v, want 16", out.TargetMegapixels)
	}
	if out.Settings["enhancementStrength"] != "high" {
		t.Fatalf("settings = %+v, want enhancementStrength=high", out.Settings)
	}
	for _, consumed := range []string{"settings"} {
		if _, ok := out.ExtraParams[consumed]; ok {
			t.Fatalf("%q should be consumed from ExtraParams, got %+v", consumed, out.ExtraParams)
		}
	}
	if out.ExtraParams["unrelated"] != "keep-me" {
		t.Fatalf("unrecognised extra params must pass through, got %+v", out.ExtraParams)
	}
}

// type=background_removal (and its aliases) selects the removeBackground task, which shares the
// tool envelope with upscale but takes none of the upscaler-specific fields.
func TestToRunwareImageEditRequest_RemoveBackground(t *testing.T) {
	for _, alias := range []string{"background_removal", "remove_background", "remove_bg", "Remove-Background"} {
		req := upscaleEditRequest(nil)
		req.Model = "runware:109@1"
		req.Params.Type = &alias

		out, err := ToRunwareImageEditRequest(req)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", alias, err)
		}
		if out.TaskType != taskTypeRemoveBackground {
			t.Fatalf("%s: taskType = %q, want %q", alias, out.TaskType, taskTypeRemoveBackground)
		}
		if out.Inputs == nil || out.Inputs.Image == nil {
			t.Fatalf("%s: expected inputs.image, got %+v", alias, out.Inputs)
		}
		if out.UpscaleFactor != nil || out.TargetMegapixels != nil {
			t.Fatalf("%s: upscaler fields must stay unset, got %+v", alias, out)
		}
		if out.PositivePrompt != nil || out.Width != nil || out.Height != nil {
			t.Fatalf("%s: prompt/dimensions must stay unset, got %+v", alias, out)
		}
		if out.IncludeCost == nil || !*out.IncludeCost {
			t.Fatalf("%s: expected includeCost=true", alias)
		}
	}
}

// removeBackground tuning lives in settings (runware models) or providerSettings (bria); both are
// consumed out of extra params so they reach the wire as nested objects.
func TestToRunwareImageEditRequest_RemoveBackgroundSettings(t *testing.T) {
	req := upscaleEditRequest(map[string]interface{}{
		"settings":         `{"returnOnlyMask":true,"rgba":[255,255,255,0]}`,
		"providerSettings": map[string]interface{}{"bria": map[string]interface{}{"preserveAlpha": true}},
		"unrelated":        "keep-me",
	})
	req.Model = "bria:2@1"
	req.Params.Type = new("background_removal")

	out, err := ToRunwareImageEditRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Settings["returnOnlyMask"] != true {
		t.Fatalf("settings = %+v, want returnOnlyMask=true", out.Settings)
	}
	if out.ProviderSettings["bria"] == nil {
		t.Fatalf("providerSettings = %+v, want a bria entry", out.ProviderSettings)
	}
	if out.ExtraParams["unrelated"] != "keep-me" {
		t.Fatalf("unrecognised extra params must pass through, got %+v", out.ExtraParams)
	}
}

// A settings object supplied by a JSON caller is used as-is, without a string round-trip.
func TestToRunwareImageEditRequest_UpscaleSettingsObject(t *testing.T) {
	out, err := ToRunwareImageEditRequest(upscaleEditRequest(map[string]interface{}{
		"settings": map[string]interface{}{"realism": true},
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Settings["realism"] != true {
		t.Fatalf("settings = %+v, want realism=true", out.Settings)
	}
}

// Edits without type=upscale keep using imageInference with a top-level seedImage.
func TestToRunwareImageEditRequest_NonUpscaleUnchanged(t *testing.T) {
	out, err := ToRunwareImageEditRequest(&schemas.BifrostImageEditRequest{
		Model: "runware:101@1",
		Input: &schemas.ImageEditInput{
			Images: []schemas.ImageInput{{Image: []byte("fake-image-bytes")}},
			Prompt: "make it night",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.TaskType != taskTypeImageInference {
		t.Fatalf("taskType = %q, want %q", out.TaskType, taskTypeImageInference)
	}
	if out.SeedImage == nil {
		t.Fatalf("expected seedImage to remain top-level for imageInference")
	}
	if out.Inputs != nil {
		t.Fatalf("inputs must stay unset for imageInference, got %+v", out.Inputs)
	}
}

// type=mask selects the imageMasking task; detector settings ride along in settings.
func TestToRunwareImageEditRequest_Mask(t *testing.T) {
	for _, alias := range []string{"mask", "segmentation", "Mask"} {
		req := upscaleEditRequest(map[string]interface{}{
			"settings": `{"confidence":0.7,"maskPadding":20,"maxDetections":4}`,
		})
		req.Model = "runware:35@1"
		req.Params.Type = &alias

		out, err := ToRunwareImageEditRequest(req)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", alias, err)
		}
		if out.TaskType != taskTypeImageMasking {
			t.Fatalf("%s: taskType = %q, want %q", alias, out.TaskType, taskTypeImageMasking)
		}
		if out.Inputs == nil || out.Inputs.Image == nil {
			t.Fatalf("%s: expected inputs.image, got %+v", alias, out.Inputs)
		}
		if out.Settings["confidence"] != 0.7 {
			t.Fatalf("%s: settings = %+v, want confidence=0.7", alias, out.Settings)
		}
	}
}

// Masking and ControlNet preprocessing return their artifact under maskImage*/guideImage*; reading
// only image* would emit an entry with no URL at all. Detections ride along on the same entry.
func TestToBifrostImageGenerationResponse_OutputFamilies(t *testing.T) {
	t.Run("maskImage with detections", func(t *testing.T) {
		out, err := ToBifrostImageGenerationResponse(&RunwareResponse{Data: []RunwareResult{{
			TaskUUID:     "mask-1",
			MaskImageURL: "https://x/mask.png",
			Detections:   []RunwareDetection{{XMin: 1, YMin: 2, XMax: 3, YMax: 4}},
		}}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out.Data[0].URL != "https://x/mask.png" {
			t.Fatalf("mask URL not surfaced: %+v", out.Data[0])
		}
		if len(out.Data[0].Detections) != 1 || out.Data[0].Detections[0].XMax != 3 {
			t.Fatalf("detections not surfaced: %+v", out.Data[0].Detections)
		}
	})

	t.Run("guideImage base64", func(t *testing.T) {
		out, err := ToBifrostImageGenerationResponse(&RunwareResponse{Data: []RunwareResult{{
			TaskUUID:             "guide-1",
			GuideImageBase64Data: "Zm9v",
		}}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out.Data[0].B64JSON != "Zm9v" {
			t.Fatalf("guide image not surfaced: %+v", out.Data[0])
		}
	})

	t.Run("image family still wins", func(t *testing.T) {
		out, err := ToBifrostImageGenerationResponse(&RunwareResponse{Data: []RunwareResult{{
			TaskUUID: "img-1", ImageURL: "https://x/a.png", MaskImageURL: "https://x/mask.png",
		}}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out.Data[0].URL != "https://x/a.png" {
			t.Fatalf("image family must take precedence, got %+v", out.Data[0])
		}
		if len(out.Data[0].Detections) != 0 {
			t.Fatalf("expected no detections, got %+v", out.Data[0].Detections)
		}
	})
}

// Runware only returns a per-task cost when the request opts in, so every generated task sets
// includeCost; without it the response carries no cost and the task is billed as $0.
func TestToRunwareRequests_AlwaysIncludeCost(t *testing.T) {
	gen, err := ToRunwareImageGenerationRequest(&schemas.BifrostImageGenerationRequest{
		Model: "runware:101@1",
		Input: &schemas.ImageGenerationInput{Prompt: "a teapot"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	edit, err := ToRunwareImageEditRequest(&schemas.BifrostImageEditRequest{
		Model: "runware:101@1",
		Input: &schemas.ImageEditInput{
			Images: []schemas.ImageInput{{Image: []byte("fake-image-bytes")}},
			Prompt: "make it night",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	upscale, err := ToRunwareImageEditRequest(upscaleEditRequest(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for name, req := range map[string]*RunwareInferenceRequest{
		"generation": gen, "edit": edit, "upscale": upscale,
	} {
		if req.IncludeCost == nil || !*req.IncludeCost {
			t.Fatalf("%s: expected includeCost=true, got %v", name, req.IncludeCost)
		}
	}
}

// Runware reports per-task cost (when includeCost is set); it is summed across results and surfaced
// as the provider-reported image cost so pricing uses it verbatim.
func TestToBifrostImageGenerationResponse_Cost(t *testing.T) {
	resp := &RunwareResponse{
		Data: []RunwareResult{
			{TaskUUID: "img-1", ImageURL: "https://x/1.jpg", Cost: 0.0006},
			{TaskUUID: "img-1", ImageURL: "https://x/2.jpg", Cost: 0.0004},
		},
	}

	out, err := ToBifrostImageGenerationResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Usage == nil || out.Usage.Cost == nil {
		t.Fatalf("expected provider-reported cost, got Usage=%+v", out.Usage)
	}
	if out.Usage.Cost.TotalCost != 0.001 {
		t.Fatalf("total cost = %v, want 0.001", out.Usage.Cost.TotalCost)
	}
}

// With no cost reported, Usage stays nil so datasheet pricing applies.
func TestToBifrostImageGenerationResponse_NoCost(t *testing.T) {
	resp := &RunwareResponse{Data: []RunwareResult{{TaskUUID: "img-2", ImageURL: "https://x/1.jpg"}}}
	out, err := ToBifrostImageGenerationResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Usage != nil {
		t.Fatalf("expected nil Usage when cost absent, got %+v", out.Usage)
	}
}

// type=vectorize drives the raster-to-SVG task on the edit path, and the prompt-to-SVG task on the
// generation path, where the models take a prompt and no input image.
func TestToRunwareVectorize(t *testing.T) {
	edit := upscaleEditRequest(nil)
	edit.Model = "recraft:1@1"
	edit.Params.Type = new("vectorize")
	editOut, err := ToRunwareImageEditRequest(edit)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if editOut.TaskType != taskTypeVectorize {
		t.Fatalf("edit taskType = %q, want %q", editOut.TaskType, taskTypeVectorize)
	}
	if editOut.Inputs == nil || editOut.Inputs.Image == nil {
		t.Fatalf("edit expects inputs.image, got %+v", editOut.Inputs)
	}

	gen, err := ToRunwareImageGenerationRequest(&schemas.BifrostImageGenerationRequest{
		Model: "recraft:v4@vector",
		Input: &schemas.ImageGenerationInput{Prompt: "a flat icon of a rocket"},
		Params: &schemas.ImageGenerationParameters{
			Type:         new("vectorize"),
			OutputFormat: new("svg"),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gen.TaskType != taskTypeVectorize {
		t.Fatalf("generation taskType = %q, want %q", gen.TaskType, taskTypeVectorize)
	}
	if gen.OutputFormat == nil || *gen.OutputFormat != "SVG" {
		t.Fatalf("outputFormat = %v, want SVG", gen.OutputFormat)
	}
	if gen.PositivePrompt == nil || *gen.PositivePrompt != "a flat icon of a rocket" {
		t.Fatalf("prompt not carried: %v", gen.PositivePrompt)
	}
}

// Output asset UUIDs are surfaced per entry, resolved from whichever output family applies.
func TestToBifrostImageGenerationResponse_AssetIDs(t *testing.T) {
	out, err := ToBifrostImageGenerationResponse(&RunwareResponse{Data: []RunwareResult{
		{ImageUUID: "img-1", ImageURL: "https://x/a.png"},
		{MaskImageUUID: "mask-1", MaskImageURL: "https://x/m.png"},
		{GuideImageUUID: "guide-1", GuideImageURL: "https://x/g.png"},
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, want := range []string{"img-1", "mask-1", "guide-1"} {
		if out.Data[i].ID != want {
			t.Fatalf("data[%d].id = %q, want %q", i, out.Data[i].ID, want)
		}
	}
}

// Runware resolves UUIDs and URLs itself, so a caller-supplied URL is forwarded untouched rather
// than being round-tripped through the gateway as base64.
func TestToRunwareImageEditRequest_ImageURL(t *testing.T) {
	const src = "https://assets.runware.ai/covers/prunaai-p-image-upscale.jpg"

	upscale, err := ToRunwareImageEditRequest(&schemas.BifrostImageEditRequest{
		Model:  "topazlabs:wonder@3.5",
		Input:  &schemas.ImageEditInput{Images: []schemas.ImageInput{{URL: src}}},
		Params: &schemas.ImageEditParameters{Type: new("upscale")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if upscale.Inputs == nil || upscale.Inputs.Image == nil || *upscale.Inputs.Image != src {
		t.Fatalf("inputs.image = %+v, want the URL verbatim", upscale.Inputs)
	}

	// The imageInference path takes the same shortcut for its seed image.
	edit, err := ToRunwareImageEditRequest(&schemas.BifrostImageEditRequest{
		Model: "runware:101@1",
		Input: &schemas.ImageEditInput{
			Images: []schemas.ImageInput{{URL: src}},
			Prompt: "make it night",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if edit.SeedImage == nil || *edit.SeedImage != src {
		t.Fatalf("seedImage = %v, want the URL verbatim", edit.SeedImage)
	}

	// Bytes still become a data URI, and an input with neither is rejected.
	bytesReq, err := ToRunwareImageEditRequest(upscaleEditRequest(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(*bytesReq.Inputs.Image, "data:") {
		t.Fatalf("expected a data URI for byte input, got %q", *bytesReq.Inputs.Image)
	}
	if _, err := ToRunwareImageEditRequest(&schemas.BifrostImageEditRequest{
		Model: "topazlabs:wonder@3.5",
		Input: &schemas.ImageEditInput{Images: []schemas.ImageInput{{}}},
	}); err == nil {
		t.Fatalf("expected an empty image input to be rejected")
	}
}
