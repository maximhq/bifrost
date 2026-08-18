package bifrost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// Regression for #5774: ListAllModels fans out to providers whose allowed_requests
// disables list_models. The doomed dispatch reaches key selection (and the
// provider, whose rejection then shows up as error noise in gateway logs)
// instead of the provider being skipped up front.

type keyFetchCountingAccount struct {
	*MockAccount
	mu         sync.Mutex
	keyFetches map[schemas.ModelProvider]int
}

func (a *keyFetchCountingAccount) GetKeysForProvider(ctx context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
	a.mu.Lock()
	if a.keyFetches == nil {
		a.keyFetches = make(map[schemas.ModelProvider]int)
	}
	a.keyFetches[provider]++
	a.mu.Unlock()
	return a.MockAccount.GetKeysForProvider(ctx, provider)
}

func (a *keyFetchCountingAccount) fetches(provider schemas.ModelProvider) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.keyFetches[provider]
}

func TestListAllModelsSkipsListModelsDisabledProviders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"m1","object":"model"}]}`))
	}))
	defer upstream.Close()

	base := NewMockAccount()
	base.AddProviderWithBaseURL("custom-enabled", 2, 10, upstream.URL)
	base.configs["custom-enabled"].CustomProviderConfig = &schemas.CustomProviderConfig{
		CustomProviderKey: "custom-enabled",
		BaseProviderType:  schemas.OpenAI,
	}
	base.AddProviderWithBaseURL("custom-disabled", 2, 10, upstream.URL)
	base.configs["custom-disabled"].CustomProviderConfig = &schemas.CustomProviderConfig{
		CustomProviderKey: "custom-disabled",
		BaseProviderType:  schemas.OpenAI,
		AllowedRequests:   &schemas.AllowedRequests{ChatCompletion: true}, // list_models omitted => disallowed
	}
	account := &keyFetchCountingAccount{MockAccount: base}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	client, err := Init(ctx, schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer client.Shutdown()

	resp, bifrostErr := client.ListAllModels(ctx, &schemas.BifrostListModelsRequest{Unfiltered: true})
	if bifrostErr != nil {
		t.Fatalf("ListAllModels: %v", bifrostErr)
	}
	if resp == nil || len(resp.Data) == 0 {
		t.Fatalf("expected models from the enabled provider, got %+v", resp)
	}

	if got := account.fetches("custom-disabled"); got != 0 {
		t.Errorf("list_models dispatched to disabled provider %d times, want 0 (should be skipped up front)", got)
	}
	if got := account.fetches("custom-enabled"); got == 0 {
		t.Errorf("enabled provider was never dispatched")
	}
}
