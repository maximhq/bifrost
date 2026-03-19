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
