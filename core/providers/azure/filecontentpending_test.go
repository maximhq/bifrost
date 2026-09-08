package azure

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestFileContent_PendingUploadReturns409 pins how an Azure 204 on the file
// content route is surfaced. Azure answers 204 No Content while an upload is
// still "pending"/"running". Echoing that status to the client would make the
// HTTP layer drop the error body (204 must not carry one), so the provider has
// to translate it into a body-bearing 409 with a message that tells the caller
// to poll the file status.
func TestFileContent_PendingUploadReturns409(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/files/file-pending/content") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	provider, err := NewAzureProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 10},
	}, &authTestLogger{})
	if err != nil {
		t.Fatalf("NewAzureProvider: %v", err)
	}

	key := schemas.Key{
		Value:          *schemas.NewSecretVar("test-api-key"),
		Models:         []string{"*"},
		AzureKeyConfig: &schemas.AzureKeyConfig{Endpoint: *schemas.NewSecretVar(server.URL)},
	}
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, bifrostErr := provider.FileContent(ctx, []schemas.Key{key}, &schemas.BifrostFileContentRequest{
		Provider: schemas.Azure,
		FileID:   "file-pending",
	})
	if resp != nil {
		t.Fatalf("expected no response for a pending file, got %+v", resp)
	}
	if bifrostErr == nil {
		t.Fatal("expected an error for a pending file, got nil")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusConflict {
		t.Fatalf("StatusCode: got %v, want %d (a 204 must never be echoed to the client)", bifrostErr.StatusCode, http.StatusConflict)
	}
	if bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "processed") {
		t.Fatalf("error message should tell the caller to poll for status processed, got %+v", bifrostErr.Error)
	}
}
