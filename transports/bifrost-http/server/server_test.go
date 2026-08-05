package server

import (
	"context"
	"reflect"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/bifrost-http/handlers"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

// reloadVirtualKeyConfigStore provides the persistence calls used by ReloadVirtualKey.
type reloadVirtualKeyConfigStore struct {
	configstore.ConfigStore
	vk *configstoreTables.TableVirtualKey
}

// RetryOnNotFound executes the supplied lookup once for deterministic tests.
func (s *reloadVirtualKeyConfigStore) RetryOnNotFound(ctx context.Context, fn func(context.Context) (any, error), _ int, _ time.Duration) (any, error) {
	return fn(ctx)
}

// GetVirtualKey returns the configured persisted virtual key.
func (s *reloadVirtualKeyConfigStore) GetVirtualKey(context.Context, string) (*configstoreTables.TableVirtualKey, error) {
	return s.vk, nil
}

// GetModelConfigsByScopeAndScopeIDs returns no scoped model configs.
func (s *reloadVirtualKeyConfigStore) GetModelConfigsByScopeAndScopeIDs(context.Context, string, []string) ([]configstoreTables.TableModelConfig, error) {
	return nil, nil
}

// reloadVirtualKeyGovernanceStore counts snapshot calls while delegating all
// mutation and direct lookup behavior to a real in-memory store.
type reloadVirtualKeyGovernanceStore struct {
	governance.GovernanceStore
	governanceDataCalls int
}

// GetGovernanceData records any accidental full-snapshot lookup.
func (s *reloadVirtualKeyGovernanceStore) GetGovernanceData(context.Context) *governance.GovernanceData {
	s.governanceDataCalls++
	return nil
}

// reloadVirtualKeyPlugin exposes the test governance store.
type reloadVirtualKeyPlugin struct {
	governance.BaseGovernancePlugin
	store governance.GovernanceStore
}

// GetName returns the standard governance plugin name.
func (p *reloadVirtualKeyPlugin) GetName() string { return governance.PluginName }

// GetGovernanceStore returns the test governance store.
func (p *reloadVirtualKeyPlugin) GetGovernanceStore() governance.GovernanceStore { return p.store }

// reloadVirtualKeyToolManager provides an empty MCP tool set.
type reloadVirtualKeyToolManager struct{}

// GetAvailableMCPTools returns no tools.
func (reloadVirtualKeyToolManager) GetAvailableMCPTools(context.Context) []schemas.ChatTool {
	return nil
}

// ExecuteChatMCPTool is unused by these tests.
func (reloadVirtualKeyToolManager) ExecuteChatMCPTool(context.Context, *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	return nil, nil
}

// ExecuteResponsesMCPTool is unused by these tests.
func (reloadVirtualKeyToolManager) ExecuteResponsesMCPTool(context.Context, *schemas.ResponsesToolMessage) (*schemas.ResponsesMessage, *schemas.BifrostError) {
	return nil, nil
}

// hasVKMCPServer reports whether the handler still caches a server for a secret.
func hasVKMCPServer(handler *handlers.MCPServerHandler, value string) bool {
	servers := reflect.ValueOf(handler).Elem().FieldByName("vkMCPServers")
	return servers.MapIndex(reflect.ValueOf(value)).IsValid()
}

// TestReloadVirtualKeyDeletesOnlyRotatedOldMCPServer pins the old-secret cleanup
// semantics and verifies the direct ID lookup avoids GetGovernanceData.
func TestReloadVirtualKeyDeletesOnlyRotatedOldMCPServer(t *testing.T) {
	handlers.SetLogger(noopTestLogger{})
	tests := []struct {
		name          string
		oldValue      string
		newValue      string
		seedOldVK     bool
		wantOldServer bool
	}{
		{name: "rotated set secret deletes old server", oldValue: "sk-bf-old", newValue: "sk-bf-new", seedOldVK: true, wantOldServer: false},
		{name: "unchanged secret keeps old server", oldValue: "sk-bf-same", newValue: "sk-bf-same", seedOldVK: true, wantOldServer: true},
		{name: "unset old secret keeps old server", oldValue: "", newValue: "sk-bf-new", seedOldVK: true, wantOldServer: true},
		{name: "missing old virtual key keeps old server", oldValue: "sk-bf-old", newValue: "sk-bf-new", seedOldVK: false, wantOldServer: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			baseStore, err := governance.NewLocalGovernanceStore(ctx, governance.NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
			if err != nil {
				t.Fatalf("create governance store: %v", err)
			}
			oldVK := &configstoreTables.TableVirtualKey{ID: "vk-id", Name: "old", Value: *schemas.NewSecretVar(tt.oldValue)}
			if tt.seedOldVK {
				baseStore.CreateVirtualKeyInMemory(ctx, oldVK)
			}
			store := &reloadVirtualKeyGovernanceStore{GovernanceStore: baseStore}
			plugin := &reloadVirtualKeyPlugin{store: store}
			persistedVK := &configstoreTables.TableVirtualKey{ID: "vk-id", Name: "new", Value: *schemas.NewSecretVar(tt.newValue)}
			config := &lib.Config{
				ConfigStore:  &reloadVirtualKeyConfigStore{vk: persistedVK},
				ClientConfig: &configstore.ClientConfig{},
			}
			plugins := []schemas.BasePlugin{plugin}
			config.BasePlugins.Store(&plugins)
			mcpHandler, err := handlers.NewMCPServerHandler(ctx, config, reloadVirtualKeyToolManager{}, nil, store)
			if err != nil {
				t.Fatalf("create MCP handler: %v", err)
			}
			mcpHandler.SyncVKMCPServer(oldVK)
			server := &BifrostHTTPServer{Ctx: schemas.NewBifrostContext(ctx, schemas.NoDeadline), Config: config, MCPServerHandler: mcpHandler}

			if _, err := server.ReloadVirtualKey(ctx, persistedVK.ID); err != nil {
				t.Fatalf("ReloadVirtualKey returned unexpected error: %v", err)
			}
			if store.governanceDataCalls != 0 {
				t.Fatalf("GetGovernanceData called %d times, want 0", store.governanceDataCalls)
			}

			oldServerExists := hasVKMCPServer(mcpHandler, tt.oldValue)
			if tt.wantOldServer && !oldServerExists {
				t.Fatal("old MCP server was deleted")
			}
			if !tt.wantOldServer && oldServerExists {
				t.Fatal("old MCP server still exists after secret rotation")
			}
		})
	}
}

// TestConfig is a sample config struct for testing
type TestConfig struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Count   int    `json:"count"`
}

type updateStatusOnlyConfigStore struct {
	configstore.ConfigStore
	calls []schemas.KeyStatus
}

type noopTestLogger struct{}

func (noopTestLogger) Debug(string, ...any)                   {}
func (noopTestLogger) Info(string, ...any)                    {}
func (noopTestLogger) Warn(string, ...any)                    {}
func (noopTestLogger) Error(string, ...any)                   {}
func (noopTestLogger) Fatal(string, ...any)                   {}
func (noopTestLogger) SetLevel(schemas.LogLevel)              {}
func (noopTestLogger) SetOutputType(schemas.LoggerOutputType) {}
func (noopTestLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func (s *updateStatusOnlyConfigStore) UpdateStatus(ctx context.Context, provider schemas.ModelProvider, keyID string, status, errorMsg string) error {
	s.calls = append(s.calls, schemas.KeyStatus{
		Provider: provider,
		KeyID:    keyID,
		Status:   schemas.KeyStatusType(status),
		Error:    &schemas.BifrostError{Error: &schemas.ErrorField{Message: errorMsg}},
	})
	return nil
}

func TestUpdateKeyStatus_KeylessProviderUpdatesProviderStatusInMemory(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	store := &updateStatusOnlyConfigStore{}
	server := &BifrostHTTPServer{
		Config: &lib.Config{
			ConfigStore: store,
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				"mock-openai": {
					CustomProviderConfig: &schemas.CustomProviderConfig{IsKeyLess: true},
					Status:               "unknown",
				},
			},
		},
	}

	server.updateKeyStatus(context.Background(), []schemas.KeyStatus{{
		Provider: "mock-openai",
		KeyID:    "",
		Status:   schemas.KeyStatusListModelsFailed,
		Error:    &schemas.BifrostError{Error: &schemas.ErrorField{Message: "preview missing model"}},
	}})

	provider := server.Config.Providers["mock-openai"]
	if provider.Status != string(schemas.KeyStatusListModelsFailed) {
		t.Fatalf("expected provider status %q, got %q", schemas.KeyStatusListModelsFailed, provider.Status)
	}
	if provider.Description != "preview missing model" {
		t.Fatalf("expected provider description to be updated, got %q", provider.Description)
	}
	if len(store.calls) != 1 {
		t.Fatalf("expected one status update call, got %d", len(store.calls))
	}
	if store.calls[0].Provider != "mock-openai" || store.calls[0].KeyID != "" {
		t.Fatalf("expected provider-level status update, got provider=%q keyID=%q", store.calls[0].Provider, store.calls[0].KeyID)
	}
}

func TestUpdateKeyStatus_EmptyKeyIDDoesNotOverwriteKeyedProviderStatus(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	store := &updateStatusOnlyConfigStore{}
	server := &BifrostHTTPServer{
		Config: &lib.Config{
			ConfigStore: store,
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				"openai": {
					Keys:   []schemas.Key{{ID: "key-1"}},
					Status: "healthy",
				},
			},
		},
	}

	server.updateKeyStatus(context.Background(), []schemas.KeyStatus{{
		Provider: "openai",
		KeyID:    "",
		Status:   schemas.KeyStatusListModelsFailed,
		Error:    &schemas.BifrostError{Error: &schemas.ErrorField{Message: "malformed status"}},
	}})

	provider := server.Config.Providers["openai"]
	if provider.Status != "healthy" {
		t.Fatalf("expected keyed provider status to remain unchanged, got %q", provider.Status)
	}
	if provider.Description != "" {
		t.Fatalf("expected keyed provider description to remain unchanged, got %q", provider.Description)
	}
	if len(store.calls) != 1 {
		t.Fatalf("expected one status update call, got %d", len(store.calls))
	}
	if store.calls[0].Provider != "openai" || store.calls[0].KeyID != "" {
		t.Fatalf("expected DB status update to retain empty key ID, got provider=%q keyID=%q", store.calls[0].Provider, store.calls[0].KeyID)
	}
}

// TestUpdateKeyStatus_SkipsWriteWhenUnchanged pins the write-gating that makes
// the background refresher affordable: ConfigStore.UpdateStatus is an
// unconditional SQL UPDATE, so a status that has not moved must not reach it.
// Without this gate a 30-key deployment refreshing on an interval writes 30
// no-op UPDATEs per pass, per node, forever.
func TestUpdateKeyStatus_SkipsWriteWhenUnchanged(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	store := &updateStatusOnlyConfigStore{}
	server := &BifrostHTTPServer{
		Config: &lib.Config{
			ConfigStore: store,
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				"openai": {
					Keys: []schemas.Key{{
						ID:          "key-1",
						Status:      schemas.KeyStatusListModelsFailed,
						Description: "upstream 503",
					}},
				},
			},
		},
	}

	unchanged := []schemas.KeyStatus{{
		Provider: "openai",
		KeyID:    "key-1",
		Status:   schemas.KeyStatusListModelsFailed,
		Error:    &schemas.BifrostError{Error: &schemas.ErrorField{Message: "upstream 503"}},
	}}

	server.updateKeyStatus(context.Background(), unchanged)
	server.updateKeyStatus(context.Background(), unchanged)

	if len(store.calls) != 0 {
		t.Fatalf("expected no status update calls for an unchanged status, got %d", len(store.calls))
	}

	// A genuine change must still get through, otherwise the gate would hide
	// a key recovering or breaking.
	server.updateKeyStatus(context.Background(), []schemas.KeyStatus{{
		Provider: "openai",
		KeyID:    "key-1",
		Status:   schemas.KeyStatusSuccess,
	}})

	if len(store.calls) != 1 {
		t.Fatalf("expected one status update call after the status changed, got %d", len(store.calls))
	}
	if got := server.Config.Providers["openai"].Keys[0].Status; got != schemas.KeyStatusSuccess {
		t.Fatalf("expected in-memory status to become success, got %q", got)
	}
	if got := server.Config.Providers["openai"].Keys[0].Description; got != "" {
		t.Fatalf("expected in-memory description to be cleared, got %q", got)
	}
}

// TestUpdateKeyStatus_WritesWhenKeyMissingFromMemory guards the fallback: with
// nothing to compare against, the gate must let the write through rather than
// silently swallowing it.
func TestUpdateKeyStatus_WritesWhenKeyMissingFromMemory(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	store := &updateStatusOnlyConfigStore{}
	server := &BifrostHTTPServer{
		Config: &lib.Config{
			ConfigStore: store,
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				"openai": {Keys: []schemas.Key{{ID: "key-1"}}},
			},
		},
	}

	server.updateKeyStatus(context.Background(), []schemas.KeyStatus{{
		Provider: "openai",
		KeyID:    "key-unknown",
		Status:   schemas.KeyStatusSuccess,
	}})

	if len(store.calls) != 1 {
		t.Fatalf("expected the write to proceed for an unknown key, got %d calls", len(store.calls))
	}
}

// TestRefreshAllLiveModels_NilClientIsNoop covers the boot-order guard: the
// refresher can fire before or after the client exists, and dereferencing a
// nil client would panic the background goroutine.
func TestRefreshAllLiveModels_NilClientIsNoop(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	server := &BifrostHTTPServer{
		Config: &lib.Config{
			ModelCatalog: modelcatalog.NewTestCatalog(nil),
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				"custom-provider": {Keys: []schemas.Key{{ID: "key-1"}}},
			},
		},
	}

	// Reaching the end without panicking is the assertion: s.Client is nil, so
	// any scheduled fetch would dereference it.
	server.RefreshAllLiveModels(context.Background())

	if models := server.Config.ModelCatalog.GetModelsForProvider("custom-provider"); len(models) != 0 {
		t.Fatalf("expected no live models to be written, got %v", models)
	}
}

// TestRefreshAllLiveModels_SkipsProvidersWithNoEnabledKeys cross-checks that
// the per-provider skip rules still apply when the fan-out is driven by the
// background pass rather than the bootstrap loop.
func TestRefreshAllLiveModels_SkipsProvidersWithNoEnabledKeys(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	server := &BifrostHTTPServer{
		Config: &lib.Config{
			ModelCatalog: modelcatalog.NewTestCatalog(nil),
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				"all-disabled": {Keys: []schemas.Key{
					{ID: "key-1", Enabled: schemas.Ptr(false)},
					{ID: "key-2", Enabled: schemas.Ptr(false)},
				}},
				"no-keys": {},
			},
		},
	}
	// Non-nil sentinel would be required for a fetch to be attempted; leaving
	// Client nil means any scheduled fetch panics and fails this test.
	server.RefreshAllLiveModels(context.Background())
}

// TestStartLiveModelRefresher_ZeroIntervalDisabled pins the opt-out: a
// non-positive interval must spawn nothing and still return a usable stop func.
func TestStartLiveModelRefresher_ZeroIntervalDisabled(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	server := &BifrostHTTPServer{}

	for _, interval := range []time.Duration{0, -time.Second} {
		before := runtime.NumGoroutine()
		stop := server.startLiveModelRefresher(context.Background(), interval)
		if stop == nil {
			t.Fatalf("interval %v: expected a non-nil stop func", interval)
		}
		if after := runtime.NumGoroutine(); after > before {
			t.Fatalf("interval %v: expected no goroutine to be spawned, went from %d to %d", interval, before, after)
		}
		stop() // must not panic
	}
}

// TestStartLiveModelRefresher_StopsOnContextCancel makes sure the refresher
// does not outlive the server: a leaked ticker would keep calling every
// upstream forever after shutdown.
func TestStartLiveModelRefresher_StopsOnContextCancel(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	server := &BifrostHTTPServer{}

	baseline := runtime.NumGoroutine()
	stop := server.startLiveModelRefresher(context.Background(), 10*time.Millisecond)

	// Let it turn over a few cycles so we are stopping a running loop, not one
	// that never started.
	time.Sleep(50 * time.Millisecond)

	stop()

	// Poll rather than sleeping a fixed amount: goroutine teardown is not
	// synchronous with cancel, and a generous window beats a flaky one.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("refresher goroutine still running 2s after stop: baseline %d, now %d", baseline, runtime.NumGoroutine())
}

// TestJitteredInterval_StaysWithinBounds pins the jitter window. Pods in a
// deployment boot together, so this is what keeps their refresh cycles from
// converging into a synchronized burst against every upstream.
func TestJitteredInterval_StaysWithinBounds(t *testing.T) {
	base := time.Hour
	lower := time.Duration(float64(base) * (1 - liveRefreshJitterFraction))
	upper := time.Duration(float64(base) * (1 + liveRefreshJitterFraction))

	seen := make(map[time.Duration]struct{})
	for range 1000 {
		got := jitteredInterval(base)
		if got < lower || got > upper {
			t.Fatalf("jitteredInterval(%v) = %v, want within [%v, %v]", base, got, lower, upper)
		}
		seen[got] = struct{}{}
	}
	// A constant would satisfy the bounds check above but defeat the purpose.
	if len(seen) < 2 {
		t.Fatalf("expected jittered intervals to vary, got %d distinct value(s)", len(seen))
	}
}

// TestBeginKeyRefresh_CoordinatesAtKeyGranularity covers the per-key in-flight
// guard: duplicate key refreshes collapse, while distinct keys and providers
// remain independent.
func TestBeginKeyRefresh_CoordinatesAtKeyGranularity(t *testing.T) {
	server := &BifrostHTTPServer{}

	release, ok := server.beginKeyRefresh("openai", "key-1")
	if !ok {
		t.Fatal("expected the first claim to succeed")
	}
	if _, ok := server.beginKeyRefresh("openai", "key-1"); ok {
		t.Fatal("expected a concurrent claim for the same key to be rejected")
	}

	otherKeyRelease, ok := server.beginKeyRefresh("openai", "key-2")
	if !ok {
		t.Fatal("expected a different key for the same provider to succeed")
	}
	otherKeyRelease()

	otherProviderRelease, ok := server.beginKeyRefresh("anthropic", "key-1")
	if !ok {
		t.Fatal("expected a claim for a different provider to succeed")
	}
	otherProviderRelease()

	release()
	release, ok = server.beginKeyRefresh("openai", "key-1")
	if !ok {
		t.Fatal("expected the key to be claimable again after release")
	}
	release()
}

func TestBeginAllKeysRefresh_IsExclusiveWithinProvider(t *testing.T) {
	server := &BifrostHTTPServer{}

	keyRelease, ok := server.beginKeyRefresh("openai", "key-1")
	if !ok {
		t.Fatal("expected the key claim to succeed")
	}
	if _, ok := server.beginAllKeysRefresh("openai"); ok {
		t.Fatal("expected all-keys refresh to conflict with an active key refresh")
	}
	keyRelease()

	allRelease, ok := server.beginAllKeysRefresh("openai")
	if !ok {
		t.Fatal("expected all-keys refresh to succeed after the key refresh completed")
	}
	if _, ok := server.beginAllKeysRefresh("openai"); ok {
		t.Fatal("expected a second all-keys refresh to be rejected")
	}
	if _, ok := server.beginKeyRefresh("openai", "key-2"); ok {
		t.Fatal("expected key refresh to conflict with an active all-keys refresh")
	}

	otherProviderRelease, ok := server.beginKeyRefresh("anthropic", "key-1")
	if !ok {
		t.Fatal("expected another provider to remain independent")
	}
	otherProviderRelease()

	allRelease()
	allRelease, ok = server.beginAllKeysRefresh("openai")
	if !ok {
		t.Fatal("expected all-keys refresh to be claimable again after release")
	}
	allRelease()
}

func TestKeyEnabled(t *testing.T) {
	tests := []struct {
		name string
		key  schemas.Key
		want bool
	}{
		{"nil Enabled defaults to enabled", schemas.Key{}, true},
		{"explicit true is enabled", schemas.Key{Enabled: schemas.Ptr(true)}, true},
		{"explicit false is disabled", schemas.Key{Enabled: schemas.Ptr(false)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := keyEnabled(tt.key); got != tt.want {
				t.Fatalf("keyEnabled(%+v) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

// TestRefreshLiveModelsForProvider_AllKeysDisabled cross-checks the "0
// enabled keys" case for issue #5037: discovery must skip scheduling any
// fetch (and therefore never touch s.Client, left nil here) rather than
// attempting doomed-to-fail per-key calls.
func TestRefreshLiveModelsForProvider_AllKeysDisabled(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	server := &BifrostHTTPServer{
		Config: &lib.Config{
			ModelCatalog: modelcatalog.NewTestCatalog(nil),
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				"custom-provider": {},
			},
		},
	}

	keys := []schemas.Key{
		{ID: "key-1", Enabled: schemas.Ptr(false)},
		{ID: "key-2", Enabled: schemas.Ptr(false)},
	}

	// Must return without panicking: s.Client is nil, so any scheduled fetch
	// would panic on the first ListModelsRequest call. Reaching the end of
	// this test proves no fetch was scheduled for either disabled key.
	server.RefreshLiveModelsForProvider(context.Background(), "custom-provider", keys)

	if models := server.Config.ModelCatalog.GetModelsForProvider("custom-provider"); len(models) != 0 {
		t.Fatalf("expected no live models to be written when all keys are disabled, got %v", models)
	}
}

// TestOnKeyAdded_DisabledKeySkipsFetch cross-checks that adding a key that
// is already disabled never schedules a discovery fetch for it.
func TestOnKeyAdded_DisabledKeySkipsFetch(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	disabledKey := schemas.Key{ID: "key-1", Enabled: schemas.Ptr(false)}
	server := &BifrostHTTPServer{
		Config: &lib.Config{
			ModelCatalog: modelcatalog.NewTestCatalog(nil),
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				"custom-provider": {Keys: []schemas.Key{disabledKey}},
			},
		},
	}

	// s.Client is left nil: if the disabled-key guard regresses, the fetch
	// goroutine would dereference it and panic, failing this test.
	if err := server.OnKeyAdded(context.Background(), "custom-provider", disabledKey); err != nil {
		t.Fatalf("OnKeyAdded returned unexpected error: %v", err)
	}

	if models := server.Config.ModelCatalog.GetModelsForProvider("custom-provider"); len(models) != 0 {
		t.Fatalf("expected no live models to be written for a disabled key, got %v", models)
	}
}

// TestOnKeyUpdated_DisabledKeySkipsFetchButInvalidatesCache cross-checks
// that toggling a key to disabled still evicts its stale cached models
// (InvalidateLive) even though the refetch is correctly skipped.
func TestOnKeyUpdated_DisabledKeySkipsFetchButInvalidatesCache(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	catalog := modelcatalog.NewTestCatalog(nil)
	catalog.UpsertLive("custom-provider", "key-1", false, []string{"custom-provider/some-model"})

	disabledKey := schemas.Key{ID: "key-1", Enabled: schemas.Ptr(false)}
	server := &BifrostHTTPServer{
		Config: &lib.Config{
			ModelCatalog: catalog,
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				"custom-provider": {Keys: []schemas.Key{disabledKey}},
			},
		},
	}

	if models := server.Config.ModelCatalog.GetModelsForProvider("custom-provider"); len(models) == 0 {
		t.Fatalf("expected seeded live model before update, got none")
	}

	// s.Client is left nil: if the disabled-key guard regresses, the fetch
	// goroutine would dereference it and panic, failing this test.
	if err := server.OnKeyUpdated(context.Background(), "custom-provider", disabledKey); err != nil {
		t.Fatalf("OnKeyUpdated returned unexpected error: %v", err)
	}

	if models := server.Config.ModelCatalog.GetModelsForProvider("custom-provider"); len(models) != 0 {
		t.Fatalf("expected InvalidateLive to drop the disabled key's cached models, got %v", models)
	}
}

// reloadProviderConfigStore is the minimum ConfigStore ReloadProvider needs:
// it only ever calls GetProvider before touching the model catalog.
type reloadProviderConfigStore struct {
	configstore.ConfigStore
	provider *configstoreTables.TableProvider
}

func (s *reloadProviderConfigStore) GetProvider(context.Context, schemas.ModelProvider) (*configstoreTables.TableProvider, error) {
	return s.provider, nil
}

// newReloadProviderServer builds a BifrostHTTPServer wired for ReloadProvider
// with model discovery guaranteed to write nothing.
//
// Disabling list_models via AllowedRequests makes FetchAndStoreLiveForKey
// return before it issues a request, which is the same observable outcome as
// the transient failures this suite is about (upstream 5xx, rate limit,
// timeout, refused connection): that function writes to the catalog only on a
// successful response, so every failure mode is indistinguishable from here.
// It also lets Client stay a bare non-nil pointer, so any regression that does
// attempt a fetch panics instead of silently reaching the network.
// The custom provider config is set on both the stored row and the in-memory
// config on purpose: ReloadProvider reads the keyless flag off the row it
// loads from the config store, while the isKeylessProvider helper that
// RefreshLiveModelsForProvider and the OnKey* handlers use reads the in-memory
// copy. A fixture that populated only one would exercise a state the running
// server never has.
func newReloadProviderServer(catalog *modelcatalog.ModelCatalog, provider schemas.ModelProvider, keys []schemas.Key, keyless bool) *BifrostHTTPServer {
	customProviderConfig := &schemas.CustomProviderConfig{
		IsKeyLess:       keyless,
		AllowedRequests: &schemas.AllowedRequests{ListModels: false},
	}
	return &BifrostHTTPServer{
		Ctx:    schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		Client: &bifrost.Bifrost{},
		Config: &lib.Config{
			ConfigStore: &reloadProviderConfigStore{provider: &configstoreTables.TableProvider{
				Name:                 string(provider),
				CustomProviderConfig: customProviderConfig,
			}},
			ModelCatalog: catalog,
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				provider: {
					Keys:                 keys,
					CustomProviderConfig: customProviderConfig,
				},
			},
		},
	}
}

// TestReloadProvider_FailedRefetchKeepsPreviousCatalog is the regression guard
// for issue #5554: ReloadProvider used to wipe the provider's whole live
// catalog before re-fetching, so a re-fetch that wrote nothing left the
// provider with no live models at all. Editing an unrelated provider field
// while the upstream was briefly unhealthy therefore emptied GET /v1/models
// until a later edit or a restart repaired it.
func TestReloadProvider_FailedRefetchKeepsPreviousCatalog(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	catalog := modelcatalog.NewTestCatalog(nil)
	catalog.UpsertLive("custom-provider", "key-1", false, []string{"some-model"})
	catalog.UpsertLive("custom-provider", "key-1", true, []string{"some-model", "unlisted-model"})

	server := newReloadProviderServer(catalog, "custom-provider", []schemas.Key{{ID: "key-1"}}, false)

	if _, err := server.ReloadProvider(context.Background(), "custom-provider"); err != nil {
		t.Fatalf("ReloadProvider returned unexpected error: %v", err)
	}

	if got := catalog.GetModelsForProvider("custom-provider"); !slices.Equal(got, []string{"some-model"}) {
		t.Errorf("filtered catalog after failed refetch = %v, want [some-model] retained", got)
	}
	if got := catalog.GetUnfilteredModelsForProvider("custom-provider"); !slices.Equal(got, []string{"some-model", "unlisted-model"}) {
		t.Errorf("unfiltered catalog after failed refetch = %v, want [some-model unlisted-model] retained", got)
	}
}

// TestReloadProvider_PrunesRemovedAndDisabledKeys pins the half of the old
// invalidate that must survive the fix. Retaining is not "skip the cleanup":
// keys that left the provider's set, and keys that are still configured but
// disabled, must still lose their cached entries. Disabled keys matter because
// RefreshLiveModelsForProvider never re-fetches for them, so a retained entry
// would be served indefinitely for a key core rejects at routing time.
func TestReloadProvider_PrunesRemovedAndDisabledKeys(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	catalog := modelcatalog.NewTestCatalog(nil)
	catalog.UpsertLive("custom-provider", "key-enabled", false, []string{"kept-model"})
	catalog.UpsertLive("custom-provider", "key-disabled", false, []string{"disabled-model"})
	catalog.UpsertLive("custom-provider", "key-removed", false, []string{"removed-model"})
	catalog.UpsertLive("custom-provider", "key-removed", true, []string{"removed-model"})

	keys := []schemas.Key{
		{ID: "key-enabled"},
		{ID: "key-disabled", Enabled: schemas.Ptr(false)},
	}
	server := newReloadProviderServer(catalog, "custom-provider", keys, false)

	if _, err := server.ReloadProvider(context.Background(), "custom-provider"); err != nil {
		t.Fatalf("ReloadProvider returned unexpected error: %v", err)
	}

	if got := catalog.GetModelsForProvider("custom-provider"); !slices.Equal(got, []string{"kept-model"}) {
		t.Errorf("filtered catalog = %v, want only [kept-model] (disabled and removed keys pruned)", got)
	}
	if got := catalog.GetUnfilteredModelsForProvider("custom-provider"); len(got) != 0 {
		t.Errorf("unfiltered catalog = %v, want empty (only the removed key had an unfiltered entry)", got)
	}
}

// TestReloadProvider_KeylessProviderRetainsSentinelEntry covers keyless
// providers (Vertex workload identity, Bedrock IAM, custom keyless), whose
// entries are cached under the "" key ID rather than a real key ID. The retain
// set has to name that sentinel explicitly or the fix would still empty every
// keyless provider's catalog on a failed refetch.
func TestReloadProvider_KeylessProviderRetainsSentinelEntry(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	catalog := modelcatalog.NewTestCatalog(nil)
	catalog.UpsertLive("keyless-provider", "", false, []string{"keyless-model"})

	server := newReloadProviderServer(catalog, "keyless-provider", nil, true)

	if _, err := server.ReloadProvider(context.Background(), "keyless-provider"); err != nil {
		t.Fatalf("ReloadProvider returned unexpected error: %v", err)
	}

	if got := catalog.GetModelsForProvider("keyless-provider"); !slices.Equal(got, []string{"keyless-model"}) {
		t.Errorf("keyless catalog after failed refetch = %v, want [keyless-model] retained", got)
	}
}

// TestReloadProvider_NoKeysDropsEverything pins the branch that must keep
// wiping: a keyed provider with no keys left can serve nothing, so no cached
// entry is still meaningful and none will ever be refreshed.
func TestReloadProvider_NoKeysDropsEverything(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	catalog := modelcatalog.NewTestCatalog(nil)
	catalog.UpsertLive("custom-provider", "key-1", false, []string{"orphaned-model"})

	server := newReloadProviderServer(catalog, "custom-provider", nil, false)

	if _, err := server.ReloadProvider(context.Background(), "custom-provider"); err != nil {
		t.Fatalf("ReloadProvider returned unexpected error: %v", err)
	}

	if got := catalog.GetModelsForProvider("custom-provider"); len(got) != 0 {
		t.Errorf("catalog for keyless-less provider = %v, want empty", got)
	}
}

// TestReloadProvider_ConcurrentReloadsAndReadsAreRaceFree covers the layer
// above the store. ReloadProvider's catalog work is three separate store calls
// (SetKeyConfigForProvider, RetainLiveKeys, then the per-key upserts inside
// RefreshLiveModelsForProvider), so it is atomic per call but not as a
// sequence. Two provider edits landing at once, or an edit landing while
// GET /v1/models is being served, must stay race-free and leave the catalog
// coherent even though the interleaving of those three steps is unspecified.
//
// The keep map is built fresh inside each ReloadProvider call and never
// escapes it, which is what keeps concurrent callers from sharing it.
func TestReloadProvider_ConcurrentReloadsAndReadsAreRaceFree(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	catalog := modelcatalog.NewTestCatalog(nil)
	catalog.UpsertLive("custom-provider", "key-1", false, []string{"some-model"})
	catalog.UpsertLive("custom-provider", "key-1", true, []string{"some-model", "unlisted-model"})

	server := newReloadProviderServer(catalog, "custom-provider", []schemas.Key{{ID: "key-1"}}, false)

	const reloaders = 4
	const readers = 4
	const iterations = 50

	var wg sync.WaitGroup
	for range reloaders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				if _, err := server.ReloadProvider(context.Background(), "custom-provider"); err != nil {
					t.Errorf("concurrent ReloadProvider returned error: %v", err)
					return
				}
			}
		}()
	}
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				_ = catalog.GetModelsForProvider("custom-provider")
				_ = catalog.GetUnfilteredModelsForProvider("custom-provider")
			}
		}()
	}
	wg.Wait()

	// Retention is not just race-free but stable under repetition: no
	// interleaving of concurrent reloads may drop an entry whose key is
	// present and enabled in every one of them.
	if got := catalog.GetModelsForProvider("custom-provider"); !slices.Equal(got, []string{"some-model"}) {
		t.Errorf("filtered catalog after concurrent reloads = %v, want [some-model] retained", got)
	}
	if got := catalog.GetUnfilteredModelsForProvider("custom-provider"); !slices.Equal(got, []string{"some-model", "unlisted-model"}) {
		t.Errorf("unfiltered catalog after concurrent reloads = %v, want both entries retained", got)
	}
}

func TestMarshalPluginConfig_WithPointerType(t *testing.T) {
	// Test case 1: source is already *T
	expected := &TestConfig{
		Name:    "test-plugin",
		Enabled: true,
		Count:   42,
	}

	result, err := MarshalPluginConfig[TestConfig](expected)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if result != expected {
		t.Errorf("Expected same pointer, got different pointer")
	}

	if result.Name != expected.Name {
		t.Errorf("Expected Name=%s, got %s", expected.Name, result.Name)
	}
	if result.Enabled != expected.Enabled {
		t.Errorf("Expected Enabled=%v, got %v", expected.Enabled, result.Enabled)
	}
	if result.Count != expected.Count {
		t.Errorf("Expected Count=%d, got %d", expected.Count, result.Count)
	}
}

func TestMarshalPluginConfig_WithMap(t *testing.T) {
	// Test case 2: source is map[string]any
	configMap := map[string]any{
		"name":    "test-plugin",
		"enabled": true,
		"count":   42,
	}

	result, err := MarshalPluginConfig[TestConfig](configMap)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if result.Name != "test-plugin" {
		t.Errorf("Expected Name=test-plugin, got %s", result.Name)
	}
	if result.Enabled != true {
		t.Errorf("Expected Enabled=true, got %v", result.Enabled)
	}
	if result.Count != 42 {
		t.Errorf("Expected Count=42, got %d", result.Count)
	}
}

func TestMarshalPluginConfig_WithString(t *testing.T) {
	// Test case 3: source is string (JSON)
	configStr := `{"name":"test-plugin","enabled":true,"count":42}`

	result, err := MarshalPluginConfig[TestConfig](configStr)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if result.Name != "test-plugin" {
		t.Errorf("Expected Name=test-plugin, got %s", result.Name)
	}
	if result.Enabled != true {
		t.Errorf("Expected Enabled=true, got %v", result.Enabled)
	}
	if result.Count != 42 {
		t.Errorf("Expected Count=42, got %d", result.Count)
	}
}

func TestMarshalPluginConfig_WithInvalidType(t *testing.T) {
	// Test case 4: source is invalid type (should return error)
	invalidSource := 12345

	result, err := MarshalPluginConfig[TestConfig](invalidSource)
	if err == nil {
		t.Fatal("Expected error for invalid type, got nil")
	}

	if result != nil {
		t.Errorf("Expected nil result for invalid type, got %v", result)
	}

	expectedError := "invalid config type"
	if err.Error() != expectedError {
		t.Errorf("Expected error message '%s', got '%s'", expectedError, err.Error())
	}
}

func TestMarshalPluginConfig_WithInvalidJSONString(t *testing.T) {
	// Test case 5: source is string but invalid JSON
	invalidJSON := `{"name":"test-plugin","enabled":true,count:42}` // missing quotes around count

	result, err := MarshalPluginConfig[TestConfig](invalidJSON)
	if err == nil {
		t.Fatal("Expected error for invalid JSON, got nil")
	}

	if result != nil {
		t.Errorf("Expected nil result for invalid JSON, got %v", result)
	}
}

func TestMarshalPluginConfig_WithInvalidMapData(t *testing.T) {
	// Test case 6: source is map but contains invalid data types
	configMap := map[string]any{
		"name":    "test-plugin",
		"enabled": "not-a-boolean", // wrong type
		"count":   42,
	}

	result, err := MarshalPluginConfig[TestConfig](configMap)
	if err == nil {
		t.Fatal("Expected error for invalid map data, got nil")
	}

	if result != nil {
		t.Errorf("Expected nil result for invalid map data, got %v", result)
	}
}

func TestMarshalPluginConfig_WithEmptyMap(t *testing.T) {
	// Test case 7: source is empty map (should work, return zero values)
	configMap := map[string]any{}

	result, err := MarshalPluginConfig[TestConfig](configMap)
	if err != nil {
		t.Fatalf("Expected no error for empty map, got: %v", err)
	}

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// All fields should have zero values
	if result.Name != "" {
		t.Errorf("Expected empty Name, got %s", result.Name)
	}
	if result.Enabled != false {
		t.Errorf("Expected Enabled=false, got %v", result.Enabled)
	}
	if result.Count != 0 {
		t.Errorf("Expected Count=0, got %d", result.Count)
	}
}

func TestMarshalPluginConfig_WithEmptyString(t *testing.T) {
	// Test case 8: source is empty string (should fail as invalid JSON)
	configStr := ""

	result, err := MarshalPluginConfig[TestConfig](configStr)
	if err == nil {
		t.Fatal("Expected error for empty string, got nil")
	}

	if result != nil {
		t.Errorf("Expected nil result for empty string, got %v", result)
	}
}

func TestMarshalPluginConfig_WithNil(t *testing.T) {
	// Test case 9: source is nil (should return error as invalid type)
	result, err := MarshalPluginConfig[TestConfig](nil)
	if err == nil {
		t.Fatal("Expected error for nil source, got nil")
	}

	if result != nil {
		t.Errorf("Expected nil result for nil source, got %v", result)
	}
}

// Benchmark tests
func BenchmarkMarshalPluginConfig_WithPointerType(b *testing.B) {
	config := &TestConfig{
		Name:    "test-plugin",
		Enabled: true,
		Count:   42,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = MarshalPluginConfig[TestConfig](config)
	}
}

func BenchmarkMarshalPluginConfig_WithMap(b *testing.B) {
	configMap := map[string]any{
		"name":    "test-plugin",
		"enabled": true,
		"count":   42,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = MarshalPluginConfig[TestConfig](configMap)
	}
}

func BenchmarkMarshalPluginConfig_WithString(b *testing.B) {
	configStr := `{"name":"test-plugin","enabled":true,"count":42}`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = MarshalPluginConfig[TestConfig](configStr)
	}
}

// Complex config for additional testing
type ComplexConfig struct {
	Settings map[string]string `json:"settings"`
	Tags     []string          `json:"tags"`
	Metadata map[string]any    `json:"metadata"`
	Nested   *TestConfig       `json:"nested"`
}

func TestMarshalPluginConfig_WithComplexType(t *testing.T) {
	// Test with a more complex nested structure
	configMap := map[string]any{
		"settings": map[string]any{
			"key1": "value1",
			"key2": "value2",
		},
		"tags": []any{"tag1", "tag2", "tag3"},
		"metadata": map[string]any{
			"version": "1.0.0",
			"author":  "test",
		},
		"nested": map[string]any{
			"name":    "nested-config",
			"enabled": true,
			"count":   10,
		},
	}

	result, err := MarshalPluginConfig[ComplexConfig](configMap)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if len(result.Settings) != 2 {
		t.Errorf("Expected 2 settings, got %d", len(result.Settings))
	}
	if len(result.Tags) != 3 {
		t.Errorf("Expected 3 tags, got %d", len(result.Tags))
	}
	if result.Nested == nil {
		t.Fatal("Expected non-nil nested config")
	}
	if result.Nested.Name != "nested-config" {
		t.Errorf("Expected nested name=nested-config, got %s", result.Nested.Name)
	}
}
