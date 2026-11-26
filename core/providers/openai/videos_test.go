package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestVideoGenerationCustomJSON(t *testing.T) {
	for _, tc := range []struct {
		name        string
		config      string
		passthrough bool
		wantJSON    bool
	}{
		{name: "default multipart", config: `{}`},
		{name: "JSON with extras", config: `{"openai_config":{"video_generation_json":true}}`, passthrough: true, wantJSON: true},
		{name: "JSON without extras", config: `{"openai_config":{"video_generation_json":true}}`, wantJSON: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.wantJSON {
					var body map[string]json.RawMessage
					if got := r.Header.Get("Content-Type"); got != "application/json" {
						t.Errorf("Content-Type = %q, want application/json", got)
						http.Error(w, "expected JSON", http.StatusUnsupportedMediaType)
						return
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode upstream JSON: %v", err)
						http.Error(w, "invalid JSON", http.StatusBadRequest)
						return
					}
					for field, want := range map[string]string{"model": `"fixture"`, "size": `"3840x2160"`, "seed": `41`} {
						if got := string(body[field]); got != want {
							t.Errorf("%s = %s, want %s", field, got, want)
						}
					}
					for field, want := range map[string]string{"input_video": `{"job_id":"draft"}`, "image_reference": `[{"file_id":"image"}]`, "sampler": `"up3"`} {
						if !tc.passthrough {
							want = ""
						}
						if got := string(body[field]); got != want {
							t.Errorf("%s = %s, want %s", field, got, want)
						}
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusAccepted)
				} else {
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Errorf("default mode did not send multipart: %v", err)
					}
					if got := r.FormValue("size"); got != "720x1280" {
						t.Errorf("default size = %q, want 720x1280", got)
					}
					w.Header().Set("Content-Type", "application/json")
				}
				_, _ = w.Write([]byte(`{"id":"video_fixture","status":"queued"}`))
			}))
			defer server.Close()
			var config schemas.ProviderConfig
			if err := json.Unmarshal([]byte(tc.config), &config); err != nil {
				t.Fatal(err)
			}
			config.NetworkConfig.BaseURL = server.URL
			p := NewOpenAIProvider(&config, testNoopLogger{})
			ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, tc.passthrough)
			resp, err := p.VideoGeneration(ctx, schemas.Key{}, &schemas.BifrostVideoGenerationRequest{
				Model: "fixture",
				Input: &schemas.VideoGenerationInput{Prompt: "fixture"},
				Params: &schemas.VideoGenerationParameters{
					Size: "3840x2160",
					Seed: schemas.Ptr(41),
					ExtraParams: map[string]any{
						"input_video": json.RawMessage(`{"job_id":"draft"}`),
						"extra_params": map[string]any{
							"image_reference": json.RawMessage(`[{"file_id":"image"}]`),
							"Size":            "unexpected-size",
						},
						"sampler": "up3",
						"model":   "unexpected-model",
					},
				},
			})
			if err != nil {
				t.Fatalf("video generation failed: %+v", err)
			}
			if resp == nil || resp.ID != "video_fixture:openai" || resp.Status != schemas.VideoStatusQueued {
				t.Fatalf("expected the native queued job response, got %+v", resp)
			}
		})
	}
}

func BenchmarkVideoGenerationBody(b *testing.B) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), time.Minute)
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	req := &schemas.BifrostVideoGenerationRequest{
		Model: "fixture",
		Input: &schemas.VideoGenerationInput{Prompt: "fixture"},
		Params: &schemas.VideoGenerationParameters{
			Size: "1280x720", Seconds: schemas.Ptr("5"),
			ExtraParams: map[string]any{"sampler": "custom"},
		},
	}
	for _, tc := range []struct {
		name    string
		useJSON bool
	}{
		{name: "default multipart"},
		{name: "JSON with extras", useJSON: true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			outbound := fasthttp.AcquireRequest()
			defer fasthttp.ReleaseRequest(outbound)
			b.ReportAllocs()
			for b.Loop() {
				if err := setOpenAIVideoGenerationBody(ctx, outbound, req, schemas.OpenAI, tc.useJSON); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// An uploaded source is carried as bytes; a referenced one keeps only the ID, with the provider
// suffix Bifrost stamps onto every video ID stripped back off before it reaches OpenAI.
func TestToOpenAIVideoEditRequest_SourceForms(t *testing.T) {
	uploaded, err := ToOpenAIVideoEditRequest(&schemas.BifrostVideoEditRequest{
		Model: "sora-2-pro",
		Input: &schemas.VideoEditInput{
			Prompt: "warm backlight",
			Video:  schemas.VideoInput{Video: []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(uploaded.Video.Bytes) == 0 || uploaded.Video.ID != "" {
		t.Fatalf("uploaded source should carry bytes only, got %+v", uploaded.Video)
	}
	if uploaded.Prompt != "warm backlight" || uploaded.Model != "sora-2-pro" {
		t.Fatalf("prompt/model not carried: %+v", uploaded)
	}

	referenced, err := ToOpenAIVideoEditRequest(&schemas.BifrostVideoEditRequest{
		Input: &schemas.VideoEditInput{
			Prompt: "warm backlight",
			Video:  schemas.VideoInput{ID: "video_abc123:openai"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if referenced.Video.ID != "video_abc123" {
		t.Fatalf("video id = %q, want the provider suffix stripped", referenced.Video.ID)
	}
	if referenced.Model != "" {
		t.Fatalf("model must stay empty so OpenAI infers it from the source video, got %q", referenced.Model)
	}
}

// OpenAI's edit endpoint takes an uploaded file or a video ID. A URL has no equivalent, and it is
// the sole input rather than a droppable parameter, so it is rejected instead of ignored.
func TestToOpenAIVideoEditRequest_RejectsURL(t *testing.T) {
	_, err := ToOpenAIVideoEditRequest(&schemas.BifrostVideoEditRequest{
		Model: "sora-2-pro",
		Input: &schemas.VideoEditInput{
			Prompt: "warm backlight",
			Video:  schemas.VideoInput{URL: "https://example.com/clip.mp4"},
		},
	})
	if err == nil {
		t.Fatal("expected an error for a url source")
	}
	if !strings.Contains(err.Error(), "url") {
		t.Fatalf("error should name the unsupported form, got %q", err.Error())
	}
}

func TestToOpenAIVideoEditRequest_RequiresPromptAndSource(t *testing.T) {
	if _, err := ToOpenAIVideoEditRequest(&schemas.BifrostVideoEditRequest{
		Model: "sora-2-pro",
		Input: &schemas.VideoEditInput{Video: schemas.VideoInput{ID: "video_abc123"}},
	}); err == nil {
		t.Fatal("expected an error when prompt is missing")
	}
	if _, err := ToOpenAIVideoEditRequest(&schemas.BifrostVideoEditRequest{
		Model: "sora-2-pro",
		Input: &schemas.VideoEditInput{Prompt: "warm backlight"},
	}); err == nil {
		t.Fatal("expected an error when the source video is missing")
	}
}

// The multipart body must name the file part "video" and give it a real video content type, since
// OpenAI reads the source from that part.
func TestParseVideoEditFormDataBodyFromRequest(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	bifrostErr := parseVideoEditFormDataBodyFromRequest(writer, &OpenAIVideoEditRequest{
		Prompt: "warm backlight",
		Model:  "sora-2-pro",
		Video:  OpenAIVideoEditVideoInput{Bytes: []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")},
	})
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}

	form, err := multipart.NewReader(&body, writer.Boundary()).ReadForm(1 << 20)
	if err != nil {
		t.Fatalf("produced an unreadable multipart body: %v", err)
	}
	if got := form.Value["prompt"]; len(got) != 1 || got[0] != "warm backlight" {
		t.Fatalf("prompt field = %v", got)
	}
	if got := form.Value["model"]; len(got) != 1 || got[0] != "sora-2-pro" {
		t.Fatalf("model field = %v", got)
	}
	videoParts := form.File["video"]
	if len(videoParts) != 1 {
		t.Fatalf("expected exactly one 'video' file part, got %d", len(videoParts))
	}
	if ct := videoParts[0].Header.Get("Content-Type"); ct != "video/mp4" {
		t.Fatalf("video part content type = %q, want video/mp4", ct)
	}
	if fn := videoParts[0].Filename; !strings.HasSuffix(fn, ".mp4") {
		t.Fatalf("video part filename = %q, want an .mp4 extension", fn)
	}
}

// A model omitted for a referenced source must not be written as an empty form field.
func TestParseVideoEditFormDataBodyFromRequest_OmitsEmptyModel(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if bifrostErr := parseVideoEditFormDataBodyFromRequest(writer, &OpenAIVideoEditRequest{
		Prompt: "warm backlight",
		Video:  OpenAIVideoEditVideoInput{Bytes: []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")},
	}); bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	form, err := multipart.NewReader(&body, writer.Boundary()).ReadForm(1 << 20)
	if err != nil {
		t.Fatalf("produced an unreadable multipart body: %v", err)
	}
	if _, ok := form.Value["model"]; ok {
		t.Fatalf("empty model must be omitted, got %v", form.Value["model"])
	}
}

// Inbound OpenAI-format requests split "provider/model" the same way the other video routes do.
func TestToBifrostVideoEditRequest(t *testing.T) {
	out := ToBifrostVideoEditRequest(&OpenAIVideoEditRequest{
		Prompt: "warm backlight",
		Model:  "openai/sora-2-pro",
		Video:  OpenAIVideoEditVideoInput{ID: "video_abc123"},
	})
	if out == nil {
		t.Fatal("expected a converted request")
	}
	if out.Provider != schemas.OpenAI || out.Model != "sora-2-pro" {
		t.Fatalf("provider/model = %q/%q, want openai/sora-2-pro", out.Provider, out.Model)
	}
	if out.Input == nil || out.Input.Video.ID != "video_abc123" || out.Input.Prompt != "warm backlight" {
		t.Fatalf("input not carried: %+v", out.Input)
	}
}

// The official SDKs send no model on the video edit route, so the provider the transport resolved
// from the query parameter, header, or video ID suffix is the only routing signal available.
func TestToBifrostVideoEditRequest_ProviderFallback(t *testing.T) {
	out := ToBifrostVideoEditRequest(&OpenAIVideoEditRequest{
		Prompt:   "make it grayscale",
		Video:    OpenAIVideoEditVideoInput{ID: "vid_123"},
		Provider: schemas.Runware,
	})
	if out.Provider != schemas.Runware {
		t.Fatalf("provider = %q, want runware", out.Provider)
	}
	if out.Model != "" {
		t.Fatalf("model should stay empty when none was sent, got %q", out.Model)
	}

	// An explicit "provider/model" string still wins over the resolved fallback.
	explicit := ToBifrostVideoEditRequest(&OpenAIVideoEditRequest{
		Prompt:   "make it grayscale",
		Video:    OpenAIVideoEditVideoInput{ID: "vid_123"},
		Model:    "replicate/some-model",
		Provider: schemas.Runware,
	})
	if explicit.Provider != schemas.Replicate || explicit.Model != "some-model" {
		t.Fatalf("model string should win, got provider=%q model=%q", explicit.Provider, explicit.Model)
	}
}
