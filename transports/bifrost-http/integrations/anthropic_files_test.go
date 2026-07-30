package integrations

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
)

func findAnthropicFilesRoute(t *testing.T, routes []RouteConfig, method, path string) *RouteConfig {
	t.Helper()
	for i := range routes {
		if routes[i].Method == method && routes[i].Path == path {
			return &routes[i]
		}
	}
	t.Fatalf("no %s route registered at %s", method, path)
	return nil
}

// TestAnthropicFilesContentRouteDispatchesFileContent pins the fix for the
// files download route: GET /v1/files/{file_id}/content previously mounted the
// metadata-retrieve handler (FileRetrieveRequest dispatch), so downloads
// returned the file's metadata JSON with HTTP 200 instead of the file bytes.
// The route must dispatch FileContentRequest so the generic router's binary
// path streams the upstream bytes through.
func TestAnthropicFilesContentRouteDispatchesFileContent(t *testing.T) {
	routes := CreateAnthropicFilesRouteConfigs("/anthropic", nil)
	route := findAnthropicFilesRoute(t, routes, "GET", "/anthropic/v1/files/{file_id}/content")

	if got := route.GetHTTPRequestType(nil); got != schemas.FileContentRequest {
		t.Fatalf("content route HTTP request type = %v, want %v", got, schemas.FileContentRequest)
	}

	req, ok := route.GetRequestTypeInstance(context.Background()).(*anthropic.AnthropicFileContentRequest)
	if !ok {
		t.Fatalf("content route request instance = %T, want *anthropic.AnthropicFileContentRequest", route.GetRequestTypeInstance(context.Background()))
	}

	cases := []struct {
		name       string
		provider   schemas.ModelProvider
		fileID     string
		wantFileID string
	}{
		{"anthropic id passes through", schemas.Anthropic, "file_011abc", "file_011abc"},
		{"gemini id is translated", schemas.Gemini, "files-abc123", "files/abc123"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			instance := *req
			instance.FileID = tt.fileID
			bifrostCtx := createTestBifrostContextWithProvider(tt.provider)

			fileReq, err := route.FileRequestConverter(bifrostCtx, &instance)
			if err != nil {
				t.Fatalf("FileRequestConverter returned error: %v", err)
			}
			if fileReq.Type != schemas.FileContentRequest {
				t.Fatalf("dispatched FileRequest.Type = %v, want %v", fileReq.Type, schemas.FileContentRequest)
			}
			if fileReq.RetrieveRequest != nil {
				t.Fatal("content route populated RetrieveRequest; the pre-fix misroute is back")
			}
			if fileReq.ContentRequest == nil {
				t.Fatal("content route did not populate ContentRequest")
			}
			if fileReq.ContentRequest.FileID != tt.wantFileID {
				t.Fatalf("ContentRequest.FileID = %q, want %q", fileReq.ContentRequest.FileID, tt.wantFileID)
			}
			if fileReq.ContentRequest.Provider != tt.provider {
				t.Fatalf("ContentRequest.Provider = %v, want %v", fileReq.ContentRequest.Provider, tt.provider)
			}
		})
	}
}

// TestAnthropicFilesRetrieveRouteMountedOnMetadataPath pins the companion fix:
// the metadata-retrieve handler now lives on GET /v1/files/{file_id} (it was
// previously mounted on the /content path, leaving the metadata endpoint
// unrouted entirely).
func TestAnthropicFilesRetrieveRouteMountedOnMetadataPath(t *testing.T) {
	routes := CreateAnthropicFilesRouteConfigs("/anthropic", nil)
	route := findAnthropicFilesRoute(t, routes, "GET", "/anthropic/v1/files/{file_id}")

	if got := route.GetHTTPRequestType(nil); got != schemas.FileRetrieveRequest {
		t.Fatalf("retrieve route HTTP request type = %v, want %v", got, schemas.FileRetrieveRequest)
	}

	req, ok := route.GetRequestTypeInstance(context.Background()).(*anthropic.AnthropicFileRetrieveRequest)
	if !ok {
		t.Fatalf("retrieve route request instance = %T, want *anthropic.AnthropicFileRetrieveRequest", route.GetRequestTypeInstance(context.Background()))
	}
	req.FileID = "file_011abc"

	fileReq, err := route.FileRequestConverter(createTestBifrostContextWithProvider(schemas.Anthropic), req)
	if err != nil {
		t.Fatalf("FileRequestConverter returned error: %v", err)
	}
	if fileReq.Type != schemas.FileRetrieveRequest {
		t.Fatalf("dispatched FileRequest.Type = %v, want %v", fileReq.Type, schemas.FileRetrieveRequest)
	}
	if fileReq.RetrieveRequest == nil || fileReq.RetrieveRequest.FileID != "file_011abc" {
		t.Fatalf("retrieve route did not populate RetrieveRequest with the file id: %+v", fileReq.RetrieveRequest)
	}
}
