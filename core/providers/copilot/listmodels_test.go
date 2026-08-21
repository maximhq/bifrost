package copilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// seedTokenManager installs a manager with an already-valid JWT and API base so
// ListModels routing can be exercised without a live token exchange.
func seedTokenManager(provider *CopilotProvider, key schemas.Key, jwt, apiBase string) {
	tm := newCopilotTokenManager(key.Value.GetValue(), provider.tokenClient, provider.logger)
	tm.apiToken = jwt
	tm.apiBase = apiBase
	tm.expiresAt = time.Now().Add(30 * time.Minute)
	provider.tokenManagers.Store(key.ID, &CopilotTokenManagerEntry{tm: tm, accessToken: key.Value.GetValue()})
}

func modelsServer(t *testing.T, modelID string, recorder *[]string, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*recorder = append(*recorder, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   []map[string]any{{"id": modelID, "object": "model"}},
		})
	}))
}

// TestListModelsUsesPerKeyAPIBase pins that each key is listed against the API
// base its own token exchange returned. Copilot serves individual and enterprise
// accounts from different hosts, so reusing the first key's base sent other keys
// to the wrong host and dropped their models.
func TestListModelsUsesPerKeyAPIBase(t *testing.T) {
	var mu sync.Mutex
	var individualAuth, enterpriseAuth []string

	individual := modelsServer(t, "gpt-4o", &individualAuth, &mu)
	defer individual.Close()
	enterprise := modelsServer(t, "gpt-4o-enterprise", &enterpriseAuth, &mu)
	defer enterprise.Close()

	provider := &CopilotProvider{client: &fasthttp.Client{}}
	keyA := schemas.Key{ID: "key-individual", Value: *schemas.NewSecretVar("ghu_a"), Models: []string{"*"}}
	keyB := schemas.Key{ID: "key-enterprise", Value: *schemas.NewSecretVar("ghu_b"), Models: []string{"*"}}
	seedTokenManager(provider, keyA, "jwt-a", individual.URL)
	seedTokenManager(provider, keyB, "jwt-b", enterprise.URL)

	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	resp, bifrostErr := provider.ListModels(ctx, []schemas.Key{keyA, keyB}, &schemas.BifrostListModelsRequest{})
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr.Error.Message)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(individualAuth) != 1 || individualAuth[0] != "Bearer jwt-a" {
		t.Errorf("individual host got %v, want one request with 'Bearer jwt-a'", individualAuth)
	}
	if len(enterpriseAuth) != 1 || enterpriseAuth[0] != "Bearer jwt-b" {
		t.Errorf("enterprise host got %v, want one request with 'Bearer jwt-b'", enterpriseAuth)
	}

	got := map[string]bool{}
	for _, m := range resp.Data {
		got[m.ID] = true
	}
	if !got["copilot/gpt-4o"] || !got["copilot/gpt-4o-enterprise"] {
		t.Errorf("expected models from both hosts, got %v", got)
	}
}

// TestListModelsSkipsKeyWithTokenError pins that one unusable key does not sink
// the whole listing.
func TestListModelsSkipsKeyWithTokenError(t *testing.T) {
	var mu sync.Mutex
	var good []string
	srv := modelsServer(t, "gpt-4o", &good, &mu)
	defer srv.Close()

	provider := &CopilotProvider{client: &fasthttp.Client{}, tokenClient: &fasthttp.Client{}}
	keyOK := schemas.Key{ID: "key-ok", Value: *schemas.NewSecretVar("ghu_ok"), Models: []string{"*"}}
	keyBad := schemas.Key{ID: "key-bad", Value: *schemas.NewSecretVar("ghu_bad"), Models: []string{"*"}}
	seedTokenManager(provider, keyOK, "jwt-ok", srv.URL)

	// No cached JWT and an unreachable exchange endpoint, so this key fails to resolve.
	badTM := newCopilotTokenManager("ghu_bad", &fasthttp.Client{}, nil)
	badTM.tokenExchangeURL = "http://127.0.0.1:1"
	provider.tokenManagers.Store(keyBad.ID, &CopilotTokenManagerEntry{tm: badTM, accessToken: "ghu_bad"})

	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	resp, bifrostErr := provider.ListModels(ctx, []schemas.Key{keyOK, keyBad}, &schemas.BifrostListModelsRequest{})
	if bifrostErr != nil {
		t.Fatalf("expected the healthy key to still return models, got error: %v", bifrostErr.Error.Message)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "copilot/gpt-4o" {
		t.Errorf("expected only the healthy key's model, got %v", resp.Data)
	}
}

func TestListModelsWithNoKeysReturnsUnauthorized(t *testing.T) {
	provider := &CopilotProvider{client: &fasthttp.Client{}}
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

	_, bifrostErr := provider.ListModels(ctx, nil, &schemas.BifrostListModelsRequest{})
	if bifrostErr == nil {
		t.Fatal("expected an error when no keys are configured")
	}
	if *bifrostErr.StatusCode != 401 {
		t.Errorf("expected status 401, got %d", *bifrostErr.StatusCode)
	}
}
