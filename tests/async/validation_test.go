package async

import (
	"net/http"
	"testing"
)

// streamEndpoints lists async endpoints that reject stream=true in the JSON body.
// Speech uses stream_format instead and is tested separately.
// image_edits and image_variations are multipart-only endpoints; their stream field
// is a multipart form value — not a JSON body field — so they are not listed here.
var streamEndpoints = []struct {
	name       string
	submitPath string
	body       map[string]any
}{
	{
		name:       "text_completions",
		submitPath: "/v1/async/completions",
		body: map[string]any{
			"model":  modelFor("ASYNC_TEXT_COMPLETION_MODEL"),
			"prompt": "Hello",
			"stream": true,
		},
	},
	{
		name:       "chat_completions",
		submitPath: "/v1/async/chat/completions",
		body: map[string]any{
			"model":    modelFor("ASYNC_CHAT_COMPLETION_MODEL"),
			"messages": []map[string]any{{"role": "user", "content": "Hello"}},
			"stream":   true,
		},
	},
	{
		name:       "responses",
		submitPath: "/v1/async/responses",
		body: map[string]any{
			"model":  modelFor("ASYNC_RESPONSES_MODEL"),
			"input":  "Hello",
			"stream": true,
		},
	},
	{
		name:       "image_generations",
		submitPath: "/v1/async/images/generations",
		body: map[string]any{
			"model":  modelFor("ASYNC_IMAGE_GEN_MODEL"),
			"prompt": "A circle",
			"stream": true,
		},
	},
}

// TestValidation_StreamRejected_Returns400 confirms that stream=true is rejected
// with 400 before any job is created. Runs in both global and VK modes because the
// stream check happens before VK resolution.
func TestValidation_StreamRejected_Returns400(t *testing.T) {
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			for _, ep := range streamEndpoints {
				t.Run(ep.name, func(t *testing.T) {
					code, _, body := submitJSON(t, ep.submitPath, ep.body, mode.headers)
					if code != http.StatusBadRequest {
						t.Errorf("expected 400 for stream=true on %s, got %d: %s",
							ep.submitPath, code, body)
					}
				})
			}
		})
	}
}

// TestValidation_SpeechStreamFormatRejected_Returns400 confirms that the speech
// endpoint rejects stream_format=sse with 400.
func TestValidation_SpeechStreamFormatRejected_Returns400(t *testing.T) {
	body := map[string]any{
		"model":         modelFor("ASYNC_SPEECH_MODEL"),
		"input":         "Hello",
		"voice":         "alloy",
		"stream_format": "sse",
	}
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			code, _, raw := submitJSON(t, "/v1/async/audio/speech", body, mode.headers)
			if code != http.StatusBadRequest {
				t.Errorf("expected 400 for stream_format=sse on speech, got %d: %s", code, raw)
			}
		})
	}
}

// TestValidation_MalformedJSON_Returns400 verifies that sending malformed JSON to any
// async JSON endpoint returns 400 before a job is created.
func TestValidation_MalformedJSON_Returns400(t *testing.T) {
	jsonEndpoints := []endpointCase{}
	for _, ec := range endpointCases() {
		if ec.multipart == nil {
			jsonEndpoints = append(jsonEndpoints, ec)
		}
	}

	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			for _, ec := range jsonEndpoints {
				t.Run(ec.name, func(t *testing.T) {
					code, body := submitRaw(t, ec.submitPath, []byte(`{invalid json`),
						"application/json", mode.headers)
					if code != http.StatusBadRequest {
						t.Errorf("expected 400 for malformed JSON on %s, got %d: %s",
							ec.submitPath, code, body)
					}
				})
			}
		})
	}
}

// TestValidation_TranscriptionStreamRejected_Returns400 confirms that the transcription
// endpoint rejects stream=true (sent as a multipart field) with 400.
func TestValidation_TranscriptionStreamRejected_Returns400(t *testing.T) {
	mp := &multipartCase{
		fields: map[string]string{
			"model":  modelFor("ASYNC_TRANSCRIPTION_MODEL"),
			"stream": "true",
		},
		files: map[string]fileFixture{
			"file": {filename: "sample.mp3", data: sampleAudio()},
		},
	}
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			code, _, body := submitMultipart(t, "/v1/async/audio/transcriptions", mp, mode.headers)
			if code != http.StatusBadRequest {
				t.Errorf("expected 400 for stream=true on transcription, got %d: %s", code, body)
			}
		})
	}
}

// TestValidation_MissingModel_Returns400 verifies that submitting without a model field
// is rejected with 400 across all JSON endpoints.
func TestValidation_MissingModel_Returns400(t *testing.T) {
	missingModelCases := []struct {
		name string
		path string
		body map[string]any
	}{
		{
			"chat_completions",
			"/v1/async/chat/completions",
			map[string]any{"messages": []map[string]any{{"role": "user", "content": "Hello"}}},
		},
		{
			"text_completions",
			"/v1/async/completions",
			map[string]any{"prompt": "Hello"},
		},
		{
			"embeddings",
			"/v1/async/embeddings",
			map[string]any{"input": "Hello"},
		},
		{
			"responses",
			"/v1/async/responses",
			map[string]any{"input": "Hello"},
		},
		{
			"speech",
			"/v1/async/audio/speech",
			map[string]any{"input": "Hello", "voice": "alloy"},
		},
		{
			"rerank",
			"/v1/async/rerank",
			map[string]any{
				"query":     "test",
				"documents": []map[string]any{{"text": "test document"}},
			},
		},
		{
			"ocr",
			"/v1/async/ocr",
			map[string]any{
				"document": map[string]any{
					"type":      "image_url",
					"image_url": envOr("ASYNC_OCR_IMAGE_URL", "https://pestworldcdn-dcf2a8gbggazaghf.z01.azurefd.net/media/561791/carpenter-ant4.jpg"),
				},
			},
		},
	}
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			for _, mc := range missingModelCases {
				t.Run(mc.name, func(t *testing.T) {
					code, _, body := submitJSON(t, mc.path, mc.body, mode.headers)
					if code != http.StatusBadRequest {
						t.Errorf("expected 400 for missing model on %s, got %d: %s", mc.path, code, body)
					}
				})
			}
		})
	}
}

// TestValidation_ImageEditStreamRejected_Returns400 confirms that the image edit endpoint
// rejects stream=true (sent as a multipart form field) with 400. This requires a complete
// valid multipart body because stream validation runs after successful form parsing.
func TestValidation_ImageEditStreamRejected_Returns400(t *testing.T) {
	mp := &multipartCase{
		fields: map[string]string{
			"model":  modelFor("ASYNC_IMAGE_EDIT_MODEL"),
			"prompt": "Make it blue",
			"stream": "true",
		},
		files: map[string]fileFixture{
			"image": {filename: "image.png", data: samplePNG()},
		},
	}
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			code, _, body := submitMultipart(t, "/v1/async/images/edits", mp, mode.headers)
			if code != http.StatusBadRequest {
				t.Errorf("expected 400 for stream=true on image edits, got %d: %s", code, body)
			}
		})
	}
}

// TestValidation_Transcription_MissingFile_Returns400 verifies that a transcription request
// without the required audio file is rejected with 400 at the multipart parse stage.
func TestValidation_Transcription_MissingFile_Returns400(t *testing.T) {
	mp := &multipartCase{
		fields: map[string]string{
			"model": modelFor("ASYNC_TRANSCRIPTION_MODEL"),
		},
		// no "file" entry
	}
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			code, _, body := submitMultipart(t, "/v1/async/audio/transcriptions", mp, mode.headers)
			if code != http.StatusBadRequest {
				t.Errorf("expected 400 for missing audio file on transcription, got %d: %s", code, body)
			}
		})
	}
}

// TestValidation_ImageEdit_MissingImage_Returns400 verifies that an image edit request
// without the required image file is rejected with 400.
func TestValidation_ImageEdit_MissingImage_Returns400(t *testing.T) {
	mp := &multipartCase{
		fields: map[string]string{
			"model":  modelFor("ASYNC_IMAGE_EDIT_MODEL"),
			"prompt": "Make it blue",
		},
		// no "image" file entry
	}
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			code, _, body := submitMultipart(t, "/v1/async/images/edits", mp, mode.headers)
			if code != http.StatusBadRequest {
				t.Errorf("expected 400 for missing image file on image edits, got %d: %s", code, body)
			}
		})
	}
}

// TestValidation_ImageVariation_MissingImage_Returns400 verifies that an image variation
// request without the required image file is rejected with 400.
func TestValidation_ImageVariation_MissingImage_Returns400(t *testing.T) {
	mp := &multipartCase{
		fields: map[string]string{
			"model": modelFor("ASYNC_IMAGE_VARIATION_MODEL"),
		},
		// no "image" file entry
	}
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			code, _, body := submitMultipart(t, "/v1/async/images/variations", mp, mode.headers)
			if code != http.StatusBadRequest {
				t.Errorf("expected 400 for missing image file on image variations, got %d: %s", code, body)
			}
		})
	}
}

// TestHTTP_WrongMethod_Rejected verifies that POST on a poll-only path does not return
// a success status code.  The converse (GET on a submit path) is not checked here
// because the server's UI layer intercepts bare GET requests on /v1/async/* paths
// before the async router is reached.
func TestHTTP_WrongMethod_Rejected(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, cfg.BaseURL+"/v1/async/chat/completions/00000000-0000-0000-0000-000000000000", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/async/chat/completions/{id} failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST on poll path returned %d, expected 404 or 405", resp.StatusCode)
	}
}
