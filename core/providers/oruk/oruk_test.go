package oruk

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

type noopLogger struct{}

func (noopLogger) Debug(string, ...any)                   {}
func (noopLogger) Info(string, ...any)                    {}
func (noopLogger) Warn(string, ...any)                    {}
func (noopLogger) Error(string, ...any)                   {}
func (noopLogger) Fatal(string, ...any)                   {}
func (noopLogger) SetLevel(schemas.LogLevel)              {}
func (noopLogger) SetOutputType(schemas.LoggerOutputType) {}
func (noopLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

type capturedMultipart struct {
	method   string
	path     string
	auth     string
	fields   map[string][]string
	fileName string
	fileBody []byte
}

func newMockOruk(t *testing.T, handler http.HandlerFunc) *OrukProvider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	provider, err := NewOrukProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL + "/v1/",
			DefaultRequestTimeoutInSeconds: 10,
			AllowPrivateNetwork:            true,
		},
	}, noopLogger{})
	if err != nil {
		t.Fatalf("NewOrukProvider: %v", err)
	}
	return provider
}

func testContext(t *testing.T) *schemas.BifrostContext {
	t.Helper()
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func testKey() schemas.Key {
	return schemas.Key{Value: *schemas.NewSecretVar("test-oruk-key"), Models: []string{"*"}}
}

func buildMultipartPayload(t *testing.T, fields map[string]string, fileName string, fileData []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("WriteField(%s): %v", k, err)
		}
	}
	if fileData != nil {
		part, err := w.CreateFormFile("file", fileName)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := part.Write(fileData); err != nil {
			t.Fatalf("write file part: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

func TestOrukDefaultBaseURL(t *testing.T) {
	config := &schemas.ProviderConfig{}
	provider, err := NewOrukProvider(config, noopLogger{})
	if err != nil {
		t.Fatalf("NewOrukProvider: %v", err)
	}
	if provider.GetProviderKey() != schemas.Oruk {
		t.Fatalf("GetProviderKey() = %q, want %q", provider.GetProviderKey(), schemas.Oruk)
	}
	if config.NetworkConfig.BaseURL != "https://speech-api.oruk.ai" {
		t.Fatalf("BaseURL = %q, want https://speech-api.oruk.ai", config.NetworkConfig.BaseURL)
	}

	// Trailing /v1 suffix should be normalized so /v1/audio/transcriptions is never doubled.
	withV1 := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: "https://speech-api.oruk.ai/v1/"},
	}
	if _, err := NewOrukProvider(withV1, noopLogger{}); err != nil {
		t.Fatalf("NewOrukProvider(/v1): %v", err)
	}
	if withV1.NetworkConfig.BaseURL != "https://speech-api.oruk.ai" {
		t.Fatalf("BaseURL = %q, want https://speech-api.oruk.ai", withV1.NetworkConfig.BaseURL)
	}
}

func TestOrukTranscription_SanitizesStrictSchemaAndBackfillsUsage(t *testing.T) {
	var got capturedMultipart
	provider := newMockOruk(t, func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		got.auth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		got.fields = make(map[string][]string, len(r.MultipartForm.Value))
		for k, v := range r.MultipartForm.Value {
			got.fields[k] = append([]string(nil), v...)
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer f.Close()
		got.fileName = hdr.Filename
		got.fileBody, _ = io.ReadAll(f)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hello from fourier 2b","duration":12.5,"language":"en"}`))
	})

	temp := 0.2
	prompt := "unsupported prompt"
	lang := "en_us"
	format := "verbose_json"
	resp, bifrostErr := provider.Transcription(testContext(t), testKey(), &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Oruk,
		Model:    "oruk-fourier-2b",
		Input: &schemas.TranscriptionInput{
			File:     []byte("RIFF\x00\x00\x00\x00WAVEfmt "),
			Filename: "clip.wav",
		},
		Params: &schemas.TranscriptionParameters{
			Language:               &lang,
			ResponseFormat:         &format,
			Prompt:                 &prompt,
			Temperature:            &temp,
			TimestampGranularities: []string{"word"},
			Include:                []string{"logprobs"},
			ExtraParams:            map[string]any{"chunking_strategy": "auto"},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("Transcription error: %+v", bifrostErr.Error)
	}
	if resp.Text != "hello from fourier 2b" {
		t.Fatalf("Text = %q, want %q", resp.Text, "hello from fourier 2b")
	}
	if resp.Duration == nil || *resp.Duration != 12.5 {
		t.Fatalf("Duration = %v, want 12.5", resp.Duration)
	}
	if resp.Usage == nil || resp.Usage.Type != "duration" || resp.Usage.Seconds == nil || *resp.Usage.Seconds != 12.5 {
		t.Fatalf("Usage = %+v, want duration usage with Seconds=12.5", resp.Usage)
	}
	if got.method != http.MethodPost || got.path != "/v1/audio/transcriptions" {
		t.Fatalf("request = %s %s, want POST /v1/audio/transcriptions", got.method, got.path)
	}
	if got.auth != "Bearer test-oruk-key" {
		t.Fatalf("Authorization = %q, want Bearer test-oruk-key", got.auth)
	}
	if got.fileName != "clip.wav" || len(got.fileBody) == 0 {
		t.Fatalf("file part = (%q, %d bytes), want clip.wav", got.fileName, len(got.fileBody))
	}
	if got.fields["model"][0] != "oruk-fourier-2b" {
		t.Fatalf("model = %v, want [oruk-fourier-2b]", got.fields["model"])
	}
	if got.fields["language"][0] != "en-US" {
		t.Fatalf("language = %v, want [en-US]", got.fields["language"])
	}
	if got.fields["response_format"][0] != "verbose_json" {
		t.Fatalf("response_format = %v, want [verbose_json]", got.fields["response_format"])
	}
	// Strict Fourier2bRequest schema: unsupported fields must never be forwarded.
	for _, forbidden := range []string{"prompt", "temperature", "timestamp_granularities[]", "include[]", "stream", "chunking_strategy"} {
		if _, exists := got.fields[forbidden]; exists {
			t.Fatalf("unexpected field %q forwarded to Oruk: %v", forbidden, got.fields)
		}
	}
}

func TestOrukTranscription_PreservesUpstreamUsage(t *testing.T) {
	provider := newMockOruk(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hello","duration":12.5,"usage":{"type":"duration","seconds":15.0}}`))
	})

	resp, bifrostErr := provider.Transcription(testContext(t), testKey(), &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Oruk,
		Model:    "oruk-fourier-2b",
		Input:    &schemas.TranscriptionInput{File: []byte("RIFFWAVE"), Filename: "clip.wav"},
	})
	if bifrostErr != nil {
		t.Fatalf("Transcription error: %+v", bifrostErr.Error)
	}
	if resp.Usage == nil || resp.Usage.Seconds == nil || *resp.Usage.Seconds != 15.0 {
		t.Fatalf("Usage = %+v, want upstream Seconds=15.0 preserved", resp.Usage)
	}
}

func TestOrukTranscription_LargePayloadMode(t *testing.T) {
	t.Run("sanitizes multipart stream and backfills duration usage", func(t *testing.T) {
		var got capturedMultipart
		provider := newMockOruk(t, func(w http.ResponseWriter, r *http.Request) {
			got.method = r.Method
			got.path = r.URL.Path
			got.auth = r.Header.Get("Authorization")
			if err := r.ParseMultipartForm(4 << 20); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			got.fields = make(map[string][]string, len(r.MultipartForm.Value))
			for k, v := range r.MultipartForm.Value {
				got.fields[k] = append([]string(nil), v...)
			}
			f, hdr, err := r.FormFile("file")
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			defer f.Close()
			got.fileName = hdr.Filename
			got.fileBody, _ = io.ReadAll(f)

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text":"large payload ok","duration":45.0,"language":"en"}`))
		})

		rawBody, contentType := buildMultipartPayload(t, map[string]string{
			"model":           "oruk/oruk-fourier-2b",
			"language":        "en_gb",
			"response_format": "verbose_json",
			"prompt":          "must be dropped",
			"temperature":     "0.3",
		}, "large.wav", []byte("RIFF\x00\x00\x00\x00WAVEfmt "))

		ctx := testContext(t)
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadReader, bytes.NewReader(rawBody))
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentLength, len(rawBody))
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentType, contentType)
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadMetadata, &schemas.LargePayloadMetadata{
			Model: "oruk/oruk-fourier-2b",
		})

		// Input.File is intentionally empty in large-payload mode.
		resp, bErr := provider.Transcription(ctx, testKey(), &schemas.BifrostTranscriptionRequest{
			Provider: schemas.Oruk,
			Model:    "oruk-fourier-2b",
			Input:    &schemas.TranscriptionInput{},
		})
		if bErr != nil {
			t.Fatalf("large-payload Transcription error: %+v", bErr.Error)
		}
		if resp.Text != "large payload ok" {
			t.Fatalf("Text = %q, want %q", resp.Text, "large payload ok")
		}
		if resp.Usage == nil || resp.Usage.Seconds == nil || *resp.Usage.Seconds != 45.0 {
			t.Fatalf("Usage = %+v, want Seconds=45.0", resp.Usage)
		}
		if got.fields["model"][0] != "oruk-fourier-2b" {
			t.Fatalf("model = %v, want [oruk-fourier-2b]", got.fields["model"])
		}
		if got.fields["language"][0] != "en-GB" {
			t.Fatalf("language = %v, want [en-GB]", got.fields["language"])
		}
		if _, exists := got.fields["prompt"]; exists {
			t.Fatalf("prompt was not stripped in large-payload mode: %v", got.fields)
		}
		if _, exists := got.fields["temperature"]; exists {
			t.Fatalf("temperature was not stripped in large-payload mode: %v", got.fields)
		}
		if got.fileName != "large.wav" || len(got.fileBody) == 0 {
			t.Fatalf("file = (%q, %d bytes), want large.wav", got.fileName, len(got.fileBody))
		}
	})

	t.Run("plain text response works in large-payload mode", func(t *testing.T) {
		provider := newMockOruk(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("streamed plain text"))
		})
		rawBody, contentType := buildMultipartPayload(t, map[string]string{
			"model":           "oruk-fourier-2b",
			"response_format": "text",
		}, "large.wav", []byte("RIFFWAVE"))

		ctx := testContext(t)
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadReader, bytes.NewReader(rawBody))
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentLength, len(rawBody))
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentType, contentType)

		resp, bErr := provider.Transcription(ctx, testKey(), &schemas.BifrostTranscriptionRequest{
			Provider: schemas.Oruk,
			Model:    "oruk-fourier-2b",
			Input:    &schemas.TranscriptionInput{},
		})
		if bErr != nil {
			t.Fatalf("plain-text large-payload error: %+v", bErr.Error)
		}
		if resp.Text != "streamed plain text" {
			t.Fatalf("Text = %q, want %q", resp.Text, "streamed plain text")
		}
	})

	t.Run("rejects payload over 30 MiB limit with 413", func(t *testing.T) {
		provider := newMockOruk(t, func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("upstream must not be called for oversized upload")
		})
		ctx := testContext(t)
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadReader, bytes.NewReader(nil))
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentLength, maxAudioBytes+maxMultipartOverheadBytes+1)
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentType, "multipart/form-data; boundary=abc")

		_, bErr := provider.Transcription(ctx, testKey(), &schemas.BifrostTranscriptionRequest{
			Provider: schemas.Oruk,
			Model:    "oruk-fourier-2b",
		})
		if bErr == nil || bErr.StatusCode == nil || *bErr.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413 for oversized large payload, got %+v", bErr)
		}
	})
}

func TestOrukTranscription_PlainTextAndDefaults(t *testing.T) {
	var gotModel string
	provider := newMockOruk(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(4 << 20)
		gotModel = r.FormValue("model")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("plain text transcript"))
	})

	format := "text"
	resp, bifrostErr := provider.Transcription(testContext(t), testKey(), &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Oruk,
		Input: &schemas.TranscriptionInput{
			File:     []byte("RIFF\x00\x00\x00\x00WAVEfmt "),
			Filename: "clip.wav",
		},
		Params: &schemas.TranscriptionParameters{
			ResponseFormat: &format,
		},
	})
	if bifrostErr != nil {
		t.Fatalf("Transcription error: %+v", bifrostErr.Error)
	}
	if gotModel != "oruk-fourier-2b" {
		t.Fatalf("default model = %q, want oruk-fourier-2b", gotModel)
	}
	if resp.Text != "plain text transcript" {
		t.Fatalf("Text = %q, want %q", resp.Text, "plain text transcript")
	}
}

func TestOrukTranscription_ValidationErrors(t *testing.T) {
	provider := newMockOruk(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("upstream should not be called on local validation failure")
	})

	t.Run("missing input returns 400", func(t *testing.T) {
		_, err := provider.Transcription(testContext(t), testKey(), &schemas.BifrostTranscriptionRequest{
			Provider: schemas.Oruk,
			Model:    "oruk-fourier-2b",
		})
		if err == nil || err.StatusCode == nil || *err.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 error for missing input, got %+v", err)
		}
	})

	t.Run("non-English language rejected with 400", func(t *testing.T) {
		lang := "fr"
		_, err := provider.Transcription(testContext(t), testKey(), &schemas.BifrostTranscriptionRequest{
			Provider: schemas.Oruk,
			Model:    "oruk-fourier-2b",
			Input:    &schemas.TranscriptionInput{File: []byte("RIFFWAVE"), Filename: "a.wav"},
			Params:   &schemas.TranscriptionParameters{Language: &lang},
		})
		if err == nil || err.StatusCode == nil || *err.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 error for non-English language, got %+v", err)
		}
	})

	t.Run("unsupported response_format rejected with 400", func(t *testing.T) {
		format := "diarized_json"
		_, err := provider.Transcription(testContext(t), testKey(), &schemas.BifrostTranscriptionRequest{
			Provider: schemas.Oruk,
			Model:    "oruk-fourier-2b",
			Input:    &schemas.TranscriptionInput{File: []byte("RIFFWAVE"), Filename: "a.wav"},
			Params:   &schemas.TranscriptionParameters{ResponseFormat: &format},
		})
		if err == nil || err.StatusCode == nil || *err.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 error for diarized_json, got %+v", err)
		}
	})

	t.Run("uppercase response_format rejected with 400", func(t *testing.T) {
		format := "TEXT"
		_, err := provider.Transcription(testContext(t), testKey(), &schemas.BifrostTranscriptionRequest{
			Provider: schemas.Oruk,
			Model:    "oruk-fourier-2b",
			Input:    &schemas.TranscriptionInput{File: []byte("RIFFWAVE"), Filename: "a.wav"},
			Params:   &schemas.TranscriptionParameters{ResponseFormat: &format},
		})
		if err == nil || err.StatusCode == nil || *err.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 error for uppercase TEXT format, got %+v", err)
		}
	})
}

func TestOrukTranscription_UpstreamError(t *testing.T) {
	provider := newMockOruk(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API key","type":"authentication_error"}}`))
	})

	_, err := provider.Transcription(testContext(t), testKey(), &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Oruk,
		Model:    "oruk-fourier-2b",
		Input:    &schemas.TranscriptionInput{File: []byte("RIFFWAVE"), Filename: "a.wav"},
	})
	if err == nil || err.StatusCode == nil || *err.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 upstream error, got %+v", err)
	}
}

func TestOrukListModels(t *testing.T) {
	provider, err := NewOrukProvider(&schemas.ProviderConfig{}, noopLogger{})
	if err != nil {
		t.Fatalf("NewOrukProvider: %v", err)
	}

	resp, bifrostErr := provider.ListModels(testContext(t), []schemas.Key{testKey()}, &schemas.BifrostListModelsRequest{
		Provider: schemas.Oruk,
	})
	if bifrostErr != nil {
		t.Fatalf("ListModels error: %+v", bifrostErr.Error)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "oruk/oruk-fourier-2b" {
		t.Fatalf("ListModels Data = %+v, want [oruk/oruk-fourier-2b]", resp.Data)
	}
}

func TestOrukUnsupportedOperations(t *testing.T) {
	provider, err := NewOrukProvider(&schemas.ProviderConfig{}, noopLogger{})
	if err != nil {
		t.Fatalf("NewOrukProvider: %v", err)
	}
	ctx := testContext(t)
	key := testKey()

	if _, bErr := provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{}); bErr == nil {
		t.Fatal("expected unsupported error for ChatCompletion")
	}
	if _, bErr := provider.Embedding(ctx, key, &schemas.BifrostEmbeddingRequest{}); bErr == nil {
		t.Fatal("expected unsupported error for Embedding")
	}
	if _, bErr := provider.Speech(ctx, key, &schemas.BifrostSpeechRequest{}); bErr == nil {
		t.Fatal("expected unsupported error for Speech")
	}
	if _, bErr := provider.TranscriptionStream(ctx, nil, nil, key, &schemas.BifrostTranscriptionRequest{}); bErr == nil {
		t.Fatal("expected unsupported error for TranscriptionStream")
	}
}

func BenchmarkSanitizeTranscriptionRequest(b *testing.B) {
	lang := "en_us"
	format := "verbose_json"
	prompt := "ignored prompt"
	temp := 0.2
	req := &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Oruk,
		Model:    "oruk-fourier-2b",
		Input: &schemas.TranscriptionInput{
			File:     []byte("RIFF\x00\x00\x00\x00WAVEfmt "),
			Filename: "clip.wav",
		},
		Params: &schemas.TranscriptionParameters{
			Language:       &lang,
			ResponseFormat: &format,
			Prompt:         &prompt,
			Temperature:    &temp,
			ExtraParams:    map[string]any{"chunking_strategy": "auto"},
		},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sanitized, err := sanitizeTranscriptionRequest(req)
		if err != nil || sanitized == nil {
			b.Fatalf("unexpected error: %v", err)
		}
	}
}

func BenchmarkNormalizeOrukLanguage(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := normalizeOrukLanguage("en_us"); err != nil {
			b.Fatalf("unexpected error: %v", err)
		}
	}
}
