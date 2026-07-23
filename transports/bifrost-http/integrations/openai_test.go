package integrations

import (
	"bytes"
	"mime/multipart"
	"testing"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// buildFileUploadCtx builds a fasthttp request context carrying a multipart
// file upload body with the given extra form fields.
func buildFileUploadCtx(t *testing.T, fields map[string]string) *fasthttp.RequestCtx {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "test.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("fake-png-bytes")); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("purpose", "vision"); err != nil {
		t.Fatal(err)
	}
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetContentType(writer.FormDataContentType())
	ctx.Request.SetBody(body.Bytes())
	return ctx
}

func TestParseOpenAIFileUploadMultipartRequest_ExpiresAfter(t *testing.T) {
	tests := []struct {
		name    string
		fields  map[string]string
		want    *schemas.FileExpiresAfter
		wantErr bool
	}{
		{
			name: "both fields present",
			fields: map[string]string{
				"expires_after[anchor]":  "created_at",
				"expires_after[seconds]": "3600",
			},
			want: &schemas.FileExpiresAfter{Anchor: "created_at", Seconds: 3600},
		},
		{
			name:   "neither field present",
			fields: nil,
			want:   nil,
		},
		{
			// Partial input is passed through; the upstream provider rejects it.
			name: "anchor only",
			fields: map[string]string{
				"expires_after[anchor]": "created_at",
			},
			want: &schemas.FileExpiresAfter{Anchor: "created_at", Seconds: 0},
		},
		{
			name: "seconds only",
			fields: map[string]string{
				"expires_after[seconds]": "3600",
			},
			want: &schemas.FileExpiresAfter{Anchor: "", Seconds: 3600},
		},
		{
			// Out-of-range values are passed through; the upstream provider rejects them.
			name: "out-of-range seconds",
			fields: map[string]string{
				"expires_after[anchor]":  "created_at",
				"expires_after[seconds]": "100",
			},
			want: &schemas.FileExpiresAfter{Anchor: "created_at", Seconds: 100},
		},
		{
			name: "non-numeric seconds",
			fields: map[string]string{
				"expires_after[anchor]":  "created_at",
				"expires_after[seconds]": "not-a-number",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := buildFileUploadCtx(t, tt.fields)
			uploadReq := &schemas.BifrostFileUploadRequest{}
			err := parseOpenAIFileUploadMultipartRequest(ctx, uploadReq)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if uploadReq.Purpose != schemas.FilePurpose("vision") {
				t.Errorf("purpose = %q, want %q", uploadReq.Purpose, "vision")
			}
			if tt.want == nil {
				if uploadReq.ExpiresAfter != nil {
					t.Errorf("ExpiresAfter = %+v, want nil", uploadReq.ExpiresAfter)
				}
				return
			}
			if uploadReq.ExpiresAfter == nil {
				t.Fatal("ExpiresAfter is nil, expected it to be populated")
			}
			if *uploadReq.ExpiresAfter != *tt.want {
				t.Errorf("ExpiresAfter = %+v, want %+v", *uploadReq.ExpiresAfter, *tt.want)
			}
		})
	}
}

// buildTranscriptionMultipartCtx builds a fasthttp request context carrying
// a multipart transcription upload body with the given extra form fields.
// Fields with multiple values (e.g. repeated "timestamp_granularities[]")
// are written once per slice element, preserving order.
func buildTranscriptionMultipartCtx(t *testing.T, fields map[string][]string) *fasthttp.RequestCtx {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "gpt-4o-transcribe-diarize"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "sample.mp3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("fake-audio-bytes")); err != nil {
		t.Fatal(err)
	}
	for k, values := range fields {
		for _, v := range values {
			if err := writer.WriteField(k, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetContentType(writer.FormDataContentType())
	ctx.Request.SetBody(body.Bytes())
	return ctx
}

func TestParseTranscriptionMultipartRequest_ExtraParamsPassthrough(t *testing.T) {
	ctx := buildTranscriptionMultipartCtx(t, map[string][]string{
		"diarize":            {"true"},      // ElevenLabs-specific: JSON-decodable (bool)
		"num_speakers":       {"2"},         // ElevenLabs-specific: JSON-decodable (number)
		"unstructured_extra": {"not-json{"}, // falls back to raw string on decode failure
		"chunking_strategy":  {"auto"},      // has its own dedicated handling; must not also appear via the generic passthrough
	})

	req := &openai.OpenAITranscriptionRequest{}
	if err := parseTranscriptionMultipartRequest(ctx, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if req.ExtraParams["diarize"] != true {
		t.Errorf("ExtraParams[diarize] = %#v, want true (bool)", req.ExtraParams["diarize"])
	}
	if req.ExtraParams["num_speakers"] != float64(2) {
		t.Errorf("ExtraParams[num_speakers] = %#v, want float64(2)", req.ExtraParams["num_speakers"])
	}
	if req.ExtraParams["unstructured_extra"] != "not-json{" {
		t.Errorf("ExtraParams[unstructured_extra] = %#v, want raw string fallback", req.ExtraParams["unstructured_extra"])
	}

	// chunking_strategy must be set exactly once, via its own dedicated
	// handling above the generic passthrough loop, not duplicated.
	if req.ExtraParams["chunking_strategy"] != "auto" {
		t.Errorf("ExtraParams[chunking_strategy] = %#v, want %q", req.ExtraParams["chunking_strategy"], "auto")
	}
}

func TestParseTranscriptionMultipartRequest_TypedFieldsNotShadowedByExtraParams(t *testing.T) {
	ctx := buildTranscriptionMultipartCtx(t, map[string][]string{
		"temperature":               {"0.3"},
		"timestamp_granularities[]": {"word", "segment"},
	})

	req := &openai.OpenAITranscriptionRequest{}
	if err := parseTranscriptionMultipartRequest(ctx, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if req.Temperature == nil || *req.Temperature != 0.3 {
		t.Fatalf("Temperature = %v, want *0.3", req.Temperature)
	}
	if len(req.TimestampGranularities) != 2 || req.TimestampGranularities[0] != "word" || req.TimestampGranularities[1] != "segment" {
		t.Fatalf("TimestampGranularities = %v, want [word segment]", req.TimestampGranularities)
	}
	// These are known/typed fields now, so they must not also leak into
	// ExtraParams via the generic passthrough loop.
	if _, ok := req.ExtraParams["temperature"]; ok {
		t.Errorf("temperature leaked into ExtraParams: %#v", req.ExtraParams["temperature"])
	}
	if _, ok := req.ExtraParams["timestamp_granularities[]"]; ok {
		t.Errorf("timestamp_granularities[] leaked into ExtraParams: %#v", req.ExtraParams["timestamp_granularities[]"])
	}
}
