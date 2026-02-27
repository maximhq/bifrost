package network

import (
	"bytes"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// buildMultipartBody is a test helper that creates a multipart/form-data body
// with the given text fields and optional file parts.
func buildMultipartBody(t *testing.T, fields map[string]string, files map[string][]byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for name, val := range fields {
		if err := writer.WriteField(name, val); err != nil {
			t.Fatalf("WriteField(%q): %v", name, err)
		}
	}
	for name, data := range files {
		fw, err := writer.CreateFormFile(name, name+".bin")
		if err != nil {
			t.Fatalf("CreateFormFile(%q): %v", name, err)
		}
		if _, err := fw.Write(data); err != nil {
			t.Fatalf("Write file data: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}
	return buf.Bytes(), writer.FormDataContentType()
}

func TestParseMultipartFormFields(t *testing.T) {
	t.Run("extracts text fields and skips files", func(t *testing.T) {
		body, ct := buildMultipartBody(t,
			map[string]string{"model": "gpt-4", "prompt": "hello"},
			map[string][]byte{"image": {0xFF, 0xD8, 0xFF}},
		)
		result, err := ParseMultipartFormFields(ct, body)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result["model"] != "gpt-4" {
			t.Errorf("model = %v, want gpt-4", result["model"])
		}
		if result["prompt"] != "hello" {
			t.Errorf("prompt = %v, want hello", result["prompt"])
		}
		if _, exists := result["image"]; exists {
			t.Error("file part 'image' should have been skipped")
		}
	})

	t.Run("returns error on missing boundary", func(t *testing.T) {
		_, err := ParseMultipartFormFields("multipart/form-data", []byte("irrelevant"))
		if err == nil {
			t.Fatal("expected error for missing boundary")
		}
	})

	t.Run("returns error on invalid content-type", func(t *testing.T) {
		_, err := ParseMultipartFormFields(";;;invalid", []byte("irrelevant"))
		if err == nil {
			t.Fatal("expected error for invalid content-type")
		}
	})

	t.Run("returns empty map for empty body", func(t *testing.T) {
		// A valid multipart body with no parts (just the closing boundary).
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		ct := writer.FormDataContentType()
		_ = writer.Close()

		result, err := ParseMultipartFormFields(ct, buf.Bytes())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 0 {
			t.Errorf("expected empty map, got %v", result)
		}
	})
}

func TestReconstructMultipartBody(t *testing.T) {
	t.Run("replaces text field value", func(t *testing.T) {
		body, ct := buildMultipartBody(t,
			map[string]string{"model": "gpt-3.5", "prompt": "hi"},
			nil,
		)
		payload := map[string]any{"model": "gpt-4"}
		newBody, newCT, err := ReconstructMultipartBody(ct, body, payload)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(newCT, "multipart/form-data") {
			t.Errorf("content-type = %v, want multipart/form-data prefix", newCT)
		}
		// Parse the reconstructed body and verify the value was replaced.
		parsed, err := ParseMultipartFormFields(newCT, newBody)
		if err != nil {
			t.Fatalf("failed to parse reconstructed body: %v", err)
		}
		if parsed["model"] != "gpt-4" {
			t.Errorf("model = %v, want gpt-4", parsed["model"])
		}
		if parsed["prompt"] != "hi" {
			t.Errorf("prompt = %v, want hi (should be preserved)", parsed["prompt"])
		}
	})

	t.Run("adds new fields from payload", func(t *testing.T) {
		body, ct := buildMultipartBody(t,
			map[string]string{"model": "gpt-4"},
			nil,
		)
		payload := map[string]any{"model": "gpt-4", "temperature": "0.7"}
		newBody, newCT, err := ReconstructMultipartBody(ct, body, payload)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		parsed, err := ParseMultipartFormFields(newCT, newBody)
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}
		if parsed["temperature"] != "0.7" {
			t.Errorf("temperature = %v, want 0.7", parsed["temperature"])
		}
	})

	t.Run("returns error on missing boundary", func(t *testing.T) {
		_, _, err := ReconstructMultipartBody("multipart/form-data", []byte("data"), map[string]any{})
		if err == nil {
			t.Fatal("expected error for missing boundary")
		}
	})
}

func TestWriteMultipartField(t *testing.T) {
	t.Run("writes string value", func(t *testing.T) {
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		if err := WriteMultipartField(writer, "key", "value"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_ = writer.Close()
		parsed, err := ParseMultipartFormFields(writer.FormDataContentType(), buf.Bytes())
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		if parsed["key"] != "value" {
			t.Errorf("key = %v, want value", parsed["key"])
		}
	})

	t.Run("writes []string as JSON array", func(t *testing.T) {
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		if err := WriteMultipartField(writer, "tags", []string{"a", "b"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_ = writer.Close()
		parsed, err := ParseMultipartFormFields(writer.FormDataContentType(), buf.Bytes())
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		val, ok := parsed["tags"].(string)
		if !ok {
			t.Fatalf("tags not a string, got %T", parsed["tags"])
		}
		var arr []string
		if err := sonic.UnmarshalString(val, &arr); err != nil {
			t.Fatalf("failed to unmarshal tags JSON: %v", err)
		}
		if len(arr) != 2 || arr[0] != "a" || arr[1] != "b" {
			t.Errorf("tags = %v, want [a b]", arr)
		}
	})

	t.Run("writes non-string with Sprintf", func(t *testing.T) {
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		if err := WriteMultipartField(writer, "count", 42); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_ = writer.Close()
		parsed, err := ParseMultipartFormFields(writer.FormDataContentType(), buf.Bytes())
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		if parsed["count"] != "42" {
			t.Errorf("count = %v, want 42", parsed["count"])
		}
	})
}

func TestSerializePayloadToRequest(t *testing.T) {
	t.Run("JSON path", func(t *testing.T) {
		req := &schemas.HTTPRequest{
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    []byte(`{"old":"data"}`),
		}
		payload := map[string]any{"model": "gpt-4", "prompt": "test"}
		if err := SerializePayloadToRequest(req, payload, false, "application/json"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var result map[string]any
		if err := sonic.Unmarshal(req.Body, &result); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}
		if result["model"] != "gpt-4" {
			t.Errorf("model = %v, want gpt-4", result["model"])
		}
	})

	t.Run("multipart path", func(t *testing.T) {
		body, ct := buildMultipartBody(t,
			map[string]string{"model": "gpt-3.5"},
			nil,
		)
		req := &schemas.HTTPRequest{
			Headers: map[string]string{"Content-Type": ct},
			Body:    body,
		}
		payload := map[string]any{"model": "gpt-4"}
		if err := SerializePayloadToRequest(req, payload, true, ct); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Verify content-type was updated
		newCT := req.Headers["Content-Type"]
		if !strings.HasPrefix(newCT, "multipart/form-data") {
			t.Errorf("content-type = %v, want multipart/form-data prefix", newCT)
		}
		// Verify the body contains the updated model
		parsed, err := ParseMultipartFormFields(newCT, req.Body)
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		if parsed["model"] != "gpt-4" {
			t.Errorf("model = %v, want gpt-4", parsed["model"])
		}
	})

	t.Run("multipart path removes old content-type header case-insensitively", func(t *testing.T) {
		body, ct := buildMultipartBody(t,
			map[string]string{"field": "val"},
			nil,
		)
		req := &schemas.HTTPRequest{
			Headers: map[string]string{"content-type": ct},
			Body:    body,
		}
		payload := map[string]any{"field": "val"}
		if err := SerializePayloadToRequest(req, payload, true, ct); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// The old lowercase "content-type" should be gone, replaced by "Content-Type".
		if _, exists := req.Headers["content-type"]; exists {
			t.Error("old lowercase content-type header should have been removed")
		}
		if _, exists := req.Headers["Content-Type"]; !exists {
			t.Error("new Content-Type header should be set")
		}
	})
}
