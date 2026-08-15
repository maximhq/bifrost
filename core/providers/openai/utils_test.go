package openai

import "testing"

// glm5Minor backs the GLM version floors: isGLM52OrLater (reasoning_effort
// support incl. "max") and isGLM53OrLaterModel (the max/high/low-only clamp).
func TestGLM5Minor(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"glm-5", -1},
		{"glm-5.x", -1},
		{"glm-5.1", 1},
		{"glm-5.2", 2},
		{"glm-5.2[1m]", 2},
		{"glm-5.2-air", 2},
		{"glm-5.3", 3},
		{"glm-5.5", 5},
		{"glm-5.10", 10},
		{"glm-4.7", -1},
		{"kimi-k3", -1},
		{"", -1},
	}
	for _, tt := range tests {
		if got := glm5Minor(tt.model); got != tt.want {
			t.Errorf("glm5Minor(%q) = %d, want %d", tt.model, got, tt.want)
		}
	}
}

func TestIsGLM53OrLaterModel(t *testing.T) {
	schemas := []struct {
		model string
		want  bool
	}{
		{"glm-5.3", true},
		{"GLM-5.3", true},
		{"glm-5.3-air", true},
		{"glm-5.5", true},
		{"glm-5.2", false},
		{"glm-5.2[1m]", false},
		{"glm-5.1", false},
		{"glm-5", false},
		{"glm-4.7", false},
		{"gpt-5.6", false},
		{"", false},
	}
	for _, tt := range schemas {
		if got := isGLM53OrLaterModel(tt.model); got != tt.want {
			t.Errorf("isGLM53OrLaterModel(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}
