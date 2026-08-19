package openai

import (
	"bytes"
	"mime/multipart"
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

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
