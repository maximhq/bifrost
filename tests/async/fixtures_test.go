package async

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

// endpointCase describes a single async endpoint and the request payload to send.
type endpointCase struct {
	name       string
	submitPath string // POST target, e.g. /v1/async/chat/completions
	pollBase   string // GET base; job ID is appended as /{job_id}
	body       map[string]any
	multipart  *multipartCase
}

// multipartCase holds fields and named files for a multipart/form-data submission.
type multipartCase struct {
	fields map[string]string
	files  map[string]fileFixture
}

type fileFixture struct {
	filename string
	data     []byte
}

// defaultModels maps each ASYNC_*_MODEL env key to its default model string.
var defaultModels = map[string]string{
	"ASYNC_TEXT_COMPLETION_MODEL": "openai/gpt-3.5-turbo-instruct",
	"ASYNC_CHAT_COMPLETION_MODEL": "openai/gpt-4o-mini",
	"ASYNC_RESPONSES_MODEL":       "openai/gpt-4o-mini",
	"ASYNC_EMBEDDINGS_MODEL":      "openai/text-embedding-3-small",
	"ASYNC_SPEECH_MODEL":          "openai/tts-1",
	"ASYNC_TRANSCRIPTION_MODEL":   "openai/whisper-1",
	"ASYNC_IMAGE_GEN_MODEL":       "openai/dall-e-3",
	"ASYNC_IMAGE_EDIT_MODEL":      "openai/dall-e-2",
	"ASYNC_IMAGE_VARIATION_MODEL": "openai/dall-e-2",
	"ASYNC_RERANK_MODEL":          "cohere/rerank-english-v3.0",
	"ASYNC_OCR_MODEL":             "mistral/mistral-ocr-latest",
}

// modelFor returns the env-var override for envKey, falling back to the default in defaultModels.
func modelFor(envKey string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return defaultModels[envKey]
}

// endpointCases returns the full set of async endpoint fixtures, one per supported endpoint.
// Override any model via the corresponding ASYNC_*_MODEL environment variable.
func endpointCases() []endpointCase {
	return []endpointCase{
		{
			name:       "text_completions",
			submitPath: "/v1/async/completions",
			pollBase:   "/v1/async/completions",
			body: map[string]any{
				"model":      modelFor("ASYNC_TEXT_COMPLETION_MODEL"),
				"prompt":     "Say hello in one word.",
				"max_tokens": 10,
			},
		},
		{
			name:       "chat_completions",
			submitPath: "/v1/async/chat/completions",
			pollBase:   "/v1/async/chat/completions",
			body: map[string]any{
				"model": modelFor("ASYNC_CHAT_COMPLETION_MODEL"),
				"messages": []map[string]any{
					{"role": "user", "content": "Say hello in one word."},
				},
				"max_tokens": 10,
			},
		},
		{
			name:       "responses",
			submitPath: "/v1/async/responses",
			pollBase:   "/v1/async/responses",
			body: map[string]any{
				"model": modelFor("ASYNC_RESPONSES_MODEL"),
				"input": "Say hello in one word.",
			},
		},
		{
			name:       "embeddings",
			submitPath: "/v1/async/embeddings",
			pollBase:   "/v1/async/embeddings",
			body: map[string]any{
				"model": modelFor("ASYNC_EMBEDDINGS_MODEL"),
				"input": "Hello world",
			},
		},
		{
			name:       "speech",
			submitPath: "/v1/async/audio/speech",
			pollBase:   "/v1/async/audio/speech",
			body: map[string]any{
				"model": modelFor("ASYNC_SPEECH_MODEL"),
				"input": "Hello",
				"voice": "alloy",
			},
		},
		{
			name:       "transcriptions",
			submitPath: "/v1/async/audio/transcriptions",
			pollBase:   "/v1/async/audio/transcriptions",
			multipart: &multipartCase{
				fields: map[string]string{
					"model": modelFor("ASYNC_TRANSCRIPTION_MODEL"),
				},
				files: map[string]fileFixture{
					"file": {filename: "sample.mp3", data: sampleAudio()},
				},
			},
		},
		{
			name:       "image_generations",
			submitPath: "/v1/async/images/generations",
			pollBase:   "/v1/async/images/generations",
			body: map[string]any{
				"model":  modelFor("ASYNC_IMAGE_GEN_MODEL"),
				"prompt": "A simple red circle on a white background",
				"n":      1,
				"size":   "1024x1024",
			},
		},
		{
			name:       "image_edits",
			submitPath: "/v1/async/images/edits",
			pollBase:   "/v1/async/images/edits",
			multipart: &multipartCase{
				fields: map[string]string{
					"model":  modelFor("ASYNC_IMAGE_EDIT_MODEL"),
					"prompt": "Make it blue",
					"n":      "1",
					"size":   "256x256",
				},
				files: map[string]fileFixture{
					"image": {filename: "image.png", data: samplePNG()},
				},
			},
		},
		{
			name:       "image_variations",
			submitPath: "/v1/async/images/variations",
			pollBase:   "/v1/async/images/variations",
			multipart: &multipartCase{
				fields: map[string]string{
					"model": modelFor("ASYNC_IMAGE_VARIATION_MODEL"),
					"n":     "1",
					"size":  "256x256",
				},
				files: map[string]fileFixture{
					"image": {filename: "image.png", data: samplePNG()},
				},
			},
		},
		{
			name:       "rerank",
			submitPath: "/v1/async/rerank",
			pollBase:   "/v1/async/rerank",
			body: map[string]any{
				"model": modelFor("ASYNC_RERANK_MODEL"),
				"query": "What is the capital of France?",
				"documents": []map[string]any{
					{"text": "Paris is the capital of France."},
					{"text": "London is the capital of the United Kingdom."},
					{"text": "Berlin is the capital of Germany."},
				},
			},
		},
		{
			name:       "ocr",
			submitPath: "/v1/async/ocr",
			pollBase:   "/v1/async/ocr",
			body: map[string]any{
				"model": modelFor("ASYNC_OCR_MODEL"),
				"document": map[string]any{
					"type":      "image_url",
					"image_url": envOr("ASYNC_OCR_IMAGE_URL", "https://pestworldcdn-dcf2a8gbggazaghf.z01.azurefd.net/media/561791/carpenter-ant4.jpg"),
				},
			},
		},
	}
}

// sampleAudio reads core/internal/llmtests/scenarios/media/sample.mp3.
// go test sets the working directory to the package source directory, so the
// relative path is stable without runtime.Caller (which breaks under -trimpath).
func sampleAudio() []byte {
	mediaPath := filepath.Join("..", "..", "core", "internal", "llmtests", "scenarios", "media", "sample.mp3")
	data, err := os.ReadFile(mediaPath)
	if err != nil {
		panic("sampleAudio: cannot read " + mediaPath + ": " + err.Error())
	}
	return data
}

// samplePNG generates a 256x256 white RGBA PNG for image edit / variation fixtures.
// DALL-E 2 requires images with an alpha channel (RGBA PNG).
func samplePNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	for y := range 256 {
		for x := range 256 {
			img.Set(x, y, white)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic("samplePNG: encode failed: " + err.Error())
	}
	return buf.Bytes()
}
