package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// scaleCountingConfigStore wraps a real store and counts the read methods the
// scale fixes are meant to avoid or batch, so a test can assert query shape.
type scaleCountingConfigStore struct {
	configstore.ConfigStore

	mu                    sync.Mutex
	getModelConfigsCalls  int
	byScopeIDsCalls       int
	byScopeIDsMaxIDs      int
	getVirtualKeyCalls    int
	getRedactedCalls      int
	getRedactedEmptyCalls int
}

// GetModelConfigs counts full-table model config loads.
func (s *scaleCountingConfigStore) GetModelConfigs(ctx context.Context) ([]configstoreTables.TableModelConfig, error) {
	s.mu.Lock()
	s.getModelConfigsCalls++
	s.mu.Unlock()
	return s.ConfigStore.GetModelConfigs(ctx)
}

// GetModelConfigsByScopeAndScopeIDs counts scoped loads and the widest id list seen.
func (s *scaleCountingConfigStore) GetModelConfigsByScopeAndScopeIDs(ctx context.Context, scope string, scopeIDs []string, tx ...*gorm.DB) ([]configstoreTables.TableModelConfig, error) {
	s.mu.Lock()
	s.byScopeIDsCalls++
	if len(scopeIDs) > s.byScopeIDsMaxIDs {
		s.byScopeIDsMaxIDs = len(scopeIDs)
	}
	s.mu.Unlock()
	return s.ConfigStore.GetModelConfigsByScopeAndScopeIDs(ctx, scope, scopeIDs, tx...)
}

// GetVirtualKey counts full per-VK reads (every relation preloaded).
func (s *scaleCountingConfigStore) GetVirtualKey(ctx context.Context, id string) (*configstoreTables.TableVirtualKey, error) {
	s.mu.Lock()
	s.getVirtualKeyCalls++
	s.mu.Unlock()
	return s.ConfigStore.GetVirtualKey(ctx, id)
}

// GetRedactedVirtualKeys counts batched id+name reads, and flags the empty-id
// form, which returns every VK in the table.
func (s *scaleCountingConfigStore) GetRedactedVirtualKeys(ctx context.Context, ids []string) ([]configstoreTables.TableVirtualKey, error) {
	s.mu.Lock()
	s.getRedactedCalls++
	if len(ids) == 0 {
		s.getRedactedEmptyCalls++
	}
	s.mu.Unlock()
	return s.ConfigStore.GetRedactedVirtualKeys(ctx, ids)
}

// newScaleTestStore opens a SQLite config store in a temp dir.
func newScaleTestStore(t *testing.T) configstore.ConfigStore {
	t.Helper()
	store, err := configstore.NewConfigStore(context.Background(), &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "pgscale.db")},
	}, &mockLogger{})
	require.NoError(t, err)
	return store
}

// seedScaleVKs creates n VKs, each with VK-level, provider-level and per-model
// VK-scoped model configs, plus `noise` global model configs that no VK owns.
func seedScaleVKs(t *testing.T, store configstore.ConfigStore, n, noise int) []string {
	t.Helper()
	ctx := context.Background()
	active := true
	openai := "openai"
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		vkID := fmt.Sprintf("vk-scale-%03d", i)
		ids = append(ids, vkID)
		require.NoError(t, store.CreateVirtualKey(ctx, &configstoreTables.TableVirtualKey{
			ID:       vkID,
			Name:     fmt.Sprintf("Scale Key %03d", i),
			Value:    *schemas.NewSecretVar(fmt.Sprintf("sk-bf-scale-%03d", i)),
			IsActive: &active,
			ProviderConfigs: []configstoreTables.TableVirtualKeyProviderConfig{
				{VirtualKeyID: vkID, Provider: openai, AllowAllKeys: true, AllowedModels: schemas.WhiteList{"*"}},
			},
		}))
		scopeID := vkID
		for _, mc := range []*configstoreTables.TableModelConfig{
			{ID: "mc-vk-" + vkID, ModelName: configstoreTables.ModelConfigAllModels, Scope: configstoreTables.ModelConfigScopeVirtualKey, ScopeID: &scopeID,
				Budgets: []configstoreTables.TableBudget{{ID: "b-vk-" + vkID, MaxLimit: 100, CurrentUsage: float64(i), ResetDuration: "1d"}}},
			{ID: "mc-pc-" + vkID, ModelName: configstoreTables.ModelConfigAllModels, Scope: configstoreTables.ModelConfigScopeVirtualKey, ScopeID: &scopeID, Provider: &openai,
				Budgets: []configstoreTables.TableBudget{{ID: "b-pc-" + vkID, MaxLimit: 50, ResetDuration: "1d"}}},
			{ID: "mc-m-" + vkID, ModelName: "gpt-4o", Scope: configstoreTables.ModelConfigScopeVirtualKey, ScopeID: &scopeID, Provider: &openai,
				Budgets: []configstoreTables.TableBudget{{ID: "b-m-" + vkID, MaxLimit: 25, ResetDuration: "1d"}}},
		} {
			require.NoError(t, store.CreateModelConfig(ctx, mc))
		}
	}
	for i := 0; i < noise; i++ {
		require.NoError(t, store.CreateModelConfig(ctx, &configstoreTables.TableModelConfig{
			ID:        fmt.Sprintf("mc-noise-%04d", i),
			ModelName: fmt.Sprintf("noise-model-%04d", i),
			Scope:     configstoreTables.ModelConfigScopeGlobal,
		}))
	}
	return ids
}

// legacyHydrateVKList is the pre-fix hydration (every model config in the DB),
// kept here as the reference the batched version must match byte for byte.
func legacyHydrateVKList(t *testing.T, store configstore.ConfigStore, vks []configstoreTables.TableVirtualKey) {
	t.Helper()
	all, err := store.GetModelConfigs(context.Background())
	require.NoError(t, err)
	ptrs := make([]*configstoreTables.TableModelConfig, len(all))
	for i := range all {
		ptrs[i] = &all[i]
	}
	byKey := buildVKModelConfigIndex(ptrs)
	perModel := buildVKModelBudgetsIndex(ptrs)
	for i := range vks {
		applyVKGovernanceFromModelConfigs(&vks[i], byKey, perModel)
	}
}

// loadScaleVKPage loads the named VKs the way the list handler does.
func loadScaleVKPage(t *testing.T, store configstore.ConfigStore, ids []string) []configstoreTables.TableVirtualKey {
	t.Helper()
	page := make([]configstoreTables.TableVirtualKey, 0, len(ids))
	for _, id := range ids {
		vk, err := store.GetVirtualKey(context.Background(), id)
		require.NoError(t, err)
		page = append(page, *vk)
	}
	return page
}

// TestHydrateVKListGovernanceLoadsOnlyPageModelConfigs pins H10: hydrating one
// page of VKs must not load every model config in the database, and the
// response must be identical to the full-load hydration.
func TestHydrateVKListGovernanceLoadsOnlyPageModelConfigs(t *testing.T) {
	SetLogger(&mockLogger{})
	inner := newScaleTestStore(t)
	ids := seedScaleVKs(t, inner, 6, 150)
	pageIDs := ids[1:4]

	want := loadScaleVKPage(t, inner, pageIDs)
	legacyHydrateVKList(t, inner, want)

	store := &scaleCountingConfigStore{ConfigStore: inner}
	h := &GovernanceHandler{configStore: store}
	got := loadScaleVKPage(t, inner, pageIDs)
	require.NoError(t, h.hydrateVKListGovernance(context.Background(), got))

	require.Equal(t, 0, store.getModelConfigsCalls, "hydration must not load every model config")
	require.Equal(t, 1, store.byScopeIDsCalls, "one scoped load per page")
	require.Equal(t, len(pageIDs), store.byScopeIDsMaxIDs)

	wantJSON, err := json.Marshal(want)
	require.NoError(t, err)
	gotJSON, err := json.Marshal(got)
	require.NoError(t, err)
	require.JSONEq(t, string(wantJSON), string(gotJSON))
	require.Len(t, got[0].Budgets, 1)
	require.Len(t, got[0].ProviderConfigs[0].ModelBudgets, 1)
}

// TestHydrateVKListGovernanceChunksLargeLists pins that an export-sized VK list
// is loaded in bounded id chunks, so the IN list never approaches the Postgres
// bind-parameter limit.
func TestHydrateVKListGovernanceChunksLargeLists(t *testing.T) {
	SetLogger(&mockLogger{})
	inner := newScaleTestStore(t)
	store := &scaleCountingConfigStore{ConfigStore: inner}
	h := &GovernanceHandler{configStore: store}
	vks := make([]configstoreTables.TableVirtualKey, vkHydrationChunkSize*2+1)
	for i := range vks {
		vks[i].ID = fmt.Sprintf("vk-missing-%05d", i)
	}
	require.NoError(t, h.hydrateVKListGovernance(context.Background(), vks))
	require.Equal(t, 3, store.byScopeIDsCalls)
	require.Equal(t, vkHydrationChunkSize, store.byScopeIDsMaxIDs)
	require.Equal(t, 0, store.getModelConfigsCalls)
}

// hydrationFailingConfigStore serves an export-sized VK list and fails the
// second model-config chunk, so hydration fails after a partial load.
type hydrationFailingConfigStore struct {
	configstore.ConfigStore
	vks        []configstoreTables.TableVirtualKey
	chunkCalls int
}

// GetVirtualKeys returns the synthetic VK list (unpaginated path).
func (s *hydrationFailingConfigStore) GetVirtualKeys(context.Context) ([]configstoreTables.TableVirtualKey, error) {
	return append([]configstoreTables.TableVirtualKey(nil), s.vks...), nil
}

// GetVirtualKeysPaginated returns the synthetic VK list (paginated/export path).
func (s *hydrationFailingConfigStore) GetVirtualKeysPaginated(context.Context, configstore.VirtualKeyQueryParams) ([]configstoreTables.TableVirtualKey, int64, error) {
	return append([]configstoreTables.TableVirtualKey(nil), s.vks...), int64(len(s.vks)), nil
}

// GetModelConfigsByScopeAndScopeIDs succeeds for the first chunk and fails after.
func (s *hydrationFailingConfigStore) GetModelConfigsByScopeAndScopeIDs(context.Context, string, []string, ...*gorm.DB) ([]configstoreTables.TableModelConfig, error) {
	s.chunkCalls++
	if s.chunkCalls > 1 {
		return nil, fmt.Errorf("connection reset")
	}
	return nil, nil
}

// TestGetVirtualKeysFailsWhenGovernanceHydrationFails pins that a VK list whose
// governance hydration fails part-way returns 500 instead of a 200 whose keys
// carry no budgets or rate limits, which the UI would show as unlimited keys.
func TestGetVirtualKeysFailsWhenGovernanceHydrationFails(t *testing.T) {
	SetLogger(&mockLogger{})
	for _, query := range []string{"", "export=true"} {
		t.Run("query="+query, func(t *testing.T) {
			vks := make([]configstoreTables.TableVirtualKey, vkHydrationChunkSize+1)
			for i := range vks {
				vks[i].ID = fmt.Sprintf("vk-%05d", i)
			}
			store := &hydrationFailingConfigStore{vks: vks}
			h := &GovernanceHandler{configStore: store}
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI("/api/governance/virtual-keys?" + query)
			h.getVirtualKeys(ctx)
			require.Equal(t, 2, store.chunkCalls, "the second chunk must be the one that fails")
			require.Equal(t, 500, ctx.Response.StatusCode(), "partial hydration must not return keys without their limits: %s", ctx.Response.Body())
		})
	}
}

// TestEnrichModelConfigScopeNamesBatchesVirtualKeyLookups pins H11: resolving
// scope names for a page of VK-scoped model configs costs one batched id+name
// read instead of one fully preloaded GetVirtualKey per distinct VK, and yields
// the same names.
func TestEnrichModelConfigScopeNamesBatchesVirtualKeyLookups(t *testing.T) {
	SetLogger(&mockLogger{})
	inner := newScaleTestStore(t)
	ids := seedScaleVKs(t, inner, 5, 0)
	store := &scaleCountingConfigStore{ConfigStore: inner}
	h, err := NewGovernanceHandler(&mockGovernanceManagerForVK{}, store, nil, nil, nil, nil)
	require.NoError(t, err)

	missing := "vk-does-not-exist"
	configs := []configstoreTables.TableModelConfig{
		{ID: "g", Scope: configstoreTables.ModelConfigScopeGlobal},
		{ID: "missing", Scope: configstoreTables.ModelConfigScopeVirtualKey, ScopeID: &missing},
	}
	for i := range ids {
		for j := 0; j < 2; j++ {
			id := ids[i]
			configs = append(configs, configstoreTables.TableModelConfig{ID: fmt.Sprintf("%s-%d", id, j), Scope: configstoreTables.ModelConfigScopeVirtualKey, ScopeID: &id})
		}
	}
	h.enrichModelConfigScopeNames(context.Background(), configs)

	require.Equal(t, 0, store.getVirtualKeyCalls, "must not read each VK with all its preloads")
	require.Equal(t, 1, store.getRedactedCalls)
	require.Equal(t, 0, store.getRedactedEmptyCalls)
	require.Equal(t, "", configs[0].ScopeName)
	require.Equal(t, "", configs[1].ScopeName)
	for _, mc := range configs[2:] {
		vk, err := inner.GetVirtualKey(context.Background(), *mc.ScopeID)
		require.NoError(t, err)
		require.Equal(t, vk.Name, mc.ScopeName)
	}

	// A page with no VK-scoped configs must never issue the empty-id read,
	// which would return every VK in the table.
	store.getRedactedCalls = 0
	h.enrichModelConfigScopeNames(context.Background(), []configstoreTables.TableModelConfig{{ID: "g", Scope: configstoreTables.ModelConfigScopeGlobal}})
	require.Equal(t, 0, store.getRedactedCalls)
	require.Equal(t, 0, store.getRedactedEmptyCalls)
}

// TestRegisterScopeNameResolverOverridesBatchResolver pins that a downstream
// build re-registering a per-id resolver for a scope wins over the built-in
// batch resolver for that scope.
func TestRegisterScopeNameResolverOverridesBatchResolver(t *testing.T) {
	SetLogger(&mockLogger{})
	inner := newScaleTestStore(t)
	ids := seedScaleVKs(t, inner, 1, 0)
	store := &scaleCountingConfigStore{ConfigStore: inner}
	h, err := NewGovernanceHandler(&mockGovernanceManagerForVK{}, store, nil, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		// Restore the default resolvers for later tests in the package.
		_, _ = NewGovernanceHandler(&mockGovernanceManagerForVK{}, inner, nil, nil, nil, nil)
	})

	RegisterScopeNameResolver(configstoreTables.ModelConfigScopeVirtualKey, func(_ context.Context, id string) (string, bool) {
		return "override-" + id, true
	})
	configs := []configstoreTables.TableModelConfig{{ID: "a", Scope: configstoreTables.ModelConfigScopeVirtualKey, ScopeID: &ids[0]}}
	h.enrichModelConfigScopeNames(context.Background(), configs)
	require.Equal(t, "override-"+ids[0], configs[0].ScopeName)
	require.Equal(t, 0, store.getRedactedCalls)
}

// mcpRefreshConfigStore serves one MCP client row and its VK assignments, and
// counts the reads the MCP client update path makes.
type mcpRefreshConfigStore struct {
	configstore.ConfigStore

	client          *configstoreTables.TableMCPClient
	assignments     []configstoreTables.TableVirtualKeyMCPConfig
	getClientCalls  int
	assignmentCalls int
}

// GetMCPClientByID returns the configured client row.
func (s *mcpRefreshConfigStore) GetMCPClientByID(_ context.Context, id string) (*configstoreTables.TableMCPClient, error) {
	s.getClientCalls++
	if s.client == nil || s.client.ClientID != id {
		return nil, configstore.ErrNotFound
	}
	c := *s.client
	return &c, nil
}

// GetVirtualKeyMCPConfigsByMCPClientID returns the configured assignments.
func (s *mcpRefreshConfigStore) GetVirtualKeyMCPConfigsByMCPClientID(_ context.Context, _ uint) ([]configstoreTables.TableVirtualKeyMCPConfig, error) {
	s.assignmentCalls++
	return s.assignments, nil
}

// reloadCountingGovernanceManager counts per-key reloads.
type reloadCountingGovernanceManager struct {
	GovernanceManager
	reloaded []string
	// reloadedBatches records every ReloadVirtualKeys id list; batchErr, when
	// set, makes the batched reload fail.
	reloadedBatches [][]string
	batchErr        error
}

// ReloadVirtualKeys records the batch and returns batchErr.
func (m *reloadCountingGovernanceManager) ReloadVirtualKeys(_ context.Context, ids []string) error {
	if m.batchErr != nil {
		return m.batchErr
	}
	m.reloadedBatches = append(m.reloadedBatches, append([]string(nil), ids...))
	return nil
}

// ReloadVirtualKey records the reloaded key.
func (m *reloadCountingGovernanceManager) ReloadVirtualKey(_ context.Context, id string) (*configstoreTables.TableVirtualKey, error) {
	m.reloaded = append(m.reloaded, id)
	return nil, nil
}

// newMCPRefreshFixture builds a governance store holding n keys assigned to MCP
// client 7 and one key assigned only to client 8.
func newMCPRefreshFixture(t *testing.T, n int) (*governance.LocalGovernanceStore, []configstoreTables.TableVirtualKeyMCPConfig) {
	t.Helper()
	oldClient := configstoreTables.TableMCPClient{ID: 7, ClientID: "c7", Name: "old_name"}
	other := configstoreTables.TableMCPClient{ID: 8, ClientID: "c8", Name: "other"}
	active := true
	var vks []configstoreTables.TableVirtualKey
	var assignments []configstoreTables.TableVirtualKeyMCPConfig
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("vk-mcp-%03d", i)
		cfg := configstoreTables.TableVirtualKeyMCPConfig{ID: uint(i + 1), VirtualKeyID: id, MCPClientID: 7, MCPClient: oldClient, ToolsToExecute: schemas.WhiteList{"*"}}
		assignments = append(assignments, cfg)
		vks = append(vks, configstoreTables.TableVirtualKey{ID: id, Name: id, Value: *schemas.NewSecretVar("sk-bf-" + id), IsActive: &active, MCPConfigs: []configstoreTables.TableVirtualKeyMCPConfig{cfg}})
	}
	vks = append(vks, configstoreTables.TableVirtualKey{ID: "vk-other", Name: "vk-other", Value: *schemas.NewSecretVar("sk-bf-other"), IsActive: &active,
		MCPConfigs: []configstoreTables.TableVirtualKeyMCPConfig{{ID: 999, VirtualKeyID: "vk-other", MCPClientID: 8, MCPClient: other}}})
	store, err := governance.NewLocalGovernanceStore(context.Background(), &mockLogger{}, nil, &configstore.GovernanceConfig{VirtualKeys: vks}, nil, nil)
	require.NoError(t, err)
	return store, assignments
}

// TestUpdateMCPClientReloadsAssignedVirtualKeysInOneCall pins that an MCP client
// update reloads every assigned key through one batched ReloadVirtualKeys call,
// which does per key what ReloadVirtualKey does (key re-read, model configs,
// token and credential eviction) and which a clustered deployment propagates,
// with no per-key reloads.
func TestUpdateMCPClientReloadsAssignedVirtualKeysInOneCall(t *testing.T) {
	SetLogger(&mockLogger{})
	_, assignments := newMCPRefreshFixture(t, 3)
	cs := &mcpRefreshConfigStore{client: &configstoreTables.TableMCPClient{ID: 7, ClientID: "c7", Name: "new_name"}, assignments: assignments}
	gm := &reloadCountingGovernanceManager{}
	h := &MCPHandler{store: &lib.Config{ConfigStore: cs}, governanceManager: gm}

	h.refreshMCPClientOnAssignedVirtualKeys(context.Background(), "c7", 7)

	require.Equal(t, [][]string{{"vk-mcp-000", "vk-mcp-001", "vk-mcp-002"}}, gm.reloadedBatches, "one batched reload of every assigned key")
	require.Empty(t, gm.reloaded, "no per-key reloads when the batched reload succeeds")
}

// TestUpdateMCPClientBatchedReloadFallsBackPerKey pins that when the batched
// reload fails, every assigned key is still reloaded, one ReloadVirtualKey each.
func TestUpdateMCPClientBatchedReloadFallsBackPerKey(t *testing.T) {
	SetLogger(&mockLogger{})
	_, assignments := newMCPRefreshFixture(t, 3)
	cs := &mcpRefreshConfigStore{client: &configstoreTables.TableMCPClient{ID: 7, ClientID: "c7"}, assignments: assignments}
	gm := &reloadCountingGovernanceManager{batchErr: fmt.Errorf("config store does not support batched virtual key reads")}
	h := &MCPHandler{store: &lib.Config{ConfigStore: cs}, governanceManager: gm}

	h.refreshMCPClientOnAssignedVirtualKeys(context.Background(), "c7", 7)
	require.Equal(t, []string{"vk-mcp-000", "vk-mcp-001", "vk-mcp-002"}, gm.reloaded)
}
