package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileUploadSendsContentTypeToGemini(t *testing.T) {
	const contentType = "application/pdf"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/upload/v1beta/files" {
			http.Error(w, fmt.Sprintf("unexpected path: %s", r.URL.Path), http.StatusNotFound)
			return
		}
		if got := r.Header.Get("x-goog-api-key"); got != "dummy-key" {
			http.Error(w, fmt.Sprintf("unexpected api key: %s", got), http.StatusUnauthorized)
			return
		}

		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to read multipart body: %v", err), http.StatusBadRequest)
			return
		}

		var sawMetadata, sawFile bool
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, fmt.Sprintf("failed to read multipart part: %v", err), http.StatusBadRequest)
				return
			}
			body, err := io.ReadAll(part)
			if err != nil {
				http.Error(w, fmt.Sprintf("failed to read multipart part body: %v", err), http.StatusBadRequest)
				return
			}

			switch part.FormName() {
			case "metadata":
				sawMetadata = true
				var metadata struct {
					File struct {
						DisplayName string `json:"displayName"`
						MIMEType    string `json:"mimeType"`
					} `json:"file"`
				}
				if err := json.Unmarshal(body, &metadata); err != nil {
					http.Error(w, fmt.Sprintf("failed to unmarshal metadata: %v", err), http.StatusBadRequest)
					return
				}
				if metadata.File.DisplayName != "tiny.pdf" {
					http.Error(w, fmt.Sprintf("unexpected displayName: %s", metadata.File.DisplayName), http.StatusBadRequest)
					return
				}
				if metadata.File.MIMEType != contentType {
					http.Error(w, fmt.Sprintf("unexpected metadata mimeType: %s", metadata.File.MIMEType), http.StatusBadRequest)
					return
				}
			case "file":
				sawFile = true
				if part.FileName() != "tiny.pdf" {
					http.Error(w, fmt.Sprintf("unexpected filename: %s", part.FileName()), http.StatusBadRequest)
					return
				}
				if got := part.Header.Get("Content-Type"); got != contentType {
					http.Error(w, fmt.Sprintf("unexpected file part content type: %s", got), http.StatusBadRequest)
					return
				}
				if string(body) != "ok" {
					http.Error(w, fmt.Sprintf("unexpected file body: %s", string(body)), http.StatusBadRequest)
					return
				}
			}
		}
		if !sawMetadata || !sawFile {
			http.Error(w, "missing metadata or file multipart part", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"file":{"name":"files/test","displayName":"tiny.pdf","mimeType":"application/pdf","sizeBytes":"2","createTime":"2026-07-01T00:00:00Z","state":"ACTIVE","uri":"https://generativelanguage.googleapis.com/v1beta/files/test"}}`))
	}))
	defer ts.Close()

	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: ts.URL + "/v1beta"},
	}, testNoopLogger{})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}
	resp, bifrostErr := provider.FileUpload(ctx, key, &schemas.BifrostFileUploadRequest{
		Provider:    schemas.Gemini,
		File:        []byte("ok"),
		Filename:    "tiny.pdf",
		Purpose:     schemas.FilePurposeUserData,
		ContentType: schemas.Ptr(contentType),
	})

	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)
	assert.Equal(t, "files/test", resp.ID)
	assert.Equal(t, "tiny.pdf", resp.Filename)
	assert.Equal(t, "https://generativelanguage.googleapis.com/v1beta/files/test", resp.StorageURI)
}
