package openai

import (
	"net"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// TestTypedResourceIDsStayOnExpectedEndpoint drives GET, DELETE and cancel handlers against a mock
// upstream: a path-shaping ID must never be dispatched, and a valid ID must arrive as one segment.
func TestTypedResourceIDsStayOnExpectedEndpoint(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var mu sync.Mutex
	var received []string
	go fasthttp.Serve(ln, func(ctx *fasthttp.RequestCtx) { //nolint:errcheck
		mu.Lock()
		received = append(received, string(ctx.Method())+" "+string(ctx.Request.Header.RequestURI()))
		mu.Unlock()
		ctx.SetBodyString(`{"id":"x"}`)
	})

	provider := &OpenAIProvider{client: &fasthttp.Client{}, networkConfig: schemas.NetworkConfig{BaseURL: "http://" + ln.Addr().String()}}
	keys := []schemas.Key{{Value: *schemas.NewSecretVar("sk-test")}}
	newCtx := func() *schemas.BifrostContext { return schemas.NewBifrostContext(t.Context(), schemas.NoDeadline) }

	operations := []struct {
		name   string
		prefix string
		suffix string
		call   func(string) *schemas.BifrostError
	}{
		{"GET file content", "GET /v1/files/", "/content", func(id string) *schemas.BifrostError {
			_, bifrostErr := provider.FileContent(newCtx(), keys, &schemas.BifrostFileContentRequest{FileID: id})
			return bifrostErr
		}},
		{"DELETE file", "DELETE /v1/files/", "", func(id string) *schemas.BifrostError {
			_, bifrostErr := provider.FileDelete(newCtx(), keys, &schemas.BifrostFileDeleteRequest{FileID: id})
			return bifrostErr
		}},
		{"POST batch cancel", "POST /v1/batches/", "/cancel", func(id string) *schemas.BifrostError {
			_, bifrostErr := provider.BatchCancel(newCtx(), keys, &schemas.BifrostBatchCancelRequest{BatchID: id})
			return bifrostErr
		}},
		{"DELETE response", "DELETE /v1/responses/", "", func(id string) *schemas.BifrostError {
			_, bifrostErr := provider.ResponsesDelete(newCtx(), keys[0], &schemas.BifrostResponsesDeleteRequest{ResponseID: id})
			return bifrostErr
		}},
	}
	invalidIDs := []string{
		"../models?#", "%2e%2e%2fmodels%3f%23", "%252e%252e%252fmodels",
		"../models", "/models", "%2fmodels", "..",
		"?limit=100", "%3flimit=100", "#fragment", "%23fragment",
		"\\models", "%5cmodels",
	}
	validIDs := map[string]string{"obj-123_ABC": "obj-123_ABC", "obj 123": "obj%20123"}

	for _, operation := range operations {
		for _, id := range invalidIDs {
			bifrostErr := operation.call(id)
			if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 400 {
				t.Fatalf("%s: ID %q returned %+v, want a 400", operation.name, id, bifrostErr)
			}
		}
		mu.Lock()
		if len(received) != 0 {
			t.Fatalf("%s: invalid IDs were dispatched upstream: %v", operation.name, received)
		}
		mu.Unlock()

		for id, segment := range validIDs {
			operation.call(id) //nolint:errcheck
			mu.Lock()
			want := operation.prefix + segment + operation.suffix
			if len(received) != 1 || received[0] != want {
				t.Fatalf("%s: ID %q reached upstream as %v, want %q", operation.name, id, received, want)
			}
			received = nil
			mu.Unlock()
		}
	}
}
