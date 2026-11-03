package openai

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// TestContainerFileListFindsTheContainersKey pins that a container's file listing asks each key until
// one owns the container, keeps later pages on that key, and never continues into the next key.
func TestContainerFileListFindsTheContainersKey(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		seen = append(seen, auth+" after="+r.URL.Query().Get("after"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if auth != "Bearer key-b" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"Container not found","type":"invalid_request_error"}}`))
			return
		}
		if r.URL.Query().Get("after") == "" {
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"cfile-1","object":"container.file"}],"has_more":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"cfile-2","object":"container.file"}],"has_more":false}`))
	}))
	t.Cleanup(server.Close)

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 10, AllowPrivateNetwork: true},
	}, nil)
	keys := []schemas.Key{
		{ID: "a", Value: *schemas.NewSecretVar("key-a")},
		{ID: "b", Value: *schemas.NewSecretVar("key-b")},
		{ID: "c", Value: *schemas.NewSecretVar("key-c")},
	}
	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)

	first, err := provider.ContainerFileList(ctx, keys, &schemas.BifrostContainerFileListRequest{Provider: schemas.OpenAI, ContainerID: "cntr_1"})
	if err != nil {
		t.Fatalf("first page: %v", err.Error.Message)
	}
	if len(first.Data) != 1 || first.Data[0].ID != "cfile-1" || !first.HasMore || first.After == nil {
		t.Fatalf("first page = %+v", first)
	}

	second, err := provider.ContainerFileList(ctx, keys, &schemas.BifrostContainerFileListRequest{Provider: schemas.OpenAI, ContainerID: "cntr_1", After: first.After})
	if err != nil {
		t.Fatalf("second page: %v", err.Error.Message)
	}
	if len(second.Data) != 1 || second.Data[0].ID != "cfile-2" || second.HasMore || second.After != nil {
		t.Fatalf("second page = %+v", second)
	}

	want := []string{"Bearer key-a after=", "Bearer key-b after=", "Bearer key-b after=cfile-1"}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != len(want) {
		t.Fatalf("upstream calls = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("upstream calls = %v, want %v", seen, want)
		}
	}
}

// TestContainerFileListNotFoundOnEveryKey pins that a container no key owns comes back as the provider's 404.
func TestContainerFileListNotFoundOnEveryKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"Container not found","type":"invalid_request_error"}}`))
	}))
	t.Cleanup(server.Close)

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 10, AllowPrivateNetwork: true},
	}, nil)
	keys := []schemas.Key{{ID: "a", Value: *schemas.NewSecretVar("key-a")}, {ID: "b", Value: *schemas.NewSecretVar("key-b")}}
	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)

	_, err := provider.ContainerFileList(ctx, keys, &schemas.BifrostContainerFileListRequest{Provider: schemas.OpenAI, ContainerID: "cntr_missing"})
	if err == nil || err.StatusCode == nil || *err.StatusCode != http.StatusNotFound {
		t.Fatalf("error = %+v, want 404", err)
	}
}
