package elevenlabs

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestIsElevenlabsSoundModel(t *testing.T) {
	cases := map[string]bool{
		"eleven_text_to_sound_v2": true,
		"eleven_text_to_sound_v3": true, // future-proof: prefix match
		"eleven_multilingual_v2":  false,
		"eleven_turbo_v2_5":       false,
		"":                        false,
		"scribe_v1":               false,
	}
	for model, want := range cases {
		if got := schemas.IsElevenlabsSoundModel(model); got != want {
			t.Errorf("IsElevenlabsSoundModel(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestToElevenlabsSoundGenerationRequest_NilInput(t *testing.T) {
	if got := ToElevenlabsSoundGenerationRequest(nil, nil); got != nil {
		t.Errorf("expected nil for nil request, got %+v", got)
	}
	if got := ToElevenlabsSoundGenerationRequest(nil, &schemas.BifrostSpeechRequest{}); got != nil {
		t.Errorf("expected nil for request without input, got %+v", got)
	}
}

func TestToElevenlabsSoundGenerationRequest_BasicMapping(t *testing.T) {
	req := &schemas.BifrostSpeechRequest{
		Model: "eleven_text_to_sound_v2",
		Input: &schemas.SpeechInput{Input: "glass shattering"},
	}
	out := ToElevenlabsSoundGenerationRequest(nil, req)
	if out == nil {
		t.Fatal("expected non-nil result")
	}
	if out.Text != "glass shattering" {
		t.Errorf("Text = %q, want %q", out.Text, "glass shattering")
	}
	if out.ModelID != "eleven_text_to_sound_v2" {
		t.Errorf("ModelID = %q, want %q", out.ModelID, "eleven_text_to_sound_v2")
	}
	if out.DurationSeconds != nil || out.Loop != nil || out.PromptInfluence != nil {
		t.Errorf("expected unset optional fields, got duration=%v loop=%v influence=%v", out.DurationSeconds, out.Loop, out.PromptInfluence)
	}
}

func TestToElevenlabsSoundGenerationRequest_ParamsFromExtraParams(t *testing.T) {
	extra := map[string]interface{}{
		"duration_seconds":    float64(2),
		"loop":                true,
		"prompt_influence":    float64(0.7),
		"unknown_passthrough": "keep-me",
	}
	req := &schemas.BifrostSpeechRequest{
		Model:  "eleven_text_to_sound_v2",
		Input:  &schemas.SpeechInput{Input: "thunder"},
		Params: &schemas.SpeechParameters{ExtraParams: extra},
	}
	out := ToElevenlabsSoundGenerationRequest(nil, req)
	if out.DurationSeconds == nil || *out.DurationSeconds != 2 {
		t.Errorf("DurationSeconds = %v, want 2", out.DurationSeconds)
	}
	if out.Loop == nil || *out.Loop != true {
		t.Errorf("Loop = %v, want true", out.Loop)
	}
	if out.PromptInfluence == nil || *out.PromptInfluence != 0.7 {
		t.Errorf("PromptInfluence = %v, want 0.7", out.PromptInfluence)
	}
	// Consumed keys must be removed; unknown keys must remain for passthrough.
	if _, ok := out.ExtraParams["duration_seconds"]; ok {
		t.Error("duration_seconds should be removed from ExtraParams")
	}
	if _, ok := out.ExtraParams["loop"]; ok {
		t.Error("loop should be removed from ExtraParams")
	}
	if _, ok := out.ExtraParams["prompt_influence"]; ok {
		t.Error("prompt_influence should be removed from ExtraParams")
	}
	if _, ok := out.ExtraParams["unknown_passthrough"]; !ok {
		t.Error("unknown_passthrough should be preserved in ExtraParams")
	}
	// The caller's original map must remain intact so fallback/retry reuse keeps
	// the SFX params (regression guard for the in-place mutation bug).
	for _, k := range []string{"duration_seconds", "loop", "prompt_influence"} {
		if _, ok := extra[k]; !ok {
			t.Errorf("caller's ExtraParams[%q] was mutated/removed; must be preserved", k)
		}
	}
}

func TestToElevenlabsSoundGenerationRequest_Clamping(t *testing.T) {
	cases := []struct {
		name         string
		duration     float64
		influence    float64
		wantDuration float64
		wantInfl     float64
	}{
		{"below range", 0.1, -0.5, 0.5, 0},
		{"above range", 99, 5, 30, 1},
		{"within range", 12.5, 0.3, 12.5, 0.3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := &schemas.BifrostSpeechRequest{
				Model: "eleven_text_to_sound_v2",
				Input: &schemas.SpeechInput{Input: "x"},
				Params: &schemas.SpeechParameters{ExtraParams: map[string]interface{}{
					"duration_seconds": c.duration,
					"prompt_influence": c.influence,
				}},
			}
			out := ToElevenlabsSoundGenerationRequest(nil, req)
			if out.DurationSeconds == nil || *out.DurationSeconds != c.wantDuration {
				t.Errorf("DurationSeconds = %v, want %v", out.DurationSeconds, c.wantDuration)
			}
			if out.PromptInfluence == nil || *out.PromptInfluence != c.wantInfl {
				t.Errorf("PromptInfluence = %v, want %v", out.PromptInfluence, c.wantInfl)
			}
		})
	}
}

func TestToElevenlabsSoundGenerationRequest_ResolvesAliasModelName(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "best-sfx",
		Config: &schemas.AliasConfig{
			ModelID:   "best-sfx",
			ModelName: schemas.Ptr("eleven_text_to_sound_v2"),
		},
	})
	req := &schemas.BifrostSpeechRequest{
		Model: "best-sfx",
		Input: &schemas.SpeechInput{Input: "thunder"},
	}
	out := ToElevenlabsSoundGenerationRequest(ctx, req)
	if out == nil {
		t.Fatal("expected non-nil result")
	}
	if out.ModelID != "eleven_text_to_sound_v2" {
		t.Errorf("ModelID = %q, want canonical %q", out.ModelID, "eleven_text_to_sound_v2")
	}
}

func TestClampFloat64Ptr(t *testing.T) {
	if got := clampFloat64Ptr(nil, 0, 1); got != nil {
		t.Errorf("expected nil passthrough, got %v", got)
	}
	v := 5.0
	if got := clampFloat64Ptr(&v, 0, 1); got == nil || *got != 1 {
		t.Errorf("expected clamp to 1, got %v", got)
	}
}
