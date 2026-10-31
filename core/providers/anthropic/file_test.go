package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Verbatim body from api.anthropic.com GET /v1/files/{file_id} for a
// code-execution generated PDF.
const anthropicFileRetrieveBody = `{"type":"file","id":"file_01AAXwcM7Z9Kbo6BR3vTL6Da","size_bytes":3889,"created_at":"2026-08-31T11:32:41.893769Z","filename":"sample.pdf","mime_type":"application/pdf","downloadable":true}`

func TestAnthropicFileMetadataRoundTrip(t *testing.T) {
	var upstream AnthropicFileResponse
	if err := json.Unmarshal([]byte(anthropicFileRetrieveBody), &upstream); err != nil {
		t.Fatalf("unmarshal upstream: %v", err)
	}

	t.Run("retrieve", func(t *testing.T) {
		out := ToAnthropicFileRetrieveResponse(upstream.ToBifrostFileRetrieveResponse(0, false, false, nil, nil))
		if out.MimeType != "application/pdf" {
			t.Errorf("mime_type = %q, want application/pdf", out.MimeType)
		}
		if out.Downloadable == nil || !*out.Downloadable {
			t.Errorf("downloadable = %v, want true", out.Downloadable)
		}
	})

	t.Run("upload", func(t *testing.T) {
		out := ToAnthropicFileUploadResponse(upstream.ToBifrostFileUploadResponse(0, false, false, nil, nil))
		if out.MimeType != "application/pdf" {
			t.Errorf("mime_type = %q, want application/pdf", out.MimeType)
		}
		if out.Downloadable == nil || !*out.Downloadable {
			t.Errorf("downloadable = %v, want true", out.Downloadable)
		}
	})

	t.Run("list", func(t *testing.T) {
		out := ToAnthropicFileListResponse(&schemas.BifrostFileListResponse{
			Data: []schemas.FileObject{{
				ID:           upstream.ID,
				Object:       upstream.Type,
				Filename:     upstream.Filename,
				ContentType:  upstream.MimeType,
				Downloadable: upstream.Downloadable,
			}},
		})
		if len(out.Data) != 1 {
			t.Fatalf("got %d files, want 1", len(out.Data))
		}
		if out.Data[0].MimeType != "application/pdf" {
			t.Errorf("mime_type = %q, want application/pdf", out.Data[0].MimeType)
		}
		if out.Data[0].Downloadable == nil || !*out.Data[0].Downloadable {
			t.Errorf("downloadable = %v, want true", out.Data[0].Downloadable)
		}
	})
}

// A provider that does not report these fields must omit them rather than
// claim an empty mime type and a non-downloadable file.
func TestAnthropicFileMetadataOmittedWhenUnreported(t *testing.T) {
	out := ToAnthropicFileRetrieveResponse(&schemas.BifrostFileRetrieveResponse{
		ID:       "file_abc",
		Object:   "file",
		Filename: "sample.pdf",
	})

	body, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := decoded["mime_type"]; ok {
		t.Errorf("mime_type present in %s, want omitted", body)
	}
	if _, ok := decoded["downloadable"]; ok {
		t.Errorf("downloadable present in %s, want omitted", body)
	}
}
