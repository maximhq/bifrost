package replicate

import (
	"fmt"
	"math"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func newUpscaleRequest(extraParams map[string]interface{}) *schemas.BifrostImageGenerationRequest {
	return &schemas.BifrostImageGenerationRequest{
		Model: "prunaai/p-image-upscale",
		Params: &schemas.ImageGenerationParameters{
			ExtraParams: extraParams,
		},
	}
}

func TestResolveUpscaleOutputPixels_TargetMode(t *testing.T) {
	req := newUpscaleRequest(map[string]interface{}{
		"target":       float64(16),
		"upscale_mode": "target",
	})
	got := resolveUpscaleOutputPixels(req, nil)
	want := 16_000_000
	if got != want {
		t.Errorf("target=16 upscale_mode=target: want %d pixels, got %d", want, got)
	}
}

func TestResolveUpscaleOutputPixels_TargetMode_DefaultsWhenModeOmitted(t *testing.T) {
	// upscale_mode defaults to "target" per Replicate's docs, so a bare
	// "target" param (no explicit upscale_mode) must still resolve.
	req := newUpscaleRequest(map[string]interface{}{
		"target": float64(8),
	})
	got := resolveUpscaleOutputPixels(req, nil)
	want := 8_000_000
	if got != want {
		t.Errorf("target=8 (no upscale_mode): want %d pixels, got %d", want, got)
	}
}

func TestResolveUpscaleOutputPixels_FactorMode_IgnoresTargetFallsBackToMetrics(t *testing.T) {
	// A stray "target" value must not be used once upscale_mode explicitly
	// says "factor" — factor mode's output size depends on the input image,
	// not the target param.
	req := newUpscaleRequest(map[string]interface{}{
		// Deliberately distinct from the metrics band's 16MP upper bound, so a
		// regression that honours "target" in factor mode fails this test
		// instead of coincidentally producing the same pixel count.
		"target":       float64(8), // should be ignored
		"upscale_mode": "factor",
		"factor":       float64(4),
	})
	resolutionTarget := "8-16MP"
	prediction := &ReplicatePredictionResponse{
		Metrics: &ReplicateMetrics{ResolutionTarget: &resolutionTarget},
	}
	got := resolveUpscaleOutputPixels(req, prediction)
	want := 16_000_000 // upper bound of the "8-16MP" band, not the ignored target=8
	if got != want {
		t.Errorf("factor mode: want %d pixels (from metrics band), got %d", want, got)
	}
}

func TestResolveUpscaleOutputPixels_NoSignal(t *testing.T) {
	req := newUpscaleRequest(nil)
	if got := resolveUpscaleOutputPixels(req, nil); got != 0 {
		t.Errorf("no target, no metrics: want 0, got %d", got)
	}
}

func TestResolveUpscaleOutputPixelsFromMetrics(t *testing.T) {
	cases := []struct {
		name string
		band *string
		want int
	}{
		{"range band", strPtr("8-16MP"), 16_000_000},
		{"single value band", strPtr("16MP"), 16_000_000},
		{"lowercase mp", strPtr("4-8mp"), 8_000_000},
		{"fractional bounds", strPtr("0.5-1MP"), 1_000_000},
		{"nil metrics field", nil, 0},
		// Malformed bands must not yield a billable size: this value feeds
		// resolution-tiered pricing, so parsing only the trailing component
		// would let junk through as a real output resolution.
		{"non-numeric lower bound", strPtr("invalid-16MP"), 0},
		{"empty middle component", strPtr("8--16MP"), 0},
		{"descending bounds", strPtr("16-8MP"), 0},
		{"three components", strPtr("4-8-16MP"), 0},
		{"zero lower bound", strPtr("0-16MP"), 0},
		// Non-finite floats parse without error, and every comparison against
		// NaN is false, so they slip past ordering checks unless rejected
		// outright. Infinity additionally saturates the int conversion.
		{"nan lower bound", strPtr("NaN-16MP"), 0},
		{"nan upper bound", strPtr("8-NaNMP"), 0},
		{"infinite upper bound", strPtr("InfMP"), 0},
		{"infinite lower bound", strPtr("Inf-16MP"), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prediction := &ReplicatePredictionResponse{
				Metrics: &ReplicateMetrics{ResolutionTarget: tc.band},
			}
			got := resolveUpscaleOutputPixelsFromMetrics(prediction)
			if got != tc.want {
				t.Errorf("want %d, got %d", tc.want, got)
			}
		})
	}
	// nil prediction / nil metrics must not panic.
	if got := resolveUpscaleOutputPixelsFromMetrics(nil); got != 0 {
		t.Errorf("nil prediction: want 0, got %d", got)
	}
	if got := resolveUpscaleOutputPixelsFromMetrics(&ReplicatePredictionResponse{}); got != 0 {
		t.Errorf("nil metrics: want 0, got %d", got)
	}
}

func TestFormatSquarePixelSize_NeverUndercountsAtBoundary(t *testing.T) {
	// 8,000,000 has no integer square root (sqrt ≈ 2828.43); ceil-rounding
	// must keep width*height >= 8,000,000 so an exact-boundary target
	// (e.g. target: 8) never gets miscategorized into the tier below it.
	size := formatSquarePixelSize(8_000_000)
	w, h := parseWxH(t, size)
	if w*h < 8_000_000 {
		t.Errorf("formatSquarePixelSize(8_000_000) = %q, width*height = %d, want >= 8_000_000", size, w*h)
	}

	// A perfect square should round-trip exactly.
	size = formatSquarePixelSize(16_000_000)
	if size != "4000x4000" {
		t.Errorf("formatSquarePixelSize(16_000_000) = %q, want 4000x4000", size)
	}
}

func TestApplyUpscaleOutputResolution(t *testing.T) {
	req := newUpscaleRequest(map[string]interface{}{
		"target":       float64(16),
		"upscale_mode": "target",
	})
	resp := &schemas.BifrostImageGenerationResponse{}
	applyUpscaleOutputResolution(req, nil, resp)

	if resp.ImageGenerationResponseParameters == nil || resp.ImageGenerationResponseParameters.Size == "" {
		t.Fatal("expected Size to be backfilled")
	}
	w, h := parseWxH(t, resp.ImageGenerationResponseParameters.Size)
	if w*h < 16_000_000 {
		t.Errorf("backfilled size %q has %d total pixels, want >= 16_000_000", resp.ImageGenerationResponseParameters.Size, w*h)
	}
}

func TestApplyUpscaleOutputResolution_DoesNotOverwriteExistingSize(t *testing.T) {
	req := newUpscaleRequest(map[string]interface{}{"target": float64(16)})
	resp := &schemas.BifrostImageGenerationResponse{
		ImageGenerationResponseParameters: &schemas.ImageGenerationResponseParameters{Size: "1234x1234"},
	}
	applyUpscaleOutputResolution(req, nil, resp)

	if resp.ImageGenerationResponseParameters.Size != "1234x1234" {
		t.Errorf("existing Size was overwritten: got %q", resp.ImageGenerationResponseParameters.Size)
	}
}

func TestApplyUpscaleOutputResolution_NoSignalLeavesSizeEmpty(t *testing.T) {
	req := newUpscaleRequest(nil)
	resp := &schemas.BifrostImageGenerationResponse{}
	applyUpscaleOutputResolution(req, nil, resp)

	if resp.ImageGenerationResponseParameters != nil && resp.ImageGenerationResponseParameters.Size != "" {
		t.Errorf("expected no Size to be set, got %q", resp.ImageGenerationResponseParameters.Size)
	}
}

func strPtr(s string) *string { return &s }

func parseWxH(t *testing.T, size string) (int, int) {
	t.Helper()
	var w, h int
	n, err := fmt.Sscanf(size, "%dx%d", &w, &h)
	if err != nil || n != 2 {
		t.Fatalf("could not parse size %q: %v", size, err)
	}
	return w, h
}

func newUpscaleEditRequest(params *schemas.ImageEditParameters) *schemas.BifrostImageEditRequest {
	return &schemas.BifrostImageEditRequest{
		Model:  "prunaai/p-image-upscale",
		Input:  &schemas.ImageEditInput{Prompt: "upscale"},
		Params: params,
	}
}

func TestResolveUpscaleEditOutputPixels_TargetMegapixels(t *testing.T) {
	// The edit path has a first-class target_megapixels param, so an upscale
	// routed through /images/edits must resolve without any extra params.
	req := newUpscaleEditRequest(&schemas.ImageEditParameters{
		TargetMegapixels: schemas.Ptr(16),
	})
	got := resolveUpscaleEditOutputPixels(req, nil)
	want := 16_000_000
	if got != want {
		t.Errorf("target_megapixels=16: want %d pixels, got %d", want, got)
	}
}

func TestResolveUpscaleEditOutputPixels_UpscaleFactorIgnoresTargetFallsBackToMetrics(t *testing.T) {
	// upscale_factor is mutually exclusive with target_megapixels and makes the
	// output size depend on the input image, so a stray target must be ignored
	// in favour of the band Replicate reports back.
	req := newUpscaleEditRequest(&schemas.ImageEditParameters{
		UpscaleFactor:    schemas.Ptr(4),
		TargetMegapixels: schemas.Ptr(8), // should be ignored
	})
	resolutionTarget := "8-16MP"
	prediction := &ReplicatePredictionResponse{
		Metrics: &ReplicateMetrics{ResolutionTarget: &resolutionTarget},
	}
	got := resolveUpscaleEditOutputPixels(req, prediction)
	want := 16_000_000
	if got != want {
		t.Errorf("upscale_factor mode: want %d pixels (from metrics band), got %d", want, got)
	}
}

func TestResolveUpscaleEditOutputPixels_ExtraParamsTarget(t *testing.T) {
	// Callers passing Replicate's native param names straight through must
	// resolve on the edit path too, not just on image generation.
	req := newUpscaleEditRequest(&schemas.ImageEditParameters{
		ExtraParams: map[string]interface{}{"target": float64(4)},
	})
	got := resolveUpscaleEditOutputPixels(req, nil)
	want := 4_000_000
	if got != want {
		t.Errorf("extra param target=4: want %d pixels, got %d", want, got)
	}
}

func TestResolveUpscaleEditOutputPixels_NoSignal(t *testing.T) {
	if got := resolveUpscaleEditOutputPixels(newUpscaleEditRequest(nil), nil); got != 0 {
		t.Errorf("no signal: want 0, got %d", got)
	}
}

func TestApplyUpscaleEditOutputResolution_SetsSize(t *testing.T) {
	req := newUpscaleEditRequest(&schemas.ImageEditParameters{TargetMegapixels: schemas.Ptr(16)})
	response := &schemas.BifrostImageGenerationResponse{}
	applyUpscaleEditOutputResolution(req, nil, response)
	if response.ImageGenerationResponseParameters == nil {
		t.Fatal("want ImageGenerationResponseParameters populated, got nil")
	}
	if got := response.ImageGenerationResponseParameters.Size; got != "4000x4000" {
		t.Errorf("want size 4000x4000 (sqrt of 16MP), got %q", got)
	}
}

func TestApplyUpscaleEditOutputResolution_PreservesExistingSize(t *testing.T) {
	req := newUpscaleEditRequest(&schemas.ImageEditParameters{TargetMegapixels: schemas.Ptr(16)})
	response := &schemas.BifrostImageGenerationResponse{
		ImageGenerationResponseParameters: &schemas.ImageGenerationResponseParameters{Size: "1024x1024"},
	}
	applyUpscaleEditOutputResolution(req, nil, response)
	if got := response.ImageGenerationResponseParameters.Size; got != "1024x1024" {
		t.Errorf("want provider-reported size preserved, got %q", got)
	}
}

func TestApplyUpscaleStreamOutputResolution(t *testing.T) {
	t.Run("sets size on completion chunk", func(t *testing.T) {
		chunk := &schemas.BifrostImageGenerationStreamResponse{}
		applyUpscaleStreamOutputResolution(16_000_000, chunk)
		if chunk.Size != "4000x4000" {
			t.Errorf("want size 4000x4000, got %q", chunk.Size)
		}
	})
	t.Run("no-op without a resolved pixel count", func(t *testing.T) {
		chunk := &schemas.BifrostImageGenerationStreamResponse{}
		applyUpscaleStreamOutputResolution(0, chunk)
		if chunk.Size != "" {
			t.Errorf("want size left empty, got %q", chunk.Size)
		}
	})
	t.Run("preserves a size already set", func(t *testing.T) {
		chunk := &schemas.BifrostImageGenerationStreamResponse{Size: "1024x1024"}
		applyUpscaleStreamOutputResolution(16_000_000, chunk)
		if chunk.Size != "1024x1024" {
			t.Errorf("want existing size preserved, got %q", chunk.Size)
		}
	})
}


func TestResolveUpscaleOutputPixels_RejectsNonFiniteTarget(t *testing.T) {
	// A non-finite target must not saturate the conversion into a
	// billion-pixel output that then selects the top pricing tier.
	for name, target := range map[string]float64{
		"positive infinity": math.Inf(1),
		"negative infinity": math.Inf(-1),
		"nan":               math.NaN(),
	} {
		t.Run(name, func(t *testing.T) {
			req := newUpscaleRequest(map[string]interface{}{"target": target})
			if got := resolveUpscaleOutputPixels(req, nil); got != 0 {
				t.Errorf("want 0 pixels, got %d", got)
			}
		})
	}
}

func TestResolveUpscaleOutputPixels_RoundsFractionalTargetUp(t *testing.T) {
	// formatSquarePixelSize rounds up so a value on a tier boundary is never
	// billed one tier down; truncating here would defeat that on the way in.
	req := newUpscaleRequest(map[string]interface{}{"target": 16.0000001})
	got := resolveUpscaleOutputPixels(req, nil)
	want := 16_000_001
	if got != want {
		t.Errorf("target=16.0000001: want %d pixels, got %d", want, got)
	}
}

func TestResolveUpscaleEditOutputPixels_RejectsOverflowingTargetMegapixels(t *testing.T) {
	// int64 multiplication wraps negative well before this, so the bound has
	// to be checked before the multiply rather than after it.
	req := newUpscaleEditRequest(&schemas.ImageEditParameters{
		TargetMegapixels: schemas.Ptr(9_300_000_000_000),
	})
	if got := resolveUpscaleEditOutputPixels(req, nil); got != 0 {
		t.Errorf("want 0 pixels for an out-of-range target, got %d", got)
	}
}
