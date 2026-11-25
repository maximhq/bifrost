package server

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fasthttp/router"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/agent"
	"github.com/maximhq/bifrost/core/network"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/streamusage"
	"github.com/maximhq/bifrost/framework"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/livestoretest"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/mcpdiscovery"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/rules"
	"github.com/maximhq/bifrost/plugins/semanticcache"
	"github.com/maximhq/bifrost/plugins/telemetry"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/handlers"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib/libtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// newTestProviderLiveStore returns a new, empty live store closed when the test ends.
var newTestProviderLiveStore = livestoretest.New

// newTestPerUserDiscovery returns a per-user discovery store over store with its cache in a live
// store of its own.
func newTestPerUserDiscovery(t testing.TB, store configstore.ConfigStore, logger schemas.Logger) *mcpdiscovery.PerUserStore {
	t.Helper()
	s, err := mcpdiscovery.NewPerUserStore(store, newTestProviderLiveStore(t), logger)
	if err != nil {
		t.Fatalf("per-user discovery store: %v", err)
	}
	return s
}

func TestAgentRegistrationCallbacksRejectUninitializedGateway(t *testing.T) {
	t.Parallel()

	server := &BifrostHTTPServer{}
	ctx := context.Background()

	_, err := server.CreateAgentRegistration(ctx, agent.CreateRequest{})
	if !errors.Is(err, errAgentGatewayNotInitialized) {
		t.Fatalf("CreateAgentRegistration error = %v, want %v", err, errAgentGatewayNotInitialized)
	}

	_, err = server.UpdateAgentRegistration(ctx, "fixture", agent.UpdateRequest{})
	if !errors.Is(err, errAgentGatewayNotInitialized) {
		t.Fatalf("UpdateAgentRegistration error = %v, want %v", err, errAgentGatewayNotInitialized)
	}

	err = server.DeleteAgentRegistration(ctx, "fixture")
	if !errors.Is(err, errAgentGatewayNotInitialized) {
		t.Fatalf("DeleteAgentRegistration error = %v, want %v", err, errAgentGatewayNotInitialized)
	}
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

// governedServer returns a server over a new SQLite config store with a governance plugin whose
// store reads the server's live store, as a running gateway's does, and that store.
func governedServer(t *testing.T) (*BifrostHTTPServer, *governance.LocalGovernanceStore) {
	t.Helper()
	handlers.SetLogger(noopTestLogger{})
	previousLogger := logger
	logger = noopTestLogger{}
	t.Cleanup(func() { logger = previousLogger })
	ctx := context.Background()
	config := libtest.Config(t, nil)
	config.ClientConfig = &configstore.ClientConfig{}
	gs, err := governance.NewLocalGovernanceStore(ctx, governance.NewMockLogger(), nil, config.LiveStore, nil)
	require.NoError(t, err)
	t.Cleanup(gs.Close)
	plugins := []schemas.BasePlugin{&reloadVirtualKeyPlugin{store: gs}}
	config.BasePlugins.Store(&plugins)
	return &BifrostHTTPServer{Ctx: schemas.NewBifrostContext(ctx, schemas.NoDeadline), Config: config}, gs
}

// assignedVirtualMCPs is the virtual MCPs the key with this ID is assigned, as governance holds it.
func assignedVirtualMCPs(t *testing.T, server *BifrostHTTPServer, gs *governance.LocalGovernanceStore, id string) []uint {
	t.Helper()
	vk, ok := gs.GetVirtualKeyByID(context.Background(), id)
	require.True(t, ok)
	set, ok := server.Config.LiveStore.Collections.GrantSet(vk.Grants)
	require.True(t, ok, "the key's grant set is held")
	return set.VirtualMCPIDs
}

// storeTestVirtualKey writes an active key with this ID, name and value to the server's config
// store.
func storeTestVirtualKey(t *testing.T, server *BifrostHTTPServer, id, name, value string) {
	t.Helper()
	require.NoError(t, server.Config.ConfigStore.CreateVirtualKey(context.Background(), &configstoreTables.TableVirtualKey{
		ID: id, Name: name, Value: *schemas.NewSecretVar(value), IsActive: schemas.Ptr(true),
	}))
}

// reloadVirtualKeyToolManager provides an empty MCP tool set.
type reloadVirtualKeyToolManager struct{}

// GetMCPServerInstructions returns no instructions.
func (reloadVirtualKeyToolManager) GetMCPServerInstructions(context.Context) string {
	return ""
}

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

// TestVirtualKeyReloadAndRemoveFollowTheDatabase checks the key wrappers against a real config
// store: ReloadVirtualKey answers with the key's row and brings the key into memory, a later reload
// picks up a budget and a key-scoped model config written after it, a reload after that model
// config is deleted drops it, and RemoveVirtualKey drops the key and its budget once its row is
// deleted.
func TestVirtualKeyReloadAndRemoveFollowTheDatabase(t *testing.T) {
	server, gs := governedServer(t)
	ctx := context.Background()
	store := server.Config.ConfigStore
	vkScope := configstoreTables.ModelConfigScopeVirtualKey
	keyID := "vk-1"

	storeTestVirtualKey(t, server, keyID, "app", "sk-bf-app")
	row, err := server.ReloadVirtualKey(ctx, keyID)
	require.NoError(t, err)
	require.Equal(t, "app", row.Name, "the response is the key's row")
	vk, ok := gs.GetVirtualKey(ctx, "sk-bf-app")
	require.True(t, ok, "the reloaded key is served by its value")
	require.Empty(t, vk.BudgetIDs)

	require.NoError(t, store.CreateBudget(ctx, &configstoreTables.TableBudget{ID: "budget-1", MaxLimit: 10, ResetDuration: "1d", LastReset: time.Now(), VirtualKeyID: &keyID}))
	require.NoError(t, store.CreateModelConfig(ctx, &configstoreTables.TableModelConfig{ID: "mc-1", ModelName: "gpt-4o", Scope: vkScope, ScopeID: &keyID}))
	_, ok = gs.GetVirtualKeyByID(ctx, keyID)
	require.True(t, ok)
	require.Nil(t, gs.LoadBudget(ctx, "budget-1"), "memory changes only when the key is reloaded")
	_, err = server.ReloadVirtualKey(ctx, keyID)
	require.NoError(t, err)
	vk, ok = gs.GetVirtualKeyByID(ctx, keyID)
	require.True(t, ok)
	require.Equal(t, []string{"budget-1"}, vk.BudgetIDs, "the key holds the new budget")
	budget := gs.LoadBudget(ctx, "budget-1")
	require.NotNil(t, budget, "governance enforces the new budget")
	require.Equal(t, 10.0, budget.MaxLimit)
	require.Equal(t, []string{"mc-1"}, scopedModelConfigIDs(gs, vkScope, keyID), "the key-scoped model config came with the key")

	require.NoError(t, store.DeleteModelConfig(ctx, "mc-1"))
	_, err = server.ReloadVirtualKey(ctx, keyID)
	require.NoError(t, err)
	require.Empty(t, scopedModelConfigIDs(gs, vkScope, keyID), "a key-scoped model config the write deleted stops enforcing")

	require.NoError(t, store.DeleteVirtualKey(ctx, keyID))
	require.NoError(t, server.RemoveVirtualKey(ctx, keyID))
	_, ok = gs.GetVirtualKeyByID(ctx, keyID)
	require.False(t, ok, "the removed key is gone")
	_, ok = gs.GetVirtualKey(ctx, "sk-bf-app")
	require.False(t, ok, "and no longer served by its value")
	require.Nil(t, gs.LoadBudget(ctx, "budget-1"), "and its budget went with it")
}

// TestGetVirtualKeyAnswersAsyncJobKeyLookups checks the async job executor's key lookup: a key's
// value resolves to a row carrying only its ID and name, a rotated key's previous value still
// resolves during its grace period, an unknown value does not, and nothing resolves once the
// governance plugin is not loaded.
func TestGetVirtualKeyAnswersAsyncJobKeyLookups(t *testing.T) {
	server, _ := governedServer(t)
	ctx := context.Background()

	storeTestVirtualKey(t, server, "vk-1", "app", "sk-bf-app")
	_, err := server.ReloadVirtualKey(ctx, "vk-1")
	require.NoError(t, err)
	row, ok := server.GetVirtualKey(ctx, "sk-bf-app")
	require.True(t, ok, "a loaded key resolves by its value")
	require.Equal(t, &configstoreTables.TableVirtualKey{ID: "vk-1", Name: "app"}, row, "the row carries only the key's ID and name")

	now := time.Now().UTC()
	graceEnds := now.Add(5 * time.Minute)
	require.NoError(t, server.Config.ConfigStore.CreateVirtualKey(ctx, &configstoreTables.TableVirtualKey{
		ID: "vk-2", Name: "rotated", Value: *schemas.NewSecretVar("sk-bf-new"), IsActive: schemas.Ptr(true),
		PreviousValue: *schemas.NewSecretVar("sk-bf-old"), PreviousValueExpiresAt: &graceEnds, RotatedAt: &now,
	}))
	_, err = server.ReloadVirtualKey(ctx, "vk-2")
	require.NoError(t, err)
	row, ok = server.GetVirtualKey(ctx, "sk-bf-old")
	require.True(t, ok, "a rotated key's previous value resolves during its grace period")
	require.Equal(t, &configstoreTables.TableVirtualKey{ID: "vk-2", Name: "rotated"}, row)

	row, ok = server.GetVirtualKey(ctx, "sk-bf-unknown")
	require.False(t, ok, "an unknown value does not resolve")
	require.Nil(t, row)

	server.Config.BasePlugins.Store(&[]schemas.BasePlugin{})
	row, ok = server.GetVirtualKey(ctx, "sk-bf-app")
	require.False(t, ok, "nothing resolves once the governance plugin is not loaded")
	require.Nil(t, row)
}

// TestReloadVirtualKeysMatchesPerKeyReload checks that a batched reload leaves every key as
// ReloadVirtualKey would: each key's row replaces its record, with its key-scoped model configs,
// duplicates are read once, and an id with no row is skipped without an error and without waiting
// for the row to appear.
func TestReloadVirtualKeysMatchesPerKeyReload(t *testing.T) {
	server, gs := governedServer(t)
	ctx := context.Background()
	store := server.Config.ConfigStore
	vkScope := configstoreTables.ModelConfigScopeVirtualKey

	storeTestVirtualKey(t, server, "vk-a", "old-a", "sk-bf-a")
	storeTestVirtualKey(t, server, "vk-b", "old-b", "sk-bf-b")
	require.NoError(t, server.ReloadVirtualKeys(ctx, []string{"vk-a", "vk-b"}))

	require.NoError(t, store.DB().Model(&configstoreTables.TableVirtualKey{}).Where("id = ?", "vk-a").Update("name", "new-a").Error)
	scopeB := "vk-b"
	require.NoError(t, store.CreateModelConfig(ctx, &configstoreTables.TableModelConfig{ID: "mc-b", ModelName: "gpt-4o", Scope: vkScope, ScopeID: &scopeB}))

	started := time.Now()
	require.NoError(t, server.ReloadVirtualKeys(ctx, []string{"vk-a", "vk-b", "vk-missing", "vk-a"}))
	require.Less(t, time.Since(started), lib.DBLookupDelay, "a key with no row is not waited for")

	vk, ok := gs.GetVirtualKeyByID(ctx, "vk-a")
	require.True(t, ok)
	require.Equal(t, "new-a", vk.Name)
	require.Equal(t, []string{"mc-b"}, scopedModelConfigIDs(gs, vkScope, "vk-b"))
	_, ok = gs.GetVirtualKeyByID(ctx, "vk-missing")
	require.False(t, ok)
}

// TestReloadVirtualKeysDropsAKeyDeletedMeanwhile checks that a batched reload of a held key whose
// row is gone drops its record: the row was deleted between the write and the read.
func TestReloadVirtualKeysDropsAKeyDeletedMeanwhile(t *testing.T) {
	server, gs := governedServer(t)
	ctx := context.Background()
	storeTestVirtualKey(t, server, "vk-gone", "gone", "sk-bf-gone")
	require.NoError(t, server.ReloadVirtualKeys(ctx, []string{"vk-gone"}))
	_, ok := gs.GetVirtualKeyByID(ctx, "vk-gone")
	require.True(t, ok)

	require.NoError(t, server.Config.ConfigStore.DB().Where("id = ?", "vk-gone").Delete(&configstoreTables.TableVirtualKey{}).Error)
	require.NoError(t, server.ReloadVirtualKeys(ctx, []string{"vk-gone"}))
	_, ok = gs.GetVirtualKeyByID(ctx, "vk-gone")
	require.False(t, ok, "a key whose row is gone is dropped")
}

// TestReloadModelConfigKeepsLiveUsage checks that a model config reload installs the row's new
// budget settings, keeps the usage and reset evidence governance counted since the budget was
// loaded, answers with them and writes nothing back to the database.
func TestReloadModelConfigKeepsLiveUsage(t *testing.T) {
	server, gs := governedServer(t)
	ctx := context.Background()
	store := server.Config.ConfigStore
	mcID := "mc-1"

	require.NoError(t, store.CreateModelConfig(ctx, &configstoreTables.TableModelConfig{ID: mcID, ModelName: "gpt-4o", Scope: configstoreTables.ModelConfigScopeGlobal}))
	require.NoError(t, store.CreateBudget(ctx, &configstoreTables.TableBudget{ID: "budget-mc", MaxLimit: 10, ResetDuration: "1d", LastReset: time.Now(), ModelConfigID: &mcID}))
	_, err := server.ReloadModelConfig(ctx, mcID)
	require.NoError(t, err)
	resetAt := time.Now()
	_, ok := gs.ResetBudgetAt(ctx, "budget-mc", resetAt)
	require.True(t, ok)
	require.NoError(t, gs.BumpBudgetUsage(ctx, "budget-mc", 4))

	// Another write raises the limit. Saving a budget row validates the whole row, so the test
	// writes the one column directly.
	require.NoError(t, store.DB().Session(&gorm.Session{SkipHooks: true}).Model(&configstoreTables.TableBudget{}).Where("id = ?", "budget-mc").Update("max_limit", 20).Error)
	reloaded, err := server.ReloadModelConfig(ctx, mcID)
	require.NoError(t, err)
	require.Len(t, reloaded.Budgets, 1)
	require.Equal(t, 20.0, reloaded.Budgets[0].MaxLimit, "the response carries the new setting")
	require.Equal(t, 4.0, reloaded.Budgets[0].CurrentUsage, "and the usage governance counted")
	require.Equal(t, resetAt.UnixNano(), reloaded.Budgets[0].UsageResetEpoch, "and the reset evidence")

	live := gs.LoadBudget(ctx, "budget-mc")
	require.NotNil(t, live)
	require.Equal(t, 20.0, live.MaxLimit, "governance enforces the new setting")
	require.Equal(t, 4.0, live.CurrentUsage, "without losing the usage")
	row, err := store.GetBudget(ctx, "budget-mc")
	require.NoError(t, err)
	require.Zero(t, row.CurrentUsage, "a reload writes nothing back; usage reaches the database through the tracker")

	require.NoError(t, store.DeleteModelConfig(ctx, mcID))
	require.NoError(t, server.RemoveModelConfig(ctx, mcID))
	require.Nil(t, gs.LoadBudget(ctx, "budget-mc"), "removing the model config drops its budget")
}

// TestVirtualMCPAssignmentsFollowTheKey checks that attaching and detaching a Virtual MCP refresh
// the key, which carries its assignments, and that removing a definition drops it from memory.
func TestVirtualMCPAssignmentsFollowTheKey(t *testing.T) {
	server, gs := governedServer(t)
	ctx := context.Background()
	store := server.Config.ConfigStore

	storeTestVirtualKey(t, server, "vk-1", "app", "sk-bf-app")
	_, err := server.ReloadVirtualKey(ctx, "vk-1")
	require.NoError(t, err)
	def := &configstoreTables.TableVirtualMCP{Name: "research", Enabled: true}
	require.NoError(t, store.CreateVirtualMCP(ctx, def))
	reloaded, err := server.ReloadVirtualMCP(ctx, def.ID)
	require.NoError(t, err)
	require.Equal(t, "research", reloaded.Name)
	_, held := server.Config.LiveStore.Collections.VirtualMCPs.Get(registry.VirtualMCPKey(def.ID))
	require.True(t, held, "the definition is in memory")

	require.NoError(t, store.AttachVirtualMCPToVirtualKey(ctx, def.ID, "vk-1"))
	require.NoError(t, server.ReloadVirtualMCPAssignment(ctx, "vk-1", def.ID, true))
	require.Equal(t, []uint{def.ID}, assignedVirtualMCPs(t, server, gs, "vk-1"))

	require.NoError(t, store.DetachVirtualMCPFromVirtualKey(ctx, def.ID, "vk-1"))
	require.NoError(t, server.ReloadVirtualMCPAssignment(ctx, "vk-1", def.ID, false))
	require.Empty(t, assignedVirtualMCPs(t, server, gs, "vk-1"))

	require.NoError(t, store.DeleteVirtualMCP(ctx, def.ID))
	require.NoError(t, server.RemoveVirtualMCP(ctx, def.ID))
	_, held = server.Config.LiveStore.Collections.VirtualMCPs.Get(registry.VirtualMCPKey(def.ID))
	require.False(t, held, "the removed definition is gone")
}

// perCallerNamesConfigStore counts the reads of the names in per-user discoveries.
type perCallerNamesConfigStore struct {
	configstore.ConfigStore
	reads atomic.Int32
}

// GetMCPPerUserDiscoveryNames counts the read and answers that no caller holds a discovery.
func (s *perCallerNamesConfigStore) GetMCPPerUserDiscoveryNames(context.Context, []string) (map[string]configstore.MCPPerUserDiscoveryNames, error) {
	s.reads.Add(1)
	return nil, nil
}

// Every per-user discovery eviction (this process's own write, a delete from the sessions page,
// another process's write) brings /mcp's per-caller entries up to date, off the caller's path.
func TestEvictMCPPerUserDiscoveryCache_ResyncsTheMCPGateway(t *testing.T) {
	handlers.SetLogger(noopTestLogger{})
	ctx := context.Background()
	store := &perCallerNamesConfigStore{}
	config := libtest.MCPClients(t, &lib.Config{
		ConfigStore:  store,
		ClientConfig: &configstore.ClientConfig{},
		MCPConfig:    &schemas.MCPConfig{},
	}, &schemas.MCPClientConfig{ID: "notes-id", Name: "notes", PerUserDiscovery: schemas.Ptr(true)})
	config.MCPPerUserDiscoveryStore = newTestPerUserDiscovery(t, store, noopTestLogger{})
	mcpHandler, err := handlers.NewMCPServerHandler(ctx, config, reloadVirtualKeyToolManager{}, nil, nil)
	require.NoError(t, err)
	server := &BifrostHTTPServer{Config: config, MCPServerHandler: mcpHandler}

	for name, evict := range map[string]func(){
		"one binding": func() { server.EvictMCPPerUserDiscoveryCache(ctx, schemas.MCPAuthModeUser, "alice", "notes-id") },
		"one client":  func() { server.EvictMCPPerUserDiscoveryCacheByMCPClient(ctx, "notes-id") },
	} {
		before := store.reads.Load()
		evict()
		assert.Eventually(t, func() bool { return store.reads.Load() > before }, 5*time.Second, 10*time.Millisecond, name)
	}
}

// agentGatewayRouteStore satisfies only the persistence calls the Agent Gateway
// startup path makes, so route registration can be exercised without a database.
type agentGatewayRouteStore struct {
	configstore.ConfigStore
}

func (*agentGatewayRouteStore) CreateAgentRegistration(context.Context, *schemas.AgentRegistration) error {
	return nil
}
func (*agentGatewayRouteStore) UpdateAgentRegistration(context.Context, *schemas.AgentRegistration) error {
	return nil
}
func (*agentGatewayRouteStore) ListAgentRegistrations(context.Context) ([]schemas.AgentRegistration, error) {
	return nil, nil
}
func (*agentGatewayRouteStore) GetAgentRegistration(context.Context, string) (*schemas.AgentRegistration, error) {
	return nil, agent.ErrNotFound
}
func (*agentGatewayRouteStore) DeleteAgentRegistration(context.Context, string) error { return nil }
func (*agentGatewayRouteStore) ListDueAgentPushDeliveries(context.Context, time.Time, int) ([]schemas.AgentPushDelivery, error) {
	return nil, nil
}
func (*agentGatewayRouteStore) PruneAgentPushDeliveries(context.Context, time.Time) error { return nil }
func (*agentGatewayRouteStore) GetVirtualKey(context.Context, string) (*configstoreTables.TableVirtualKey, error) {
	return nil, configstore.ErrNotFound
}
func (*agentGatewayRouteStore) GetVirtualKeyByValue(context.Context, string) (*configstoreTables.TableVirtualKey, error) {
	return nil, configstore.ErrNotFound
}

type agentGatewayRouteLogger struct{}

func (agentGatewayRouteLogger) Debug(string, ...any)                   {}
func (agentGatewayRouteLogger) Info(string, ...any)                    {}
func (agentGatewayRouteLogger) Warn(string, ...any)                    {}
func (agentGatewayRouteLogger) Error(string, ...any)                   {}
func (agentGatewayRouteLogger) Fatal(string, ...any)                   {}
func (agentGatewayRouteLogger) SetLevel(schemas.LogLevel)              {}
func (agentGatewayRouteLogger) SetOutputType(schemas.LoggerOutputType) {}
func (agentGatewayRouteLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func TestAgentGatewayRoutesRegisteredAfterStartupInitialization(t *testing.T) {
	store := &agentGatewayRouteStore{}
	s := &BifrostHTTPServer{
		Config: &lib.Config{
			ConfigStore: store,
			// Protocol routes answer 503 until a public A2A URL is configured.
			ClientConfig: &configstore.ClientConfig{A2AExternalClientURL: schemas.NewSecretVar("https://bifrost.example.com")},
		},
		Router: router.New(),
	}

	requireNoError := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	SetLogger(agentGatewayRouteLogger{})
	t.Cleanup(func() { SetLogger(nil) })
	requireNoError(s.InitializeAgentGateway(context.Background(), nil, nil))
	firstHandler := s.AgentGatewayHandler
	if firstHandler == nil {
		t.Fatal("expected Agent Gateway handler to be initialized")
	}
	defer s.CloseAgentGateway()
	requireNoError(s.InitializeAgentGateway(context.Background(), nil, nil))
	if s.AgentGatewayHandler != firstHandler {
		t.Fatal("expected Agent Gateway handler to be constructed only once")
	}

	s.AgentGatewayHandler.RegisterManagementRoutes(s.Router)
	s.AgentGatewayHandler.RegisterProtocolRoutes(s.Router)
	s.Router.NotFound = func(ctx *fasthttp.RequestCtx) { ctx.SetStatusCode(fasthttp.StatusTeapot) }

	for _, route := range []struct {
		method     string
		path       string
		wantStatus int
	}{
		{fasthttp.MethodGet, "/api/agents", fasthttp.StatusOK},
		{fasthttp.MethodGet, "/agents/a2a/missing/.well-known/agent-card.json", fasthttp.StatusNotFound},
	} {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.SetMethod(route.method)
		ctx.Request.SetRequestURI(route.path)
		s.Router.Handler(ctx)
		if ctx.Response.StatusCode() != route.wantStatus {
			t.Fatalf("route %s returned %d, want %d", route.path, ctx.Response.StatusCode(), route.wantStatus)
		}
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

// UpdateStatus records the call, then writes it through when the mock wraps a real store.
func (s *updateStatusOnlyConfigStore) UpdateStatus(ctx context.Context, provider schemas.ModelProvider, keyID string, status, errorMsg string) error {
	s.calls = append(s.calls, schemas.KeyStatus{
		Provider: provider,
		KeyID:    keyID,
		Status:   schemas.KeyStatusType(status),
		Error:    &schemas.BifrostError{Error: &schemas.ErrorField{Message: errorMsg}},
	})
	if s.ConfigStore == nil {
		return nil
	}
	return s.ConfigStore.UpdateStatus(ctx, provider, keyID, status, errorMsg)
}

func TestUpdateKeyStatus_KeylessProviderUpdatesProviderStatusInMemory(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	config := libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		"mock-openai": {
			CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, IsKeyLess: true},
			Status:               "unknown",
		},
	})
	store := &updateStatusOnlyConfigStore{ConfigStore: config.ConfigStore}
	config.ConfigStore = store
	server := &BifrostHTTPServer{Config: config}

	server.updateKeyStatus(context.Background(), []schemas.KeyStatus{{
		Provider: "mock-openai",
		KeyID:    "",
		Status:   schemas.KeyStatusListModelsFailed,
		Error:    &schemas.BifrostError{Error: &schemas.ErrorField{Message: "preview missing model"}},
	}})

	provider := libtest.Providers(server.Config)["mock-openai"]
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

	config := libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		"openai": {
			Keys:   []schemas.Key{{ID: "key-1"}},
			Status: "healthy",
		},
	})
	store := &updateStatusOnlyConfigStore{ConfigStore: config.ConfigStore}
	config.ConfigStore = store
	server := &BifrostHTTPServer{Config: config}

	server.updateKeyStatus(context.Background(), []schemas.KeyStatus{{
		Provider: "openai",
		KeyID:    "",
		Status:   schemas.KeyStatusListModelsFailed,
		Error:    &schemas.BifrostError{Error: &schemas.ErrorField{Message: "malformed status"}},
	}})

	provider := libtest.Providers(server.Config)["openai"]
	if provider.Status != "healthy" {
		t.Fatalf("expected keyed provider status to remain unchanged, got %q", provider.Status)
	}
	if provider.Description != "" {
		t.Fatalf("expected keyed provider description to remain unchanged, got %q", provider.Description)
	}
	// Memory always matches the database, so the provider row is not written either.
	if len(store.calls) != 0 {
		t.Fatalf("expected no status update call for a keyed provider, got %d", len(store.calls))
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

	config := libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{{
				ID:          "key-1",
				Status:      schemas.KeyStatusListModelsFailed,
				Description: "upstream 503",
			}},
		},
	})
	store := &updateStatusOnlyConfigStore{ConfigStore: config.ConfigStore}
	config.ConfigStore = store
	server := &BifrostHTTPServer{Config: config}

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
	if got := libtest.Providers(server.Config)["openai"].Keys[0].Status; got != schemas.KeyStatusSuccess {
		t.Fatalf("expected in-memory status to become success, got %q", got)
	}
	if got := libtest.Providers(server.Config)["openai"].Keys[0].Description; got != "" {
		t.Fatalf("expected in-memory description to be cleared, got %q", got)
	}
}

// TestUpdateKeyStatus_RereadsOnlyTheChangedKey checks that a key's status change re-reads that key
// alone: the provider record and the provider's other keys are left as they are, so nothing derived
// from providers is rebuilt on every list-models pass.
func TestUpdateKeyStatus_RereadsOnlyTheChangedKey(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	config := libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		"openai": {Keys: []schemas.Key{{ID: "key-1"}, {ID: "key-2"}}},
	})
	server := &BifrostHTTPServer{Config: config}
	collections := config.LiveStore.Collections
	providerChanges := 0
	cancel := collections.Providers.Subscribe(func(_ context.Context, changes []livestore.Change[registry.Provider]) {
		providerChanges += len(changes)
	})
	defer cancel()
	otherBefore, _ := collections.ProviderKeys.Get("key-2")

	server.updateKeyStatus(context.Background(), []schemas.KeyStatus{{
		Provider: "openai",
		KeyID:    "key-1",
		Status:   schemas.KeyStatusListModelsFailed,
		Error:    &schemas.BifrostError{Error: &schemas.ErrorField{Message: "upstream 503"}},
	}})

	changed, ok := collections.ProviderKeys.Get("key-1")
	if !ok || changed.Key.Status != schemas.KeyStatusListModelsFailed || changed.Key.Description != "upstream 503" {
		t.Fatalf("expected key-1 to hold the new status, got %+v", changed)
	}
	if providerChanges != 0 {
		t.Fatalf("expected the provider record to be left alone, got %d provider changes", providerChanges)
	}
	if other, _ := collections.ProviderKeys.Get("key-2"); other != otherBefore {
		t.Fatal("expected the provider's other key record to be left alone")
	}
}

// TestUpdateKeyStatus_WritesWhenKeyMissingFromMemory guards the fallback: with
// nothing to compare against, the gate must let the write through rather than
// silently swallowing it.
func TestUpdateKeyStatus_WritesWhenKeyMissingFromMemory(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	config := libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		"openai": {Keys: []schemas.Key{{ID: "key-1"}}},
	})
	store := &updateStatusOnlyConfigStore{ConfigStore: config.ConfigStore}
	config.ConfigStore = store
	server := &BifrostHTTPServer{Config: config}

	server.updateKeyStatus(context.Background(), []schemas.KeyStatus{{
		Provider: "openai",
		KeyID:    "key-unknown",
		Status:   schemas.KeyStatusSuccess,
	}})

	if len(store.calls) != 1 {
		t.Fatalf("expected the write to proceed for an unknown key, got %d calls", len(store.calls))
	}
}

// TestUpdateKeyStatus_LastStatusForAKeyInOneBatchWins covers a batch that reports one key twice,
// as list-models does when the key succeeds on one page and fails on the next. Memory is
// refreshed once after the batch, so the batch's last status must be the one compared and
// written; comparing each against the pre-batch value would skip the last and keep the first.
func TestUpdateKeyStatus_LastStatusForAKeyInOneBatchWins(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	config := libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{{
				ID:          "key-1",
				Status:      schemas.KeyStatusListModelsFailed,
				Description: "upstream 503",
			}},
		},
	})
	store := &updateStatusOnlyConfigStore{ConfigStore: config.ConfigStore}
	config.ConfigStore = store
	server := &BifrostHTTPServer{Config: config}

	server.updateKeyStatus(context.Background(), []schemas.KeyStatus{
		{Provider: "openai", KeyID: "key-1", Status: schemas.KeyStatusSuccess},
		{Provider: "openai", KeyID: "key-1", Status: schemas.KeyStatusListModelsFailed,
			Error: &schemas.BifrostError{Error: &schemas.ErrorField{Message: "upstream 503"}}},
	})

	rows, err := store.ConfigStore.GetProviderKeys(context.Background(), "openai")
	if err != nil {
		t.Fatalf("failed to read the key rows: %v", err)
	}
	i := slices.IndexFunc(rows, func(k schemas.Key) bool { return k.ID == "key-1" })
	if i < 0 {
		t.Fatal("the key row is gone")
	}
	row := rows[i]
	if row.Status != schemas.KeyStatusListModelsFailed || row.Description != "upstream 503" {
		t.Fatalf("expected the row to end %q (%q), got %q (%q)", schemas.KeyStatusListModelsFailed, "upstream 503", row.Status, row.Description)
	}
	live := libtest.Providers(server.Config)["openai"].Keys[0]
	if live.Status != schemas.KeyStatusListModelsFailed || live.Description != "upstream 503" {
		t.Fatalf("expected the live record to end %q (%q), got %q (%q)", schemas.KeyStatusListModelsFailed, "upstream 503", live.Status, live.Description)
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
		Config: libtest.FileOnly(t, &lib.Config{
			ModelCatalog: catalogForTest(t, nil),
		}, map[schemas.ModelProvider]configstore.ProviderConfig{
			"custom-provider": {Keys: []schemas.Key{{ID: "key-1"}}},
		}),
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
		Config: libtest.FileOnly(t, &lib.Config{
			ModelCatalog: catalogForTest(t, nil),
		}, map[schemas.ModelProvider]configstore.ProviderConfig{
			"all-disabled": {Keys: []schemas.Key{
				{ID: "key-1", Enabled: schemas.Ptr(false)},
				{ID: "key-2", Enabled: schemas.Ptr(false)},
			}},
			"no-keys": {},
		}),
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
		Config: libtest.FileOnly(t, &lib.Config{
			ModelCatalog: catalogForTest(t, nil),
		}, map[schemas.ModelProvider]configstore.ProviderConfig{
			"custom-provider": {},
		}),
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

// TestRefreshLiveModelsForProvider_KeylessWithResidualDisabledKeys is the
// regression guard for issue #8500: a custom provider created keyed, whose
// only key was disabled before it was switched to keyless, kept that disabled
// key in its config. Discovery counted enabled keys before asking whether the
// provider needs keys at all, so it logged "no enabled keys configured" and
// never fetched, leaving the provider with no models. Core ignores keys
// entirely for keyless providers, so discovery must take the keyless path
// whatever residual keys are still stored.
func TestRefreshLiveModelsForProvider_KeylessWithResidualDisabledKeys(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"keyless-model","object":"model","owned_by":"test"}]}`))
	}))
	defer upstream.Close()

	const provider schemas.ModelProvider = "keyless-provider"
	cfg := libtest.FileOnly(t, &lib.Config{
		ModelCatalog: catalogForTest(t, nil),
	}, map[schemas.ModelProvider]configstore.ProviderConfig{
		provider: {
			Keys: []schemas.Key{{ID: "residual-key", Value: *schemas.NewSecretVar("sk-residual"), Models: schemas.WhiteList{"*"}, Weight: 1, Enabled: schemas.Ptr(false)}},
			NetworkConfig: &schemas.NetworkConfig{
				BaseURL:                        upstream.URL,
				AllowPrivateNetwork:            true,
				DefaultRequestTimeoutInSeconds: 5,
			},
			CustomProviderConfig: &schemas.CustomProviderConfig{
				IsKeyLess:        true,
				BaseProviderType: schemas.OpenAI,
			},
		},
	})
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: lib.NewBaseAccount(cfg),
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	defer client.Shutdown()

	server := &BifrostHTTPServer{Config: cfg, Client: client}
	keys := libtest.Providers(cfg)[provider].Keys

	server.RefreshLiveModelsForProvider(context.Background(), provider, keys)

	require.NotZero(t, calls.Load(), "keyless provider with a residual disabled key never reached the upstream /v1/models")
	require.NotEmpty(t, cfg.ModelCatalog.GetModelsForProvider(provider), "keyless provider with a residual disabled key has no live models after discovery")
}

// TestRefreshLiveModelsForProvider_KeylessWithResidualEnabledKey covers the
// other half of issue #8500's root cause: a keyless provider whose residual key
// is still enabled was fanned out per key and cached under that key's ID, even
// though core ignores keys for keyless providers. Its entries must land under
// the "" sentinel that the OnKey* helpers and ReloadProvider's keep set use.
func TestRefreshLiveModelsForProvider_KeylessWithResidualEnabledKey(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"keyless-model","object":"model","owned_by":"test"}]}`))
	}))
	defer upstream.Close()

	const provider schemas.ModelProvider = "keyless-provider"
	cfg := libtest.FileOnly(t, &lib.Config{
		ModelCatalog: catalogForTest(t, nil),
	}, map[schemas.ModelProvider]configstore.ProviderConfig{
		provider: {
			Keys: []schemas.Key{{ID: "residual-key", Value: *schemas.NewSecretVar("sk-residual"), Models: schemas.WhiteList{"*"}, Weight: 1}},
			NetworkConfig: &schemas.NetworkConfig{
				BaseURL:                        upstream.URL,
				AllowPrivateNetwork:            true,
				DefaultRequestTimeoutInSeconds: 5,
			},
			CustomProviderConfig: &schemas.CustomProviderConfig{
				IsKeyLess:        true,
				BaseProviderType: schemas.OpenAI,
			},
		},
	})
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: lib.NewBaseAccount(cfg),
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	defer client.Shutdown()

	server := &BifrostHTTPServer{Config: cfg, Client: client}
	keys := libtest.Providers(cfg)[provider].Keys

	server.RefreshLiveModelsForProvider(context.Background(), provider, keys)
	require.NotEmpty(t, cfg.ModelCatalog.GetModelsForProvider(provider), "keyless provider has no live models after discovery")

	// Dropping the residual key's entries must leave the catalog intact, and
	// dropping the sentinel's must empty it: everything was cached under "".
	cfg.ModelCatalog.InvalidateLive(provider, "residual-key")
	require.NotEmpty(t, cfg.ModelCatalog.GetModelsForProvider(provider), "keyless models were cached under the residual key's ID")
	cfg.ModelCatalog.InvalidateLive(provider, "")
	require.Empty(t, cfg.ModelCatalog.GetModelsForProvider(provider), "keyless models were not cached under the \"\" sentinel")
}

// TestOnKeyAdded_DisabledKeySkipsFetch cross-checks that adding a key that
// is already disabled never schedules a discovery fetch for it.
func TestOnKeyAdded_DisabledKeySkipsFetch(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	disabledKey := schemas.Key{ID: "key-1", Enabled: schemas.Ptr(false)}
	server := &BifrostHTTPServer{
		Config: libtest.FileOnly(t, &lib.Config{
			ModelCatalog: catalogForTest(t, nil),
		}, map[schemas.ModelProvider]configstore.ProviderConfig{
			"custom-provider": {Keys: []schemas.Key{disabledKey}},
		}),
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

	catalog := catalogForTest(t, nil)
	catalog.UpsertLive("custom-provider", "key-1", false, []string{"custom-provider/some-model"})

	disabledKey := schemas.Key{ID: "key-1", Enabled: schemas.Ptr(false)}
	server := &BifrostHTTPServer{
		Config: libtest.FileOnly(t, &lib.Config{
			ModelCatalog: catalog,
		}, map[schemas.ModelProvider]configstore.ProviderConfig{
			"custom-provider": {Keys: []schemas.Key{disabledKey}},
		}),
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
func newReloadProviderServer(t testing.TB, catalog *modelcatalog.ModelCatalog, provider schemas.ModelProvider, keys []schemas.Key, keyless bool) *BifrostHTTPServer {
	customProviderConfig := &schemas.CustomProviderConfig{
		IsKeyLess:       keyless,
		AllowedRequests: &schemas.AllowedRequests{ListModels: false},
	}
	return &BifrostHTTPServer{
		Ctx:    schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		Client: &bifrost.Bifrost{},
		Config: libtest.FileOnly(t, &lib.Config{
			ConfigStore: &reloadProviderConfigStore{provider: &configstoreTables.TableProvider{
				Name:                 string(provider),
				CustomProviderConfig: customProviderConfig,
			}},
			ModelCatalog: catalog,
		}, map[schemas.ModelProvider]configstore.ProviderConfig{
			provider: {
				Keys:                 keys,
				CustomProviderConfig: customProviderConfig,
			},
		}),
	}
}

var providerRegistrationSequence atomic.Uint64

type providerRegistrationFixture struct {
	router                  *router.Router
	server                  *BifrostHTTPServer
	provider                schemas.ModelProvider
	createBody              string
	targetCalls, otherCalls *atomic.Int64
}

// Discovery is disabled so it cannot warm the target. The virtual key gives
// all load-balancing weight to a competitor; the explicit target prefix must win.
func newProviderRegistrationFixture(t *testing.T) *providerRegistrationFixture {
	t.Helper()
	previousLogger := logger
	logger = noopTestLogger{}
	t.Cleanup(func() { logger = previousLogger })
	handlers.SetLogger(noopTestLogger{})
	lib.SetLogger(noopTestLogger{})
	target := schemas.ModelProvider(fmt.Sprintf("registration-%d", providerRegistrationSequence.Add(1)))
	other := schemas.ModelProvider(string(target) + "-other")
	var targetCalls, otherCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		var request struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "registration-model" {
			http.Error(w, "unexpected model or discovery request", http.StatusBadRequest)
			return
		}
		const response = `{"id":"resp_registration","object":"response","status":"completed","model":"registration-model","output":[{"id":"msg_registration","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"registration-ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			if r.URL.Path == "/v1/responses" {
				fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
			} else {
				fmt.Fprint(w, "data: {\"id\":\"chat_registration\",\"object\":\"chat.completion.chunk\",\"model\":\"registration-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"registration-ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat_registration\",\"object\":\"chat.completion.chunk\",\"model\":\"registration-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/responses" {
			fmt.Fprint(w, response)
		} else {
			fmt.Fprint(w, `{"id":"chat_registration","object":"chat.completion","model":"registration-model","choices":[{"index":0,"message":{"role":"assistant","content":"registration-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		}
	}))
	t.Cleanup(upstream.Close)
	competitor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherCalls.Add(1)
		http.Error(w, "explicit provider was load balanced", http.StatusBadGateway)
	}))
	t.Cleanup(competitor.Close)
	pc := configstore.ProviderConfig{
		NetworkConfig:            &schemas.NetworkConfig{BaseURL: competitor.URL, AllowPrivateNetwork: true, MaxRetries: 0},
		ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 2},
		CustomProviderConfig:     &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, IsKeyLess: true},
	}
	cc := lib.DefaultClientConfig
	cfg := libtest.WithProviders(t, &lib.Config{ClientConfig: &cc, ModelCatalog: catalogForTest(t, nil)}, nil)
	// The key is written to the config store and brought into the live store, which the governance
	// store reads it from.
	require.NoError(t, cfg.ConfigStore.CreateVirtualKey(context.Background(), &configstoreTables.TableVirtualKey{
		ID: "registration-vk", Name: "registration-vk", Value: *schemas.NewSecretVar("sk-bf-registration"), IsActive: schemas.Ptr(true),
		ProviderConfigs: []configstoreTables.TableVirtualKeyProviderConfig{
			{Provider: string(target), AllowedModels: schemas.WhiteList{"*"}, AllowAllKeys: true, Weight: schemas.Ptr(0.0)},
			{Provider: string(other), AllowedModels: schemas.WhiteList{"*"}, AllowAllKeys: true, Weight: schemas.Ptr(1.0)},
		},
	}))
	require.NoError(t, cfg.LiveStore.Refresh(context.Background(), []livestore.Ref{{Kind: livestore.KindVirtualKey, ID: "registration-vk"}}))
	gov, err := governance.Init(context.Background(), nil, noopTestLogger{}, nil, cfg.LiveStore, nil, nil)
	require.NoError(t, err)
	routingPlugin, err := routing.Init(context.Background(), nil, noopTestLogger{}, nil, cfg.LiveStore, gov)
	require.NoError(t, err)
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{Account: lib.NewBaseAccount(cfg), Logger: noopTestLogger{}, LLMPlugins: []schemas.LLMPlugin{gov, routingPlugin}})
	require.NoError(t, err)
	t.Cleanup(func() {
		client.Shutdown()
		for _, provider := range []schemas.ModelProvider{target, other} {
			schemas.UnregisterKnownProvider(provider)
		}
	})
	cfg.SetBifrostClient(client)
	require.NoError(t, cfg.AddProvider(context.Background(), other, pc))
	cfg.BasePlugins.Store(&[]schemas.BasePlugin{gov, routingPlugin})
	s := &BifrostHTTPServer{Config: cfg, Client: client, Ctx: schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)}
	r := router.New()
	handlers.NewProviderHandler(s, cfg, client).RegisterRoutes(r)
	handlers.NewInferenceHandler(s, client, cfg).RegisterRoutes(r)
	integrations.NewOpenAIRouter(client, cfg, s, noopTestLogger{}).RegisterRoutes(r)
	createBody := fmt.Sprintf(`{"provider":%q,"network_config":{"base_url":%q,"allow_private_network":true,"max_retries":0},"concurrency_and_buffer_size":{"concurrency":1,"buffer_size":2},"custom_provider_config":{"base_provider_type":"openai","is_key_less":true,"allowed_requests":{"chat_completion":true,"chat_completion_stream":true,"responses":true,"responses_stream":true}}}`, target, upstream.URL)
	return &providerRegistrationFixture{router: r, server: s, provider: target, createBody: createBody, targetCalls: &targetCalls, otherCalls: &otherCalls}
}

func (f *providerRegistrationFixture) startGateway(t *testing.T) (string, *http.Client) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	gateway := &fasthttp.Server{Handler: f.router.Handler}
	served := make(chan error, 1)
	go func() { served <- gateway.Serve(listener) }()
	t.Cleanup(func() {
		// This is a one-shot listener with non-keepalive requests. Shutdown
		// resets fasthttp's Done channel while SQL cancellation watchers can
		// still retire; closing the listener keeps that channel stable.
		require.NoError(t, listener.Close())
		if err := <-served; err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("test gateway failed: %v", err)
		}
	})
	return "http://" + listener.Addr().String(), &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
}

func providerRegistrationHTTPRequest(t *testing.T, client *http.Client, method, url, body, virtualKey string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if virtualKey != "" {
		req.Header.Set("x-bf-vk", virtualKey)
	}
	response, err := client.Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, string(data)
}

func TestProviderRegistration_FirstHTTPRequestHonorsPrefix(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		noKeys           bool
	}{
		{"chat", "/v1/chat/completions", `{"model":%q,"messages":[{"role":"user","content":"Hi"}]}`, false},
		{"chat_stream", "/v1/chat/completions", `{"model":%q,"messages":[{"role":"user","content":"Hi"}],"stream":true}`, false},
		{"responses", "/v1/responses", `{"model":%q,"input":"Hi"}`, false},
		{"responses_stream", "/v1/responses", `{"model":%q,"input":"Hi","stream":true}`, false},
		{"openai_chat", "/openai/v1/chat/completions", `{"model":%q,"messages":[{"role":"user","content":"Hi"}]}`, false},
		{"openai_chat_stream", "/openai/v1/chat/completions", `{"model":%q,"messages":[{"role":"user","content":"Hi"}],"stream":true}`, false},
		{"openai_responses", "/openai/v1/responses", `{"model":%q,"input":"Hi"}`, false},
		{"openai_responses_stream", "/openai/v1/responses", `{"model":%q,"input":"Hi","stream":true}`, false},
		{"chat_without_keys", "/v1/chat/completions", `{"model":%q,"messages":[{"role":"user","content":"Hi"}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newProviderRegistrationFixture(t)
			if tc.noKeys {
				fixture.createBody = strings.Replace(fixture.createBody, `"is_key_less":true`, `"is_key_less":false`, 1)
			}
			baseURL, httpClient := fixture.startGateway(t)
			status, body := providerRegistrationHTTPRequest(t, httpClient, "POST", baseURL+"/api/providers", fixture.createBody, "")
			require.Equal(t, 200, status, body)
			require.Zero(t, fixture.targetCalls.Load(), "discovery must not warm the provider")

			status, body = providerRegistrationHTTPRequest(t, httpClient, "POST", baseURL+tc.path, fmt.Sprintf(tc.body, string(fixture.provider)+"/registration-model"), "sk-bf-registration")
			if tc.noKeys {
				require.Equal(t, 400, status, body)
				var response schemas.BifrostError
				require.NoError(t, json.Unmarshal([]byte(body), &response))
				assert.Equal(t, fixture.provider, response.ExtraFields.Provider)
				assert.Contains(t, body, "no keys found")
				assert.Zero(t, fixture.targetCalls.Load(), "registration must not bypass credential requirements")
			} else {
				require.Equal(t, 200, status, body)
				assert.Contains(t, body, "registration-ok")
				assert.EqualValues(t, 1, fixture.targetCalls.Load())
			}
			assert.Zero(t, fixture.otherCalls.Load(), "explicit prefix must bypass provider load balancing")
		})
	}
}

// reloadWithRequest sends a first inference request to the new provider while its first post-create
// reload is in flight, runs during (if set), then fails that reload when fail is set so the create is
// rolled back. Later reloads pass straight through.
type reloadWithRequest struct {
	*BifrostHTTPServer
	t              *testing.T
	client         *http.Client
	baseURL, model string
	fail           bool
	during         func()
	calls          atomic.Int32
	status         int
	body           string
}

func (r *reloadWithRequest) ReloadProvider(ctx context.Context, provider schemas.ModelProvider, isNew bool) (*configstoreTables.TableProvider, error) {
	table, err := r.BifrostHTTPServer.ReloadProvider(ctx, provider, isNew)
	if err != nil || r.calls.Add(1) > 1 {
		return table, err
	}
	r.status, r.body = providerRegistrationHTTPRequest(r.t, r.client, "POST", r.baseURL+"/v1/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"Hi"}]}`, r.model), "sk-bf-registration")
	if r.during != nil {
		r.during()
	}
	if r.fail {
		return nil, errors.New("reload failed")
	}
	return table, nil
}

func startReloadWithRequest(t *testing.T, fixture *providerRegistrationFixture, fail bool) (*reloadWithRequest, string, *http.Client) {
	t.Helper()
	manager := &reloadWithRequest{BifrostHTTPServer: fixture.server, t: t, model: string(fixture.provider) + "/registration-model", fail: fail}
	r := router.New()
	handlers.NewProviderHandler(manager, fixture.server.Config, fixture.server.Client).RegisterRoutes(r)
	handlers.NewInferenceHandler(fixture.server, fixture.server.Client, fixture.server.Config).RegisterRoutes(r)
	fixture.router = r
	baseURL, httpClient := fixture.startGateway(t)
	manager.baseURL, manager.client = baseURL, httpClient
	return manager, baseURL, httpClient
}

// Create persists the configuration before the post-create reload (model discovery, up to 15s), so
// the prefix must already route during that reload, not only once the create response is sent.
func TestProviderRegistration_PrefixRoutesDuringPostCreateReload(t *testing.T) {
	fixture := newProviderRegistrationFixture(t)
	manager, baseURL, httpClient := startReloadWithRequest(t, fixture, false)

	status, body := providerRegistrationHTTPRequest(t, httpClient, "POST", baseURL+"/api/providers", fixture.createBody, "")
	require.Equal(t, 200, status, body)
	require.Equal(t, 200, manager.status, manager.body)
	assert.Contains(t, manager.body, "registration-ok")
	assert.EqualValues(t, 1, fixture.targetCalls.Load())
	assert.Zero(t, fixture.otherCalls.Load(), "a request during the reload lost its prefix and was load balanced")
}

// deleteObserver reports each provider deletion to onDelete before passing it to the store.
type deleteObserver struct {
	configstore.ConfigStore
	onDelete func(schemas.ModelProvider)
}

func (d *deleteObserver) DeleteProvider(ctx context.Context, provider schemas.ModelProvider, tx ...*gorm.DB) error {
	d.onDelete(provider)
	return d.ConfigStore.DeleteProvider(ctx, provider, tx...)
}

// A request during the post-create reload prepares the provider in core. A failed create must stop
// that runtime provider, not only drop the configuration and the prefix. The configuration goes
// first: while it is still stored, a request can lazily prepare a provider that was just stopped.
func TestProviderRegistration_FailedCreateStopsRuntimeProvider(t *testing.T) {
	fixture := newProviderRegistrationFixture(t)
	runningAtConfigDelete := false
	fixture.server.Config.ConfigStore = &deleteObserver{ConfigStore: fixture.server.Config.ConfigStore, onDelete: func(provider schemas.ModelProvider) {
		if provider == fixture.provider {
			// GetConfiguredProviders reads only live providers; GetProviderByKey would lazily prepare one.
			running, _ := fixture.server.Client.GetConfiguredProviders()
			runningAtConfigDelete = slices.Contains(running, provider)
		}
	}}
	manager, baseURL, httpClient := startReloadWithRequest(t, fixture, true)

	status, body := providerRegistrationHTTPRequest(t, httpClient, "POST", baseURL+"/api/providers", fixture.createBody, "")
	require.Equal(t, 500, status, body)
	require.Equal(t, 200, manager.status, manager.body)
	require.EqualValues(t, 1, fixture.targetCalls.Load(), "the in-window request should have reached the new provider")
	assert.True(t, runningAtConfigDelete, "runtime provider was stopped while its configuration was still stored, so a request could prepare it again")
	assert.Nil(t, fixture.server.Client.GetProviderByKey(fixture.provider), "rolled-back provider is still running in core")
	assert.False(t, schemas.IsKnownProvider(string(fixture.provider)), "rolled-back provider prefix is still registered")
	assert.False(t, fixture.server.Config.IsProviderConfigured(fixture.provider), "rolled-back provider configuration is still stored")
}

// A rolled-back create removes the configuration and then stops the runtime provider. A second
// create for the same name must not run in between: its configuration would pass the absence
// check and the rollback would then stop its runtime and unregister its prefix. Creates for one
// name are serialized, so the second create waits for the rollback and then keeps its provider.
func TestProviderRegistration_ConcurrentCreateWaitsForRollback(t *testing.T) {
	fixture := newProviderRegistrationFixture(t)
	manager, baseURL, httpClient := startReloadWithRequest(t, fixture, true)
	second := make(chan [2]any, 1)
	manager.during = func() {
		go func() {
			status, body := providerRegistrationHTTPRequest(t, httpClient, "POST", baseURL+"/api/providers", fixture.createBody, "")
			second <- [2]any{status, body}
		}()
		time.Sleep(200 * time.Millisecond) // let the second create reach the handler while this one is in flight
	}

	status, body := providerRegistrationHTTPRequest(t, httpClient, "POST", baseURL+"/api/providers", fixture.createBody, "")
	require.Equal(t, 500, status, body)
	result := <-second
	require.Equal(t, 200, result[0], result[1])
	assert.True(t, schemas.IsKnownProvider(string(fixture.provider)), "the rollback unregistered the second create's prefix")

	status, body = providerRegistrationHTTPRequest(t, httpClient, "POST", baseURL+"/v1/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"Hi"}]}`, string(fixture.provider)+"/registration-model"), "sk-bf-registration")
	require.Equal(t, 200, status, body)
	assert.Zero(t, fixture.otherCalls.Load(), "the second create's provider lost its prefix")
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

	catalog := catalogForTest(t, nil)
	catalog.UpsertLive("custom-provider", "key-1", false, []string{"some-model"})
	catalog.UpsertLive("custom-provider", "key-1", true, []string{"some-model", "unlisted-model"})

	server := newReloadProviderServer(t, catalog, "custom-provider", []schemas.Key{{ID: "key-1"}}, false)

	if _, err := server.ReloadProvider(context.Background(), "custom-provider", false); err != nil {
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

	catalog := catalogForTest(t, nil)
	catalog.UpsertLive("custom-provider", "key-enabled", false, []string{"kept-model"})
	catalog.UpsertLive("custom-provider", "key-disabled", false, []string{"disabled-model"})
	catalog.UpsertLive("custom-provider", "key-removed", false, []string{"removed-model"})
	catalog.UpsertLive("custom-provider", "key-removed", true, []string{"removed-model"})

	keys := []schemas.Key{
		{ID: "key-enabled"},
		{ID: "key-disabled", Enabled: schemas.Ptr(false)},
	}
	server := newReloadProviderServer(t, catalog, "custom-provider", keys, false)

	if _, err := server.ReloadProvider(context.Background(), "custom-provider", false); err != nil {
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

	catalog := catalogForTest(t, nil)
	catalog.UpsertLive("keyless-provider", "", false, []string{"keyless-model"})

	server := newReloadProviderServer(t, catalog, "keyless-provider", nil, true)

	if _, err := server.ReloadProvider(context.Background(), "keyless-provider", false); err != nil {
		t.Fatalf("ReloadProvider returned unexpected error: %v", err)
	}

	if got := catalog.GetModelsForProvider("keyless-provider"); !slices.Equal(got, []string{"keyless-model"}) {
		t.Errorf("keyless catalog after failed refetch = %v, want [keyless-model] retained", got)
	}
}

// TestReloadProvider_ZeroKeyProviderClaimsModelsOnlyWhenKeyless pins that a
// custom provider reloaded with zero keys is a catalog candidate for a bare
// model name only when it is keyless. One that needs keys (its only key was
// rejected at save) used to claim every model and win the resolver's
// alphabetical pick, so POST /openai/v1/embeddings {"model":"text-embedding-005"}
// failed with no_key_supports_model instead of reaching vertex.
func TestReloadProvider_ZeroKeyProviderClaimsModelsOnlyWhenKeyless(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	const provider schemas.ModelProvider = "harness-oauthkey-run"
	for _, keyless := range []bool{false, true} {
		t.Run(fmt.Sprintf("keyless=%v", keyless), func(t *testing.T) {
			catalog := catalogForTest(t, nil)
			server := newReloadProviderServer(t, catalog, provider, nil, keyless)
			if _, err := server.ReloadProvider(context.Background(), provider, false); err != nil {
				t.Fatalf("ReloadProvider returned unexpected error: %v", err)
			}
			got := slices.Contains(catalog.GetProvidersForModel("text-embedding-005"), provider)
			if got != keyless {
				t.Fatalf("zero-key provider (keyless=%v) is a candidate for text-embedding-005: %v, want %v", keyless, got, keyless)
			}
		})
	}
}

// TestReloadProvider_KeylessPrunesResidualKeyEntries pins that a keyless
// provider keeps only its "" sentinel entry across a reload. Entries cached
// under a residual key's ID (written before issue #8500's fix, when keyless
// discovery still fanned out per enabled key) are never refetched, so keeping
// them would advertise their models indefinitely.
func TestReloadProvider_KeylessPrunesResidualKeyEntries(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	catalog := catalogForTest(t, nil)
	catalog.UpsertLive("keyless-provider", "", false, []string{"keyless-model"})
	catalog.UpsertLive("keyless-provider", "residual-key", false, []string{"residual-model"})

	server := newReloadProviderServer(t, catalog, "keyless-provider", []schemas.Key{{ID: "residual-key"}}, true)

	if _, err := server.ReloadProvider(context.Background(), "keyless-provider", false); err != nil {
		t.Fatalf("ReloadProvider returned unexpected error: %v", err)
	}

	if got := catalog.GetModelsForProvider("keyless-provider"); !slices.Equal(got, []string{"keyless-model"}) {
		t.Errorf("keyless catalog after reload = %v, want only [keyless-model] (residual key entry pruned)", got)
	}
}

// TestReloadProvider_NoKeysDropsEverything pins the branch that must keep
// wiping: a keyed provider with no keys left can serve nothing, so no cached
// entry is still meaningful and none will ever be refreshed.
func TestReloadProvider_NoKeysDropsEverything(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	catalog := catalogForTest(t, nil)
	catalog.UpsertLive("custom-provider", "key-1", false, []string{"orphaned-model"})

	server := newReloadProviderServer(t, catalog, "custom-provider", nil, false)

	if _, err := server.ReloadProvider(context.Background(), "custom-provider", false); err != nil {
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

	catalog := catalogForTest(t, nil)
	catalog.UpsertLive("custom-provider", "key-1", false, []string{"some-model"})
	catalog.UpsertLive("custom-provider", "key-1", true, []string{"some-model", "unlisted-model"})

	server := newReloadProviderServer(t, catalog, "custom-provider", []schemas.Key{{ID: "key-1"}}, false)

	const reloaders = 4
	const readers = 4
	const iterations = 50

	var wg sync.WaitGroup
	for range reloaders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				if _, err := server.ReloadProvider(context.Background(), "custom-provider", false); err != nil {
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

func TestNotificationPublisher_ResolvesPublisherSetAfterRegistration(t *testing.T) {
	s := &BifrostHTTPServer{Config: &lib.Config{}}
	// Captured while the notification service does not exist yet, as RegisterAPIRoutes
	// does when Bootstrap has not run.
	publish := s.notificationPublisher()
	require.NotNil(t, publish, "a handler registered early must still reach the publisher set later")

	var got []schemas.NotificationInput
	s.Config.NotificationPublisher = func(_ context.Context, input schemas.NotificationInput) (*schemas.Notification, error) {
		got = append(got, input)
		return &schemas.Notification{}, nil
	}
	_, err := publish(context.Background(), schemas.NotificationInput{Title: "late"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "late", got[0].Title)
}

// TestReloadProxyConfigUpdatesHTTPClientFactory pins that saving the global proxy reaches
// the factory behind webhooks, skills and plugin downloads, not only provider inference.
func TestReloadProxyConfigUpdatesHTTPClientFactory(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	factory := network.NewHTTPClientFactory(nil, nil)
	s := &BifrostHTTPServer{Config: &lib.Config{LiveStore: liveStoreForTest(t)}, HTTPClientFactory: factory}
	proxy := &configstoreTables.GlobalProxyConfig{Enabled: true, Type: network.GlobalProxyTypeHTTP, URL: "http://10.0.0.9:3128", EnableForAPI: true}

	if err := s.ReloadProxyConfig(context.Background(), proxy); err != nil {
		t.Fatalf("ReloadProxyConfig: %v", err)
	}
	got := factory.GetProxyConfig()
	if got == nil || !got.Enabled || got.URL != proxy.URL || !got.EnableForAPI {
		t.Fatalf("factory proxy config = %+v, want the reloaded one", got)
	}
}

// TestReloadProxyConfigUpdatesConfigFactory pins that a server running its own bootstrap
// (enterprise never sets s.HTTPClientFactory) still pushes a proxy change to the
// config's factory, the one registered as the process default.
func TestReloadProxyConfigUpdatesConfigFactory(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	factory := network.NewHTTPClientFactory(nil, nil)
	s := &BifrostHTTPServer{Config: &lib.Config{LiveStore: liveStoreForTest(t), HTTPClientFactory: factory}}
	proxy := &configstoreTables.GlobalProxyConfig{Enabled: true, Type: network.GlobalProxyTypeHTTP, URL: "http://10.0.0.9:3128", EnableForAPI: true}
	if err := s.ReloadProxyConfig(context.Background(), proxy); err != nil {
		t.Fatalf("ReloadProxyConfig: %v", err)
	}
	if got := factory.GetProxyConfig(); got == nil || got.URL != proxy.URL {
		t.Fatalf("config factory proxy = %+v, want the reloaded one", got)
	}
}

// TestReloadProxyConfigAcceptsRemoval pins that removing the global proxy (a nil config,
// which enterprise passes on removal) clears it everywhere instead of panicking.
func TestReloadProxyConfigAcceptsRemoval(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	factory := network.NewHTTPClientFactory(&network.GlobalProxyConfig{Enabled: true, URL: "http://10.0.0.9:3128", EnableForAPI: true}, nil)
	s := &BifrostHTTPServer{Config: &lib.Config{LiveStore: liveStoreForTest(t), HTTPClientFactory: factory}}
	if err := s.ReloadProxyConfig(context.Background(), nil); err != nil {
		t.Fatalf("ReloadProxyConfig(nil): %v", err)
	}
	if got := factory.GetProxyConfig(); got != nil {
		t.Fatalf("factory proxy = %+v, want none after removal", got)
	}
}

// ctxRecordingVectorStore records the context the semantic cache constructor creates its
// namespace with, which it derives from its own, and whether it was already done then; the
// constructor cancels it on return. That is how a test observes the context ReloadPlugin
// handed the plugin constructor.
type ctxRecordingVectorStore struct {
	vectorstore.VectorStore
	gotCtx context.Context
	errAt  error
}

// errStopAfterInstantiation ends ReloadPlugin at the constructor, before it needs a live
// Bifrost client, so the test can inspect the context the constructor was given.
var errStopAfterInstantiation = errors.New("stop after instantiation")

// CreateNamespace records ctx and stops the constructor.
func (s *ctxRecordingVectorStore) CreateNamespace(ctx context.Context, _ string, _ int, _ map[string]vectorstore.VectorStoreProperties) error {
	s.gotCtx, s.errAt = ctx, ctx.Err()
	return errStopAfterInstantiation
}

// A plugin outlives the request that loaded it, so ReloadPlugin must hand its constructor
// the server-lifetime context, never the pooled *fasthttp.RequestCtx an admin reload
// arrives on. Deriving long-lived work from the request context is what produced the
// "missing cancel error" panic on shutdown.
func TestReloadPluginGivesTheConstructorTheServerContext(t *testing.T) {
	SetLogger(bifrost.NewNoOpLogger())

	serverCtx, cancelServer := schemas.NewBifrostContextWithCancel(
		context.WithValue(context.Background(), schemas.BifrostContextKeyGovernancePluginName, "governance-custom"),
	)
	defer cancelServer()

	store := &ctxRecordingVectorStore{}
	s := &BifrostHTTPServer{Ctx: serverCtx, Config: &lib.Config{VectorStore: store}}

	// The admin reload request is already over by the time the constructor runs.
	reloadReqCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()

	err := s.ReloadPlugin(reloadReqCtx, semanticcache.PluginName, nil, map[string]any{"dimension": 1}, nil, nil)

	if store.gotCtx == nil {
		t.Fatal("the plugin constructor never reached the store")
	}
	if cerr := store.errAt; cerr != nil {
		t.Errorf("constructor context was already cancelled (%v); it must be the server context, not the reload request's", cerr)
	}
	if !errors.Is(err, errStopAfterInstantiation) {
		t.Errorf("ReloadPlugin error = %v, want the constructor's own error", err)
	}
	if got := governancePluginNameFromContext(store.gotCtx); got != "governance-custom" {
		t.Errorf("constructor context governance plugin name = %q, want it inherited from the server context", got)
	}
}

// mismatchVectorStore reports every namespace as already holding vectors of another size, which is
// how a semantic cache config change that the store cannot serve fails to load.
type mismatchVectorStore struct {
	vectorstore.VectorStore
}

func (mismatchVectorStore) CreateNamespace(_ context.Context, namespace string, dimension int, _ map[string]vectorstore.VectorStoreProperties) error {
	return fmt.Errorf("%w: namespace %q already exists with dimension 1536 but config requires %d", vectorstore.ErrDimensionMismatch, namespace, dimension)
}

// loadedStubPlugin stands in for an instance that is already running under a name.
type loadedStubPlugin struct{ name string }

func (p loadedStubPlugin) GetName() string { return p.name }
func (p loadedStubPlugin) Cleanup() error  { return nil }

// A reload whose new instance fails to load leaves the running one in place, so the status keeps
// describing that instance rather than reporting an error for a plugin that is still serving. With
// nothing running, the failure is the plugin's state and is reported as an error.
func TestReloadPluginFailureKeepsTheRunningInstanceStatus(t *testing.T) {
	SetLogger(bifrost.NewNoOpLogger())
	directOnly := map[string]any{"dimension": 1}

	t.Run("an instance is running", func(t *testing.T) {
		cfg := &lib.Config{VectorStore: mismatchVectorStore{}}
		running := []schemas.BasePlugin{loadedStubPlugin{name: semanticcache.PluginName}}
		cfg.BasePlugins.Store(&running)
		cfg.UpdatePluginOverallStatus(semanticcache.PluginName, semanticcache.PluginName, schemas.PluginStatusActive, nil, []schemas.PluginType{schemas.PluginTypeLLM})
		s := &BifrostHTTPServer{Ctx: schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), Config: cfg}

		err := s.ReloadPlugin(context.Background(), semanticcache.PluginName, nil, directOnly, nil, nil)
		require.ErrorIs(t, err, vectorstore.ErrDimensionMismatch)

		status, ok := cfg.GetPluginStatusByName(semanticcache.PluginName)
		require.True(t, ok)
		assert.Equal(t, schemas.PluginStatusActive, status.Status, "the running instance is still serving")
	})

	t.Run("nothing is running", func(t *testing.T) {
		cfg := &lib.Config{VectorStore: mismatchVectorStore{}}
		s := &BifrostHTTPServer{Ctx: schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), Config: cfg}

		err := s.ReloadPlugin(context.Background(), semanticcache.PluginName, nil, directOnly, nil, nil)
		require.ErrorIs(t, err, vectorstore.ErrDimensionMismatch)

		status, ok := cfg.GetPluginStatusByName(semanticcache.PluginName)
		require.True(t, ok)
		assert.Equal(t, schemas.PluginStatusError, status.Status)
	})
}

// semantic_cache configs are checked when the plugin is about to run, not when one is saved. The
// plugins API loads a plugin only when it is enabled, so a cache that no longer passes (an
// embedding provider since deleted, a row saved before validation existed) can still be disabled.
// A config refused at load wraps lib.ErrInvalidPluginConfig and touches no plugin status.
func TestReloadPluginValidatesSemanticCacheConfig(t *testing.T) {
	SetLogger(bifrost.NewNoOpLogger())
	cfg := libtest.FileOnly(t, &lib.Config{
		VectorStore: mismatchVectorStore{},
	}, map[schemas.ModelProvider]configstore.ProviderConfig{schemas.OpenAI: {}})
	s := &BifrostHTTPServer{Ctx: schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), Config: cfg}

	rejected := []struct {
		name   string
		config map[string]any
		want   string
	}{
		{"empty config", map[string]any{}, "requires 'provider' for semantic mode"},
		{"dimension 0", map[string]any{"dimension": 0}, "'dimension' must be >= 1"},
		{"provider with dimension 1", map[string]any{"provider": "openai", "dimension": 1}, "requires 'dimension' > 1 when 'provider' is set"},
		{"provider without embedding_model", map[string]any{"provider": "openai", "dimension": 1536}, "requires 'embedding_model'"},
		{"empty embedding_model", map[string]any{"provider": "openai", "embedding_model": "", "dimension": 1536}, "non-empty 'embedding_model'"},
		{"provider not configured", map[string]any{"provider": "missing", "embedding_model": "text-embedding-3-small", "dimension": 1536}, "failed to get provider config"},
		{"ttl abc", map[string]any{"dimension": 1, "ttl": "abc"}, "failed to parse TTL duration string 'abc'"},
		{"negative ttl", map[string]any{"dimension": 1, "ttl": "-5s"}, "TTL must be non-negative"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			err := s.ReloadPlugin(context.Background(), semanticcache.PluginName, nil, tc.config, nil, nil)
			require.ErrorIs(t, err, lib.ErrInvalidPluginConfig)
			assert.Contains(t, err.Error(), tc.want)
			_, hasStatus := cfg.GetPluginStatusByName(semanticcache.PluginName)
			assert.False(t, hasStatus, "a refused config must leave no plugin status behind")
		})
	}

	t.Run("saving does not validate, so a cache that no longer passes can be disabled", func(t *testing.T) {
		_, err := s.NormalizePluginConfig(semanticcache.PluginName, map[string]any{"provider": "missing", "embedding_model": "text-embedding-3-small", "dimension": 1536})
		require.NoError(t, err)
	})

	t.Run("a valid config goes on to load, nulls included", func(t *testing.T) {
		// The mismatch store refuses every namespace, so a config that passed validation ends in
		// the store's error, not the validation one.
		err := s.ReloadPlugin(context.Background(), semanticcache.PluginName, nil, map[string]any{"provider": nil, "embedding_model": nil, "dimension": 1, "ttl": "10m"}, nil, nil)
		require.ErrorIs(t, err, vectorstore.ErrDimensionMismatch)
		assert.NotErrorIs(t, err, lib.ErrInvalidPluginConfig)
	})
}

// The accessor's own contract: it tracks the server, not the request that called it.
func TestPluginInitContextOutlivesTheReloadRequest(t *testing.T) {
	serverCtx, cancelServer := schemas.NewBifrostContextWithCancel(
		context.WithValue(context.Background(), schemas.BifrostContextKeyGovernancePluginName, "governance-custom"),
	)
	defer cancelServer()
	initCtx := (&BifrostHTTPServer{Ctx: serverCtx}).PluginInitContext()

	if got := governancePluginNameFromContext(initCtx); got != "governance-custom" {
		t.Errorf("governance plugin name = %q, want it inherited from the server context", got)
	}
	// Shutdown must still reach the plugin, or nothing releases its long-lived work.
	cancelServer()
	select {
	case <-initCtx.Done():
	case <-time.After(time.Second):
		t.Error("plugin init context outlived server shutdown; it must be the server context")
	}
}

// A server with no context yet must still yield a usable, never-cancelled context rather
// than a nil one a constructor would panic on.
func TestPluginInitContextFallsBackToBackground(t *testing.T) {
	initCtx := (&BifrostHTTPServer{}).PluginInitContext()
	if initCtx == nil {
		t.Fatal("PluginInitContext returned nil")
	}
	select {
	case <-initCtx.Done():
		t.Error("fallback context is already cancelled")
	default:
	}
}

// issue8212CleanupEvent records when a real deletion ran and which cutoff it used.
type issue8212CleanupEvent struct {
	startedAt time.Time
	cutoff    time.Time
	deleted   int64
	err       error
}

// issue8212RetentionObserver records real SQLite deletions without changing
// the cutoff, batch size, or result.
type issue8212RetentionObserver struct {
	manager logstore.LogRetentionManager
	events  chan issue8212CleanupEvent
}

// failingRetentionConfigStore simulates a failed configuration save.
type failingRetentionConfigStore struct {
	configstore.ConfigStore
}

// UpdateClientConfig rejects the save so the test can verify retention stays unchanged.
func (failingRetentionConfigStore) UpdateClientConfig(context.Context, *configstore.ClientConfig) error {
	return fmt.Errorf("client config persistence failed")
}

// DeleteLogsBatch delegates to SQLite and reports the completed deletion.
func (m *issue8212RetentionObserver) DeleteLogsBatch(ctx context.Context, cutoff time.Time, size int) (int64, error) {
	startedAt := time.Now()
	deleted, err := m.manager.DeleteLogsBatch(ctx, cutoff, size)
	m.events <- issue8212CleanupEvent{startedAt: startedAt, cutoff: cutoff, deleted: deleted, err: err}
	return deleted, err
}

// issue8212AwaitCleanup waits for a deletion and fails if it errors or never runs.
func issue8212AwaitCleanup(t *testing.T, m *issue8212RetentionObserver) issue8212CleanupEvent {
	t.Helper()
	select {
	case event := <-m.events:
		if event.err != nil {
			t.Fatalf("real SQLite deletion failed: %v", event.err)
		}
		return event
	case <-time.After(10 * time.Second):
		t.Fatal("scheduled cleanup did not run")
		return issue8212CleanupEvent{}
	}
}

// TestIssue8212RetentionChange verifies saved retention reaches the next scheduled
// pass of the same running cleaner, using virtual time and real SQLite stores.
func TestIssue8212RetentionChange(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		oldDays, newDays     int
		wantStatus, wantDays int
		failPersistence      bool
	}{
		{"increase_365_to_1000", 365, 1000, 200, 1000, false},
		{"decrease_1000_to_365", 1000, 365, 200, 365, false},
		{"reject_zero", 365, 0, 400, 365, false},
		{"reject_negative", 365, -1, 400, 365, false},
		{"persistence_failure", 365, 1000, 500, 365, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				logger = noopTestLogger{}
				handlers.SetLogger(noopTestLogger{})
				cs, err := configstore.NewConfigStore(ctx, &configstore.Config{
					Enabled: true, Type: configstore.ConfigStoreTypeSQLite,
					Config: &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
				}, noopTestLogger{})
				if err != nil {
					t.Fatal(err)
				}
				defer cs.Close(ctx)
				ls, err := logstore.NewLogStore(ctx, &logstore.Config{
					Enabled: true, Type: logstore.LogStoreTypeSQLite,
					Config: &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "logs.db")},
				}, noopTestLogger{})
				if err != nil {
					t.Fatal(err)
				}
				defer ls.Close(ctx)
				initial := &configstore.ClientConfig{LogRetentionDays: tc.oldDays}
				if err := cs.UpdateClientConfig(ctx, initial); err != nil {
					t.Fatal(err)
				}
				initial, err = cs.GetClientConfig(ctx)
				if err != nil {
					t.Fatal(err)
				}
				cfg := &lib.Config{ConfigStore: cs, LogsStore: ls, ClientConfig: initial}
				s := &BifrostHTTPServer{Config: cfg}
				manager, ok := ls.(logstore.LogRetentionManager)
				if !ok {
					t.Fatal("SQLite store does not support retention")
				}
				observer := &issue8212RetentionObserver{manager: manager, events: make(chan issue8212CleanupEvent, 64)}
				s.LogsCleaner = logstore.NewLogsCleaner(observer, logstore.CleanerConfig{RetentionDays: initial.LogRetentionDays}, noopTestLogger{})
				s.LogsCleaner.StartCleanupRoutine()
				defer func() {
					s.LogsCleaner.StopCleanupRoutine()
					synctest.Wait()
				}()
				startup := issue8212AwaitCleanup(t, observer)
				if startup.deleted != 0 {
					t.Fatalf("expected empty startup pass, got %d deletions", startup.deleted)
				}
				// Wait for the initial pass to finish and the daily timer to be armed.
				// Virtual time stays still while this goroutine seeds and saves.
				synctest.Wait()
				if tc.failPersistence {
					cfg.ConfigStore = failingRetentionConfigStore{ConfigStore: cs}
				}

				seed := func(id string, ageDays int) {
					ts := time.Now().UTC().AddDate(0, 0, -ageDays)
					if err := ls.Create(ctx, &logstore.Log{ID: id, Timestamp: ts, CreatedAt: ts,
						Object: "chat_completion", Provider: "openai", Model: "test", Status: "success"}); err != nil {
						t.Fatal(err)
					}
				}
				for _, age := range []int{200, 500, 1200} {
					seed(fmt.Sprintf("age-%d", age), age)
				}
				for _, age := range []int{200, 500, 1200} {
					present, err := ls.IsLogEntryPresent(ctx, fmt.Sprintf("age-%d", age))
					if err != nil || !present {
						t.Fatalf("seed age %d missing before save: %v", age, err)
					}
				}

				// Exercise the production route and the actual server reload callback.
				r := router.New()
				handlers.NewConfigHandler(s, cfg).RegisterRoutes(r)
				var req fasthttp.Request
				req.Header.SetMethod("PUT")
				req.Header.SetContentType("application/json")
				req.SetRequestURI("/api/config")
				saveConfig := *initial
				saveConfig.LogRetentionDays = tc.newDays
				body, err := providerUtils.MarshalSorted(struct {
					ClientConfig *configstore.ClientConfig `json:"client_config"`
				}{ClientConfig: &saveConfig})
				if err != nil {
					t.Fatal(err)
				}
				req.SetBody(body)
				var requestCtx fasthttp.RequestCtx
				requestCtx.Init(&req, nil, nil)
				r.Handler(&requestCtx)
				if requestCtx.Response.StatusCode() != tc.wantStatus {
					t.Fatalf("PUT /api/config returned %d, want %d: %s", requestCtx.Response.StatusCode(), tc.wantStatus, requestCtx.Response.Body())
				}
				savedAt := time.Now()
				persisted, err := cs.GetClientConfig(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if persisted.LogRetentionDays != tc.wantDays || cfg.ClientConfig.LogRetentionDays != tc.wantDays {
					t.Fatalf("unexpected retention: DB=%d live=%d expected=%d", persisted.LogRetentionDays, cfg.ClientConfig.LogRetentionDays, tc.wantDays)
				}
				t.Logf("PUT /api/config status=%d; DB retention=%d; live retention=%d", requestCtx.Response.StatusCode(), persisted.LogRetentionDays, cfg.ClientConfig.LogRetentionDays)
				// Cross the production interval (24h plus 15-30m jitter) without
				// restarting the cleaner or changing its scheduler.
				time.Sleep(25 * time.Hour)
				synctest.Wait()

				next := issue8212AwaitCleanup(t, observer)
				if !next.startedAt.After(savedAt) {
					t.Fatal("expected a cleanup pass that started after the save")
				}
				usedDays := int(math.Round(time.Since(next.cutoff).Hours() / 24))
				t.Logf("next scheduled cleanup: retention=%d days; actual SQLite deletions=%d", usedDays, next.deleted)
				for _, age := range []int{200, 500, 1200} {
					present, err := ls.IsLogEntryPresent(ctx, fmt.Sprintf("age-%d", age))
					if err != nil {
						t.Fatal(err)
					}
					want := age < tc.wantDays
					t.Logf("log age=%d days: present=%t; expected=%t", age, present, want)
					if present != want {
						t.Errorf("log age %d days present=%t, want %t for retention=%d", age, present, want, tc.wantDays)
					}
				}
				if usedDays != tc.wantDays {
					t.Errorf("scheduled cleaner used the wrong retention: got %d days, want %d", usedDays, tc.wantDays)
				}

				// A fresh cleaner from persisted settings is the process-restart control.
				s.LogsCleaner.StopCleanupRoutine()
				synctest.Wait()
				func() {
					seed("restart-age-500", 500)
					freshObserver := &issue8212RetentionObserver{manager: manager, events: make(chan issue8212CleanupEvent, 64)}
					fresh := logstore.NewLogsCleaner(freshObserver, logstore.CleanerConfig{RetentionDays: persisted.LogRetentionDays}, noopTestLogger{})
					fresh.StartCleanupRoutine()
					defer func() {
						fresh.StopCleanupRoutine()
						synctest.Wait()
					}()
					event := issue8212AwaitCleanup(t, freshObserver)
					freshDays := int(math.Round(time.Since(event.cutoff).Hours() / 24))
					present, err := ls.IsLogEntryPresent(ctx, "restart-age-500")
					if err != nil {
						t.Fatal(err)
					}
					t.Logf("fresh cleaner retention=%d; 500-day log present=%t", freshDays, present)
					if freshDays != tc.wantDays || present != (500 < tc.wantDays) {
						t.Fatal("fresh cleaner did not honor saved retention")
					}
				}()
			})
		})
	}
}

// The telemetry plugin reads config.CustomLabels, but the loader only ever seeded it
// from client_config.prometheus_labels, so the plugin's own custom_labels were accepted
// and ignored. Both name extra Prometheus labels, so they union.
func TestMergeCustomLabels(t *testing.T) {
	for _, tc := range []struct {
		label  string
		client []string
		plugin []string
		want   []string
	}{
		{"plugin labels alone are honoured", nil, []string{"team"}, []string{"team"}},
		{"client labels alone still work", []string{"environment"}, nil, []string{"environment"}},
		{"both sources union, client first", []string{"environment"}, []string{"team"}, []string{"environment", "team"}},
		{"duplicates collapse", []string{"team"}, []string{"team", "region"}, []string{"team", "region"}},
		{"blank entries are dropped", []string{""}, []string{"team", ""}, []string{"team"}},
		{"neither set yields nil", nil, nil, nil},
	} {
		t.Run(tc.label, func(t *testing.T) {
			got := mergeCustomLabels(tc.client, tc.plugin)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("label %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// The merge must not write into either input's spare capacity, which is how the
// duplicate-label 500s happened before.
func TestMergeCustomLabelsDoesNotAliasItsInputs(t *testing.T) {
	client := make([]string, 1, 8) // spare capacity for an append to scribble into
	client[0] = "environment"

	got := mergeCustomLabels(client, []string{"team"})
	if len(client) != 1 || client[0] != "environment" {
		t.Errorf("client slice mutated: %v", client)
	}
	got[0] = "clobbered"
	if client[0] != "environment" {
		t.Errorf("writing to the result changed the input: %v", client)
	}
}

// newTelemetryPluginForTest builds a telemetry plugin with its own registry, so a test
// can tell which instance recorded a request.
func newTelemetryPluginForTest(t *testing.T) (*telemetry.PrometheusPlugin, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	p, err := telemetry.Init(&telemetry.Config{Registry: reg}, nil, bifrost.NewNoOpLogger())
	if err != nil {
		t.Fatalf("telemetry.Init: %v", err)
	}
	return p, reg
}

func httpRequestsTotal(t *testing.T, reg *prometheus.Registry) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var total float64
	for _, f := range families {
		if f.GetName() != "http_requests_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			total += m.GetCounter().GetValue()
		}
	}
	return total
}

// A config reload constructs a fresh telemetry plugin. The middleware is built once at
// startup, so capturing the plugin pointer there left it recording into the orphaned
// instance and http_* series stopped appearing on /metrics after any reload. Resolving
// per request means the live instance gets the writes.
func TestPrometheusHTTPMiddlewareFollowsAReloadedPlugin(t *testing.T) {
	SetLogger(bifrost.NewNoOpLogger())

	first, firstReg := newTelemetryPluginForTest(t)
	second, secondReg := newTelemetryPluginForTest(t)

	cfg := &lib.Config{}
	cfg.BasePlugins.Store(&[]schemas.BasePlugin{first})
	s := &BifrostHTTPServer{Config: cfg}

	// Built once, as it is at startup, and reused across the reload below.
	mw := s.prometheusHTTPMiddleware()
	handler := mw(func(ctx *fasthttp.RequestCtx) { ctx.Response.SetStatusCode(200) })

	send := func() {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI("/v1/chat/completions")
		ctx.Request.Header.SetMethod("POST")
		handler(ctx)
	}

	send()
	if got := httpRequestsTotal(t, firstReg); got != 1 {
		t.Fatalf("first instance recorded %v requests, want 1", got)
	}

	// The reload: a freshly constructed plugin replaces the one the middleware saw.
	cfg.BasePlugins.Store(&[]schemas.BasePlugin{second})
	send()

	if got := httpRequestsTotal(t, secondReg); got != 1 {
		t.Errorf("reloaded instance recorded %v requests, want 1; the middleware is still writing to the orphaned plugin", got)
	}
	if got := httpRequestsTotal(t, firstReg); got != 1 {
		t.Errorf("orphaned instance recorded %v requests, want it left at 1", got)
	}
}

// With no telemetry plugin loaded the request must still be served.
func TestPrometheusHTTPMiddlewarePassesThroughWithoutThePlugin(t *testing.T) {
	SetLogger(bifrost.NewNoOpLogger())

	s := &BifrostHTTPServer{Config: &lib.Config{}}
	served := false
	handler := s.prometheusHTTPMiddleware()(func(ctx *fasthttp.RequestCtx) { served = true })

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/v1/chat/completions")
	handler(ctx)

	if !served {
		t.Error("request was not served when no telemetry plugin is loaded")
	}
}

// TestApplyBasePath pins the resolve-and-store step that Bootstrap runs, and
// that builds with their own bootstrap (enterprise) call: the flag value wins
// over server.base_path, the normalized result lands on the server, on its
// ServerConfig (created when absent) and in lib.BasePath, and an invalid value
// is an error.
func TestApplyBasePath(t *testing.T) {
	t.Cleanup(func() { lib.SetBasePath("") })

	s := &BifrostHTTPServer{BasePath: "/from-flag/", Config: &lib.Config{ServerConfig: &lib.ServerConfig{BasePath: "/from-config"}}}
	require.NoError(t, s.ApplyBasePath())
	assert.Equal(t, "/from-flag", s.BasePath)
	assert.Equal(t, "/from-flag", s.Config.ServerConfig.BasePath)
	assert.Equal(t, "/from-flag", lib.BasePath())

	s = &BifrostHTTPServer{Config: &lib.Config{ServerConfig: &lib.ServerConfig{BasePath: "/from-config/"}}}
	require.NoError(t, s.ApplyBasePath())
	assert.Equal(t, "/from-config", s.BasePath)
	assert.Equal(t, "/from-config", lib.BasePath())

	s = &BifrostHTTPServer{BasePath: "/bifrost", Config: &lib.Config{}}
	require.NoError(t, s.ApplyBasePath())
	require.NotNil(t, s.Config.ServerConfig)
	assert.Equal(t, "/bifrost", s.Config.ServerConfig.BasePath)

	s = &BifrostHTTPServer{BasePath: "no-slash", Config: &lib.Config{}}
	assert.Error(t, s.ApplyBasePath())
}

// clientConfigOnlyStore serves a fixed client config to ReloadClientConfigFromConfigStore.
type clientConfigOnlyStore struct {
	configstore.ConfigStore
	clientConfig configstore.ClientConfig
}

func (s *clientConfigOnlyStore) GetClientConfig(context.Context) (*configstore.ClientConfig, error) {
	cc := s.clientConfig
	return &cc, nil
}

// reloadSnapshotTracer hands a fixed accumulated response to cancelled-stream estimation.
type reloadSnapshotTracer struct {
	schemas.NoOpTracer
	snapshot *schemas.BifrostResponse
}

func (s *reloadSnapshotTracer) GetAccumulatedResponse(string) *schemas.BifrostResponse {
	return s.snapshot
}

// PUT /api/config no longer applies usage_estimation_tokenizers itself: the reload that
// follows a successful save is the only path that hands the stored list to the client.
// This pins that the reload does, for enabling a family and for clearing the list.
func TestReloadClientConfigFromConfigStore_AppliesUsageEstimationTokenizers(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	t.Cleanup(func() { logger = prevLogger })
	t.Cleanup(func() { streamusage.Configure(nil) })

	store := &clientConfigOnlyStore{}
	cfg := &lib.Config{LiveStore: liveStoreForTest(t), ConfigStore: store, ClientConfig: &configstore.ClientConfig{}}
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: lib.NewBaseAccount(cfg),
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)
	server := &BifrostHTTPServer{Config: cfg, Client: client}

	const model = "claude-opus-4-5"
	tokenizer := func() string {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyTracer, schemas.Tracer(&reloadSnapshotTracer{snapshot: &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{Message: &schemas.ChatMessage{
				Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: new("hello world")},
			}}}},
		}}}))
		ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-reload-tokenizers")
		failure := &schemas.BifrostError{Error: &schemas.ErrorField{Type: new(schemas.RequestCancelled)}}
		streamusage.Estimate(ctx, &schemas.BifrostRequest{RequestType: schemas.ChatCompletionStreamRequest, ChatRequest: &schemas.BifrostChatRequest{
			Model: model, Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("hi")}}},
		}}, model, failure)
		require.NotNil(t, failure.ExtraFields.BilledUsage)
		require.NotNil(t, failure.ExtraFields.BilledUsage.Estimation)
		return failure.ExtraFields.BilledUsage.Estimation.Tokenizer
	}
	require.Equal(t, "cl100k_base", tokenizer(), "no family is enabled before the first reload")

	store.clientConfig.UsageEstimationTokenizers = []string{"anthropic", "llama3"}
	require.NoError(t, server.ReloadClientConfigFromConfigStore(context.Background()))
	assert.Equal(t, []string{"anthropic", "llama3"}, cfg.ClientConfig.UsageEstimationTokenizers, "the live config holds the stored list")
	assert.Equal(t, "anthropic", tokenizer(), "the client estimates with the stored families")

	store.clientConfig.UsageEstimationTokenizers = nil
	require.NoError(t, server.ReloadClientConfigFromConfigStore(context.Background()))
	assert.Empty(t, cfg.ClientConfig.UsageEstimationTokenizers)
	assert.Equal(t, "cl100k_base", tokenizer(), "clearing the stored list returns the client to the base encodings")
}

// The routing plugin bounds captured request vectors by the logs' retention;
// if it stopped satisfying the setter, bootstrap and reload would both skip
// it without a word.
var _ logRetentionSetter = (*routing.RoutingPlugin)(nil)

type retentionRecorder struct{ days func() int }

func (r *retentionRecorder) SetLogRetention(days func() int) { r.days = days }

// A reload builds a fresh routing plugin, which must get the same log
// retention as the one bootstrap wired, or its captured vectors outlive their
// logs. The retention is read at each sweep: 0 while there is no cleaner (logs
// kept forever), then whatever the cleaner holds now.
func TestApplyRoutingLogRetentionReadsTheLiveCleaner(t *testing.T) {
	s := &BifrostHTTPServer{}
	rec := &retentionRecorder{}
	s.applyRoutingLogRetention(rec)
	require.NotNil(t, rec.days)
	assert.Equal(t, 0, rec.days(), "no cleaner: logs are kept forever")

	s.LogsCleaner = logstore.NewLogsCleaner(nil, logstore.CleanerConfig{RetentionDays: 5}, bifrost.NewNoOpLogger())
	assert.Equal(t, 5, rec.days(), "a cleaner built after wiring is still read")
	s.LogsCleaner.UpdateRetentionDays(2)
	assert.Equal(t, 2, rec.days(), "a retention changed at runtime is read at the next sweep")

	s.applyRoutingLogRetention(struct{}{}) // a plugin without the setter is left alone
}

// sharedVectorStore stands in for a vector store shared across nodes
// (Weaviate, Qdrant, ...): request capture is off on a bare Chromem store,
// which lives in one node's memory.
type sharedVectorStore struct{ vectorstore.VectorStore }

// unusedGovernance satisfies the routing engine's required governance for
// tests that never route a request; any call would panic on the nil embed.
type unusedGovernance struct{ routing.Governance }

// Captured request vectors follow their logs through the real log-delete
// subscription: deleting a log removes its vector, through whichever routing
// plugin is registered now, so a replaced plugin does not strand the
// subscription. Removing the routing plugin drops every capture namespace,
// because no plugin is left to forget or age out what was captured.
func TestCapturedVectorsFollowLogDeletesAndTheRoutingPlugin(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()
	ctx := context.Background()
	quiet := bifrost.NewDefaultLogger(schemas.LogLevelError)

	ls, err := logstore.NewLogStore(ctx, &logstore.Config{
		Enabled: true, Type: logstore.LogStoreTypeSQLite,
		Config: &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "logs.db")},
	}, noopTestLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ls.Close(ctx) })
	inner, err := vectorstore.NewVectorStore(ctx, &vectorstore.Config{
		Enabled: true, Type: vectorstore.VectorStoreTypeChromem, Config: vectorstore.ChromemConfig{},
	}, quiet)
	require.NoError(t, err)
	vs := sharedVectorStore{inner} // capture is off on a bare Chromem store
	loggerPlugin, err := logging.Init(ctx, &logging.Config{}, noopTestLogger{}, ls, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = loggerPlugin.Cleanup() })

	// A captured request: its log, and its vector under the log's ID (a UUID
	// request ID is its own point ID) in a capture namespace.
	ns := complexity.CaptureNamespace("support-router", "openai", "m", 2, 1)
	require.NoError(t, vs.CreateNamespace(ctx, ns, 2, nil))
	capture := func(id string) {
		t.Helper()
		require.NoError(t, ls.Create(ctx, &logstore.Log{ID: id, Timestamp: time.Now(), Object: "chat_completion", Provider: "openai", Model: "gpt-4o", Status: "success"}))
		require.NoError(t, vs.Add(ctx, ns, id, []float32{1, 0}, map[string]any{"kind": "request", "timestamp": time.Now().Unix(), "log_id": id}))
	}
	captured := func(id string) bool {
		_, err := vs.GetChunk(ctx, ns, id)
		return err == nil
	}
	newRouting := func() *routing.RoutingPlugin {
		t.Helper()
		ruleStore, err := rules.NewTestRules(ctx, t.TempDir(), quiet)
		require.NoError(t, err)
		t.Cleanup(ruleStore.Close)
		p, err := routing.InitFromStore(ctx, &routing.Config{}, quiet, nil, ruleStore.Store, unusedGovernance{})
		require.NoError(t, err)
		p.SetComplexityWarmupDependencies(vs, nil)
		return p
	}

	cfg := &lib.Config{LiveStore: liveStoreForTest(t), LogsStore: ls, VectorStore: vs}
	first := newRouting()
	t.Cleanup(func() { _ = first.Cleanup() })
	cfg.BasePlugins.Store(&[]schemas.BasePlugin{first})
	s := &BifrostHTTPServer{Config: cfg}
	s.subscribeCaptureDeletes(loggerPlugin)
	logs := loggerPlugin.GetPluginLogManager()

	a, b, c := uuid.NewString(), uuid.NewString(), uuid.NewString()
	capture(a)
	capture(b)
	require.NoError(t, logs.DeleteLog(ctx, a))
	assert.False(t, captured(a), "deleting a log removes its captured vector")
	assert.True(t, captured(b), "other logs keep theirs")

	// A reload replaces the plugin: the subscription reaches the new one.
	second := newRouting()
	cfg.BasePlugins.Store(&[]schemas.BasePlugin{second})
	require.NoError(t, logs.DeleteLogs(ctx, []string{b}))
	assert.False(t, captured(b), "deletes reach the plugin registered now")

	// Removing routing drops what was captured: nothing is left to own it.
	capture(c)
	client, err := bifrost.Init(ctx, schemas.BifrostConfig{
		Account: lib.NewBaseAccount(cfg), Logger: quiet, LLMPlugins: []schemas.LLMPlugin{second},
	})
	require.NoError(t, err)
	defer client.Shutdown()
	s.Client = client
	cfg.UpdatePluginOverallStatus(routing.PluginName, routing.PluginName, schemas.PluginStatusActive, nil, []schemas.PluginType{schemas.PluginTypeLLM})
	require.NoError(t, s.RemovePlugin(ctx, routing.PluginName))
	namespaces, err := vs.ListNamespaces(ctx, complexity.CaptureNamespacePrefix+"_")
	require.NoError(t, err)
	assert.Empty(t, namespaces, "the routing plugin's captures go with it")
	assert.False(t, captured(c))

	// A log deleted with no routing plugin has nothing left to forget.
	require.NoError(t, logs.DeleteLog(ctx, c))
}

// At startup without a routing plugin, captures from an earlier run are
// dropped only when routing is disabled by config; a plugin that failed to
// start keeps them for its next start.
func TestDropCapturesOfDisabledRouting(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()
	ctx := context.Background()
	vs, err := vectorstore.NewVectorStore(ctx, &vectorstore.Config{
		Enabled: true, Type: vectorstore.VectorStoreTypeChromem, Config: vectorstore.ChromemConfig{},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	ns := complexity.CaptureNamespace("support-router", "openai", "m", 2, 1)
	require.NoError(t, vs.CreateNamespace(ctx, ns, 2, nil))
	cfg := &lib.Config{VectorStore: vs}
	s := &BifrostHTTPServer{Config: cfg}
	namespaces := func() []string {
		names, err := vs.ListNamespaces(ctx, complexity.CaptureNamespacePrefix+"_")
		require.NoError(t, err)
		return names
	}

	s.dropCapturesOfDisabledRouting(ctx)
	assert.Equal(t, []string{ns}, namespaces(), "no status: nothing says routing is gone")

	cfg.UpdatePluginOverallStatus(routing.PluginName, routing.PluginName, schemas.PluginStatusError, nil, nil)
	s.dropCapturesOfDisabledRouting(ctx)
	assert.Equal(t, []string{ns}, namespaces(), "a plugin that failed to start keeps its captures")

	cfg.UpdatePluginOverallStatus(routing.PluginName, routing.PluginName, schemas.PluginStatusDisabled, nil, nil)
	s.dropCapturesOfDisabledRouting(ctx)
	assert.Empty(t, namespaces(), "a disabled plugin's captures are dropped")

	(&BifrostHTTPServer{Config: &lib.Config{}}).dropCapturesOfDisabledRouting(ctx) // no vector store: nothing to do
}

// setOpenAPIURL sets the framework config's openapi_url the way a config save
// publishes it: a whole new FrameworkConfig.
func setOpenAPIURL(cfg *lib.Config, value string) {
	cfg.FrameworkConfig = &framework.FrameworkConfig{Pricing: &modelcatalog.Config{OpenAPIURL: &value}}
}

// The Bifrost MCP's settings come from the config: the spec (the framework config's
// openapi_url, else the build's default), the writes switch (on unless turned off),
// and the base its calls address, which is bifrost_url's origin, and nothing
// without it: never the listen address or a request's Host.
func TestBifrostMCPOptionsFollowClientConfig(t *testing.T) {
	server := &BifrostHTTPServer{
		Host: "0.0.0.0", Port: "8080", defaultOpenAPIURL: handlers.DefaultOpenAPIURL,
		Config: &lib.Config{ClientConfig: &configstore.ClientConfig{}},
	}
	opts := server.bifrostMCPOptions(handlers.BifrostMCPOptions{AppDir: "/app"})
	assert.Equal(t, handlers.DefaultOpenAPIURL, opts.SpecURL)
	assert.True(t, opts.AllowWrites)
	assert.Empty(t, opts.BaseURL, "no bifrost_url, no base: the Bifrost MCP does not load")
	assert.False(t, opts.BaseIsEdge)
	assert.Equal(t, "/app", opts.AppDir, "options outside the settings are kept")

	setOpenAPIURL(server.Config, "specs/openapi.json")
	server.Config.ClientConfig.BifrostMCPAllowWrites = new(false)
	server.Config.ClientConfig.BifrostURL = schemas.NewSecretVar("https://gw.example.com/bifrost/")
	opts = server.bifrostMCPOptions(opts)
	assert.Equal(t, "specs/openapi.json", opts.SpecURL)
	assert.False(t, opts.AllowWrites)
	assert.Equal(t, "https://gw.example.com", opts.BaseURL, "the origin only: the in-process router accepts paths without the base path")
	assert.True(t, opts.BaseIsEdge)

	setOpenAPIURL(server.Config, "  ")
	opts = server.bifrostMCPOptions(opts)
	assert.Equal(t, handlers.DefaultOpenAPIURL, opts.SpecURL, "a blank openapi_url falls back to the build's default")

	server.defaultOpenAPIURL = "https://example.com/other-build/openapi.json"
	opts = server.bifrostMCPOptions(opts)
	assert.Equal(t, "https://example.com/other-build/openapi.json", opts.SpecURL, "a build's own default replaces DefaultOpenAPIURL")
}

// Without -app-dir the gateway loads config from the OS default directory, and a
// relative openapi_url resolves against that directory: the one config
// was loaded from, not the empty -app-dir.
func TestStartBifrostMCPResolvesRelativeSpecAgainstConfigDir(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	t.Cleanup(func() { logger = prevLogger })
	handlers.SetLogger(noopTestLogger{})
	lib.SetLogger(noopTestLogger{})
	ctx := context.Background()

	dir := t.TempDir()
	spec, err := os.ReadFile("../../../docs/openapi/openapi.json")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openapi.json"), spec, 0o600))
	cfg, err := lib.LoadConfig(ctx, dir)
	require.NoError(t, err)
	setOpenAPIURL(cfg, "openapi.json")
	cfg.ClientConfig.BifrostURL = schemas.NewSecretVar("https://gw.example.com")

	r := router.New()
	r.GET("/api/providers", func(*fasthttp.RequestCtx) {}) // a routed operation, so the server has a tool to serve
	server := &BifrostHTTPServer{AppDir: "", Config: cfg, Router: r}
	server.StartBifrostMCP(ctx, handlers.DefaultOpenAPIURL, func(*fasthttp.RequestCtx) {}, 4<<20, 64<<10)
	t.Cleanup(func() { _ = server.bifrostMCPExec.Close() })
	assert.Equal(t, dir, server.BifrostMCP.Options().AppDir, "the spec resolves against the directory config was loaded from")

	deadline := time.Now().Add(10 * time.Second)
	var loadErr error
	for time.Now().Before(deadline) {
		if _, loadErr = server.BifrostMCP.Server(ctx); loadErr == nil {
			return
		}
		if !strings.Contains(loadErr.Error(), "still loading") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the relative spec did not load: %v", loadErr)
}

// A config save that changes a Bifrost MCP setting rebuilds it; one that does not
// leaves the loaded server alone; a rebuild that fails keeps the previous server.
func TestReloadBifrostMCPOnSettingsChange(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	t.Cleanup(func() { logger = prevLogger })
	handlers.SetLogger(noopTestLogger{})
	spec, err := filepath.Abs("../../../docs/openapi/openapi.json")
	require.NoError(t, err)

	server := &BifrostHTTPServer{
		Host: "localhost", Port: "8080", defaultOpenAPIURL: spec,
		Config: &lib.Config{ClientConfig: &configstore.ClientConfig{BifrostURL: schemas.NewSecretVar("https://gw.example.com")}},
	}
	server.BifrostMCP = handlers.NewBifrostMCP(server.bifrostMCPOptions(handlers.BifrostMCPOptions{ExecClient: http.DefaultClient}))
	before, err := server.BifrostMCP.Server(context.Background())
	require.NoError(t, err)
	_, served := before.ListTools()["createVirtualKey"]
	require.True(t, served, "writes are on by default")

	server.reloadBifrostMCP(context.Background())
	unchanged, err := server.BifrostMCP.Server(context.Background())
	require.NoError(t, err)
	assert.Same(t, before, unchanged, "a save that changed nothing must not rebuild")

	server.Config.ClientConfig.BifrostMCPAllowWrites = new(false)
	server.reloadBifrostMCP(context.Background())
	after, err := server.BifrostMCP.Server(context.Background())
	require.NoError(t, err)
	_, served = after.ListTools()["createVirtualKey"]
	assert.False(t, served, "turning writes off rebuilt the server without them")

	setOpenAPIURL(server.Config, "missing/openapi.json")
	server.reloadBifrostMCP(context.Background())
	kept, err := server.BifrostMCP.Server(context.Background())
	require.NoError(t, err)
	assert.Same(t, after, kept, "a failed rebuild keeps the previous server")

	// One save that turns writes back on and points at the missing spec restricts
	// nothing, so the server stays; one that then turns writes off again cannot
	// keep a server built with them on, and unloads it.
	setOpenAPIURL(server.Config, "")
	server.Config.ClientConfig.BifrostMCPAllowWrites = nil
	server.reloadBifrostMCP(context.Background())
	withWrites, err := server.BifrostMCP.Server(context.Background())
	require.NoError(t, err)
	_, served = withWrites.ListTools()["createVirtualKey"]
	require.True(t, served)
	setOpenAPIURL(server.Config, "missing/openapi.json")
	server.Config.ClientConfig.BifrostMCPAllowWrites = new(false)
	server.reloadBifrostMCP(context.Background())
	_, err = server.BifrostMCP.Server(context.Background())
	assert.Error(t, err, "writes off with a spec that does not load must not keep the write-capable server")
}

// Clearing bifrost_url while the spec is unavailable cannot leave the server
// built for the old URL serving.
func TestReloadBifrostMCPClearedBifrostURLUnloads(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	t.Cleanup(func() { logger = prevLogger })
	handlers.SetLogger(noopTestLogger{})
	spec, err := filepath.Abs("../../../docs/openapi/openapi.json")
	require.NoError(t, err)
	server := &BifrostHTTPServer{
		Host: "localhost", Port: "8080", defaultOpenAPIURL: spec,
		Config: &lib.Config{ClientConfig: &configstore.ClientConfig{BifrostURL: schemas.NewSecretVar("https://gw.example.com")}},
	}
	server.BifrostMCP = handlers.NewBifrostMCP(server.bifrostMCPOptions(handlers.BifrostMCPOptions{ExecClient: http.DefaultClient}))
	_, err = server.BifrostMCP.Server(context.Background())
	require.NoError(t, err)

	server.Config.ClientConfig.BifrostURL = nil
	setOpenAPIURL(server.Config, "missing/openapi.json")
	server.reloadBifrostMCP(context.Background())
	_, err = server.BifrostMCP.Server(context.Background())
	assert.Error(t, err, "the server built for the cleared bifrost_url must not keep serving")
}

// fixedUserGovernance answers for any user with an empty limits projection.
type fixedUserGovernance struct{}

func (fixedUserGovernance) DescribeUserGovernance(_ context.Context, userID string) (*warp.UserGovernance, error) {
	return &warp.UserGovernance{UserID: userID}, nil
}

// TestRegisterUserLimitsRoutes: without a user governance reader (the OSS build)
// the user limits routes are not registered; with one they are, behind the
// middleware chain RegisterAPIRoutes passes.
func TestRegisterUserLimitsRoutes(t *testing.T) {
	paths := []string{"/api/governance/users/me/limits", "/api/governance/users/u-1/limits"}
	serve := func(s *BifrostHTTPServer, path string) *fasthttp.RequestCtx {
		var ctx fasthttp.RequestCtx
		ctx.Request.Header.SetMethod(fasthttp.MethodGet)
		ctx.Request.SetRequestURI(path)
		ctx.SetUserValue(schemas.BifrostContextKeyUserID, "u-1")
		s.Router.Handler(&ctx)
		return &ctx
	}

	without := &BifrostHTTPServer{Router: router.New()}
	without.registerUserLimitsRoutes()
	for _, path := range paths {
		ctx := serve(without, path)
		require.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode(), "%s is registered without a reader", path)
	}

	ran := 0
	mark := schemas.BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			ran++
			ctx.Response.Header.Set("X-Test-Middleware", "ran")
			next(ctx)
		}
	})
	with := &BifrostHTTPServer{Router: router.New(), WarpUserGovernanceReader: fixedUserGovernance{}}
	with.registerUserLimitsRoutes(mark)
	for _, path := range paths {
		ctx := serve(with, path)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "%s: %s", path, ctx.Response.Body())
		require.Equal(t, "ran", string(ctx.Response.Header.Peek("X-Test-Middleware")), "%s skipped the middleware chain", path)
	}
	require.Equal(t, len(paths), ran)
}

// callerMarkerKey stands in for the caller identity a request context carries.
type callerMarkerKey struct{}

// reloadModelConfigConfigStore answers a model config read and records whether the read carried
// the caller marker, the way a scoped store would narrow a read for that caller.
type reloadModelConfigConfigStore struct {
	configstore.ConfigStore
	sawCaller bool
}

// GetModelConfigByID records whether ctx carries the caller marker and returns the row.
func (s *reloadModelConfigConfigStore) GetModelConfigByID(ctx context.Context, id string) (*configstoreTables.TableModelConfig, error) {
	s.sawCaller = ctx.Value(callerMarkerKey{}) != nil
	return &configstoreTables.TableModelConfig{ID: id, ModelName: "gpt-4o", Scope: "global"}, nil
}

// TestReloadModelConfigReadsWithoutTheCallersContext checks that the reload's store read does not
// carry the caller's context, so a caller's row-visibility scope cannot hide the config it just
// wrote from the in-memory reload.
func TestReloadModelConfigReadsWithoutTheCallersContext(t *testing.T) {
	handlers.SetLogger(noopTestLogger{})
	store := &reloadModelConfigConfigStore{}
	server := &BifrostHTTPServer{Ctx: schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), Config: &lib.Config{ConfigStore: store}}

	callerCtx := context.WithValue(context.Background(), callerMarkerKey{}, "alice")
	_, _ = server.ReloadModelConfig(callerCtx, "mc-1") // no governance plugin: only the read matters here
	if store.sawCaller {
		t.Fatal("the reload read the model config on the caller's context")
	}
}

// gatewayTestServer serves root through newFastHTTPServer, the production server settings, on an
// in-memory listener, and returns a client for it.
func gatewayTestServer(tb testing.TB, limitMB int, router fasthttp.RequestHandler, buffered bool) *fasthttp.Client {
	tb.Helper()
	handlers.SetLogger(noopTestLogger{})
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{MaxRequestBodySizeMB: limitMB}}
	srv := newFastHTTPServer(handlers.ServerRootHandler(handlers.NewCorsMiddleware(config), config, router), limitMB, 64<<10, nil)
	if buffered {
		// The settings before uploads were streamed, for comparison benchmarks.
		srv.StreamRequestBody, srv.DisablePreParseMultipartForm = false, false
	}
	// A loopback TCP listener, not fasthttputil's in-memory pipe, which copies every write into
	// new buffers and would count the whole upload as gateway allocations.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	go srv.Serve(ln) //nolint:errcheck
	tb.Cleanup(func() { ln.Close() })
	addr := ln.Addr().String()
	return &fasthttp.Client{Dial: func(string) (net.Conn, error) { return net.Dial("tcp", addr) }, MaxConnsPerHost: 1, MaxResponseBodySize: 1 << 20}
}

// multipartUploadRequest builds a Files API upload request carrying file.
func multipartUploadRequest(tb testing.TB, file []byte) *fasthttp.Request {
	tb.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	w.WriteField("purpose", "batch")
	part, err := w.CreateFormFile("file", "batch.jsonl")
	if err != nil {
		tb.Fatal(err)
	}
	part.Write(file)
	w.Close()
	req := &fasthttp.Request{}
	req.SetRequestURI("http://bifrost/v1/files")
	req.Header.SetMethod(fasthttp.MethodPost)
	req.Header.SetContentType(w.FormDataContentType())
	req.SetBody(body.Bytes())
	return req
}

// TestGatewayServerStreamsMultipartUploads pins that the gateway server never buffers an upload: a
// 17 MB file reaches the handler unread on the connection, is parsed into a temp file, and costs
// a small fraction of its size. fasthttp's parse while reading the request (the previous settings)
// buffered the first 16 MB of every file before spilling it, ~64 MB allocated per upload.
func TestGatewayServerStreamsMultipartUploads(t *testing.T) {
	const fileSize = 17 << 20
	file := bytes.Repeat([]byte("0123456789abcdef"), fileSize/16)
	wantSum := sha256.Sum256(file)
	var onDisk, streamed bool
	var gotSum [sha256.Size]byte
	var purpose string
	client := gatewayTestServer(t, 100, func(ctx *fasthttp.RequestCtx) {
		streamed = ctx.Request.IsBodyStream() // not parsed yet: the handler reads it from the connection
		form, err := ctx.MultipartForm()
		if err != nil {
			ctx.Error(err.Error(), fasthttp.StatusBadRequest)
			return
		}
		purpose = form.Value["purpose"][0]
		f, err := form.File["file"][0].Open()
		if err != nil {
			ctx.Error(err.Error(), fasthttp.StatusInternalServerError)
			return
		}
		defer f.Close()
		_, onDisk = f.(*os.File)
		h := sha256.New()
		io.Copy(h, f)
		copy(gotSum[:], h.Sum(nil))
	}, false)
	req := multipartUploadRequest(t, file)
	resp := &fasthttp.Response{}

	// TotalAlloc is process-wide, so goroutines left by other tests in the package add noise;
	// the smallest of three uploads is the upload's own cost.
	allocated := uint64(math.MaxUint64)
	for i := 0; i < 3; i++ {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		if err := client.Do(req, resp); err != nil {
			t.Fatal(err)
		}
		runtime.ReadMemStats(&after)
		allocated = min(allocated, after.TotalAlloc-before.TotalAlloc)

		if resp.StatusCode() != fasthttp.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode(), resp.Body())
		}
		if purpose != "batch" || gotSum != wantSum {
			t.Errorf("handler got purpose=%q and a file that differs from the upload", purpose)
		}
		if !onDisk {
			t.Error("uploaded file was held in memory, want it in a temp file")
		}
		if !streamed {
			t.Error("the upload reached the handler already parsed, want it unread on the connection (parsing while reading buffers 16 MB per file)")
		}
	}
	t.Logf("%d-byte upload allocated %d bytes", fileSize, allocated)
	// Alone this upload allocates ~0.2 MB; the bound leaves room for the package's background
	// goroutines and still fails the read-time parse, which allocates ~64 MB.
	if allocated > 32<<20 {
		t.Errorf("a %d-byte upload allocated %d bytes; the file must be streamed to disk, not buffered", fileSize, allocated)
	}
}

// TestGatewayServerBuffersOtherBodies asserts every body that is not a plain multipart upload
// reaches handlers as a buffering server delivered it: in memory, with no body stream left, which
// code such as GenAI request-type detection reads as "large payload". Bodies larger than
// fasthttp's 8 KB streaming prefetch, compressed bodies and chunked bodies are covered.
func TestGatewayServerBuffersOtherBodies(t *testing.T) {
	jsonBody := []byte(`{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"` + strings.Repeat("hello ", 40_000) + `"}]}`)
	var gzipped bytes.Buffer
	zw := gzip.NewWriter(&gzipped)
	zw.Write(jsonBody)
	zw.Close()

	var gotBody []byte
	var hadStream bool
	client := gatewayTestServer(t, 100, func(ctx *fasthttp.RequestCtx) {
		hadStream = ctx.RequestBodyStream() != nil
		gotBody = append([]byte(nil), ctx.Request.Body()...)
	}, false)
	for _, tc := range []struct {
		name string
		set  func(req *fasthttp.Request)
	}{
		{"json over the prefetch", func(req *fasthttp.Request) { req.SetBody(jsonBody) }},
		{"gzip json", func(req *fasthttp.Request) {
			req.Header.Set("Content-Encoding", "gzip")
			req.SetBody(gzipped.Bytes())
		}},
		{"chunked json", func(req *fasthttp.Request) { req.SetBodyStream(bytes.NewReader(jsonBody), -1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &fasthttp.Request{}
			req.SetRequestURI("http://bifrost/v1/chat/completions")
			req.Header.SetMethod(fasthttp.MethodPost)
			req.Header.SetContentType("application/json")
			tc.set(req)
			resp := &fasthttp.Response{}
			if err := client.Do(req, resp); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode() != fasthttp.StatusOK {
				t.Fatalf("status %d: %s", resp.StatusCode(), resp.Body())
			}
			if hadStream {
				t.Error("handler saw a request body stream, want the body buffered as before")
			}
			if !bytes.Equal(gotBody, jsonBody) {
				t.Errorf("handler got %d bytes, want the %d-byte body", len(gotBody), len(jsonBody))
			}
		})
	}
}

// TestGatewayServerEnforcesBodyLimit asserts max_request_body_size_mb still answers 413 with the
// explanatory message and closes the connection, for every body shape. A streaming fasthttp server
// does not reject an oversized body itself, so RequestBodyMiddleware must: from Content-Length, and
// while reading a chunked body. Each oversized request is written on a raw connection only as far
// as the server reads before it answers, so the 413 is always delivered rather than lost to a
// reset, and a server that drops the connection without answering fails. A chunked body under the
// limit still goes through whole.
func TestGatewayServerEnforcesBodyLimit(t *testing.T) {
	const limitMB = 1
	const limit = limitMB << 20
	handlerRan := false
	var handlerBody int
	root := func(ctx *fasthttp.RequestCtx) {
		handlerRan = true
		handlerBody = len(ctx.Request.Body())
	}
	upload := multipartUploadRequest(t, nil)
	for _, tc := range []struct {
		name  string
		write func(conn net.Conn)
	}{
		{"json over the limit", func(conn net.Conn) {
			// Headers declaring 2 MB, then only fasthttp's 8 KB streaming prefetch.
			fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: bifrost\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", 2*limit)
			conn.Write(bytes.Repeat([]byte("x"), 8<<10))
		}},
		{"chunked over the limit", func(conn net.Conn) {
			// One chunk of limit+1 bytes and its closing CRLF (fasthttp reads it with the chunk's
			// last bytes), but no final chunk: exactly what the middleware reads before refusing.
			fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: bifrost\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n", limit+1)
			conn.Write(bytes.Repeat([]byte("x"), limit+1))
			conn.Write([]byte("\r\n"))
		}},
		{"multipart upload over the limit", func(conn net.Conn) {
			fmt.Fprintf(conn, "POST /v1/files HTTP/1.1\r\nHost: bifrost\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n", upload.Header.ContentType(), 2*limit)
			conn.Write(bytes.Repeat([]byte("x"), 8<<10))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handlerRan = false
			resp := rawGatewayRequest(t, limitMB, root, tc.write)
			if resp.StatusCode() != fasthttp.StatusRequestEntityTooLarge {
				t.Fatalf("status %d, want 413: %s", resp.StatusCode(), resp.Body())
			}
			if body := string(resp.Body()); !strings.Contains(body, "request_too_large") || !strings.Contains(body, "max_request_body_size_mb") {
				t.Errorf("413 body lost the explanatory message: %s", body)
			}
			if !resp.ConnectionClose() {
				t.Error("413 for an unread body must close the connection")
			}
			if handlerRan {
				t.Error("handler ran for an oversized body")
			}
		})
	}

	t.Run("chunked under the limit", func(t *testing.T) {
		under := bytes.Repeat([]byte("y"), 512<<10)
		client := gatewayTestServer(t, limitMB, root, false)
		req := &fasthttp.Request{}
		req.SetRequestURI("http://bifrost/v1/chat/completions")
		req.Header.SetMethod(fasthttp.MethodPost)
		req.SetBodyStream(bytes.NewReader(under), -1)
		resp := &fasthttp.Response{}
		if err := client.Do(req, resp); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode() != fasthttp.StatusOK || handlerBody != len(under) {
			t.Errorf("status %d, handler got %d bytes; want 200 and %d bytes", resp.StatusCode(), handlerBody, len(under))
		}
	})
}

// rawGatewayRequest serves root through the production server settings and root chain, writes a
// request with write on a raw connection, and reads the response.
func rawGatewayRequest(t *testing.T, limitMB int, root fasthttp.RequestHandler, write func(conn net.Conn)) *fasthttp.Response {
	t.Helper()
	handlers.SetLogger(noopTestLogger{})
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{MaxRequestBodySizeMB: limitMB}}
	srv := newFastHTTPServer(handlers.ServerRootHandler(handlers.NewCorsMiddleware(config), config, root), limitMB, 64<<10, nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { ln.Close() })
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	write(conn)
	resp := &fasthttp.Response{}
	if err := resp.Read(bufio.NewReader(conn)); err != nil {
		t.Fatalf("no response: %v", err)
	}
	return resp
}

// BenchmarkGatewayServerJSONBody measures a JSON request end to end through the gateway's server
// settings and root middleware chain, against the previous buffered settings: the common path
// must not regress now that request bodies are streamed and read back into memory by
// RequestBodyMiddleware.
func BenchmarkGatewayServerJSONBody(b *testing.B) {
	for _, size := range []int{1 << 10, 64 << 10, 1 << 20} {
		body := []byte(`{"model":"openai/gpt-4o-mini","input":"` + strings.Repeat("a", size) + `"}`)
		for _, mode := range []struct {
			name     string
			buffered bool
		}{{"buffered", true}, {"streaming", false}} {
			b.Run(fmt.Sprintf("%dKB/%s", size>>10, mode.name), func(b *testing.B) {
				client := gatewayTestServer(b, 100, func(ctx *fasthttp.RequestCtx) {
					if len(ctx.Request.Body()) != len(body) {
						ctx.SetStatusCode(fasthttp.StatusBadRequest)
					}
				}, mode.buffered)
				req := &fasthttp.Request{}
				req.SetRequestURI("http://bifrost/v1/responses")
				req.Header.SetMethod(fasthttp.MethodPost)
				req.Header.SetContentType("application/json")
				req.SetBody(body)
				resp := &fasthttp.Response{}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := client.Do(req, resp); err != nil || resp.StatusCode() != fasthttp.StatusOK {
						b.Fatalf("status %d err %v", resp.StatusCode(), err)
					}
				}
			})
		}
	}
}

// TestGatewayServerRejectedUploadKeepsNextRequestIntact pins that an upload answered before its
// body is read (here a 401, as an auth failure or a rate limit answers) reaches the client with
// its status and does not leave the rest of the body on the keep-alive connection. A streaming
// fasthttp server does not drain it, so the next request on that connection would be parsed from
// the leftover upload bytes; RequestBodyMiddleware drains it. Both requests must share one
// connection: closing it after the 401 would also keep the next request intact, but would lose
// keep-alive reuse for every refused upload.
func TestGatewayServerRejectedUploadKeepsNextRequestIntact(t *testing.T) {
	jsonBody := []byte(`{"model":"openai/gpt-4o-mini","input":"next request"}`)
	var gotBody []byte
	client := gatewayTestServer(t, 100, func(ctx *fasthttp.RequestCtx) {
		if string(ctx.Path()) == "/v1/files" {
			ctx.Error("unauthorized", fasthttp.StatusUnauthorized) // the upload is never read
			return
		}
		gotBody = append([]byte(nil), ctx.Request.Body()...)
	}, false)
	var dials atomic.Int32
	dial := client.Dial
	client.Dial = func(addr string) (net.Conn, error) {
		dials.Add(1)
		return dial(addr)
	}

	upload := multipartUploadRequest(t, bytes.Repeat([]byte("u"), 4<<20))
	resp := &fasthttp.Response{}
	if err := client.Do(upload, resp); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode() != fasthttp.StatusUnauthorized {
		t.Fatalf("upload status %d, want 401", resp.StatusCode())
	}

	next := &fasthttp.Request{}
	next.SetRequestURI("http://bifrost/v1/responses")
	next.Header.SetMethod(fasthttp.MethodPost)
	next.Header.SetContentType("application/json")
	next.SetBody(jsonBody)
	if err := client.Do(next, resp); err != nil {
		t.Fatalf("next request failed: %v", err)
	}
	if resp.StatusCode() != fasthttp.StatusOK || !bytes.Equal(gotBody, jsonBody) {
		t.Errorf("next request: status %d, handler body %q; want 200 and the JSON body", resp.StatusCode(), gotBody)
	}
	if n := dials.Load(); n != 1 {
		t.Errorf("dialed %d connections, want 1: the next request must reuse the rejected upload's keep-alive connection", n)
	}
}

// TestGatewayServerOversizedBodyAnswered413BeforeBody asserts a request whose Content-Length is
// over max_request_body_size_mb is answered 413, with the explanatory message, from its headers:
// the client gets the reply after sending only fasthttp's 8 KB streaming prefetch of a 2 MB body.
func TestGatewayServerOversizedBodyAnswered413BeforeBody(t *testing.T) {
	handlerRan := false
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{MaxRequestBodySizeMB: 1}}
	handlers.SetLogger(noopTestLogger{})
	srv := newFastHTTPServer(handlers.ServerRootHandler(handlers.NewCorsMiddleware(config), config, func(*fasthttp.RequestCtx) { handlerRan = true }), 1, 64<<10, nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln) //nolint:errcheck
	defer ln.Close()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "POST /v1/files HTTP/1.1\r\nHost: bifrost\r\nContent-Type: multipart/form-data; boundary=b\r\nContent-Length: %d\r\n\r\n", 2<<20)
	conn.Write(bytes.Repeat([]byte("p"), 8<<10))
	resp := &fasthttp.Response{}
	if err := resp.Read(bufio.NewReader(conn)); err != nil {
		t.Fatalf("no reply before the body was sent: %v", err)
	}
	if resp.StatusCode() != fasthttp.StatusRequestEntityTooLarge || !strings.Contains(string(resp.Body()), "max_request_body_size_mb") {
		t.Errorf("status %d body %s, want 413 naming max_request_body_size_mb", resp.StatusCode(), resp.Body())
	}
	if handlerRan {
		t.Error("handler ran for an oversized body")
	}
}

// TestReloadTeamAnswersWithLiveUsageAndWritesNothing checks a team reload's response: its budget
// carries the usage and last reset counted in memory and its rate limit the token and request usage
// and last resets, while the rows keep what the database holds.
func TestReloadTeamAnswersWithLiveUsageAndWritesNothing(t *testing.T) {
	server, gs := governedServer(t)
	ctx := context.Background()
	store := server.Config.ConfigStore
	loadedAt := time.Now().Add(-time.Hour).Truncate(time.Second)

	require.NoError(t, store.CreateRateLimit(ctx, &configstoreTables.TableRateLimit{
		ID: "rl-team", TokenMaxLimit: schemas.Ptr(int64(1000)), TokenResetDuration: schemas.Ptr("1d"),
		RequestMaxLimit: schemas.Ptr(int64(100)), RequestResetDuration: schemas.Ptr("1d"),
		TokenLastReset: loadedAt, RequestLastReset: loadedAt,
	}))
	require.NoError(t, store.CreateTeam(ctx, &configstoreTables.TableTeam{ID: "team-1", Name: "Platform", RateLimitID: schemas.Ptr("rl-team")}))
	require.NoError(t, store.CreateBudget(ctx, &configstoreTables.TableBudget{ID: "budget-team", MaxLimit: 10, ResetDuration: "1d", LastReset: loadedAt, TeamID: schemas.Ptr("team-1")}))
	_, err := server.ReloadTeam(ctx, "team-1")
	require.NoError(t, err)
	require.NoError(t, gs.BumpBudgetUsage(ctx, "budget-team", 3))
	require.NoError(t, gs.BumpRateLimitUsage(ctx, "rl-team", 40, true, true))

	// The rows' last resets change in the database; memory keeps the ones it counts from.
	budgetReset := gs.LoadBudget(ctx, "budget-team").LastReset
	tokenReset, requestReset := gs.LoadRateLimit(ctx, "rl-team").TokenLastReset, gs.LoadRateLimit(ctx, "rl-team").RequestLastReset
	movedAt := budgetReset.Add(-30 * time.Minute)
	db := store.DB().Session(&gorm.Session{SkipHooks: true})
	require.NoError(t, db.Model(&configstoreTables.TableBudget{}).Where("id = ?", "budget-team").Update("last_reset", movedAt).Error)
	require.NoError(t, db.Model(&configstoreTables.TableRateLimit{}).Where("id = ?", "rl-team").Updates(map[string]any{"token_last_reset": movedAt, "request_last_reset": movedAt}).Error)

	team, err := server.ReloadTeam(ctx, "team-1")
	require.NoError(t, err)
	require.Len(t, team.Budgets, 1)
	require.Equal(t, 3.0, team.Budgets[0].CurrentUsage, "the budget carries the usage counted in memory")
	require.True(t, team.Budgets[0].LastReset.Equal(budgetReset), "and memory's last reset")
	require.NotNil(t, team.RateLimit)
	require.Equal(t, int64(40), team.RateLimit.TokenCurrentUsage)
	require.Equal(t, int64(1), team.RateLimit.RequestCurrentUsage)
	require.True(t, team.RateLimit.TokenLastReset.Equal(tokenReset), "the rate limit carries memory's last resets")
	require.True(t, team.RateLimit.RequestLastReset.Equal(requestReset))

	budgetRow, err := store.GetBudget(ctx, "budget-team")
	require.NoError(t, err)
	require.Zero(t, budgetRow.CurrentUsage, "nothing is written back")
	rateLimitRow, err := store.GetRateLimit(ctx, "rl-team")
	require.NoError(t, err)
	require.Zero(t, rateLimitRow.TokenCurrentUsage, "nothing is written back")
	require.Zero(t, rateLimitRow.RequestCurrentUsage, "nothing is written back")
	require.True(t, budgetRow.LastReset.Equal(movedAt), "the stored last resets stay where the database put them")
	require.True(t, rateLimitRow.TokenLastReset.Equal(movedAt))
	require.True(t, rateLimitRow.RequestLastReset.Equal(movedAt))
}

// TestOverlayUsageCarriesMemoryState checks what a reload answers with for a limit: the usage and
// last resets memory counts it from, with a budget's override cycles re-derived from the result, and
// the row's own values for a limit memory does not hold.
func TestOverlayUsageCarriesMemoryState(t *testing.T) {
	server, gs := governedServer(t)
	ctx := context.Background()
	memoryReset := time.Now().Add(-time.Hour).Truncate(time.Second)
	rowReset := memoryReset.Add(-24 * time.Hour)
	store := server.Config.ConfigStore
	require.NoError(t, store.CreateBudget(ctx, &configstoreTables.TableBudget{ID: "b", MaxLimit: 10, ResetDuration: "1d", LastReset: memoryReset, CreatedAt: memoryReset}))
	require.NoError(t, store.CreateRateLimit(ctx, &configstoreTables.TableRateLimit{ID: "rl", TokenMaxLimit: schemas.Ptr(int64(10)), TokenResetDuration: schemas.Ptr("1d"), TokenLastReset: memoryReset, RequestLastReset: memoryReset}))
	require.NoError(t, server.Config.LiveStore.Refresh(ctx, []livestore.Ref{{Kind: livestore.KindBudget, ID: "b"}, {Kind: livestore.KindRateLimit, ID: "rl"}}))
	require.NoError(t, gs.BumpBudgetUsage(ctx, "b", 2))
	require.NoError(t, gs.BumpRateLimitUsage(ctx, "rl", 5, true, true))
	// The resets memory counts from, which a write of the row may have stamped.
	memoryBudget, memoryRateLimit := gs.LoadBudget(ctx, "b"), gs.LoadRateLimit(ctx, "rl")
	require.NotNil(t, memoryBudget)
	require.NotNil(t, memoryRateLimit)

	// A two-cycle override anchored at the row's reset: memory's reset is one window later, so one
	// cycle has closed and one remains.
	budget := configstoreTables.TableBudget{ID: "b", MaxLimit: 10, ResetDuration: "1d", LastReset: rowReset,
		OverrideMode: configstoreTables.BudgetOverrideModeCycles, OverrideAmount: 5, OverrideCyclesTotal: 2,
		OverrideCyclesRemaining: 2, OverrideAnchorReset: &rowReset}
	overlayBudgetUsage(ctx, gs, &budget)
	require.Equal(t, 2.0, budget.CurrentUsage)
	require.True(t, budget.LastReset.Equal(memoryBudget.LastReset), "memory's last reset, not the row's")
	require.Equal(t, 1, budget.OverrideCyclesRemaining, "override cycles are re-derived from memory's last reset")

	rateLimit := configstoreTables.TableRateLimit{ID: "rl", TokenLastReset: rowReset, RequestLastReset: rowReset}
	overlayRateLimitUsage(ctx, gs, &rateLimit)
	require.Equal(t, int64(5), rateLimit.TokenCurrentUsage)
	require.Equal(t, int64(1), rateLimit.RequestCurrentUsage)
	require.True(t, rateLimit.TokenLastReset.Equal(memoryRateLimit.TokenLastReset), "memory's last resets, not the row's")
	require.True(t, rateLimit.RequestLastReset.Equal(memoryRateLimit.RequestLastReset))

	unknown := configstoreTables.TableBudget{ID: "not-in-memory", CurrentUsage: 7, LastReset: rowReset}
	overlayBudgetUsage(ctx, gs, &unknown)
	require.Equal(t, 7.0, unknown.CurrentUsage, "a budget memory does not hold keeps the row's usage")
	require.True(t, unknown.LastReset.Equal(rowReset))
}

// TestReloadTeamOfMissingRowFailsFast checks that reloading a team with no row returns not found
// at once, without waiting for the row as a key reload does.
func TestReloadTeamOfMissingRowFailsFast(t *testing.T) {
	server, _ := governedServer(t)
	started := time.Now()
	_, err := server.ReloadTeam(context.Background(), "no-such-team")
	require.ErrorIs(t, err, configstore.ErrNotFound)
	require.Less(t, time.Since(started), lib.DBLookupDelay, "no retry")
}

// TestGovernanceWritesWithoutPluginReportItAndKeepLiveStore checks that without the governance
// plugin a team reload and removal still return the plugin's error, as they always have, while the
// live store follows the database.
func TestGovernanceWritesWithoutPluginReportItAndKeepLiveStore(t *testing.T) {
	handlers.SetLogger(noopTestLogger{})
	previousLogger := logger
	logger = noopTestLogger{}
	t.Cleanup(func() { logger = previousLogger })
	ctx := context.Background()
	config := libtest.Config(t, nil)
	config.ClientConfig = &configstore.ClientConfig{}
	server := &BifrostHTTPServer{Ctx: schemas.NewBifrostContext(ctx, schemas.NoDeadline), Config: config}

	// The error both calls must return: the plugin lookup's, not one from the database.
	_, pluginErr := server.getGovernancePlugin()
	require.Error(t, pluginErr)

	require.NoError(t, config.ConfigStore.CreateTeam(ctx, &configstoreTables.TableTeam{ID: "team-1", Name: "Platform"}))
	_, err := server.ReloadTeam(ctx, "team-1")
	require.EqualError(t, err, pluginErr.Error(), "the plugin's absence is reported")
	_, ok := config.LiveStore.Collections.Teams.Get("team-1")
	require.True(t, ok, "the live store still follows the write")

	require.NoError(t, config.ConfigStore.DeleteTeam(ctx, "team-1"))
	require.EqualError(t, server.RemoveTeam(ctx, "team-1"), pluginErr.Error(), "the plugin's absence is reported")
	_, ok = config.LiveStore.Collections.Teams.Get("team-1")
	require.False(t, ok, "the live store still follows the delete")
}

// liveStoreForTest returns a new, empty live store closed when the test ends.
var liveStoreForTest = livestoretest.New

// scopedModelConfigIDs returns the IDs of the model configs memory holds for one scope and scope ID.
func scopedModelConfigIDs(gs *governance.LocalGovernanceStore, scope, scopeID string) []string {
	var ids []string
	for _, mc := range gs.ScopedModelConfigs(scope, scopeID) {
		ids = append(ids, mc.ID)
	}
	return ids
}

// TestRemovingADeletedTeamOrCustomerClearsItsReferences checks that removing a team or customer
// whose row the database has deleted leaves no key or team naming it, as the removal always
// has: the records under it are re-read with the reference the delete cleared. The delete routes
// therefore delete the row before they remove the record; removed first, the records under it
// would be re-read still naming it.
func TestRemovingADeletedTeamOrCustomerClearsItsReferences(t *testing.T) {
	server, gs := governedServer(t)
	ctx := context.Background()
	cs := server.Config.ConfigStore
	require.NoError(t, cs.CreateCustomer(ctx, &configstoreTables.TableCustomer{ID: "cust-1", Name: "Acme"}))
	require.NoError(t, cs.CreateTeam(ctx, &configstoreTables.TableTeam{ID: "team-1", Name: "Platform", CustomerID: schemas.Ptr("cust-1")}))
	require.NoError(t, cs.CreateVirtualKey(ctx, &configstoreTables.TableVirtualKey{ID: "vk-team", Name: "vk-team", Value: *schemas.NewSecretVar("sk-bf-team"), IsActive: schemas.Ptr(true), TeamID: schemas.Ptr("team-1")}))
	require.NoError(t, cs.CreateVirtualKey(ctx, &configstoreTables.TableVirtualKey{ID: "vk-cust", Name: "vk-cust", Value: *schemas.NewSecretVar("sk-bf-cust"), IsActive: schemas.Ptr(true), CustomerID: schemas.Ptr("cust-1")}))
	_, err := server.ReloadCustomer(ctx, "cust-1")
	require.NoError(t, err)
	_, err = server.ReloadTeam(ctx, "team-1")
	require.NoError(t, err)
	for _, id := range []string{"vk-team", "vk-cust"} {
		_, err = server.ReloadVirtualKey(ctx, id)
		require.NoError(t, err)
	}
	vk, ok := gs.GetVirtualKeyByID(ctx, "vk-team")
	require.True(t, ok)
	require.Equal(t, "team-1", vk.TeamID)

	require.NoError(t, cs.DeleteTeam(ctx, "team-1"))
	require.NoError(t, server.RemoveTeam(ctx, "team-1"))
	vk, ok = gs.GetVirtualKeyByID(ctx, "vk-team")
	require.True(t, ok)
	require.Empty(t, vk.TeamID, "a key under a deleted team names no team")

	require.NoError(t, cs.CreateTeam(ctx, &configstoreTables.TableTeam{ID: "team-2", Name: "Data", CustomerID: schemas.Ptr("cust-1")}))
	_, err = server.ReloadTeam(ctx, "team-2")
	require.NoError(t, err)
	require.NoError(t, cs.DeleteCustomer(ctx, "cust-1"))
	require.NoError(t, server.RemoveCustomer(ctx, "cust-1"))
	vk, ok = gs.GetVirtualKeyByID(ctx, "vk-cust")
	require.True(t, ok)
	require.Empty(t, vk.CustomerID, "a key under a deleted customer names no customer")
	team, ok := server.Config.LiveStore.Collections.Teams.Get("team-2")
	require.True(t, ok)
	require.Empty(t, team.CustomerID, "a team under a deleted customer names no customer")
}

// keyReadHold holds the first read of key rows the live store makes once armed open after it has
// captured its rows, so a test can commit a rotation while that read is still in flight. The live
// store reads keys into a slice; the config store's single-key read, which a reload makes for its
// response, reads into one row and is let through.
type keyReadHold struct {
	armed   atomic.Bool
	reads   atomic.Int32
	reading chan struct{}
	release chan struct{}
}

// install registers the hold on db's query callbacks.
func (h *keyReadHold) install(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:hold_key_read", func(tx *gorm.DB) {
		if !h.armed.Load() || tx.Statement.Table != (configstoreTables.TableVirtualKey{}).TableName() {
			return
		}
		if _, ok := tx.Statement.Dest.(*[]configstoreTables.TableVirtualKey); !ok {
			return
		}
		if h.reads.Add(1) == 1 {
			close(h.reading)
			<-h.release
		}
	}))
}

// TestOverlappingKeyReloadsKeepTheRotatedValue checks that a reload which read the key
// before a rotation cannot install it after the rotation's own reload.
func TestOverlappingKeyReloadsKeepTheRotatedValue(t *testing.T) {
	single := func(ctx context.Context, server *BifrostHTTPServer) error {
		_, err := server.ReloadVirtualKey(ctx, "vk-id")
		return err
	}
	batch := func(ctx context.Context, server *BifrostHTTPServer) error {
		return server.ReloadVirtualKeys(ctx, []string{"vk-id", "vk-other"})
	}
	for name, reloads := range map[string][2]func(context.Context, *BifrostHTTPServer) error{
		"single then single": {single, single},
		"batch then single":  {batch, single},
		"single then batch":  {single, batch},
	} {
		t.Run(name, func(t *testing.T) {
			server, store := governedServer(t)
			ctx := t.Context()
			db := server.Config.ConfigStore
			before, after := "sk-bf-old", "sk-bf-rotated"
			storeTestVirtualKey(t, server, "vk-id", "key", before)
			_, err := server.ReloadVirtualKey(ctx, "vk-id")
			require.NoError(t, err)
			hold := &keyReadHold{reading: make(chan struct{}), release: make(chan struct{})}
			// Released once, here or when the test ends, so a failed assertion never leaves the
			// older reload blocked inside its read.
			releaseHold := sync.OnceFunc(func() { close(hold.release) })
			t.Cleanup(releaseHold)
			hold.install(t, db.DB())
			hold.armed.Store(true)

			older, newer := make(chan error, 1), make(chan error, 1)
			go func() { older <- reloads[0](ctx, server) }()
			select {
			case <-hold.reading:
			case err := <-older:
				t.Fatalf("the older reload returned before its read was held: %v", err)
			case <-time.After(10 * time.Second):
				t.Fatal("the older reload's key read was not held within 10s")
			}
			row, err := db.GetVirtualKey(ctx, "vk-id")
			require.NoError(t, err)
			row.Value = *schemas.NewSecretVar(after)
			require.NoError(t, db.UpdateVirtualKey(ctx, row))
			go func() { newer <- reloads[1](ctx, server) }()
			// Unserialized, the newer reload installs here and the older one then
			// overwrites it. Serialized, it waits for the older one to finish.
			select {
			case err := <-newer:
				newer <- err
			case <-time.After(100 * time.Millisecond):
			}
			releaseHold()
			require.NoError(t, <-older)
			require.NoError(t, <-newer)

			_, stale := store.GetVirtualKey(ctx, before)
			require.False(t, stale, "the rotated-out value must not authenticate")
			_, live := store.GetVirtualKey(ctx, after)
			require.True(t, live, "the committed value must authenticate")
		})
	}
}
