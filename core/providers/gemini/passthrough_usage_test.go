package gemini_test

import (
	"testing"

	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestExtractGeminiPassthroughUsage(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		reqBody string
		body    string
		check   func(t *testing.T, u *schemas.BifrostPassthroughUsage)
	}{
		{
			name: "generateContent text with thinking tokens",
			path: "/v1beta/models/gemini-2.5-flash:generateContent",
			body: `{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":30,"totalTokenCount":199,"thoughtsTokenCount":159,"promptTokensDetails":[{"modality":"TEXT","tokenCount":10}]}}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				// thinking tokens fold into completion: 30 + 159 = 189
				geminiMustLLM(t, u, 10, 189, 199)
				if u.LLMUsage.CompletionTokensDetails == nil || u.LLMUsage.CompletionTokensDetails.ReasoningTokens != 159 {
					t.Fatalf("reasoning tokens = %+v, want 159", u.LLMUsage.CompletionTokensDetails)
				}
			},
		},
		{
			name: "generateContent image output -> ImageUsage",
			path: "/v1beta/models/gemini-2.0-flash:generateContent",
			body: `{"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":200,"totalTokenCount":205,"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":200}]}}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.ImageUsage == nil || u.ImageUsage.OutputTokensDetails == nil ||
					u.ImageUsage.OutputTokensDetails.ImageTokens != 200 {
					t.Fatalf("image usage = %+v", u)
				}
			},
		},
		{
			// Live gemini-3.1-flash-image reply with thinkingLevel high: candidatesTokenCount sits below the IMAGE detail
			name: "generateContent image output with thinking tokens",
			path: "/v1beta/models/gemini-3.1-flash-image:generateContent",
			body: `{"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":318,"totalTokenCount":698,"thoughtsTokenCount":372,"promptTokensDetails":[{"modality":"TEXT","tokenCount":8}],"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1120}]}}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.ImageUsage == nil || u.ImageUsage.OutputTokensDetails == nil {
					t.Fatalf("image usage = %+v", u)
				}
				d := u.ImageUsage.OutputTokensDetails
				if d.ImageTokens != 1120 || d.ReasoningTokens != 372 || u.ImageUsage.OutputTokens != 690 {
					t.Fatalf("image usage = %+v, details = %+v, want 1120 image, 372 reasoning, 690 output", u.ImageUsage, d)
				}
			},
		},
		{
			name: "generateContent image output keeps the TEXT detail",
			path: "/v1beta/models/gemini-3.1-flash-image:generateContent",
			body: `{"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":418,"totalTokenCount":798,"thoughtsTokenCount":372,"candidatesTokensDetails":[{"modality":"TEXT","tokenCount":100},{"modality":"IMAGE","tokenCount":1120}]}}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.ImageUsage == nil || u.ImageUsage.OutputTokensDetails == nil {
					t.Fatalf("image usage = %+v", u)
				}
				d := u.ImageUsage.OutputTokensDetails
				if d.ImageTokens != 1120 || d.TextTokens != 100 || d.ReasoningTokens != 372 {
					t.Fatalf("details = %+v, want 1120 image, 100 text, 372 reasoning", d)
				}
			},
		},
		{
			name: "embedContent",
			path: "/v1beta/models/text-embedding-004:embedContent",
			body: `{"usageMetadata":{"promptTokenCount":7,"totalTokenCount":7}}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.LLMUsage == nil || u.LLMUsage.PromptTokens != 7 || u.LLMUsage.TotalTokens != 7 {
					t.Fatalf("embedding usage = %+v", u)
				}
			},
		},
		{
			name: "predict text embedding (token_count)",
			path: "/v1/projects/p/locations/l/publishers/google/models/text-embedding-005:predict",
			body: `{"predictions":[{"embeddings":{"statistics":{"token_count":6}}}]}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.LLMUsage == nil || u.LLMUsage.PromptTokens != 6 || u.LLMUsage.TotalTokens != 6 {
					t.Fatalf("text embedding usage = %+v", u)
				}
				if u.ImageUsage != nil {
					t.Fatalf("text embedding must not be billed as image: %+v", u.ImageUsage)
				}
			},
		},
		{
			name: "predict multimodal embedding -> embedding (not image)",
			path: "/v1/projects/p/locations/l/publishers/google/models/multimodalembedding@001:predict",
			body: `{"predictions":[{"textEmbedding":[0.1,0.2],"imageEmbedding":[0.3]}]}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.LLMUsage == nil {
					t.Fatalf("multimodal embedding usage = %+v", u)
				}
				if u.ImageUsage != nil {
					t.Fatalf("multimodal embedding must not be billed as image: %+v", u.ImageUsage)
				}
			},
		},
		{
			name: "predict imagen -> per-image count from predictions",
			path: "/v1/projects/p/locations/l/publishers/google/models/imagen-3.0-generate-002:predict",
			body: `{"predictions":[{"bytesBase64Encoded":"aaa"},{"bytesBase64Encoded":"bbb"}]}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.ImageUsage == nil || u.ImageUsage.OutputTokensDetails == nil ||
					u.ImageUsage.OutputTokensDetails.NImages != 2 {
					t.Fatalf("imagen NImages = %+v, want 2", u)
				}
			},
		},
		{
			name:    "predict imagen -> sampleCount fallback when no predictions",
			path:    "/v1/projects/p/locations/l/publishers/google/models/imagen-3.0-generate-002:predict",
			reqBody: `{"parameters":{"sampleCount":3}}`,
			body:    `{}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.ImageUsage == nil || u.ImageUsage.OutputTokensDetails == nil ||
					u.ImageUsage.OutputTokensDetails.NImages != 3 {
					t.Fatalf("imagen sampleCount fallback NImages = %+v, want 3", u)
				}
			},
		},
		{
			name:    "predictLongRunning veo -> seconds from request",
			path:    "/v1beta/models/veo-3.1-generate-preview:predictLongRunning",
			reqBody: `{"parameters":{"durationSeconds":6}}`,
			body:    `{"name":"models/veo/operations/abc"}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.VideoSeconds == nil || *u.VideoSeconds != 6 {
					t.Fatalf("veo seconds = %+v, want 6", u)
				}
			},
		},
		{
			name:    "predictLongRunning veo -> default seconds",
			path:    "/v1beta/models/veo-3.1-generate-preview:predictLongRunning",
			reqBody: `{}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.VideoSeconds == nil || *u.VideoSeconds != 8 {
					t.Fatalf("veo default seconds = %+v, want 8", u)
				}
			},
		},
		{
			name: "interactions top-level usage (standard tier dropped)",
			path: "/v1beta/interactions",
			body: `{"usage":{"total_tokens":104,"total_input_tokens":7,"total_output_tokens":27,"total_thought_tokens":70,"total_cached_tokens":0},"service_tier":"standard"}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				// output + thought folded into completion: 27 + 70 = 97
				geminiMustLLM(t, u, 7, 97, 104)
				if u.LLMUsage.CompletionTokensDetails == nil || u.LLMUsage.CompletionTokensDetails.ReasoningTokens != 70 {
					t.Fatalf("reasoning tokens = %+v, want 70", u.LLMUsage.CompletionTokensDetails)
				}
				if u.ServiceTier != nil {
					t.Fatalf("standard tier should be dropped, got %v", *u.ServiceTier)
				}
			},
		},
		{
			name: "interactions nested (streaming completed) + non-standard tier",
			path: "/v1beta/interactions",
			body: `{"interaction":{"usage":{"total_tokens":50,"total_input_tokens":10,"total_output_tokens":40},"service_tier":"priority"}}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				geminiMustLLM(t, u, 10, 40, 50)
				if u.ServiceTier == nil || *u.ServiceTier != "priority" {
					t.Fatalf("service tier = %v, want priority", u.ServiceTier)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := gemini.ExtractGeminiPassthroughUsage(tt.path, []byte(tt.reqBody), []byte(tt.body))
			tt.check(t, u)
		})
	}
}

func TestHasGeminiPassthroughUsage(t *testing.T) {
	tests := []struct {
		name  string
		event string
		want  bool
	}{
		{"usageMetadata", `{"usageMetadata":{"totalTokenCount":5}}`, true},
		{"interactions top-level usage", `{"usage":{"total_tokens":5}}`, true},
		{"interactions nested", `{"interaction":{"usage":{"total_tokens":5}}}`, true},
		{"content chunk (no usage)", `{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gemini.HasGeminiPassthroughUsage([]byte(tt.event)); got != tt.want {
				t.Fatalf("HasGeminiPassthroughUsage = %v, want %v", got, tt.want)
			}
		})
	}
}

func geminiMustLLM(t *testing.T, u *schemas.BifrostPassthroughUsage, prompt, completion, total int) {
	t.Helper()
	if u == nil || u.LLMUsage == nil {
		t.Fatalf("expected LLMUsage, got %+v", u)
	}
	if u.LLMUsage.PromptTokens != prompt || u.LLMUsage.CompletionTokens != completion || u.LLMUsage.TotalTokens != total {
		t.Fatalf("LLMUsage = {prompt:%d completion:%d total:%d}, want {%d %d %d}",
			u.LLMUsage.PromptTokens, u.LLMUsage.CompletionTokens, u.LLMUsage.TotalTokens, prompt, completion, total)
	}
}

func TestClassifyPassthroughPath(t *testing.T) {
	tests := []struct {
		path string
		want schemas.RequestType
	}{
		{"/models/gemini-2.5-flash:generateContent", schemas.ResponsesRequest},
		{"/models/gemini-2.5-flash:streamGenerateContent?alt=sse", schemas.ResponsesRequest},
		{"/projects/p/locations/us-central1/publishers/google/models/gemini-2.5-pro:generateContent", schemas.ResponsesRequest},
		{"/models/gemini-2.5-flash:countTokens", schemas.CountTokensRequest},
		{"/models/text-embedding-004:embedContent", schemas.EmbeddingRequest},
		{"/models/text-embedding-004:batchEmbedContents", schemas.EmbeddingRequest},
		{"/models/imagen-4.0-generate-001:predict", schemas.ImageGenerationRequest},
		// Vertex serves embedding models on :predict too; they are embeddings, not images.
		{"/projects/p/locations/us-central1/publishers/google/models/multimodalembedding@001:predict", schemas.EmbeddingRequest},
		{"/projects/p/locations/us-central1/publishers/google/models/text-embedding-005:predict", schemas.EmbeddingRequest},
		{"/projects/p/locations/us-central1/publishers/google/models/gemini-embedding-001:predict?alt=json", schemas.EmbeddingRequest},
		{"/models/veo-3.0-generate-001:predictLongRunning", schemas.VideoGenerationRequest},
		{"/interactions", ""},
		{"/interactions/abc:cancel", ""},
		{"/models", ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := gemini.ClassifyPassthroughPath(tt.path); got != tt.want {
				t.Fatalf("ClassifyPassthroughPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsInteractionsPassthroughPath(t *testing.T) {
	if !gemini.IsInteractionsPassthroughPath("/v1beta/interactions?alt=sse") {
		t.Fatal("expected /interactions to be an Interactions path")
	}
	if gemini.IsInteractionsPassthroughPath("/models/gemini-2.5-flash:generateContent") {
		t.Fatal("generateContent is not an Interactions path")
	}
}

// TestExtractGeminiPassthroughUsage_EmbeddingPredictBilling pins that a Vertex embedding model on
// :predict is billed from its predict response (token statistics) whatever the path classifies as,
// and that an Imagen :predict is still billed per image.
func TestExtractGeminiPassthroughUsage_EmbeddingPredictBilling(t *testing.T) {
	embedding := gemini.ExtractGeminiPassthroughUsage(
		"/projects/p/locations/us-central1/publishers/google/models/text-embedding-005:predict",
		[]byte(`{"instances":[{"content":"hi"}]}`),
		[]byte(`{"predictions":[{"embeddings":{"values":[0.1,0.2],"statistics":{"token_count":7,"truncated":false}}}]}`),
	)
	if embedding == nil || embedding.LLMUsage == nil || embedding.LLMUsage.PromptTokens != 7 || embedding.LLMUsage.TotalTokens != 7 {
		t.Fatalf("embedding :predict usage = %+v, want 7 prompt/total tokens", embedding)
	}

	imagen := gemini.ExtractGeminiPassthroughUsage(
		"/models/imagen-4.0-generate-001:predict",
		[]byte(`{"instances":[{"prompt":"a fox"}],"parameters":{"sampleCount":1}}`),
		[]byte(`{"predictions":[{"bytesBase64Encoded":"aGk=","mimeType":"image/png"}]}`),
	)
	if imagen == nil || imagen.ImageUsage == nil {
		t.Fatalf("imagen :predict usage = %+v, want per-image usage", imagen)
	}
}
