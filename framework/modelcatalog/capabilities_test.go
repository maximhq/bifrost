package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
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
	"gorm.io/gorm"
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
			if feedMode.Load() == 3 {
				_, _ = w.Write([]byte(`{"custom-params-model":{"provider":"anthropic","max_output_tokens":28000},"custom-anthropic/custom-params-model":{"supported_endpoints":[],"server_tools":{}}}`))
				return
			}
			if feedMode.Load() == 4 {
				_, _ = w.Write([]byte(`{"custom-params-model":{"provider":"anthropic","max_output_tokens":30000},"custom-anthropic/custom-params-model":{"provider":"custom-anthropic","reasoning_budget":{},"model_parameters":[{}],"supported_endpoints":[null]}}`))
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
	for _, phase := range []string{"before sync", "after sync", "after mixed sync", "after unusable sync", "after empty collections sync", "after nested empty sync", "after DB reload"} {
		switch phase {
		case "after nested empty sync":
			feedMode.Store(4)
			if err := ds.SyncModelParamsFromURL(ctx); err != nil {
				t.Fatal(err)
			}
		case "after empty collections sync":
			feedMode.Store(3)
			if err := ds.SyncModelParamsFromURL(ctx); err != nil {
				t.Fatal(err)
			}
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

// Delegate to real SQLite except for the operation whose failure is under test.
type modelParamsFailureStore struct {
	configstore.ConfigStore
	readErr  error
	writeErr error
}

// GetModelParameters injects a read failure or delegates to the real database.
func (s *modelParamsFailureStore) GetModelParameters(ctx context.Context) ([]tables.TableModelParameters, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	return s.ConfigStore.GetModelParameters(ctx)
}

// UpsertModelParametersBatch injects a write failure or persists through SQLite.
func (s *modelParamsFailureStore) UpsertModelParametersBatch(ctx context.Context, rows []tables.TableModelParameters, tx ...*gorm.DB) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	return s.ConfigStore.UpsertModelParametersBatch(ctx, rows, tx...)
}

// TestModelParametersSyncDBFailurePreservesSnapshot checks failed syncs leave
// database rows, hot and cold capability lookups, and derived indexes unchanged.
func TestModelParametersSyncDBFailurePreservesSnapshot(t *testing.T) {
	for _, operation := range []string{"read", "write"} {
		t.Run(operation, func(t *testing.T) {
			ctx := t.Context()
			cs, err := configstore.NewConfigStore(ctx, &configstore.Config{
				Enabled: true, Type: configstore.ConfigStoreTypeSQLite,
				Config: &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
			}, bifrost.NewNoOpLogger())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cs.Close(context.Background()) })
			const model = "sync-failure-model"
			const original = `{"provider":"anthropic","max_output_tokens":32000,"supported_endpoints":["/v1/responses"],"supports_function_calling":true}`
			if err := cs.UpsertModelParametersBatch(ctx, []tables.TableModelParameters{{Model: model, Data: original}}); err != nil {
				t.Fatal(err)
			}
			feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"sync-failure-model":{"provider":"anthropic","max_output_tokens":16000,"supported_endpoints":["/v1/chat/completions"],"supports_function_calling":false}}`))
			}))
			defer feed.Close()
			wrapper := &modelParamsFailureStore{ConfigStore: cs}
			ds := datasheet.New(wrapper, nil, datasheet.Config{ModelParametersURL: feed.URL})
			mc := &ModelCatalog{datasheet: ds, capabilities: lrucache.New[*schemas.ModelCapabilities](capabilityCacheSize)}
			mc.loadCapabilities = mc.datasheetCapabilityLoader
			applied := 0
			ds.SetOnModelParametersApplied(func() { applied++; mc.capabilities.Flush() })
			if _, err := ds.LoadModelParamsFromDB(ctx); err != nil {
				t.Fatal(err)
			}
			warm := mc.GetModelCapabilities(schemas.Anthropic, model)
			if warm == nil || warm.MaxOutputTokens == nil || *warm.MaxOutputTokens != 32000 {
				t.Fatalf("unexpected seed: %+v", warm)
			}
			injected := errors.New("injected database failure")
			if operation == "read" {
				wrapper.readErr = injected
			} else {
				wrapper.writeErr = injected
			}
			err = ds.SyncModelParamsFromURL(ctx)
			wrapper.readErr, wrapper.writeErr = nil, nil
			if !errors.Is(err, injected) {
				t.Fatalf("sync error=%v, want injected database failure", err)
			}
			row, err := cs.GetModelParametersByModel(ctx, model)
			if err != nil || row == nil || row.Data != original {
				t.Errorf("failed sync changed stored row: %+v, %v", row, err)
			}
			for _, name := range []string{model, "anthropic/" + model} {
				caps := mc.GetModelCapabilities(schemas.Anthropic, name)
				if caps == nil || caps.MaxOutputTokens == nil || *caps.MaxOutputTokens != 32000 {
					t.Errorf("failed sync: capabilities for %s = %+v, want 32000", name, caps)
				}
			}
			if applied != 1 || !ds.IsRequestTypeSupported(model, schemas.ResponsesRequest) {
				t.Error("failed sync changed cache or indexes")
			}
			if err := ds.SyncModelParamsFromURL(ctx); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{model, "anthropic/" + model} {
				caps := mc.GetModelCapabilities(schemas.Anthropic, name)
				if caps == nil || caps.MaxOutputTokens == nil || *caps.MaxOutputTokens != 16000 {
					t.Errorf("retry: capabilities for %s = %+v, want 16000", name, caps)
				}
			}
			if applied != 2 || ds.IsRequestTypeSupported(model, schemas.ResponsesRequest) || !ds.IsRequestTypeSupported(model, schemas.ChatCompletionRequest) {
				t.Error("successful retry did not publish the new snapshot")
			}
		})
	}
}

// Pause one gateway after its DB read or during publication. Each gateway has
// its own Store, DB connection and lock manager, as in a shared-DB deployment.
type modelParamsPausedStore struct {
	configstore.ConfigStore
	pause func(context.Context) error
}

// GetModelParameters pauses after capturing the database snapshot to expose
// overlapping writes from another gateway.
func (s *modelParamsPausedStore) GetModelParameters(ctx context.Context) ([]tables.TableModelParameters, error) {
	rows, err := s.ConfigStore.GetModelParameters(ctx)
	if err == nil && s.pause != nil {
		err = s.pause(ctx)
	}
	return rows, err
}

// TestModelParametersSyncSharedDatabase checks each sync entry point holds the
// shared lease through snapshot publication, including after its initial expiry.
func TestModelParametersSyncSharedDatabase(t *testing.T) {
	for _, entry := range []string{"startup", "periodic", "manual"} {
		for _, pauseAt := range []string{"snapshot", "publication"} {
			t.Run(entry+"/"+pauseAt, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				logger := bifrost.NewNoOpLogger()
				cfg := &configstore.Config{Enabled: true, Type: configstore.ConfigStoreTypeSQLite,
					Config: &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "shared.db")}}
				openStore := func() configstore.ConfigStore {
					cs, err := configstore.NewConfigStore(ctx, cfg, logger)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = cs.Close(context.Background()) })
					return cs
				}
				csA, csB := openStore(), openStore()
				paused, release := make(chan struct{}), make(chan struct{})
				var pauseOnce sync.Once
				pause := func(ctx context.Context) error {
					pauseOnce.Do(func() { close(paused) })
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				wrapper := &modelParamsPausedStore{ConfigStore: csA}
				if pauseAt == "snapshot" {
					wrapper.pause = pause
				}
				feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/pricing" {
						_, _ = w.Write([]byte(`{}`))
						return
					}
					_, _ = fmt.Fprintf(w, `{"custom-anthropic/%s":{"provider":"custom-anthropic","max_output_tokens":32000}}`, r.URL.Path[1:])
				}))
				defer feed.Close()
				published := make(chan *ModelCatalog, 2)
				newCatalog := func(cs configstore.ConfigStore, model string) *ModelCatalog {
					mc := &ModelCatalog{configStore: cs, logger: logger,
						distributedLockManager: configstore.NewDistributedLockManager(cs, logger,
							configstore.WithDefaultTTL(150*time.Millisecond), configstore.WithRetryInterval(10*time.Millisecond)),
						datasheet:    datasheet.New(cs, logger, datasheet.Config{URL: feed.URL + "/pricing", ModelParametersURL: feed.URL + "/" + model}),
						capabilities: lrucache.New[*schemas.ModelCapabilities](capabilityCacheSize)}
					mc.loadCapabilities = mc.datasheetCapabilityLoader
					mc.datasheet.SetOnModelParametersApplied(func() {
						mc.capabilities.Flush()
						if model == "first" && pauseAt == "publication" {
							_ = pause(ctx)
						}
						published <- mc
					})
					return mc
				}
				first, second := newCatalog(wrapper, "first"), newCatalog(csB, "second")
				firstDone, secondDone := make(chan error, 1), make(chan error, 1)
				go func() {
					switch entry {
					case "startup":
						_, syncErr := first.runParamsSync(ctx)
						firstDone <- syncErr
					case "periodic":
						first.syncTick(ctx)
						firstDone <- nil
					case "manual":
						firstDone <- first.ForceReloadPricing(ctx)
					}
				}()
				select {
				case <-paused:
				case <-ctx.Done():
					t.Fatal("first sync did not reach pause")
				}
				go func() { secondDone <- second.ForceReloadPricing(ctx) }()
				var secondErr error
				var completedEarly bool
				select {
				case secondErr = <-secondDone:
					completedEarly = true
				// Hold longer than the lease: a lock without renewal also fails.
				case <-time.After(400 * time.Millisecond):
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				close(release)
				if err := <-firstDone; err != nil {
					t.Fatal(err)
				}
				if !completedEarly {
					secondErr = <-secondDone
				}
				if secondErr != nil {
					t.Fatal(secondErr)
				}
				if completedEarly {
					t.Error("second gateway completed while the first still held its snapshot/publication")
				}
				var last *ModelCatalog
				for range 2 {
					select {
					case last = <-published:
					case <-ctx.Done():
						t.Fatal("sync did not publish")
					}
				}
				for _, model := range []string{"first", "second"} {
					row, err := csB.GetModelParametersByModel(ctx, "custom-anthropic/"+model)
					if err != nil || row == nil {
						t.Fatalf("missing committed row %s: %v", model, err)
					}
					caps := last.GetModelCapabilities("custom-anthropic", model)
					if caps == nil || caps.MaxOutputTokens == nil || *caps.MaxOutputTokens != 32000 {
						t.Errorf("last published snapshot lost stored model %s: %+v", model, caps)
					}
				}
			})
		}
	}
}

// TestModelParametersSyncCancelledLockWait checks a cancelled waiter does not
// update parameters or notify peers, and can retry after the holder releases.
func TestModelParametersSyncCancelledLockWait(t *testing.T) {
	logger := bifrost.NewNoOpLogger()
	cs, err := configstore.NewConfigStore(t.Context(), &configstore.Config{
		Enabled: true, Type: configstore.ConfigStoreTypeSQLite,
		Config: &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close(context.Background())
	var fetches, notifications atomic.Int32
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pricing" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		fetches.Add(1)
		_, _ = w.Write([]byte(`{"custom-anthropic/new-model":{"provider":"custom-anthropic","max_output_tokens":32000}}`))
	}))
	defer feed.Close()
	manager := configstore.NewDistributedLockManager(cs, logger)
	_, release, err := manager.Acquire(t.Context(), "model_catalog_params_sync")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	mc := &ModelCatalog{configStore: cs, logger: logger, distributedLockManager: manager,
		datasheet:     datasheet.New(cs, logger, datasheet.Config{URL: feed.URL + "/pricing", ModelParametersURL: feed.URL + "/params"}),
		afterSyncHook: func(context.Context) { notifications.Add(1) },
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := mc.ForceReloadPricing(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("cancelled lock wait returned %v, want deadline exceeded", err)
	}
	rows, err := cs.GetModelParameters(t.Context())
	if err != nil || len(rows) != 0 || fetches.Load() != 0 || notifications.Load() != 0 {
		t.Errorf("cancelled waiter touched parameters or notified peers: rows=%v err=%v fetches=%d notifications=%d", rows, err, fetches.Load(), notifications.Load())
	}
	release()
	if err := mc.ForceReloadPricing(t.Context()); err != nil {
		t.Fatalf("retry after peer release: %v", err)
	}
	if fetches.Load() != 1 || notifications.Load() != 1 {
		t.Errorf("retry did not complete once: fetches=%d notifications=%d", fetches.Load(), notifications.Load())
	}
}

// Observe when the second startup actually starts waiting on the shared lock.
type modelParamsStartupStore struct {
	configstore.ConfigStore
	waiting chan struct{}
	once    sync.Once
}

// TryAcquireLock signals the first parameter-lock attempt during startup.
func (s *modelParamsStartupStore) TryAcquireLock(ctx context.Context, lock *tables.TableDistributedLock) (bool, error) {
	if lock.LockKey == "model_catalog_params_sync" {
		s.once.Do(func() { close(s.waiting) })
	}
	return s.ConfigStore.TryAcquireLock(ctx, lock)
}

// Exercise Init itself with two empty-DB gateways. A normal URL fetch may take
// up to 45s, so another startup must not exhaust the default 10s lock wait.
func TestModelParametersStartupWaitsForSlowPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
	defer cancel()
	logger := bifrost.NewNoOpLogger()
	cfg := &configstore.Config{Enabled: true, Type: configstore.ConfigStoreTypeSQLite,
		Config: &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "shared.db")}}
	openStore := func() configstore.ConfigStore {
		cs, err := configstore.NewConfigStore(ctx, cfg, logger)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cs.Close(context.Background()) })
		return cs
	}
	csA, csB := openStore(), openStore()
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pricing" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if r.URL.Path == "/first" {
			enterOnce.Do(func() { close(entered) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = fmt.Fprintf(w, `{"custom-anthropic/%s":{"provider":"custom-anthropic","max_output_tokens":32000}}`, r.URL.Path[1:])
	}))
	defer feed.Close()
	defer releaseOnce.Do(func() { close(release) })
	type result struct {
		catalog *ModelCatalog
		err     error
	}
	start := func(cs configstore.ConfigStore, model string) (*result, <-chan struct{}) {
		out, done := &result{}, make(chan struct{})
		go func() {
			out.catalog, out.err = Init(ctx, &Config{PricingURL: schemas.Ptr(feed.URL + "/pricing"),
				ModelParametersURL: schemas.Ptr(feed.URL + "/" + model), MCPLibrarySyncInterval: schemas.Ptr(int64(0))}, cs, logger)
			close(done)
		}()
		t.Cleanup(func() {
			<-done
			if out.catalog != nil {
				_ = out.catalog.Cleanup()
			}
		})
		return out, done
	}
	first, firstDone := start(csA, "first")
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first startup did not fetch")
	}
	peer := &modelParamsStartupStore{ConfigStore: csB, waiting: make(chan struct{})}
	second, secondDone := start(peer, "second")
	select {
	case <-peer.waiting:
	case <-ctx.Done():
		t.Fatal("second startup did not attempt the lock")
	}
	// Real default timings, including the production Init lock configuration.
	select {
	case <-secondDone:
		t.Errorf("startup ended while peer was still fetching: %v", second.err)
	case <-time.After(12 * time.Second):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	releaseOnce.Do(func() { close(release) })
	for _, done := range []<-chan struct{}{firstDone, secondDone} {
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("startup did not finish after peer release")
		}
	}
	if first.err != nil || second.err != nil {
		t.Fatalf("startup errors: first=%v second=%v", first.err, second.err)
	}
	for _, model := range []string{"first", "second"} {
		caps := second.catalog.GetModelCapabilities("custom-anthropic", model)
		if caps == nil || caps.MaxOutputTokens == nil || *caps.MaxOutputTokens != 32000 {
			t.Errorf("second gateway lost %s after waiting: %+v", model, caps)
		}
	}
}

// Startup cancellation must interrupt the longer wait without releasing a peer's
// lease or leaving a failed catalog that prevents a later startup from succeeding.
func TestModelParametersStartupCancelledLockWait(t *testing.T) {
	logger := bifrost.NewNoOpLogger()
	cs, err := configstore.NewConfigStore(t.Context(), &configstore.Config{
		Enabled: true, Type: configstore.ConfigStoreTypeSQLite,
		Config: &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close(context.Background())
	manager := configstore.NewDistributedLockManager(cs, logger)
	_, release, err := manager.Acquire(t.Context(), "model_catalog_params_sync")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pricing" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{"custom-anthropic/retry":{"provider":"custom-anthropic","max_output_tokens":32000}}`))
	}))
	defer feed.Close()
	cfg := &Config{PricingURL: schemas.Ptr(feed.URL + "/pricing"), ModelParametersURL: schemas.Ptr(feed.URL + "/params"), MCPLibrarySyncInterval: schemas.Ptr(int64(0))}
	peer := &modelParamsStartupStore{ConfigStore: cs, waiting: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		mc, err := Init(ctx, cfg, peer, logger)
		if mc != nil {
			_ = mc.Cleanup()
		}
		done <- err
	}()
	select {
	case <-peer.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("startup did not wait on the lock")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled startup: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("startup cancellation did not interrupt the lock wait")
	}
	lock, err := cs.GetLock(t.Context(), "model_catalog_params_sync")
	if err != nil || lock == nil {
		t.Fatalf("cancelled waiter released the peer lock: lock=%v err=%v", lock, err)
	}
	release()
	mc, err := Init(t.Context(), cfg, cs, logger)
	if err != nil {
		t.Fatalf("startup after peer release: %v", err)
	}
	defer mc.Cleanup()
	caps := mc.GetModelCapabilities("custom-anthropic", "retry")
	if caps == nil || caps.MaxOutputTokens == nil || *caps.MaxOutputTokens != 32000 {
		t.Fatalf("retry did not load capabilities: %+v", caps)
	}
}
