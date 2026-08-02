package openai_test

import (
	"bytes"
	"mime/multipart"
	"testing"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

// multipartBody builds a multipart/form-data body with the given fields, matching what the
// video passthrough extractor sniffs (boundary on the first line).
func multipartBody(t *testing.T, fields map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes()
}

func TestExtractOpenAIPassthroughUsage(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		path    string
		reqBody string
		body    string
		check   func(t *testing.T, u *schemas.BifrostPassthroughUsage)
	}{
		{
			name: "chat completions usage + service tier",
			path: "/v1/chat/completions",
			body: `{"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15},"service_tier":"default"}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				mustLLM(t, u, 10, 5, 15)
				if u.ServiceTier == nil || *u.ServiceTier != schemas.BifrostServiceTierDefault {
					t.Fatalf("service tier = %v, want default", u.ServiceTier)
				}
			},
		},
		{
			name:  "chat completions zero usage -> nil",
			path:  "/v1/chat/completions",
			body:  `{"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
			check: mustNil,
		},
		{
			name:  "chat completions content delta (no usage) -> nil",
			path:  "/v1/chat/completions",
			body:  `{"choices":[{"delta":{"content":"hi"}}]}`,
			check: mustNil,
		},
		{
			name: "responses top-level usage with reasoning + search queries",
			path: "/v1/responses",
			body: `{"usage":{"input_tokens":20,"output_tokens":8,"total_tokens":28,"output_tokens_details":{"reasoning_tokens":3,"num_search_queries":2}}}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				mustLLM(t, u, 20, 8, 28)
				d := u.LLMUsage.CompletionTokensDetails
				if d == nil || d.ReasoningTokens != 3 {
					t.Fatalf("reasoning tokens = %v, want 3", d)
				}
				if d.NumSearchQueries == nil || *d.NumSearchQueries != 2 {
					t.Fatalf("num search queries = %v, want 2", d.NumSearchQueries)
				}
			},
		},
		{
			name: "responses nested response.usage",
			path: "/v1/responses",
			body: `{"response":{"usage":{"input_tokens":20,"output_tokens":8,"total_tokens":28}}}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				mustLLM(t, u, 20, 8, 28)
			},
		},
		{
			name: "embeddings",
			path: "/v1/embeddings",
			body: `{"usage":{"prompt_tokens":12,"total_tokens":12}}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.LLMUsage == nil || u.LLMUsage.PromptTokens != 12 || u.LLMUsage.TotalTokens != 12 {
					t.Fatalf("embeddings usage = %+v", u)
				}
			},
		},
		{
			name:    "speech char count from request",
			path:    "/v1/audio/speech",
			reqBody: `{"input":"héllo"}`, // 5 runes
			body:    "",
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.AudioInputChars != 5 {
					t.Fatalf("audio input chars = %+v, want 5", u)
				}
			},
		},
		{
			name: "transcription token usage",
			path: "/v1/audio/transcriptions",
			body: `{"usage":{"type":"tokens","input_tokens":4,"total_tokens":10,"input_token_details":{"audio_tokens":3,"text_tokens":1},"seconds":2}}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.LLMUsage == nil || u.LLMUsage.PromptTokens != 4 || u.LLMUsage.TotalTokens != 10 {
					t.Fatalf("transcription usage = %+v", u)
				}
				if u.AudioTokenDetails == nil || u.AudioTokenDetails.AudioTokens != 3 {
					t.Fatalf("audio token details = %+v", u.AudioTokenDetails)
				}
				if u.AudioSeconds == nil || *u.AudioSeconds != 2 {
					t.Fatalf("audio seconds = %v, want 2", u.AudioSeconds)
				}
			},
		},
		{
			name: "transcription duration fallback",
			path: "/v1/audio/transcriptions",
			body: `{"duration":3}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.AudioSeconds == nil || *u.AudioSeconds != 3 {
					t.Fatalf("audio seconds = %+v, want 3", u)
				}
			},
		},
		{
			name: "image generation response usage",
			path: "/v1/images/generations",
			body: `{"usage":{"input_tokens":5,"output_tokens":100,"total_tokens":105}}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.ImageUsage == nil || u.ImageUsage.TotalTokens != 105 {
					t.Fatalf("image usage = %+v", u)
				}
				if u.LLMUsage == nil || u.LLMUsage.TotalTokens != 105 {
					t.Fatalf("image llm usage = %+v", u.LLMUsage)
				}
			},
		},
		{
			name:    "image variation count from request n",
			path:    "/v1/images/variations",
			reqBody: `{"n":2}`,
			body:    `{"data":[{"b64_json":"x"},{"b64_json":"y"}]}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.ImageUsage == nil || u.ImageUsage.OutputTokensDetails == nil ||
					u.ImageUsage.OutputTokensDetails.NImages != 2 {
					t.Fatalf("image NImages = %+v", u)
				}
			},
		},
		{
			name: "video seconds default (no body)",
			path: "/v1/videos",
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.VideoSeconds == nil || *u.VideoSeconds != 4 {
					t.Fatalf("video seconds = %+v, want default 4", u)
				}
			},
		},
		{
			name: "container create with memory limit",
			path: "/v1/containers",
			body: `{"object":"container","id":"cntr_1","memory_limit":"1g"}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.ContainerIdentifier != "container-1g" {
					t.Fatalf("container id = %+v, want container-1g", u)
				}
			},
		},
		{
			name: "container create without memory limit",
			path: "/v1/containers",
			body: `{"object":"container","id":"cntr_1"}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.ContainerIdentifier != "container" {
					t.Fatalf("container id = %+v, want container", u)
				}
			},
		},
		{
			name:  "container list -> nil (not billable)",
			path:  "/v1/containers",
			body:  `{"object":"list","data":[{"object":"container","id":"c1"}]}`,
			check: mustNil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method := tt.method
			if method == "" {
				method = "POST"
			}
			u := openai.ExtractOpenAIPassthroughUsage(method, tt.path, []byte(tt.reqBody), []byte(tt.body))
			tt.check(t, u)
		})
	}
}

func TestExtractOpenAIPassthroughUsage_VideoMultipartSeconds(t *testing.T) {
	reqBody := multipartBody(t, map[string]string{"seconds": "6"})
	u := openai.ExtractOpenAIPassthroughUsage("POST", "/v1/videos", reqBody, nil)
	if u == nil || u.VideoSeconds == nil || *u.VideoSeconds != 6 {
		t.Fatalf("video seconds = %+v, want 6", u)
	}
}

// Video usage is billable only on POST. GET (list/retrieve) and DELETE must not accrue
// per-second video usage even though the path contains "/videos".
func TestExtractOpenAIPassthroughUsage_VideoNonPOSTNotBilled(t *testing.T) {
	for _, method := range []string{"GET", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			if u := openai.ExtractOpenAIPassthroughUsage(method, "/v1/videos", nil, nil); u != nil {
				t.Fatalf("%s /v1/videos = %+v, want nil", method, u)
			}
		})
	}
}

// JSON create-video bodies carry `seconds` as a top-level field (numeric or string).
func TestExtractOpenAIPassthroughUsage_VideoJSONSeconds(t *testing.T) {
	for _, body := range []string{`{"seconds":12}`, `{"seconds":"12"}`} {
		u := openai.ExtractOpenAIPassthroughUsage("POST", "/v1/videos", []byte(body), nil)
		if u == nil || u.VideoSeconds == nil || *u.VideoSeconds != 12 {
			t.Fatalf("video seconds for %s = %+v, want 12", body, u)
		}
	}
}

// /v1/images/variations is multipart/form-data; size/quality/n ride as form fields.
func TestExtractOpenAIPassthroughUsage_ImageMultipartParams(t *testing.T) {
	reqBody := multipartBody(t, map[string]string{"size": "1024x1024", "quality": "high", "n": "3"})
	u := openai.ExtractOpenAIPassthroughUsage("POST", "/v1/images/variations", reqBody, nil)
	if u == nil {
		t.Fatal("expected usage, got nil")
	}
	if u.ImageSize != "1024x1024" {
		t.Fatalf("image size = %q, want 1024x1024", u.ImageSize)
	}
	if u.ImageQuality != "high" {
		t.Fatalf("image quality = %q, want high", u.ImageQuality)
	}
	if u.ImageUsage == nil || u.ImageUsage.OutputTokensDetails == nil ||
		u.ImageUsage.OutputTokensDetails.NImages != 3 {
		t.Fatalf("image NImages = %+v, want 3", u)
	}
}

func TestHasOpenAIPassthroughUsage(t *testing.T) {
	tests := []struct {
		name  string
		event string
		want  bool
	}{
		{"top-level usage", `{"usage":{"total_tokens":5}}`, true},
		{"nested response.usage", `{"response":{"usage":{"total_tokens":5}}}`, true},
		{"content delta", `{"choices":[{"delta":{"content":"hi"}}]}`, false},
		{"ping", `{"type":"ping"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := openai.HasOpenAIPassthroughUsage([]byte(tt.event)); got != tt.want {
				t.Fatalf("HasOpenAIPassthroughUsage = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---- shared assertion helpers ----

func mustNil(t *testing.T, u *schemas.BifrostPassthroughUsage) {
	t.Helper()
	if u != nil {
		t.Fatalf("expected nil usage, got %+v", u)
	}
}

func mustLLM(t *testing.T, u *schemas.BifrostPassthroughUsage, prompt, completion, total int) {
	t.Helper()
	if u == nil || u.LLMUsage == nil {
		t.Fatalf("expected LLMUsage, got %+v", u)
	}
	if u.LLMUsage.PromptTokens != prompt || u.LLMUsage.CompletionTokens != completion || u.LLMUsage.TotalTokens != total {
		t.Fatalf("LLMUsage = {prompt:%d completion:%d total:%d}, want {%d %d %d}",
			u.LLMUsage.PromptTokens, u.LLMUsage.CompletionTokens, u.LLMUsage.TotalTokens, prompt, completion, total)
	}
}
