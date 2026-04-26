package openai

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func multipartPartOrder(t *testing.T, contentType string, body []byte) []string {
	t.Helper()
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("ParseMediaType(%q): %v", contentType, err)
	}
	boundary := params["boundary"]
	if boundary == "" {
		t.Fatalf("missing boundary in %q", contentType)
	}

	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	var order []string
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextPart(): %v", err)
		}
		order = append(order, part.FormName())
		_, _ = io.Copy(io.Discard, part)
		_ = part.Close()
	}
	return order
}

func TestParseTranscriptionFormDataBodyFromRequest_OrdersMetadataBeforeFile(t *testing.T) {
	language := "en"
	prompt := "transcribe this"
	responseFormat := "verbose_json"
	temperature := 0.2
	stream := true

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	req := &OpenAITranscriptionRequest{
		Model:    "whisper-1",
		File:     []byte("audio-bytes"),
		Filename: "sample.mp3",
		TranscriptionParameters: schemas.TranscriptionParameters{
			Language:               &language,
			Prompt:                 &prompt,
			ResponseFormat:         &responseFormat,
			Temperature:            &temperature,
			TimestampGranularities: []string{"word"},
			Include:                []string{"logprobs"},
		},
		Stream: &stream,
	}

	if bifrostErr := ParseTranscriptionFormDataBodyFromRequest(writer, req, schemas.OpenAI); bifrostErr != nil {
		t.Fatalf("unexpected bifrost error: %v", bifrostErr.Error.Message)
	}

	order := multipartPartOrder(t, writer.FormDataContentType(), body.Bytes())
	if len(order) == 0 {
		t.Fatal("expected multipart parts to be written")
	}
	if order[len(order)-1] != "file" {
		t.Fatalf("expected file part last, got order %v", order)
	}
	if order[0] != "model" {
		t.Fatalf("expected model part first, got order %v", order)
	}
}
