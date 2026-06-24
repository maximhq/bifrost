package gemini

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

func TestToGeminiModelResourceName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "already native", input: "models/gemini-2.5-pro", want: "models/gemini-2.5-pro"},
		{name: "provider prefixed", input: "gemini/gemini-2.5-pro", want: "models/gemini-2.5-pro"},
		{name: "bare model", input: "gemini-2.5-pro", want: "models/gemini-2.5-pro"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, toGeminiModelResourceName(tc.input))
		})
	}
}

func TestToGeminiListModelsResponse_UsesNativeModelResourceName(t *testing.T) {
	resp := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{
			{ID: "gemini/gemini-2.5-pro"},
			{ID: "models/gemini-2.5-flash"},
		},
	}

	converted := ToGeminiListModelsResponse(resp)
	if assert.Len(t, converted.Models, 2) {
		assert.Equal(t, "models/gemini-2.5-pro", converted.Models[0].Name)
		assert.Equal(t, "models/gemini-2.5-flash", converted.Models[1].Name)
	}
}

func TestNormalizeModelName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "strips google prefix for gemini model",
			input: "google/gemini-2.5-pro",
			want:  "gemini-2.5-pro",
		},
		{
			name:  "strips google prefix for veo model",
			input: "google/veo-3.0-generate-preview",
			want:  "veo-3.0-generate-preview",
		},
		{
			name:  "strips google prefix for imagen model",
			input: "google/imagen-4.0-generate-001",
			want:  "imagen-4.0-generate-001",
		},
		{
			name:  "strips google prefix for gemma model",
			input: "google/gemma-3-27b-it",
			want:  "gemma-3-27b-it",
		},
		{
			name:  "trims spaces before normalizing",
			input: "  google/gemini-2.5-flash  ",
			want:  "gemini-2.5-flash",
		},
		{
			name:  "keeps unknown google model unchanged",
			input: "google/custom-model",
			want:  "google/custom-model",
		},
		{
			name:  "keeps non google prefixed model unchanged",
			input: "openai/gpt-4o",
			want:  "openai/gpt-4o",
		},
		{
			name:  "matches google prefix case-insensitively",
			input: "Google/gemini-2.5-flash",
			want:  "gemini-2.5-flash",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, NormalizeModelName(tc.input))
		})
	}
}
