package openai

import "testing"

// isGLM52OrLater is the version-floor matcher backing both
// supportsMaxReasoningEffort and the zhipu reasoning-effort gate — it must
// cover future GLM revisions (glm-5.3, glm-5.5, ...) and the Coding Plan
// aliases, while staying below the 5.2 floor for GLM-5.0/5.1 and GLM-4.x.
func TestIsGLM52OrLater(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"glm-5.2", true},
		{"glm-5.2[1m]", true},
		{"glm-5.2-air", true},
		{"glm-5.3", true},
		{"glm-5.5", true},
		{"glm-5.10", true},
		{"glm-5.1", false},
		{"glm-5", false},
		{"glm-5.x", false},
		{"glm-4.7", false},
		{"glm-4.5-flash", false},
		{"glm-4.6v", false},
		{"kimi-k3", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isGLM52OrLater(tt.model); got != tt.want {
			t.Errorf("isGLM52OrLater(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}
