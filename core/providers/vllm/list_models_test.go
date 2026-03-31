package vllm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// newTestVLLMProvider creates a VLLMProvider suitable for unit tests.
// It uses a short timeout and no base URL (per-key URLs are expected).
func newTestVLLMProvider() *VLLMProvider {
	return &VLLMProvider{
		client: &fasthttp.Client{
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
		},
		networkConfig: schemas.NetworkConfig{},
	}
}

// modelsJSON returns a minimal OpenAI-compatible /v1/models response listing the given model IDs.
func modelsJSON(ids ...string) string {
	data := ""
	for i, id := range ids {
		if i > 0 {
			data += ","
		}
		data += fmt.Sprintf(`{"id":%q,"object":"model","owned_by":"vllm"}`, id)
	}
	return fmt.Sprintf(`{"object":"list","data":[%s]}`, data)
}

func TestListModels_QueriesAllBackends(t *testing.T) {
	t.Parallel()

	// Spin up two mock vLLM servers, each serving a different model.
	var hits1, hits2 atomic.Int32

	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits1.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, modelsJSON("model-from-backend-1"))
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits2.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, modelsJSON("model-from-backend-2"))
	}))
	defer server2.Close()

	provider := newTestVLLMProvider()

	keys := []schemas.Key{
		{
			ID:    "key-1",
			Value: schemas.EnvVar{Val: "test-api-key-1"},
			VLLMKeyConfig: &schemas.VLLMKeyConfig{
				URL: schemas.EnvVar{Val: server1.URL},
			},
		},
		{
			ID:    "key-2",
			Value: schemas.EnvVar{Val: "test-api-key-2"},
			VLLMKeyConfig: &schemas.VLLMKeyConfig{
				URL: schemas.EnvVar{Val: server2.URL},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := &schemas.BifrostListModelsRequest{
		Provider:   schemas.VLLM,
		Unfiltered: true,
	}

	resp, bifrostErr := provider.ListModels(ctx, keys, request)
	if bifrostErr != nil {
		t.Fatalf("ListModels returned error: %v", bifrostErr.Error)
	}

	// Both backends must have been queried.
	if hits1.Load() != 1 {
		t.Errorf("expected backend 1 to be queried once, got %d", hits1.Load())
	}
	if hits2.Load() != 1 {
		t.Errorf("expected backend 2 to be queried once, got %d", hits2.Load())
	}

	// Response must contain models from both backends.
	found := map[string]bool{}
	for _, m := range resp.Data {
		found[m.ID] = true
	}
	// Model IDs are prefixed with "vllm/" by ToBifrostListModelsResponse.
	if !found["vllm/model-from-backend-1"] {
		t.Errorf("response missing vllm/model-from-backend-1, got models: %v", resp.Data)
	}
	if !found["vllm/model-from-backend-2"] {
		t.Errorf("response missing vllm/model-from-backend-2, got models: %v", resp.Data)
	}

	// KeyStatuses should report success for both keys.
	if len(resp.KeyStatuses) != 2 {
		t.Fatalf("expected 2 key statuses, got %d", len(resp.KeyStatuses))
	}
	for _, ks := range resp.KeyStatuses {
		if ks.Status != schemas.KeyStatusSuccess {
			t.Errorf("key %s status = %s, want success", ks.KeyID, ks.Status)
		}
	}
}

func TestListModels_SingleBackendFailure(t *testing.T) {
	t.Parallel()

	// One healthy backend, one that returns 500.
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, modelsJSON("healthy-model"))
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"internal error","type":"server_error"}}`)
	}))
	defer server2.Close()

	provider := newTestVLLMProvider()

	keys := []schemas.Key{
		{
			ID:    "good-key",
			Value: schemas.EnvVar{Val: "key1"},
			VLLMKeyConfig: &schemas.VLLMKeyConfig{
				URL: schemas.EnvVar{Val: server1.URL},
			},
		},
		{
			ID:    "bad-key",
			Value: schemas.EnvVar{Val: "key2"},
			VLLMKeyConfig: &schemas.VLLMKeyConfig{
				URL: schemas.EnvVar{Val: server2.URL},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := &schemas.BifrostListModelsRequest{
		Provider:   schemas.VLLM,
		Unfiltered: true,
	}

	resp, bifrostErr := provider.ListModels(ctx, keys, request)
	if bifrostErr != nil {
		t.Fatalf("ListModels should not return a top-level error when one backend succeeds, got: %v", bifrostErr.Error)
	}

	// Models from the healthy backend should still appear.
	found := false
	for _, m := range resp.Data {
		if m.ID == "vllm/healthy-model" {
			found = true
		}
	}
	if !found {
		t.Error("response missing healthy-model from the working backend")
	}

	// KeyStatuses should reflect partial success.
	statusByKey := map[string]schemas.KeyStatusType{}
	for _, ks := range resp.KeyStatuses {
		statusByKey[ks.KeyID] = ks.Status
	}
	if statusByKey["good-key"] != schemas.KeyStatusSuccess {
		t.Errorf("good-key status = %s, want success", statusByKey["good-key"])
	}
	if statusByKey["bad-key"] == schemas.KeyStatusSuccess {
		t.Errorf("bad-key should not have success status, got %s", statusByKey["bad-key"])
	}
}

func TestListModels_ErrorsWithoutPerKeyURL(t *testing.T) {
	t.Parallel()

	// Keys without VLLMKeyConfig should error — there is no provider-level fallback.
	provider := newTestVLLMProvider()

	keys := []schemas.Key{
		{
			ID:    "no-config-key",
			Value: schemas.EnvVar{Val: "api-key"},
			// No VLLMKeyConfig — should produce an error.
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := &schemas.BifrostListModelsRequest{
		Provider:   schemas.VLLM,
		Unfiltered: true,
	}

	_, bifrostErr := provider.ListModels(ctx, keys, request)
	if bifrostErr == nil {
		t.Fatal("expected error for key without vllm_key_config.url, got nil")
	}
}
