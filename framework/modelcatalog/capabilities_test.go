package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/lrucache"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
)

// capabilityTestCatalog builds a catalog whose capability lookups are served by
// load instead of the datasheet, so cache behaviour can be exercised without a
// config store.
func capabilityTestCatalog(load func(schemas.ModelProvider, string) *schemas.ModelCapabilities) (*ModelCatalog, *int) {
	calls := 0
	mc := &ModelCatalog{capabilities: lrucache.New[*schemas.ModelCapabilities](capabilityCacheSize)}
	mc.loadCapabilities = func(p schemas.ModelProvider, m string) (*schemas.ModelCapabilities, error) {
		calls++
		return load(p, m), nil
	}
	return mc, &calls
}

func TestGetModelCapabilities_CachesRecord(t *testing.T) {
	yes := true
	mc, calls := capabilityTestCatalog(func(p schemas.ModelProvider, m string) *schemas.ModelCapabilities {
		if p == schemas.Azure && m == "gpt-5.1-chat" {
			return &schemas.ModelCapabilities{SupportsFastMode: &yes}
		}
		return nil
	})

	for range 3 {
		got := mc.GetModelCapabilities(schemas.Azure, "gpt-5.1-chat")
		if got == nil || got.SupportsFastMode == nil || !*got.SupportsFastMode {
			t.Fatalf("expected the loaded record, got %+v", got)
		}
	}
	if *calls != 1 {
		t.Errorf("expected 1 load for 3 lookups, got %d", *calls)
	}
}

// A model the datasheet has no record for is loaded once and then answered from
// cache. Without that, the ~10 capability checks a single request makes would
// each hit the database.
func TestGetModelCapabilities_CachesAbsence(t *testing.T) {
	mc, calls := capabilityTestCatalog(func(schemas.ModelProvider, string) *schemas.ModelCapabilities {
		return nil
	})

	for range 5 {
		if got := mc.GetModelCapabilities(schemas.OpenAI, "some-fine-tune"); got != nil {
			t.Fatalf("expected nil for a model with no record, got %+v", got)
		}
	}
	if *calls != 1 {
		t.Errorf("expected 1 load for 5 lookups, got %d", *calls)
	}
}

// Provider is part of the key, so one provider's record can never answer for
// another's — the same model name resolves independently per provider.
func TestGetModelCapabilities_KeyedByProvider(t *testing.T) {
	yes, no := true, false
	mc, _ := capabilityTestCatalog(func(p schemas.ModelProvider, m string) *schemas.ModelCapabilities {
		switch p {
		case schemas.Anthropic:
			return &schemas.ModelCapabilities{SupportsFastMode: &yes}
		case schemas.Vertex:
			return &schemas.ModelCapabilities{SupportsFastMode: &no}
		}
		return nil
	})

	anth := mc.GetModelCapabilities(schemas.Anthropic, "claude-opus-4-8")
	vertex := mc.GetModelCapabilities(schemas.Vertex, "claude-opus-4-8")
	if anth == nil || !*anth.SupportsFastMode {
		t.Fatalf("expected the anthropic record, got %+v", anth)
	}
	if vertex == nil || *vertex.SupportsFastMode {
		t.Fatalf("expected the vertex record, got %+v", vertex)
	}
}

// Keys are length-prefixed, so a value containing the separator cannot forge a
// boundary. ("a:b", "c") and ("a", "b:c") are distinct pairs and must not share
// an entry — a naive join on ":" would collapse both to "a:b:c".
func TestGetModelCapabilities_KeyCannotBeForged(t *testing.T) {
	var seen []string
	mc, calls := capabilityTestCatalog(func(p schemas.ModelProvider, m string) *schemas.ModelCapabilities {
		seen = append(seen, string(p)+"|"+m)
		return &schemas.ModelCapabilities{}
	})

	mc.GetModelCapabilities(schemas.ModelProvider("a:b"), "c")
	mc.GetModelCapabilities(schemas.ModelProvider("a"), "b:c")

	if *calls != 2 {
		t.Errorf("the two pairs must not share a cache entry, got %d loads for %v", *calls, seen)
	}
}

// A sync that applies a new sheet drops everything, records and cached absences
// alike — otherwise a model added by the sync would stay invisible.
func TestGetModelCapabilities_FlushDropsRecordsAndAbsences(t *testing.T) {
	present := false
	yes := true
	mc, calls := capabilityTestCatalog(func(schemas.ModelProvider, string) *schemas.ModelCapabilities {
		if present {
			return &schemas.ModelCapabilities{SupportsFastMode: &yes}
		}
		return nil
	})

	if got := mc.GetModelCapabilities(schemas.OpenAI, "late-arrival"); got != nil {
		t.Fatalf("expected nil before the model exists, got %+v", got)
	}
	if *calls != 1 {
		t.Fatalf("expected 1 load, got %d", *calls)
	}

	// The sheet now carries the model; without a flush the cached absence would
	// keep answering.
	present = true
	mc.capabilities.Flush()

	if got := mc.GetModelCapabilities(schemas.OpenAI, "late-arrival"); got == nil {
		t.Error("expected the newly-synced record after a flush")
	}
	if *calls != 2 {
		t.Errorf("expected the flush to force a reload, got %d loads", *calls)
	}
}

func TestGetModelCapabilities_NilCatalogAndEmptyModel(t *testing.T) {
	var nilCatalog *ModelCatalog
	if got := nilCatalog.GetModelCapabilities(schemas.OpenAI, "gpt-4o"); got != nil {
		t.Errorf("expected nil from a nil catalog, got %+v", got)
	}

	mc, calls := capabilityTestCatalog(func(schemas.ModelProvider, string) *schemas.ModelCapabilities {
		return &schemas.ModelCapabilities{}
	})
	if got := mc.GetModelCapabilities(schemas.OpenAI, ""); got != nil {
		t.Errorf("expected nil for an empty model, got %+v", got)
	}
	if *calls != 0 {
		t.Error("an empty model must not reach the loader")
	}
}

// A failed load must not be cached. A transient store error is indistinguishable
// from "this model has no row" at the call site, so caching it would pin the
// model to name-based fallbacks until the next sheet apply — potentially an hour
// of stale behaviour from a momentary blip.
func TestGetModelCapabilities_DoesNotCacheFailedLoads(t *testing.T) {
	yes := true
	calls := 0
	failing := true
	mc := &ModelCatalog{capabilities: lrucache.New[*schemas.ModelCapabilities](capabilityCacheSize)}
	mc.loadCapabilities = func(schemas.ModelProvider, string) (*schemas.ModelCapabilities, error) {
		calls++
		if failing {
			return nil, errors.New("store unavailable")
		}
		return &schemas.ModelCapabilities{SupportsFastMode: &yes}, nil
	}

	if got := mc.GetModelCapabilities(schemas.Anthropic, "claude-opus-5"); got != nil {
		t.Fatalf("expected nil while the store is failing, got %+v", got)
	}
	if calls != 1 {
		t.Fatalf("expected 1 load, got %d", calls)
	}

	// The store recovers: the next request must retry rather than serve a
	// cached absence.
	failing = false
	if got := mc.GetModelCapabilities(schemas.Anthropic, "claude-opus-5"); got == nil {
		t.Error("expected the record once the store recovered — the failed load was cached")
	}
	if calls != 2 {
		t.Errorf("expected a retry after the failed load, got %d loads", calls)
	}
}

// The capability resolver is process-global and closes over the catalog, so a
// discarded catalog must not keep serving lookups. Cleanup has to drop it; the
// loader builds its own context, so cancelling syncCtx alone does not stop it.
func TestCleanup_ClearsGlobalCapabilityResolver(t *testing.T) {
	yes := true
	mc := &ModelCatalog{
		capabilities: lrucache.New[*schemas.ModelCapabilities](capabilityCacheSize),
		done:         make(chan struct{}),
	}
	mc.loadCapabilities = func(schemas.ModelProvider, string) (*schemas.ModelCapabilities, error) {
		return &schemas.ModelCapabilities{SupportsFastMode: &yes}, nil
	}
	providerUtils.SetCapabilityResolver(mc.GetModelCapabilities)
	t.Cleanup(func() { providerUtils.SetCapabilityResolver(nil) })

	if got := providerUtils.CapabilitiesFor(schemas.Anthropic, "claude-opus-5"); got == nil {
		t.Fatal("expected the resolver to answer while the catalog is live")
	}

	if err := mc.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if got := providerUtils.CapabilitiesFor(schemas.Anthropic, "claude-opus-5"); got != nil {
		t.Errorf("resolver still answering from a torn-down catalog: %+v", got)
	}
}

// Reproduce #8073 on the real SQLite -> datasheet -> capability cache ->
// Anthropic HTTP path. The upstream witnesses max_tokens, not just a cached
// capability value, before and after the same sync used at background startup.
func TestModelParametersSyncPreservesCustomProviderMaxTokens(t *testing.T) {
	const model = "custom-params-model"
	const customProvider schemas.ModelProvider = "custom-anthropic"
	ctx := t.Context()
	logger := bifrost.NewNoOpLogger()
	cs, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(context.Background()) })
	if err := cs.UpsertModelParametersBatch(ctx, []tables.TableModelParameters{
		{Model: string(customProvider) + "/" + model, Data: `{"provider":"custom-anthropic","max_output_tokens":32000}`},
	}); err != nil {
		t.Fatal(err)
	}
	witness := make(chan int, 4)
	var feedMode atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/params.json" {
			if feedMode.Load() == 1 {
				_, _ = w.Write([]byte(`{"custom-anthropic/custom-params-model":{"provider":"custom-anthropic","max_output_tokens":"invalid"}}`))
				return
			}
			if feedMode.Load() == 2 {
				_, _ = w.Write([]byte(`{"custom-params-model":{"provider":"anthropic","max_output_tokens":24000},"custom-anthropic/custom-params-model":{"provider":"custom-anthropic","max_output_tokens":"invalid"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"custom-params-model":{"provider":"anthropic","max_output_tokens":16000}}`))
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			http.Error(w, "unexpected fixture route", http.StatusNotFound)
			return
		}
		var request struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != model {
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		witness <- request.MaxTokens
		_, _ = fmt.Fprintf(w, `{"id":"msg_fixture","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, model)
	}))
	t.Cleanup(upstream.Close)
	ds := datasheet.New(cs, logger, datasheet.Config{ModelParametersURL: upstream.URL + "/params.json"})
	mc := &ModelCatalog{datasheet: ds, capabilities: lrucache.New[*schemas.ModelCapabilities](capabilityCacheSize)}
	mc.loadCapabilities = mc.datasheetCapabilityLoader
	ds.SetOnModelParametersApplied(mc.capabilities.Flush)
	providerUtils.SetCapabilityResolver(mc.GetModelCapabilities)
	t.Cleanup(func() { providerUtils.SetCapabilityResolver(nil) })
	if _, err := ds.LoadModelParamsFromDB(ctx); err != nil {
		t.Fatal(err)
	}
	provider := anthropic.NewAnthropicProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: upstream.URL, AllowPrivateNetwork: true},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			BaseProviderType: schemas.Anthropic,
			IsKeyLess:        true,
		},
	}, logger)
	for _, phase := range []string{"before sync", "after sync", "after mixed sync", "after unusable sync", "after DB reload"} {
		switch phase {
		case "after mixed sync":
			feedMode.Store(2)
			if err := ds.SyncModelParamsFromURL(ctx); err != nil {
				t.Fatal(err)
			}
		case "after unusable sync":
			feedMode.Store(1)
			fallthrough
		case "after sync":
			if err := ds.SyncModelParamsFromURL(ctx); err != nil {
				t.Fatal(err)
			}
		case "after DB reload":
			// Also prove a cold lookup can still read the good row from SQLite.
			if _, err := ds.LoadModelParamsFromDB(ctx); err != nil {
				t.Fatal(err)
			}
		}
		for _, limit := range []*int{nil, schemas.Ptr(123)} {
			request := &schemas.BifrostResponsesRequest{
				Provider: customProvider,
				Model:    model,
				Input: []schemas.ResponsesMessage{{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")},
				}},
				Params: &schemas.ResponsesParameters{MaxOutputTokens: limit},
			}
			response, err := provider.Responses(schemas.NewBifrostContext(ctx, time.Time{}), schemas.Key{}, request)
			if err != nil || response == nil {
				t.Fatalf("%s: provider request failed: %+v", phase, err)
			}
			want := 32000
			if limit != nil {
				want = *limit
			}
			got := <-witness
			t.Logf("%s: omitted max_output_tokens=%t, upstream max_tokens=%d", phase, limit == nil, got)
			if got != want {
				t.Errorf("%s: upstream max_tokens = %d, want %d", phase, got, want)
			}
		}
	}
}
