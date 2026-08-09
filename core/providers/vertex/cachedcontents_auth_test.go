package vertex

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// TestVertexAuthHeaders_APIKeyPreservesInjectedAuthHeader verifies that when the
// key carries an API key value, vertexAuthHeaders passes it as the "key" query
// parameter and leaves an Authorization header (set upstream from context extra
// headers) untouched. This mirrors the Gemini generation endpoints and lets a
// caller inject its own bearer token via context extra headers.
func TestVertexAuthHeaders_APIKeyPreservesInjectedAuthHeader(t *testing.T) {
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	req.SetRequestURI("https://us-central1-aiplatform.googleapis.com/v1/projects/p/locations/us-central1/cachedContents")
	req.Header.Set("Authorization", "Bearer injected-token")

	key := schemas.Key{Value: *schemas.NewSecretVar("api-key-123")}
	if err := vertexAuthHeaders(req, key); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := string(req.Header.Peek("Authorization")); got != "Bearer injected-token" {
		t.Errorf("Authorization header was overwritten: got %q, want the injected token preserved", got)
	}
	if got := string(req.URI().QueryArgs().Peek("key")); got != "api-key-123" {
		t.Errorf("key query parameter: got %q, want %q", got, "api-key-123")
	}
}
