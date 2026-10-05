package bifrost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// openAIStyleModelsHandler serves a minimal OpenAI-format GET /v1/models
// response listing the given model IDs.
func openAIStyleModelsHandler(ids ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := `{"object":"list","data":[`
		for i, id := range ids {
			if i > 0 {
				body += ","
			}
			body += `{"id":"` + id + `","object":"model","created":1700000000,"owned_by":"test"}`
		}
		body += `]}`
		_, _ = w.Write([]byte(body))
	}
}

// wildcardKey returns a key whose allowlist admits every model, so the
// filtered list-models path doesn't early-exit to an empty list.
func wildcardKey(provider schemas.ModelProvider) []schemas.Key {
	return []schemas.Key{{
		ID:     "test-key-" + string(provider),
		Value:  *schemas.NewSecretVar("sk-test-" + string(provider)),
		Models: schemas.WhiteList{"*"},
		Weight: 100,
	}}
}

// tuneProviderForTest shrinks the provider's HTTP budget so an abandoned
// in-flight call to a hanging test server releases its worker (and Shutdown)
// quickly, and disables retries for deterministic single-attempt behavior.
func tuneProviderForTest(t *testing.T, account *MockAccount, provider schemas.ModelProvider) {
	t.Helper()
	cfg, ok := account.configs[provider]
	if !ok {
		t.Fatalf("provider %s not configured on mock account", provider)
	}
	cfg.NetworkConfig.DefaultRequestTimeoutInSeconds = 2
	cfg.NetworkConfig.MaxRetries = 0
}

func newListModelsTestClient(t *testing.T, account *MockAccount, catalog schemas.ModelInfoProvider) *Bifrost {
	t.Helper()
	client, initErr := Init(context.Background(), schemas.BifrostConfig{
		Account:      account,
		Logger:       NewNoOpLogger(),
		ModelCatalog: catalog,
	})
	if initErr != nil {
		t.Fatalf("Init failed: %v", initErr)
	}
	t.Cleanup(client.Shutdown)
	return client
}

func modelIDs(resp *schemas.BifrostListModelsResponse) []string {
	if resp == nil {
		return nil
	}
	ids := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		ids = append(ids, m.ID)
	}
	return ids
}

func containsModelID(resp *schemas.BifrostListModelsResponse, id string) bool {
	if resp == nil {
		return false
	}
	for _, m := range resp.Data {
		if m.ID == id {
			return true
		}
	}
	return false
}

// A single unreachable provider must not block the aggregate list: the healthy
// provider's models should come back well under the unreachable provider's
// HTTP timeout (issue #6612: the fan-out previously ran with no deadline, so
// GET /v1/models hung for the full shared request timeout on every call).
func TestListAllModelsUnreachableProviderDoesNotBlockAggregate(t *testing.T) {
	healthySrv := httptest.NewServer(openAIStyleModelsHandler("gpt-4o"))
	t.Cleanup(healthySrv.Close)

	hang := make(chan struct{})
	hangingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-hang
	}))
	// Release the hanging handler before closing the server so Close doesn't
	// wait on it. Registered before the client so Shutdown (registered later,
	// run earlier) still exercises the abandoned-call path first.
	t.Cleanup(func() {
		close(hang)
		hangingSrv.Close()
	})

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 10, healthySrv.URL)
	account.AddProviderWithBaseURL(schemas.Groq, 1, 10, hangingSrv.URL)
	account.SetKeysForProvider(schemas.OpenAI, wildcardKey(schemas.OpenAI))
	account.SetKeysForProvider(schemas.Groq, wildcardKey(schemas.Groq))
	tuneProviderForTest(t, account, schemas.OpenAI)
	tuneProviderForTest(t, account, schemas.Groq)

	// Shrink the per-provider poll deadline so the green path is fast; the
	// red path (no deadline honored) is still bounded by the 2s HTTP budget
	// above, which comfortably exceeds the assertion below.
	oldTimeout := listAllModelsProviderTimeout
	listAllModelsProviderTimeout = 400 * time.Millisecond
	t.Cleanup(func() { listAllModelsProviderTimeout = oldTimeout })

	client := newListModelsTestClient(t, account, nil)

	// No caller deadline, mirroring the HTTP handler: the aggregate itself
	// must bound the unreachable provider's poll.
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	start := time.Now()
	resp, bfErr := client.ListAllModels(ctx, &schemas.BifrostListModelsRequest{})
	elapsed := time.Since(start)

	if bfErr != nil {
		t.Fatalf("ListAllModels returned error: %s", bfErr.GetErrorString())
	}
	if elapsed >= 1500*time.Millisecond {
		t.Fatalf("aggregate blocked for %v by the unreachable provider; want well under its 2s HTTP timeout", elapsed)
	}
	if !containsModelID(resp, "openai/gpt-4o") {
		t.Fatalf("healthy provider's models missing from aggregate; got %v", modelIDs(resp))
	}
}

// staticListerCatalog is a schemas.ModelInfoProvider that also implements the
// optional per-provider model listing the aggregate falls back on, mimicking
// the framework's ModelCatalog.
type staticListerCatalog struct {
	modelsByProvider map[schemas.ModelProvider][]string
}

func (c *staticListerCatalog) GetModelInfo(provider schemas.ModelProvider, model string) *schemas.Model {
	return nil
}

func (c *staticListerCatalog) CalculateRequestCost(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse) float64 {
	return 0
}

func (c *staticListerCatalog) GetModelsForProvider(provider schemas.ModelProvider) []string {
	return c.modelsByProvider[provider]
}

func (c *staticListerCatalog) GetUnfilteredModelsForProvider(provider schemas.ModelProvider) []string {
	return c.modelsByProvider[provider]
}

// When a provider's live poll fails and a model catalog is configured, the
// aggregate should serve that provider's last-known models instead of
// dropping the provider from the response.
func TestListAllModelsServesLastKnownModelsWhenPollFails(t *testing.T) {
	healthySrv := httptest.NewServer(openAIStyleModelsHandler("gpt-4o"))
	t.Cleanup(healthySrv.Close)

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 10, healthySrv.URL)
	// Nothing listens here: the poll fails fast with connection refused.
	account.AddProviderWithBaseURL(schemas.Groq, 1, 10, "http://127.0.0.1:1")
	account.SetKeysForProvider(schemas.OpenAI, wildcardKey(schemas.OpenAI))
	account.SetKeysForProvider(schemas.Groq, wildcardKey(schemas.Groq))
	tuneProviderForTest(t, account, schemas.OpenAI)
	tuneProviderForTest(t, account, schemas.Groq)

	catalog := &staticListerCatalog{modelsByProvider: map[schemas.ModelProvider][]string{
		schemas.Groq: {"llama-3.3-70b-versatile"},
	}}
	client := newListModelsTestClient(t, account, catalog)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	resp, bfErr := client.ListAllModels(ctx, &schemas.BifrostListModelsRequest{})
	if bfErr != nil {
		t.Fatalf("ListAllModels returned error: %s", bfErr.GetErrorString())
	}
	if !containsModelID(resp, "openai/gpt-4o") {
		t.Fatalf("healthy provider's models missing from aggregate; got %v", modelIDs(resp))
	}
	if !containsModelID(resp, "groq/llama-3.3-70b-versatile") {
		t.Fatalf("last-known models for the failed provider missing from aggregate; got %v", modelIDs(resp))
	}
}

// With no catalog to fall back on, an aggregate where every poll failed must
// keep returning the first error rather than an empty success.
func TestListAllModelsAllPollsFailWithoutCatalogReturnsError(t *testing.T) {
	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.Groq, 1, 10, "http://127.0.0.1:1")
	account.SetKeysForProvider(schemas.Groq, wildcardKey(schemas.Groq))
	tuneProviderForTest(t, account, schemas.Groq)

	client := newListModelsTestClient(t, account, nil)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	resp, bfErr := client.ListAllModels(ctx, &schemas.BifrostListModelsRequest{})
	if bfErr == nil {
		t.Fatalf("expected an error when every provider poll fails with no catalog fallback; got %v", modelIDs(resp))
	}
}
