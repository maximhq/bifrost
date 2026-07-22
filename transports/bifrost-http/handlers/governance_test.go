package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/governance/complexity"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// mockGovernanceManagerForVK embeds the interface so unimplemented methods panic.
// Only GetGovernanceData is needed for the getVirtualKeys handler path.
type mockGovernanceManagerForVK struct {
	GovernanceManager
	getGovernanceDataCalls int
	data                   *governance.GovernanceData
}

func (m *mockGovernanceManagerForVK) GetGovernanceData(ctx context.Context) *governance.GovernanceData {
	m.getGovernanceDataCalls++
	return m.data
}

// mockConfigStoreForVK embeds the interface so unimplemented methods panic.
// Only GetVirtualKeysPaginated is called in the paginated path.
type mockConfigStoreForVK struct {
	configstore.ConfigStore
	getVirtualKeysCalls          int
	getVirtualKeysPaginatedCalls int
}

func (m *mockConfigStoreForVK) GetVirtualKeysPaginated(_ context.Context, _ configstore.VirtualKeyQueryParams) ([]configstoreTables.TableVirtualKey, int64, error) {
	m.getVirtualKeysPaginatedCalls++
	return nil, 0, nil
}

func (m *mockConfigStoreForVK) GetVirtualKeys(_ context.Context) ([]configstoreTables.TableVirtualKey, error) {
	m.getVirtualKeysCalls++
	return nil, nil
}

type mockRotateConfigStore struct {
	configstore.ConfigStore
	virtualKeys  map[string]*configstoreTables.TableVirtualKey
	modelConfigs map[string]*configstoreTables.TableModelConfig
	updates      int
	updateErr    error
	deletes      int
	deleteErr    error
}

func cloneTestVirtualKey(vk *configstoreTables.TableVirtualKey) *configstoreTables.TableVirtualKey {
	if vk == nil {
		return nil
	}
	clone := *vk
	clone.Budgets = append([]configstoreTables.TableBudget(nil), vk.Budgets...)
	clone.ProviderConfigs = append([]configstoreTables.TableVirtualKeyProviderConfig(nil), vk.ProviderConfigs...)
	clone.MCPConfigs = append([]configstoreTables.TableVirtualKeyMCPConfig(nil), vk.MCPConfigs...)
	return &clone
}

func (m *mockRotateConfigStore) GetVirtualKey(_ context.Context, id string) (*configstoreTables.TableVirtualKey, error) {
	vk, ok := m.virtualKeys[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return cloneTestVirtualKey(vk), nil
}

func (m *mockRotateConfigStore) UpdateVirtualKey(_ context.Context, virtualKey *configstoreTables.TableVirtualKey, _ ...*gorm.DB) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	existing, ok := m.virtualKeys[virtualKey.ID]
	if !ok {
		return configstore.ErrNotFound
	}
	updated := cloneTestVirtualKey(existing)
	updated.Value = virtualKey.Value
	m.virtualKeys[virtualKey.ID] = updated
	m.updates++
	return nil
}

func (m *mockRotateConfigStore) DeleteVirtualKey(_ context.Context, id string, _ ...*gorm.DB) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	if _, ok := m.virtualKeys[id]; !ok {
		return configstore.ErrNotFound
	}
	delete(m.virtualKeys, id)
	m.deletes++
	return nil
}

// lookupVKModelConfig resolves a VK-scoped wildcard model config from the provided
// map, mirroring the shape hydrateVKGovernance expects (scope=virtual_key,
// model_name='*'). Returns ErrNotFound when absent so callers exercise the
// "no governance" branch.
func lookupVKModelConfig(modelConfigs map[string]*configstoreTables.TableModelConfig, scope string, scopeID *string, modelName string, provider *string) (*configstoreTables.TableModelConfig, error) {
	if scope != configstoreTables.ModelConfigScopeVirtualKey || modelName != configstoreTables.ModelConfigAllModels || scopeID == nil {
		return nil, configstore.ErrNotFound
	}
	if mc, ok := modelConfigs[vkModelConfigIndexKey(*scopeID, provider)]; ok {
		return mc, nil
	}
	return nil, configstore.ErrNotFound
}

func (m *mockRotateConfigStore) GetModelConfig(_ context.Context, scope string, scopeID *string, modelName string, provider *string) (*configstoreTables.TableModelConfig, error) {
	return lookupVKModelConfig(m.modelConfigs, scope, scopeID, modelName, provider)
}

type mockRotateGovernanceManager struct {
	GovernanceManager
	store     *mockRotateConfigStore
	reloadIDs []string
	reloadErr error
	removeIDs []string
	removeErr error
}

func (m *mockRotateGovernanceManager) ReloadVirtualKey(ctx context.Context, id string) (*configstoreTables.TableVirtualKey, error) {
	m.reloadIDs = append(m.reloadIDs, id)
	if m.reloadErr != nil {
		return nil, m.reloadErr
	}
	return m.store.GetVirtualKey(ctx, id)
}

func (m *mockRotateGovernanceManager) RemoveVirtualKey(_ context.Context, id string) error {
	m.removeIDs = append(m.removeIDs, id)
	if m.removeErr != nil {
		return m.removeErr
	}
	return nil
}

type mockComplexityGovernanceManager struct {
	GovernanceManager
	reloadedConfig *complexity.AnalyzerConfig
	reloadCalls    int
	reloadErr      error
}

func (m *mockComplexityGovernanceManager) ReloadComplexityAnalyzerConfig(_ context.Context, config *complexity.AnalyzerConfig) error {
	m.reloadCalls++
	m.reloadedConfig = config
	return m.reloadErr
}

func testComplexityAnalyzerPayload(t *testing.T, cfg complexity.AnalyzerConfig) string {
	t.Helper()
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal complexity analyzer config: %v", err)
	}
	return string(body)
}

func TestComplexityAnalyzerConfigGetReturnsDefaultsWhenUnset(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	handler := &GovernanceHandler{
		configStore:       store,
		governanceManager: &mockComplexityGovernanceManager{},
	}

	ctx := newTestRequestCtx("")
	handler.getComplexityAnalyzerConfig(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	var resp complexity.AnalyzerConfig
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.TierBoundaries != complexity.DefaultTierBoundaries() {
		t.Fatalf("expected default boundaries, got %+v", resp.TierBoundaries)
	}
	if len(resp.Keywords.CodeKeywords) == 0 {
		t.Fatalf("expected default code keywords")
	}
}

func TestComplexityAnalyzerConfigPutPersistsAndReloads(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	manager := &mockComplexityGovernanceManager{}
	handler := &GovernanceHandler{
		configStore:       store,
		governanceManager: manager,
	}

	cfg := complexity.DefaultAnalyzerConfig()
	cfg.TierBoundaries.SimpleMedium = 0.12
	cfg.TierBoundaries.MediumComplex = 0.34
	cfg.TierBoundaries.ComplexReasoning = 0.78
	cfg.Keywords.CodeKeywords = []string{" Function ", "api", "API"}

	ctx := newTestRequestCtx(testComplexityAnalyzerPayload(t, cfg))
	handler.updateComplexityAnalyzerConfig(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if manager.reloadCalls != 1 {
		t.Fatalf("expected one reload, got %d", manager.reloadCalls)
	}
	if manager.reloadedConfig == nil || manager.reloadedConfig.TierBoundaries.ComplexReasoning != 0.78 {
		t.Fatalf("expected reload with normalized config, got %+v", manager.reloadedConfig)
	}

	stored, err := store.GetComplexityAnalyzerConfig(context.Background())
	if err != nil {
		t.Fatalf("get stored config: %v", err)
	}
	if stored == nil || len(stored.Keywords.CodeKeywords) != 2 || stored.Keywords.CodeKeywords[0] != "api" {
		t.Fatalf("expected normalized stored keywords, got %+v", stored)
	}
}

func TestComplexityAnalyzerConfigPutRejectsInvalidPayloads(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	handler := &GovernanceHandler{
		configStore:       store,
		governanceManager: &mockComplexityGovernanceManager{},
	}

	valid := complexity.DefaultAnalyzerConfig()
	validBody := testComplexityAnalyzerPayload(t, valid)
	invalidBoundaries := valid
	invalidBoundaries.TierBoundaries.MediumComplex = invalidBoundaries.TierBoundaries.SimpleMedium
	emptyKeywords := valid
	emptyKeywords.Keywords.CodeKeywords = nil

	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "unknown field", body: strings.TrimSuffix(validBody, "}") + `,"extra":true}`, want: "Invalid request payload"},
		{name: "multiple json values", body: validBody + `{}`, want: "multiple JSON values"},
		{name: "invalid boundaries", body: testComplexityAnalyzerPayload(t, invalidBoundaries), want: "tier boundaries"},
		{name: "empty keywords", body: testComplexityAnalyzerPayload(t, emptyKeywords), want: "keyword lists must be non-empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestRequestCtx(tt.body)
			handler.updateComplexityAnalyzerConfig(ctx)
			if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
				t.Fatalf("expected status 400, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
			}
			if !strings.Contains(string(ctx.Response.Body()), tt.want) {
				t.Fatalf("expected response to contain %q, got %s", tt.want, string(ctx.Response.Body()))
			}
		})
	}
}

func TestComplexityAnalyzerConfigResetPersistsDefaultsAndReloads(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	manager := &mockComplexityGovernanceManager{}
	handler := &GovernanceHandler{
		configStore:       store,
		governanceManager: manager,
	}

	custom := complexity.DefaultAnalyzerConfig()
	custom.TierBoundaries.ComplexReasoning = 0.80
	if err := store.UpdateComplexityAnalyzerConfig(context.Background(), &custom); err != nil {
		t.Fatalf("seed custom config: %v", err)
	}

	ctx := newTestRequestCtx("")
	handler.resetComplexityAnalyzerConfig(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if manager.reloadCalls != 1 {
		t.Fatalf("expected one reload, got %d", manager.reloadCalls)
	}
	stored, err := store.GetComplexityAnalyzerConfig(context.Background())
	if err != nil {
		t.Fatalf("get stored config: %v", err)
	}
	if stored == nil || stored.TierBoundaries != complexity.DefaultTierBoundaries() {
		t.Fatalf("expected stored defaults, got %+v", stored)
	}
}

func TestApplyVirtualKeyOwnershipUpdatePreservesOmittedAssociation(t *testing.T) {
	teamID := "team-1"
	customerID := "customer-1"
	vk := &configstoreTables.TableVirtualKey{ID: "vk-1", TeamID: &teamID, CustomerID: &customerID}
	var req UpdateVirtualKeyRequest
	if err := json.Unmarshal([]byte(`{"name":"renamed"}`), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	if err := applyVirtualKeyOwnershipUpdate(vk, &req); err != nil {
		t.Fatalf("apply ownership update: %v", err)
	}
	if vk.TeamID == nil || *vk.TeamID != teamID || vk.CustomerID == nil || *vk.CustomerID != customerID {
		t.Fatalf("omitted ownership fields changed association: %#v", vk)
	}
}

func TestApplyVirtualKeyOwnershipUpdateSwitchesAndClearsAssociation(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		initialTeam  *string
		initialCust  *string
		wantTeam     *string
		wantCustomer *string
	}{
		{
			name:         "set team clears customer",
			body:         `{"team_id":"team-2"}`,
			initialCust:  schemas.Ptr("customer-1"),
			wantTeam:     schemas.Ptr("team-2"),
			wantCustomer: nil,
		},
		{
			name:         "set customer clears team",
			body:         `{"customer_id":"customer-2"}`,
			initialTeam:  schemas.Ptr("team-1"),
			wantTeam:     nil,
			wantCustomer: schemas.Ptr("customer-2"),
		},
		{
			name:         "null team clears both",
			body:         `{"team_id":null}`,
			initialTeam:  schemas.Ptr("team-1"),
			initialCust:  schemas.Ptr("customer-1"),
			wantTeam:     nil,
			wantCustomer: nil,
		},
		{
			name:         "empty customer clears both",
			body:         `{"customer_id":""}`,
			initialTeam:  schemas.Ptr("team-1"),
			initialCust:  schemas.Ptr("customer-1"),
			wantTeam:     nil,
			wantCustomer: nil,
		},
		{
			name:         "null team and customer clears both",
			body:         `{"team_id":null,"customer_id":null}`,
			initialTeam:  schemas.Ptr("team-1"),
			initialCust:  schemas.Ptr("customer-1"),
			wantTeam:     nil,
			wantCustomer: nil,
		},
		{
			name:         "empty team and customer clears both",
			body:         `{"team_id":"","customer_id":""}`,
			initialTeam:  schemas.Ptr("team-1"),
			initialCust:  schemas.Ptr("customer-1"),
			wantTeam:     nil,
			wantCustomer: nil,
		},
		{
			name:         "team with null customer sets team",
			body:         `{"team_id":"team-2","customer_id":null}`,
			initialCust:  schemas.Ptr("customer-1"),
			wantTeam:     schemas.Ptr("team-2"),
			wantCustomer: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vk := &configstoreTables.TableVirtualKey{ID: "vk-1", TeamID: tt.initialTeam, CustomerID: tt.initialCust}
			var req UpdateVirtualKeyRequest
			if err := json.Unmarshal([]byte(tt.body), &req); err != nil {
				t.Fatalf("unmarshal request: %v", err)
			}
			if err := applyVirtualKeyOwnershipUpdate(vk, &req); err != nil {
				t.Fatalf("apply ownership update: %v", err)
			}
			assertStringPtrEqual(t, "team", vk.TeamID, tt.wantTeam)
			assertStringPtrEqual(t, "customer", vk.CustomerID, tt.wantCustomer)
		})
	}
}

func TestApplyVirtualKeyOwnershipUpdateRejectsDualAssociation(t *testing.T) {
	vk := &configstoreTables.TableVirtualKey{ID: "vk-1"}
	var req UpdateVirtualKeyRequest
	if err := json.Unmarshal([]byte(`{"team_id":"team-1","customer_id":"customer-1"}`), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if err := applyVirtualKeyOwnershipUpdate(vk, &req); !errors.Is(err, errVirtualKeyDualAssociation) {
		t.Fatalf("expected dual-association error, got %v", err)
	}
}

func assertStringPtrEqual(t *testing.T, label string, got *string, want *string) {
	t.Helper()
	if got == nil || want == nil {
		if got != want {
			t.Fatalf("%s pointer nil mismatch: got %v, want %v", label, got, want)
		}
		return
	}
	if *got != *want {
		t.Fatalf("%s value = %q, want %q", label, *got, *want)
	}
}

func TestFindExistingBudgetPrefersIDOverResetDuration(t *testing.T) {
	monthlyBudget := configstoreTables.TableBudget{
		ID:            "budget-monthly",
		MaxLimit:      100,
		ResetDuration: "1M",
		CurrentUsage:  75,
	}
	dailyBudget := configstoreTables.TableBudget{
		ID:            "budget-daily",
		MaxLimit:      20,
		ResetDuration: "1d",
		CurrentUsage:  3,
	}

	request := CreateBudgetRequest{
		ID:            "budget-monthly",
		MaxLimit:      120,
		ResetDuration: "1d",
	}
	byID, byDuration := buildBudgetLookup([]configstoreTables.TableBudget{monthlyBudget, dailyBudget}, []CreateBudgetRequest{request})
	matched, found, err := findExistingBudget(request, byID, byDuration)
	if err != nil {
		t.Fatalf("expected budget match, got error: %v", err)
	}
	if !found {
		t.Fatal("expected budget to be found")
	}
	if matched.ID != monthlyBudget.ID || matched.CurrentUsage != monthlyBudget.CurrentUsage {
		t.Fatalf("expected ID match to preserve the monthly budget, got %#v", matched)
	}
}

func TestFindExistingBudgetRejectsUnknownID(t *testing.T) {
	request := CreateBudgetRequest{
		ID:            "missing-budget",
		MaxLimit:      100,
		ResetDuration: "1d",
	}
	byID, byDuration := buildBudgetLookup([]configstoreTables.TableBudget{
		{ID: "budget-1", ResetDuration: "1d"},
	}, []CreateBudgetRequest{request})

	_, _, err := findExistingBudget(request, byID, byDuration)
	if err == nil {
		t.Fatal("expected unknown budget ID to fail")
	}
}

// TestBudgetFrequencyReplaceInheritsUsageFromOriginalBudgets exercises the
// scenario where the only existing shorter-duration budget is replaced (not
// renamed by ID) by a longer-duration budget in the same request. The usage
// from the original shorter budget must be inherited; if the inheritance
// source were the partially-built reconciled slice, it would be empty here
// (the shorter budget is being deleted, not reconciled) and usage would be
// lost.
func TestBudgetFrequencyReplaceInheritsUsageFromOriginalBudgets(t *testing.T) {
	originalLastReset := time.Now().Add(-3 * time.Hour)
	reconciled, err := reconcileBudgetRequestsForTest(
		[]configstoreTables.TableBudget{
			{
				ID:            "vk-budget-daily",
				MaxLimit:      100,
				ResetDuration: "1d",
				CurrentUsage:  42,
				LastReset:     originalLastReset,
			},
		},
		[]CreateBudgetRequest{
			{MaxLimit: 500, ResetDuration: "1w"},
		},
		false,
	)
	if err != nil {
		t.Fatalf("expected reconcile to succeed: %v", err)
	}
	if len(reconciled) != 1 {
		t.Fatalf("expected one reconciled budget, got %d: %#v", len(reconciled), reconciled)
	}
	if reconciled[0].ResetDuration != "1w" || reconciled[0].MaxLimit != 500 {
		t.Fatalf("expected new weekly@500 budget, got %#v", reconciled[0])
	}
	if reconciled[0].CurrentUsage != 42 {
		t.Fatalf("expected usage to be inherited from the daily budget (42), got %v", reconciled[0].CurrentUsage)
	}
}

// TestBudgetLookupConsumesMatchedRowsForDurationSwap exercises the scenario
// where the request renames an existing budget by ID to a longer duration
// while also adding a new budget reusing the old duration. The lookup must
// reserve the renamed row for the ID-specified entry so the duration-only
// entry creates a fresh budget instead of stealing the row.
func TestBudgetLookupConsumesMatchedRowsForDurationSwap(t *testing.T) {
	originalLastReset := time.Now().Add(-2 * time.Hour)
	reconciled, err := reconcileBudgetRequestsForTest(
		[]configstoreTables.TableBudget{
			{
				ID:            "vk-budget-1",
				MaxLimit:      100,
				ResetDuration: "1d",
				CurrentUsage:  42,
				LastReset:     originalLastReset,
			},
		},
		[]CreateBudgetRequest{
			{ID: "vk-budget-1", MaxLimit: 200, ResetDuration: "1w"},
			{MaxLimit: 50, ResetDuration: "1d"},
		},
		false,
	)
	if err != nil {
		t.Fatalf("expected reconcile to succeed: %v", err)
	}
	if len(reconciled) != 2 {
		t.Fatalf("expected two reconciled budgets, got %d: %#v", len(reconciled), reconciled)
	}

	var rename, fresh *configstoreTables.TableBudget
	for i := range reconciled {
		b := &reconciled[i]
		if b.ID == "vk-budget-1" {
			rename = b
		} else {
			fresh = b
		}
	}
	if rename == nil {
		t.Fatal("expected renamed budget to retain vk-budget-1 ID")
	}
	if rename.ResetDuration != "1w" || rename.MaxLimit != 200 {
		t.Fatalf("expected vk-budget-1 to become weekly@200, got %#v", rename)
	}
	if rename.CurrentUsage != 42 {
		t.Fatalf("expected rename to preserve usage 42, got %#v", rename)
	}
	if fresh == nil {
		t.Fatal("expected a new daily budget to be created alongside the rename")
	}
	if fresh.ResetDuration != "1d" || fresh.MaxLimit != 50 {
		t.Fatalf("expected fresh daily@50, got %#v", fresh)
	}
}

func TestResetBudgetUsageIfRequested(t *testing.T) {
	originalLastReset := time.Now().Add(-24 * time.Hour)
	budget := configstoreTables.TableBudget{
		ID:            "budget-1",
		MaxLimit:      100,
		ResetDuration: "1d",
		CurrentUsage:  42,
		LastReset:     originalLastReset,
	}

	resetBudgetUsageIfRequested(&budget, false, false)
	if budget.CurrentUsage != 42 || !budget.LastReset.Equal(originalLastReset) {
		t.Fatalf("expected usage to be preserved when reset is false, got %#v", budget)
	}

	resetBudgetUsageIfRequested(&budget, true, false)
	if budget.CurrentUsage != 0 {
		t.Fatalf("expected usage to reset, got %#v", budget)
	}
	if !budget.LastReset.After(originalLastReset) {
		t.Fatalf("expected last reset to advance, got %s", budget.LastReset)
	}
}

func reconcileBudgetRequestsForTest(existing []configstoreTables.TableBudget, requests []CreateBudgetRequest, resetUsage bool) ([]configstoreTables.TableBudget, error) {
	requestBudgets := append([]CreateBudgetRequest(nil), requests...)
	sort.Slice(requestBudgets, func(i, j int) bool {
		return compareBudgetRequestDurations(requestBudgets[i], requestBudgets[j])
	})

	byID, byDuration := buildBudgetLookup(existing, requestBudgets)
	reconciled := make([]configstoreTables.TableBudget, 0, len(requestBudgets))
	for _, request := range requestBudgets {
		budget, found, err := findExistingBudget(request, byID, byDuration)
		if err != nil {
			return nil, err
		}
		if !found {
			budget = configstoreTables.TableBudget{
				ID:            "new-budget",
				MaxLimit:      request.MaxLimit,
				CurrentUsage:  0,
				LastReset:     budgetLastReset(false, request.ResetDuration),
				ResetDuration: request.ResetDuration,
			}
			inheritUsageFromClosestShorterBudget(&budget, existing, resetUsage)
		}
		budget.MaxLimit = request.MaxLimit
		budget.ResetDuration = request.ResetDuration
		resetBudgetUsageIfRequested(&budget, resetUsage, false)
		reconciled = append(reconciled, budget)
	}
	return reconciled, nil
}

func TestVirtualKeyBudgetFrequencyChangePreservesUsageWhenRequested(t *testing.T) {
	originalLastReset := time.Now().Add(-2 * time.Hour)
	reconciled, err := reconcileBudgetRequestsForTest(
		[]configstoreTables.TableBudget{
			{
				ID:            "vk-budget-1",
				MaxLimit:      100,
				ResetDuration: "1M",
				CurrentUsage:  100,
				LastReset:     originalLastReset,
			},
		},
		[]CreateBudgetRequest{
			{
				ID:            "vk-budget-1",
				MaxLimit:      150,
				ResetDuration: "1d",
			},
		},
		false,
	)
	if err != nil {
		t.Fatalf("expected reconcile to succeed: %v", err)
	}
	if len(reconciled) != 1 {
		t.Fatalf("expected one budget, got %d", len(reconciled))
	}
	if reconciled[0].ID != "vk-budget-1" || reconciled[0].ResetDuration != "1d" || reconciled[0].MaxLimit != 150 {
		t.Fatalf("expected same budget to be updated, got %#v", reconciled[0])
	}
	if reconciled[0].CurrentUsage != 100 || !reconciled[0].LastReset.Equal(originalLastReset) {
		t.Fatalf("expected usage and last reset to be preserved, got %#v", reconciled[0])
	}
}

func TestProviderBudgetFrequencyChangePreservesUsageWhenRequested(t *testing.T) {
	originalLastReset := time.Now().Add(-2 * time.Hour)
	providerConfigID := uint(7)
	reconciled, err := reconcileBudgetRequestsForTest(
		[]configstoreTables.TableBudget{
			{
				ID:               "provider-budget-1",
				MaxLimit:         100,
				ResetDuration:    "1M",
				CurrentUsage:     100,
				LastReset:        originalLastReset,
				ProviderConfigID: &providerConfigID,
			},
		},
		[]CreateBudgetRequest{
			{
				ID:            "provider-budget-1",
				MaxLimit:      150,
				ResetDuration: "1d",
			},
		},
		false,
	)
	if err != nil {
		t.Fatalf("expected reconcile to succeed: %v", err)
	}
	if len(reconciled) != 1 {
		t.Fatalf("expected one budget, got %d", len(reconciled))
	}
	if reconciled[0].ID != "provider-budget-1" || reconciled[0].ResetDuration != "1d" || reconciled[0].MaxLimit != 150 {
		t.Fatalf("expected same provider budget to be updated, got %#v", reconciled[0])
	}
	if reconciled[0].CurrentUsage != 100 || !reconciled[0].LastReset.Equal(originalLastReset) {
		t.Fatalf("expected provider usage and last reset to be preserved, got %#v", reconciled[0])
	}
}

func TestBudgetFrequencyChangeResetsUsageWhenRequested(t *testing.T) {
	originalLastReset := time.Now().Add(-2 * time.Hour)
	reconciled, err := reconcileBudgetRequestsForTest(
		[]configstoreTables.TableBudget{
			{
				ID:            "budget-1",
				MaxLimit:      100,
				ResetDuration: "1M",
				CurrentUsage:  100,
				LastReset:     originalLastReset,
			},
		},
		[]CreateBudgetRequest{
			{
				ID:            "budget-1",
				MaxLimit:      150,
				ResetDuration: "1d",
			},
		},
		true,
	)
	if err != nil {
		t.Fatalf("expected reconcile to succeed: %v", err)
	}
	if reconciled[0].CurrentUsage != 0 {
		t.Fatalf("expected usage to reset, got %#v", reconciled[0])
	}
	if !reconciled[0].LastReset.After(originalLastReset) {
		t.Fatalf("expected last reset to advance, got %s", reconciled[0].LastReset)
	}
}

func TestExistingVirtualKeyBudgetLoweredBelowPreservedUsageIsAllowed(t *testing.T) {
	reconciled, err := reconcileBudgetRequestsForTest(
		[]configstoreTables.TableBudget{
			{
				ID:            "monthly-budget",
				MaxLimit:      300,
				ResetDuration: "1M",
				CurrentUsage:  0.11,
			},
		},
		[]CreateBudgetRequest{
			{
				ID:            "monthly-budget",
				MaxLimit:      0.01,
				ResetDuration: "1M",
			},
		},
		false,
	)
	if err != nil {
		t.Fatalf("expected preserving usage above lowered budget to be allowed: %v", err)
	}
	if reconciled[0].CurrentUsage != 0.11 || reconciled[0].MaxLimit != 0.01 {
		t.Fatalf("expected usage to be preserved above lowered budget, got %#v", reconciled[0])
	}
}

func TestExistingProviderBudgetLoweredBelowPreservedUsageIsAllowed(t *testing.T) {
	reconciled, err := reconcileBudgetRequestsForTest(
		[]configstoreTables.TableBudget{
			{
				ID:            "provider-monthly-budget",
				MaxLimit:      300,
				ResetDuration: "1M",
				CurrentUsage:  0.11,
			},
		},
		[]CreateBudgetRequest{
			{
				ID:            "provider-monthly-budget",
				MaxLimit:      0.01,
				ResetDuration: "1M",
			},
		},
		false,
	)
	if err != nil {
		t.Fatalf("expected preserving provider usage above lowered budget to be allowed: %v", err)
	}
	if reconciled[0].CurrentUsage != 0.11 || reconciled[0].MaxLimit != 0.01 {
		t.Fatalf("expected provider usage to be preserved above lowered budget, got %#v", reconciled[0])
	}
}

func TestExistingBudgetLoweredBelowUsageSucceedsWhenResetRequested(t *testing.T) {
	reconciled, err := reconcileBudgetRequestsForTest(
		[]configstoreTables.TableBudget{
			{
				ID:            "monthly-budget",
				MaxLimit:      300,
				ResetDuration: "1M",
				CurrentUsage:  0.11,
			},
		},
		[]CreateBudgetRequest{
			{
				ID:            "monthly-budget",
				MaxLimit:      0.01,
				ResetDuration: "1M",
			},
		},
		true,
	)
	if err != nil {
		t.Fatalf("expected reset usage to allow lower budget: %v", err)
	}
	if reconciled[0].CurrentUsage != 0 {
		t.Fatalf("expected usage to reset, got %#v", reconciled[0])
	}
}

func TestNewVirtualKeyBudgetInheritsClosestShorterUsage(t *testing.T) {
	reconciled, err := reconcileBudgetRequestsForTest(
		[]configstoreTables.TableBudget{
			{
				ID:            "weekly-budget",
				MaxLimit:      100,
				ResetDuration: "1w",
				CurrentUsage:  100,
			},
		},
		[]CreateBudgetRequest{
			{
				ID:            "weekly-budget",
				MaxLimit:      100,
				ResetDuration: "1w",
			},
			{
				MaxLimit:      300,
				ResetDuration: "1M",
			},
		},
		false,
	)
	if err != nil {
		t.Fatalf("expected reconcile to succeed: %v", err)
	}
	if len(reconciled) != 2 {
		t.Fatalf("expected two budgets, got %d", len(reconciled))
	}
	monthly := reconciled[1]
	if monthly.ResetDuration != "1M" || monthly.CurrentUsage != 100 {
		t.Fatalf("expected monthly budget to inherit weekly usage, got %#v", monthly)
	}
}

func TestNewProviderBudgetInheritsClosestShorterUsage(t *testing.T) {
	reconciled, err := reconcileBudgetRequestsForTest(
		[]configstoreTables.TableBudget{
			{
				ID:            "weekly-budget",
				MaxLimit:      100,
				ResetDuration: "1w",
				CurrentUsage:  100,
			},
		},
		[]CreateBudgetRequest{
			{
				ID:            "weekly-budget",
				MaxLimit:      100,
				ResetDuration: "1w",
			},
			{
				MaxLimit:      300,
				ResetDuration: "1M",
			},
		},
		false,
	)
	if err != nil {
		t.Fatalf("expected reconcile to succeed: %v", err)
	}
	monthly := reconciled[1]
	if monthly.ResetDuration != "1M" || monthly.CurrentUsage != 100 {
		t.Fatalf("expected provider monthly budget to inherit weekly usage, got %#v", monthly)
	}
}

func TestNewVirtualKeyBudgetInheritanceAboveLimitIsAllowed(t *testing.T) {
	reconciled, err := reconcileBudgetRequestsForTest(
		[]configstoreTables.TableBudget{
			{
				ID:            "weekly-budget",
				MaxLimit:      300,
				ResetDuration: "1w",
				CurrentUsage:  100,
			},
		},
		[]CreateBudgetRequest{
			{
				ID:            "weekly-budget",
				MaxLimit:      300,
				ResetDuration: "1w",
			},
			{
				MaxLimit:      50,
				ResetDuration: "1M",
			},
		},
		false,
	)
	if err != nil {
		t.Fatalf("expected inherited usage above new budget limit to be allowed: %v", err)
	}
	monthly := reconciled[1]
	if monthly.CurrentUsage != 100 || monthly.MaxLimit != 50 {
		t.Fatalf("expected inherited usage above new budget limit, got %#v", monthly)
	}
}

func TestNewProviderBudgetInheritanceAtLimitIsAllowed(t *testing.T) {
	reconciled, err := reconcileBudgetRequestsForTest(
		[]configstoreTables.TableBudget{
			{
				ID:            "weekly-budget",
				MaxLimit:      300,
				ResetDuration: "1w",
				CurrentUsage:  100,
			},
		},
		[]CreateBudgetRequest{
			{
				ID:            "weekly-budget",
				MaxLimit:      300,
				ResetDuration: "1w",
			},
			{
				MaxLimit:      100,
				ResetDuration: "1M",
			},
		},
		false,
	)
	if err != nil {
		t.Fatalf("expected inherited provider usage equal to new budget limit to be allowed: %v", err)
	}
	monthly := reconciled[1]
	if monthly.CurrentUsage != 100 || monthly.MaxLimit != 100 {
		t.Fatalf("expected inherited provider usage at new budget limit, got %#v", monthly)
	}
}

func TestNewShorterBudgetDoesNotInheritFromLongerUsage(t *testing.T) {
	reconciled, err := reconcileBudgetRequestsForTest(
		[]configstoreTables.TableBudget{
			{
				ID:            "monthly-budget",
				MaxLimit:      300,
				ResetDuration: "1M",
				CurrentUsage:  100,
			},
		},
		[]CreateBudgetRequest{
			{
				MaxLimit:      100,
				ResetDuration: "1w",
			},
			{
				ID:            "monthly-budget",
				MaxLimit:      300,
				ResetDuration: "1M",
			},
		},
		false,
	)
	if err != nil {
		t.Fatalf("expected reconcile to succeed: %v", err)
	}
	if len(reconciled) != 2 {
		t.Fatalf("expected two budgets, got %d", len(reconciled))
	}
	weekly := reconciled[0]
	if weekly.ResetDuration != "1w" || weekly.CurrentUsage != 0 {
		t.Fatalf("expected weekly budget to start at zero, got %#v", weekly)
	}
}

func TestNewBudgetInheritsClosestShorterUsage(t *testing.T) {
	reconciled, err := reconcileBudgetRequestsForTest(
		[]configstoreTables.TableBudget{
			{
				ID:            "minute-budget",
				MaxLimit:      10,
				ResetDuration: "1m",
				CurrentUsage:  5,
			},
			{
				ID:            "weekly-budget",
				MaxLimit:      100,
				ResetDuration: "1w",
				CurrentUsage:  75,
			},
		},
		[]CreateBudgetRequest{
			{
				ID:            "minute-budget",
				MaxLimit:      10,
				ResetDuration: "1m",
			},
			{
				ID:            "weekly-budget",
				MaxLimit:      100,
				ResetDuration: "1w",
			},
			{
				MaxLimit:      300,
				ResetDuration: "1M",
			},
		},
		false,
	)
	if err != nil {
		t.Fatalf("expected reconcile to succeed: %v", err)
	}
	monthly := reconciled[2]
	if monthly.ResetDuration != "1M" || monthly.CurrentUsage != 75 {
		t.Fatalf("expected monthly budget to inherit closest shorter weekly usage, got %#v", monthly)
	}
}

func TestNewLongerBudgetDoesNotInheritUsageWhenResetRequested(t *testing.T) {
	reconciled, err := reconcileBudgetRequestsForTest(
		[]configstoreTables.TableBudget{
			{
				ID:            "weekly-budget",
				MaxLimit:      100,
				ResetDuration: "1w",
				CurrentUsage:  100,
			},
		},
		[]CreateBudgetRequest{
			{
				ID:            "weekly-budget",
				MaxLimit:      100,
				ResetDuration: "1w",
			},
			{
				MaxLimit:      300,
				ResetDuration: "1M",
			},
		},
		true,
	)
	if err != nil {
		t.Fatalf("expected reconcile to succeed: %v", err)
	}
	monthly := reconciled[1]
	if monthly.ResetDuration != "1M" || monthly.CurrentUsage != 0 {
		t.Fatalf("expected monthly budget to start at zero when reset is requested, got %#v", monthly)
	}
}

func TestRotateVirtualKey_OnlyChangesValueAndReloads(t *testing.T) {
	SetLogger(&mockLogger{})

	active := true
	teamID := "team-1"
	rateLimitID := "rate-limit-1"
	store := &mockRotateConfigStore{
		virtualKeys: map[string]*configstoreTables.TableVirtualKey{
			"vk-1": {
				ID:          "vk-1",
				Name:        "Production",
				Value:       *schemas.NewSecretVar("sk-bf-old"),
				Description: "existing description",
				TeamID:      &teamID,
				RateLimitID: &rateLimitID,
				IsActive:    &active,
				Budgets: []configstoreTables.TableBudget{
					{ID: "budget-1", MaxLimit: 100, CurrentUsage: 42, ResetDuration: "1d"},
				},
				ProviderConfigs: []configstoreTables.TableVirtualKeyProviderConfig{
					{ID: 7, VirtualKeyID: "vk-1", Provider: "openai"},
				},
				MCPConfigs: []configstoreTables.TableVirtualKeyMCPConfig{
					{ID: 9, VirtualKeyID: "vk-1", MCPClientID: 3},
				},
			},
		},
	}
	manager := &mockRotateGovernanceManager{store: store}
	h := &GovernanceHandler{configStore: store, governanceManager: manager}

	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("vk_id", "vk-1")

	h.rotateVirtualKey(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected status 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if store.updates != 1 {
		t.Fatalf("expected one update, got %d", store.updates)
	}
	if len(manager.reloadIDs) != 1 || manager.reloadIDs[0] != "vk-1" {
		t.Fatalf("expected reload for vk-1, got %#v", manager.reloadIDs)
	}

	updated := store.virtualKeys["vk-1"]
	if updated.Value.GetValue() == "sk-bf-old" {
		t.Fatal("expected virtual key value to rotate")
	}
	if !strings.HasPrefix(updated.Value.GetValue(), governance.VirtualKeyPrefix) {
		t.Fatalf("expected rotated value to use %q prefix, got %q", governance.VirtualKeyPrefix, updated.Value.GetValue())
	}
	if updated.ID != "vk-1" || updated.Name != "Production" || updated.Description != "existing description" {
		t.Fatalf("rotation changed non-value fields: %#v", updated)
	}
	if updated.TeamID == nil || *updated.TeamID != teamID || updated.RateLimitID == nil || *updated.RateLimitID != rateLimitID || updated.IsActive == nil || !*updated.IsActive {
		t.Fatalf("rotation changed relationship/status fields: %#v", updated)
	}
	if len(updated.Budgets) != 1 || updated.Budgets[0].CurrentUsage != 42 {
		t.Fatalf("rotation changed budgets: %#v", updated.Budgets)
	}
	if len(updated.ProviderConfigs) != 1 || updated.ProviderConfigs[0].ID != 7 {
		t.Fatalf("rotation changed provider configs: %#v", updated.ProviderConfigs)
	}
	if len(updated.MCPConfigs) != 1 || updated.MCPConfigs[0].ID != 9 {
		t.Fatalf("rotation changed MCP configs: %#v", updated.MCPConfigs)
	}

	var resp struct {
		Message    string                            `json:"message"`
		VirtualKey configstoreTables.TableVirtualKey `json:"virtual_key"`
	}
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.VirtualKey.Value.GetValue() != updated.Value.GetValue() {
		t.Fatalf("response value = %q, want %q", resp.VirtualKey.Value.GetValue(), updated.Value.GetValue())
	}
}

func TestRotateVirtualKey_NotFound(t *testing.T) {
	SetLogger(&mockLogger{})

	store := &mockRotateConfigStore{virtualKeys: map[string]*configstoreTables.TableVirtualKey{}}
	manager := &mockRotateGovernanceManager{store: store}
	h := &GovernanceHandler{configStore: store, governanceManager: manager}

	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("vk_id", "missing")

	h.rotateVirtualKey(ctx)

	if ctx.Response.StatusCode() != 404 {
		t.Fatalf("expected status 404, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if store.updates != 0 {
		t.Fatalf("expected no updates, got %d", store.updates)
	}
	if len(manager.reloadIDs) != 0 {
		t.Fatalf("expected no reloads, got %#v", manager.reloadIDs)
	}
}

func TestRotateVirtualKey_UpdateFailureDoesNotReload(t *testing.T) {
	SetLogger(&mockLogger{})

	store := &mockRotateConfigStore{
		virtualKeys: map[string]*configstoreTables.TableVirtualKey{
			"vk-1": {ID: "vk-1", Name: "One", Value: *schemas.NewSecretVar("sk-bf-old")},
		},
		updateErr: errors.New("database unavailable"),
	}
	manager := &mockRotateGovernanceManager{store: store}
	h := &GovernanceHandler{configStore: store, governanceManager: manager}

	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("vk_id", "vk-1")

	h.rotateVirtualKey(ctx)

	if ctx.Response.StatusCode() != 500 {
		t.Fatalf("expected status 500, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if store.virtualKeys["vk-1"].Value.GetValue() != "sk-bf-old" {
		t.Fatalf("expected value to remain unchanged, got %q", store.virtualKeys["vk-1"].Value.GetValue())
	}
	if len(manager.reloadIDs) != 0 {
		t.Fatalf("expected no reloads, got %#v", manager.reloadIDs)
	}
}

func TestRotateVirtualKey_ReloadFailureReturnsErrorAfterUpdate(t *testing.T) {
	SetLogger(&mockLogger{})

	store := &mockRotateConfigStore{
		virtualKeys: map[string]*configstoreTables.TableVirtualKey{
			"vk-1": {ID: "vk-1", Name: "One", Value: *schemas.NewSecretVar("sk-bf-old")},
		},
	}
	manager := &mockRotateGovernanceManager{store: store, reloadErr: errors.New("reload failed")}
	h := &GovernanceHandler{configStore: store, governanceManager: manager}

	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("vk_id", "vk-1")

	h.rotateVirtualKey(ctx)

	if ctx.Response.StatusCode() != 500 {
		t.Fatalf("expected status 500, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if store.updates != 1 {
		t.Fatalf("expected one update, got %d", store.updates)
	}
	if store.virtualKeys["vk-1"].Value.GetValue() == "sk-bf-old" {
		t.Fatal("expected value to rotate before reload failure")
	}
	if len(manager.reloadIDs) != 1 || manager.reloadIDs[0] != "vk-1" {
		t.Fatalf("expected reload for vk-1, got %#v", manager.reloadIDs)
	}
	if !strings.Contains(string(ctx.Response.Body()), "failed to reload in-memory state") {
		t.Fatalf("expected reload failure in response, got %s", string(ctx.Response.Body()))
	}
}

func TestRotateVirtualKeys_PartialSuccess(t *testing.T) {
	SetLogger(&mockLogger{})

	store := &mockRotateConfigStore{
		virtualKeys: map[string]*configstoreTables.TableVirtualKey{
			"vk-1": {ID: "vk-1", Name: "One", Value: *schemas.NewSecretVar("sk-bf-old-1")},
			"vk-2": {ID: "vk-2", Name: "Two", Value: *schemas.NewSecretVar("sk-bf-old-2")},
		},
	}
	manager := &mockRotateGovernanceManager{store: store}
	h := &GovernanceHandler{configStore: store, governanceManager: manager}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBodyString(`{"ids":["vk-1","missing","vk-2","vk-1"]}`)

	h.rotateVirtualKeys(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected status 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if store.updates != 2 {
		t.Fatalf("expected two updates, got %d", store.updates)
	}
	if len(manager.reloadIDs) != 2 || manager.reloadIDs[0] != "vk-1" || manager.reloadIDs[1] != "vk-2" {
		t.Fatalf("expected reloads for vk-1 and vk-2, got %#v", manager.reloadIDs)
	}
	if store.virtualKeys["vk-1"].Value.GetValue() == "sk-bf-old-1" || store.virtualKeys["vk-2"].Value.GetValue() == "sk-bf-old-2" {
		t.Fatalf("expected successful IDs to rotate: %#v", store.virtualKeys)
	}

	var resp struct {
		VirtualKeys []configstoreTables.TableVirtualKey `json:"virtual_keys"`
		Errors      map[string]string                   `json:"errors"`
	}
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if len(resp.VirtualKeys) != 2 {
		t.Fatalf("expected two rotated keys in response, got %d", len(resp.VirtualKeys))
	}
	if resp.Errors["missing"] != "virtual key not found" {
		t.Fatalf("expected missing error, got %#v", resp.Errors)
	}
}

func TestRotateVirtualKeys_RejectsInvalidRequests(t *testing.T) {
	SetLogger(&mockLogger{})

	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "invalid JSON", body: `{`, want: "Invalid JSON"},
		{name: "empty IDs", body: `{"ids":[]}`, want: "At least one virtual key ID is required"},
		{name: "blank ID", body: `{"ids":["vk-1"," "]}`, want: "Virtual key ID cannot be empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockRotateConfigStore{
				virtualKeys: map[string]*configstoreTables.TableVirtualKey{
					"vk-1": {ID: "vk-1", Name: "One", Value: *schemas.NewSecretVar("sk-bf-old-1")},
				},
			}
			manager := &mockRotateGovernanceManager{store: store}
			h := &GovernanceHandler{configStore: store, governanceManager: manager}

			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetBodyString(tt.body)

			h.rotateVirtualKeys(ctx)

			if ctx.Response.StatusCode() != 400 {
				t.Fatalf("expected status 400, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
			}
			if store.updates != 0 {
				t.Fatalf("expected no updates, got %d", store.updates)
			}
			if len(manager.reloadIDs) != 0 {
				t.Fatalf("expected no reloads, got %#v", manager.reloadIDs)
			}
			if !strings.Contains(string(ctx.Response.Body()), tt.want) {
				t.Fatalf("expected response to contain %q, got %s", tt.want, string(ctx.Response.Body()))
			}
		})
	}
}

func TestRotateVirtualKeys_TrimsAndDeduplicatesIDs(t *testing.T) {
	SetLogger(&mockLogger{})

	store := &mockRotateConfigStore{
		virtualKeys: map[string]*configstoreTables.TableVirtualKey{
			"vk-1": {ID: "vk-1", Name: "One", Value: *schemas.NewSecretVar("sk-bf-old-1")},
			"vk-2": {ID: "vk-2", Name: "Two", Value: *schemas.NewSecretVar("sk-bf-old-2")},
		},
	}
	manager := &mockRotateGovernanceManager{store: store}
	h := &GovernanceHandler{configStore: store, governanceManager: manager}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBodyString(`{"ids":[" vk-1 ","vk-1","vk-2"]}`)

	h.rotateVirtualKeys(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected status 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if store.updates != 2 {
		t.Fatalf("expected two updates, got %d", store.updates)
	}
	if len(manager.reloadIDs) != 2 || manager.reloadIDs[0] != "vk-1" || manager.reloadIDs[1] != "vk-2" {
		t.Fatalf("expected reloads for vk-1 and vk-2, got %#v", manager.reloadIDs)
	}
}

func TestRotateVirtualKeys_AllFailuresReturnsServerError(t *testing.T) {
	SetLogger(&mockLogger{})

	store := &mockRotateConfigStore{virtualKeys: map[string]*configstoreTables.TableVirtualKey{}}
	manager := &mockRotateGovernanceManager{store: store}
	h := &GovernanceHandler{configStore: store, governanceManager: manager}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBodyString(`{"ids":["missing-1","missing-2"]}`)

	h.rotateVirtualKeys(ctx)

	if ctx.Response.StatusCode() != 500 {
		t.Fatalf("expected status 500, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if store.updates != 0 {
		t.Fatalf("expected no updates, got %d", store.updates)
	}

	var resp struct {
		Message     string                              `json:"message"`
		VirtualKeys []configstoreTables.TableVirtualKey `json:"virtual_keys"`
		Errors      map[string]string                   `json:"errors"`
	}
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Message != "Failed to rotate virtual keys" {
		t.Fatalf("expected failure message, got %q", resp.Message)
	}
	if len(resp.VirtualKeys) != 0 {
		t.Fatalf("expected no rotated keys, got %#v", resp.VirtualKeys)
	}
	if resp.Errors["missing-1"] != "virtual key not found" || resp.Errors["missing-2"] != "virtual key not found" {
		t.Fatalf("expected not found errors, got %#v", resp.Errors)
	}
}

func TestDeleteVirtualKeys_PartialSuccess(t *testing.T) {
	SetLogger(&mockLogger{})

	store := &mockRotateConfigStore{
		virtualKeys: map[string]*configstoreTables.TableVirtualKey{
			"vk-1": {ID: "vk-1", Name: "One", Value: *schemas.NewSecretVar("sk-bf-1")},
			"vk-2": {ID: "vk-2", Name: "Two", Value: *schemas.NewSecretVar("sk-bf-2")},
		},
	}
	manager := &mockRotateGovernanceManager{store: store}
	h := &GovernanceHandler{configStore: store, governanceManager: manager}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBodyString(`{"ids":["vk-1","missing","vk-2"]}`)

	h.deleteVirtualKeys(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected status 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if store.deletes != 2 {
		t.Fatalf("expected two database deletes, got %d", store.deletes)
	}
	if len(manager.removeIDs) != 2 || manager.removeIDs[0] != "vk-1" || manager.removeIDs[1] != "vk-2" {
		t.Fatalf("expected remove from memory for vk-1 and vk-2, got %#v", manager.removeIDs)
	}

	var resp struct {
		Message string            `json:"message"`
		Deleted int               `json:"deleted"`
		Errors  map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Message != "Virtual keys deleted successfully" {
		t.Fatalf("expected success message, got %q", resp.Message)
	}
	if resp.Deleted != 2 {
		t.Fatalf("expected deleted count to be 2, got %d", resp.Deleted)
	}
	if resp.Errors["missing"] != "virtual key not found" {
		t.Fatalf("expected missing error, got %#v", resp.Errors)
	}
}

func TestDeleteVirtualKeys_RemoveFailureAbortsDeletion(t *testing.T) {
	SetLogger(&mockLogger{})

	store := &mockRotateConfigStore{
		virtualKeys: map[string]*configstoreTables.TableVirtualKey{
			"vk-1": {ID: "vk-1", Name: "One", Value: *schemas.NewSecretVar("sk-bf-1")},
		},
	}
	manager := &mockRotateGovernanceManager{
		store:     store,
		removeErr: fmt.Errorf("in-memory remove failed"),
	}
	h := &GovernanceHandler{configStore: store, governanceManager: manager}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBodyString(`{"ids":["vk-1"]}`)

	h.deleteVirtualKeys(ctx)

	if ctx.Response.StatusCode() != 500 {
		t.Fatalf("expected status 500, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if store.deletes != 0 {
		t.Fatalf("expected no database delete when memory removal fails, got %d", store.deletes)
	}
	if len(manager.removeIDs) != 1 || manager.removeIDs[0] != "vk-1" {
		t.Fatalf("expected RemoveVirtualKey call for vk-1, got %#v", manager.removeIDs)
	}
	if len(manager.reloadIDs) != 0 {
		t.Fatalf("expected no ReloadVirtualKey call when memory removal fails, got %#v", manager.reloadIDs)
	}

	vk, err := store.GetVirtualKey(ctx, "vk-1")
	if err != nil || vk == nil {
		t.Fatalf("expected vk-1 to still exist in database after memory removal failure")
	}

	var resp struct {
		Message string            `json:"message"`
		Deleted int               `json:"deleted"`
		Errors  map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Message != "Failed to delete virtual keys" {
		t.Fatalf("expected failure message, got %q", resp.Message)
	}
	if resp.Deleted != 0 {
		t.Fatalf("expected deleted count to be 0, got %d", resp.Deleted)
	}
	if !strings.Contains(resp.Errors["vk-1"], "failed to remove from memory: in-memory remove failed") {
		t.Fatalf("expected fail message in errors, got %#v", resp.Errors)
	}
}

func TestDeleteVirtualKeys_DBDeleteFailureTriggersReload(t *testing.T) {
	SetLogger(&mockLogger{})

	store := &mockRotateConfigStore{
		virtualKeys: map[string]*configstoreTables.TableVirtualKey{
			"vk-1": {ID: "vk-1", Name: "One", Value: *schemas.NewSecretVar("sk-bf-1")},
		},
		deleteErr: fmt.Errorf("database delete failed"),
	}
	manager := &mockRotateGovernanceManager{store: store}
	h := &GovernanceHandler{configStore: store, governanceManager: manager}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBodyString(`{"ids":["vk-1"]}`)

	h.deleteVirtualKeys(ctx)

	if ctx.Response.StatusCode() != 500 {
		t.Fatalf("expected status 500, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if len(manager.removeIDs) != 1 || manager.removeIDs[0] != "vk-1" {
		t.Fatalf("expected RemoveVirtualKey call for vk-1, got %#v", manager.removeIDs)
	}
	if store.deletes != 0 {
		t.Fatalf("expected no database delete when delete fails, got %d", store.deletes)
	}
	if len(manager.reloadIDs) != 1 || manager.reloadIDs[0] != "vk-1" {
		t.Fatalf("expected ReloadVirtualKey compensation call for vk-1, got %#v", manager.reloadIDs)
	}

	vk, err := store.GetVirtualKey(ctx, "vk-1")
	if err != nil || vk == nil {
		t.Fatalf("expected vk-1 to still exist in database after failed deletion")
	}

	var resp struct {
		Message string            `json:"message"`
		Deleted int               `json:"deleted"`
		Errors  map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Message != "Failed to delete virtual keys" {
		t.Fatalf("expected failure message, got %q", resp.Message)
	}
	if resp.Deleted != 0 {
		t.Fatalf("expected deleted count to be 0, got %d", resp.Deleted)
	}
	if !strings.Contains(resp.Errors["vk-1"], "removed from memory but failed to delete from database: database delete failed") {
		t.Fatalf("expected appropriate error message, got %#v", resp.Errors)
	}
}

// mockQuotaConfigStore backs the self-service quota endpoint. It returns a VK from
// GetVirtualKeyQuotaByValue (whose direct Budgets/RateLimit are empty post-PR-#3939)
// and serves the VK-scoped model configs that own the governance via the bulk query the
// quota path uses — wildcard ("*") configs are reverse-mapped onto the VK/provider
// configs, and specific-model configs surface as the per-model usage breakdown.
type mockQuotaConfigStore struct {
	configstore.ConfigStore
	vk              *configstoreTables.TableVirtualKey
	vkErr           error
	modelConfigs    []configstoreTables.TableModelConfig
	modelConfigsErr error
	quotaCalls      int
}

func (m *mockQuotaConfigStore) GetVirtualKeyQuotaByValue(_ context.Context, _ string) (*configstoreTables.TableVirtualKey, error) {
	m.quotaCalls++
	if m.vkErr != nil {
		return nil, m.vkErr
	}
	return cloneTestVirtualKey(m.vk), nil
}

func (m *mockQuotaConfigStore) GetModelConfigsByScopeAndScopeIDs(_ context.Context, scope string, scopeIDs []string) ([]configstoreTables.TableModelConfig, error) {
	if m.modelConfigsErr != nil {
		return nil, m.modelConfigsErr
	}
	want := make(map[string]bool, len(scopeIDs))
	for _, id := range scopeIDs {
		want[id] = true
	}
	var out []configstoreTables.TableModelConfig
	for _, mc := range m.modelConfigs {
		if mc.Scope == scope && mc.ScopeID != nil && want[*mc.ScopeID] {
			out = append(out, mc)
		}
	}
	return out, nil
}

// mockQuotaLogManager backs the quota endpoint's actual per-model usage breakdown. It
// embeds the LogManager interface (so the dozens of unused methods are satisfied) and
// overrides only GetModelRankings, recording the filters it was called with so tests can
// assert the per-budget cycle window.
type mockQuotaLogManager struct {
	logging.LogManager
	rankings *logstore.ModelRankingResult
	rankErr  error
	calls    []logstore.SearchFilters
}

func (m *mockQuotaLogManager) GetModelRankings(_ context.Context, filters *logstore.SearchFilters) (*logstore.ModelRankingResult, error) {
	if filters != nil {
		m.calls = append(m.calls, *filters)
	}
	if m.rankErr != nil {
		return nil, m.rankErr
	}
	return m.rankings, nil
}

type quotaResponse struct {
	VirtualKeyName  string                                            `json:"virtual_key_name"`
	IsActive        bool                                              `json:"is_active"`
	Budgets         []quotaBudget                                     `json:"budgets"`
	RateLimit       *configstoreTables.TableRateLimit                 `json:"rate_limit"`
	ProviderConfigs []configstoreTables.TableVirtualKeyProviderConfig `json:"provider_configs"`
	Models          []quotaModelUsage                                 `json:"model_configs"`
}

// TestGetVirtualKeyQuota_HydratesBudgetsFromModelConfigs is the regression test for
// the 1.4.7 bug: after governance moved into VK-scoped model configs, the quota
// endpoint kept reading the now-empty direct VK/provider-config relationships and
// reported no budgets. The handler must hydrate from model configs.
func TestGetVirtualKeyQuota_HydratesBudgetsFromModelConfigs(t *testing.T) {
	SetLogger(&mockLogger{})

	active := true
	tokenMax := int64(1000)
	rlID := "rl-vk"
	modelTokenMax := int64(500)
	modelRLID := "rl-gpt4o"
	// Deterministic cycle start so the per-model usage query window is asserted exactly.
	cycleStart := time.Date(2026, time.January, 2, 15, 4, 5, 0, time.UTC)
	store := &mockQuotaConfigStore{
		vk: &configstoreTables.TableVirtualKey{
			ID:       "vk-1",
			Name:     "Production",
			IsActive: &active,
			// Direct relationships are empty post-migration — governance lives in model configs.
			ProviderConfigs: []configstoreTables.TableVirtualKeyProviderConfig{
				{ID: 7, VirtualKeyID: "vk-1", Provider: "openai"},
			},
		},
		modelConfigs: []configstoreTables.TableModelConfig{
			// VK top-level governance (wildcard, provider == nil).
			{
				ID:        "mc-vk",
				Scope:     configstoreTables.ModelConfigScopeVirtualKey,
				ScopeID:   schemas.Ptr("vk-1"),
				ModelName: configstoreTables.ModelConfigAllModels,
				Budgets: []configstoreTables.TableBudget{
					{ID: "b-vk", MaxLimit: 100, CurrentUsage: 30, ResetDuration: "1d", LastReset: cycleStart},
				},
				RateLimitID: &rlID,
				RateLimit:   &configstoreTables.TableRateLimit{ID: rlID, TokenMaxLimit: &tokenMax, TokenCurrentUsage: 250},
			},
			// Per-provider governance (wildcard, provider == "openai").
			{
				ID:        "mc-openai",
				Scope:     configstoreTables.ModelConfigScopeVirtualKey,
				ScopeID:   schemas.Ptr("vk-1"),
				ModelName: configstoreTables.ModelConfigAllModels,
				Provider:  schemas.Ptr("openai"),
				Budgets: []configstoreTables.TableBudget{
					{ID: "b-openai", MaxLimit: 50, CurrentUsage: 10, ResetDuration: "1d"},
				},
			},
			// Per-model governance (specific model) — surfaces as the per-model usage breakdown.
			{
				ID:        "mc-gpt4o",
				Scope:     configstoreTables.ModelConfigScopeVirtualKey,
				ScopeID:   schemas.Ptr("vk-1"),
				ModelName: "gpt-4o",
				Provider:  schemas.Ptr("openai"),
				Budgets: []configstoreTables.TableBudget{
					{ID: "b-gpt4o", MaxLimit: 25, CurrentUsage: 7, ResetDuration: "1d"},
				},
				RateLimitID: &modelRLID,
				RateLimit:   &configstoreTables.TableRateLimit{ID: modelRLID, TokenMaxLimit: &modelTokenMax, TokenCurrentUsage: 120},
			},
		},
	}
	logMgr := &mockQuotaLogManager{
		rankings: &logstore.ModelRankingResult{
			Rankings: []logstore.ModelRankingWithTrend{
				{ModelRankingEntry: logstore.ModelRankingEntry{Model: "gpt-4o", Provider: "openai", TotalRequests: 12, TotalTokens: 3400, TotalCost: 1.25}},
			},
		},
	}
	h := &GovernanceHandler{configStore: store, logManager: logMgr}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-vk", "sk-bf-secret")

	h.getVirtualKeyQuota(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected status 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if store.quotaCalls != 1 {
		t.Fatalf("expected GetVirtualKeyQuotaByValue called once, got %d", store.quotaCalls)
	}

	var resp quotaResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.VirtualKeyName != "Production" || !resp.IsActive {
		t.Fatalf("unexpected identity fields: name=%q active=%v", resp.VirtualKeyName, resp.IsActive)
	}
	if len(resp.Budgets) != 1 || resp.Budgets[0].ID != "b-vk" || resp.Budgets[0].CurrentUsage != 30 {
		t.Fatalf("expected hydrated VK budget b-vk (usage 30), got %#v", resp.Budgets)
	}
	if resp.RateLimit == nil || resp.RateLimit.ID != rlID || resp.RateLimit.TokenCurrentUsage != 250 {
		t.Fatalf("expected hydrated VK rate limit %q, got %#v", rlID, resp.RateLimit)
	}
	if len(resp.ProviderConfigs) != 1 {
		t.Fatalf("expected one provider config, got %#v", resp.ProviderConfigs)
	}
	pcBudgets := resp.ProviderConfigs[0].Budgets
	if len(pcBudgets) != 1 || pcBudgets[0].ID != "b-openai" || pcBudgets[0].CurrentUsage != 10 {
		t.Fatalf("expected hydrated provider budget b-openai (usage 10), got %#v", pcBudgets)
	}
	// Per-model usage: only the specific-model config (gpt-4o) — wildcard configs feed the
	// VK/provider governance above and must not leak into the per-model list.
	if len(resp.Models) != 1 {
		t.Fatalf("expected one per-model usage entry, got %#v", resp.Models)
	}
	m := resp.Models[0]
	if m.ModelName != "gpt-4o" || m.Provider == nil || *m.Provider != "openai" {
		t.Fatalf("unexpected per-model identity: name=%q provider=%v", m.ModelName, m.Provider)
	}
	if len(m.Budgets) != 1 || m.Budgets[0].ID != "b-gpt4o" || m.Budgets[0].CurrentUsage != 7 {
		t.Fatalf("expected per-model budget b-gpt4o (usage 7), got %#v", m.Budgets)
	}
	if m.RateLimit == nil || m.RateLimit.ID != "rl-gpt4o" || m.RateLimit.TokenCurrentUsage != 120 {
		t.Fatalf("expected per-model rate limit rl-gpt4o (usage 120), got %#v", m.RateLimit)
	}

	// Actual per-model spend from logs is now embedded in each budget. The VK has a single
	// budget (b-vk), whose models list breaks down spend over its current cycle.
	bu := resp.Budgets[0]
	if bu.ID != "b-vk" || bu.ResetDuration != "1d" || bu.CurrentUsage != 30 {
		t.Fatalf("unexpected budget envelope: %#v", bu)
	}
	if len(bu.Models) != 1 {
		t.Fatalf("expected one model spend entry, got %#v", bu.Models)
	}
	spend := bu.Models[0]
	if spend.Model != "gpt-4o" || spend.Provider != "openai" || spend.TotalRequests != 12 || spend.TotalTokens != 3400 || spend.TotalCost != 1.25 {
		t.Fatalf("unexpected model spend: %#v", spend)
	}
	// The usage query must be scoped to this VK and windowed to the budget's current cycle.
	if len(logMgr.calls) != 1 {
		t.Fatalf("expected GetModelRankings called once, got %d", len(logMgr.calls))
	}
	call := logMgr.calls[0]
	if len(call.VirtualKeyIDs) != 1 || call.VirtualKeyIDs[0] != "vk-1" {
		t.Fatalf("expected usage query scoped to vk-1, got %#v", call.VirtualKeyIDs)
	}
	if call.StartTime == nil || call.EndTime == nil {
		t.Fatalf("expected usage query to carry a cycle window, got start=%v end=%v", call.StartTime, call.EndTime)
	}
	// The window must start exactly at the budget's last reset and end at/after it.
	if !call.StartTime.Equal(cycleStart) {
		t.Fatalf("expected StartTime=%v (budget last reset), got %v", cycleStart, *call.StartTime)
	}
	if call.EndTime.Before(cycleStart) {
		t.Fatalf("expected EndTime >= StartTime, got start=%v end=%v", *call.StartTime, *call.EndTime)
	}
}

// TestGetVirtualKeyQuota_ExternalResolverReplacesWithAccessProfileBudgets verifies the
// AP-managed-VK path: when a registered ExternalQuotaBudgetResolver returns budgets
// (enterprise access-profile budgets, which carry the real usage), they REPLACE the VK's
// own budget rows in the quota response. Those rows are reset to current_usage=0 at
// adoption and never charged again, so reporting them would be a misleading $0 row.
func TestGetVirtualKeyQuota_ExternalResolverReplacesWithAccessProfileBudgets(t *testing.T) {
	SetLogger(&mockLogger{})

	active := true
	cycleStart := time.Date(2026, time.January, 2, 15, 4, 5, 0, time.UTC)
	store := &mockQuotaConfigStore{
		vk: &configstoreTables.TableVirtualKey{
			ID:       "vk-1",
			Name:     "AP Key",
			IsActive: &active,
		},
		modelConfigs: []configstoreTables.TableModelConfig{
			{
				ID:        "mc-vk",
				Scope:     configstoreTables.ModelConfigScopeVirtualKey,
				ScopeID:   schemas.Ptr("vk-1"),
				ModelName: configstoreTables.ModelConfigAllModels,
				// VK mirror row: zero usage, reset at adoption — must NOT appear in the response.
				Budgets: []configstoreTables.TableBudget{
					{ID: "b-vk", MaxLimit: 100, CurrentUsage: 0, ResetDuration: "1d", LastReset: cycleStart},
				},
			},
		},
	}
	// The AP user's inference is logged under user-1 (virtual_key_id is empty on SSO/AP
	// log rows), so the per-model usage query must be scoped to the user, not the VK.
	logMgr := &mockQuotaLogManager{
		rankings: &logstore.ModelRankingResult{
			Rankings: []logstore.ModelRankingWithTrend{
				{ModelRankingEntry: logstore.ModelRankingEntry{Model: "claude-opus-4-7", Provider: "anthropic", TotalRequests: 2, TotalTokens: 900, TotalCost: 42}},
			},
		},
	}
	h := &GovernanceHandler{
		configStore: store,
		logManager:  logMgr,
		externalQuotaBudgetResolver: func(_ context.Context, vk *configstoreTables.TableVirtualKey) (*ExternalQuotaBudgetResult, error) {
			if vk.ID != "vk-1" {
				return nil, nil
			}
			return &ExternalQuotaBudgetResult{
				// The access-profile budget that holds the real ongoing usage.
				Budgets: []configstoreTables.TableBudget{
					{ID: "b-ap", MaxLimit: 500, CurrentUsage: 42, ResetDuration: "1d", LastReset: cycleStart},
				},
				UsageUserID: "user-1",
			}, nil
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-vk", "sk-bf-secret")

	h.getVirtualKeyQuota(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected status 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	var resp quotaResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	// Only the access-profile budget — the VK's own b-vk row is replaced, not appended.
	if len(resp.Budgets) != 1 || resp.Budgets[0].ID != "b-ap" || resp.Budgets[0].CurrentUsage != 42 {
		t.Fatalf("expected only the access-profile budget b-ap (usage 42), got %#v", resp.Budgets)
	}
	// Per-model spend on that budget comes from the user-scoped log query and reconciles
	// with current_usage (both 42).
	if len(resp.Budgets[0].Models) != 1 || resp.Budgets[0].Models[0].Model != "claude-opus-4-7" || resp.Budgets[0].Models[0].TotalCost != 42 {
		t.Fatalf("expected per-model spend from user-scoped logs, got %#v", resp.Budgets[0].Models)
	}
	// The usage query must be scoped to the AP user, NOT the VK (whose logs are empty).
	if len(logMgr.calls) != 1 {
		t.Fatalf("expected GetModelRankings called once, got %d", len(logMgr.calls))
	}
	call := logMgr.calls[0]
	if len(call.UserIDs) != 1 || call.UserIDs[0] != "user-1" {
		t.Fatalf("expected usage query scoped to user-1, got UserIDs=%#v", call.UserIDs)
	}
	if len(call.VirtualKeyIDs) != 0 {
		t.Fatalf("expected no VK scoping on the AP usage query, got %#v", call.VirtualKeyIDs)
	}
}

// TestGetVirtualKeyQuota_ExternalResolverErrorFailsClosed verifies the endpoint returns
// 500 (not a partial response) when the registered resolver errors — usage must not be
// silently under-reported.
func TestGetVirtualKeyQuota_ExternalResolverErrorFailsClosed(t *testing.T) {
	SetLogger(&mockLogger{})

	active := true
	store := &mockQuotaConfigStore{
		vk: &configstoreTables.TableVirtualKey{ID: "vk-1", Name: "AP Key", IsActive: &active},
	}
	h := &GovernanceHandler{
		configStore: store,
		externalQuotaBudgetResolver: func(_ context.Context, _ *configstoreTables.TableVirtualKey) (*ExternalQuotaBudgetResult, error) {
			return nil, errors.New("boom")
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-vk", "sk-bf-secret")

	h.getVirtualKeyQuota(ctx)

	if ctx.Response.StatusCode() != 500 {
		t.Fatalf("expected status 500 on resolver error, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
}

// TestGetVirtualKeyQuota_NoGovernanceReturnsEmpty verifies that a VK without any
// VK-scoped model configs reports empty governance (not a stale direct-relationship
// read) and still returns 200 with identity fields.
func TestGetVirtualKeyQuota_NoGovernanceReturnsEmpty(t *testing.T) {
	SetLogger(&mockLogger{})

	active := true
	store := &mockQuotaConfigStore{
		vk: &configstoreTables.TableVirtualKey{
			ID:       "vk-2",
			Name:     "NoGov",
			IsActive: &active,
			ProviderConfigs: []configstoreTables.TableVirtualKeyProviderConfig{
				{ID: 1, VirtualKeyID: "vk-2", Provider: "openai"},
			},
		},
		// No model configs → nothing to hydrate.
	}
	h := &GovernanceHandler{configStore: store}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-vk", "sk-bf-secret")

	h.getVirtualKeyQuota(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected status 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	var resp quotaResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.VirtualKeyName != "NoGov" || !resp.IsActive {
		t.Fatalf("unexpected identity fields: name=%q active=%v", resp.VirtualKeyName, resp.IsActive)
	}
	if len(resp.Budgets) != 0 {
		t.Fatalf("expected no budgets, got %#v", resp.Budgets)
	}
	if resp.RateLimit != nil {
		t.Fatalf("expected no rate limit, got %#v", resp.RateLimit)
	}
	if len(resp.ProviderConfigs) != 1 || len(resp.ProviderConfigs[0].Budgets) != 0 {
		t.Fatalf("expected provider config with no budgets, got %#v", resp.ProviderConfigs)
	}
}

func TestGetVirtualKeyQuota_MissingHeaderReturns401(t *testing.T) {
	SetLogger(&mockLogger{})

	store := &mockQuotaConfigStore{}
	h := &GovernanceHandler{configStore: store}

	ctx := &fasthttp.RequestCtx{}
	h.getVirtualKeyQuota(ctx)

	if ctx.Response.StatusCode() != 401 {
		t.Fatalf("expected status 401, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if !strings.Contains(string(ctx.Response.Body()), "api-key header") {
		t.Fatalf("expected missing VK message to include api-key header, got %s", string(ctx.Response.Body()))
	}
	if store.quotaCalls != 0 {
		t.Fatalf("expected store not queried without a VK, got %d calls", store.quotaCalls)
	}
}

func TestGetVirtualKeyQuota_NotFoundReturns401(t *testing.T) {
	SetLogger(&mockLogger{})

	store := &mockQuotaConfigStore{vkErr: configstore.ErrNotFound}
	h := &GovernanceHandler{configStore: store}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-vk", "sk-bf-unknown")

	h.getVirtualKeyQuota(ctx)

	if ctx.Response.StatusCode() != 401 {
		t.Fatalf("expected status 401, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if store.quotaCalls != 1 {
		t.Fatalf("expected one lookup attempt, got %d", store.quotaCalls)
	}
}

// TestGetVirtualKeyQuota_ModelConfigLoadErrorFailsClosed verifies the endpoint returns 500
// (not a 200 with silently-empty governance) when the model-config lookup fails. Failing
// open here would leave vk.Budgets un-hydrated and report "budgets": [], hiding configured
// limits from a client that reads len(budgets)==0 as "no limits".
func TestGetVirtualKeyQuota_ModelConfigLoadErrorFailsClosed(t *testing.T) {
	SetLogger(&mockLogger{})

	active := true
	store := &mockQuotaConfigStore{
		vk:              &configstoreTables.TableVirtualKey{ID: "vk-1", Name: "Prod", IsActive: &active},
		modelConfigsErr: errors.New("db down"),
	}
	h := &GovernanceHandler{configStore: store}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-vk", "sk-bf-secret")

	h.getVirtualKeyQuota(ctx)

	if ctx.Response.StatusCode() != 500 {
		t.Fatalf("expected status 500 on model-config load error, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
}

// TestGetVirtualKeyQuota_RankingsErrorFailsClosed verifies that a log-store failure fails
// closed (500) rather than returning per_model_usage: [], which is indistinguishable from a
// legitimately empty breakdown (logging disabled).
func TestGetVirtualKeyQuota_RankingsErrorFailsClosed(t *testing.T) {
	SetLogger(&mockLogger{})

	active := true
	store := &mockQuotaConfigStore{
		vk: &configstoreTables.TableVirtualKey{
			ID:       "vk-1",
			Name:     "Prod",
			IsActive: &active,
		},
		modelConfigs: []configstoreTables.TableModelConfig{
			{
				ID:        "mc-vk",
				Scope:     configstoreTables.ModelConfigScopeVirtualKey,
				ScopeID:   schemas.Ptr("vk-1"),
				ModelName: configstoreTables.ModelConfigAllModels,
				Budgets: []configstoreTables.TableBudget{
					{ID: "b-vk", MaxLimit: 100, CurrentUsage: 30, ResetDuration: "1d"},
				},
			},
		},
	}
	logMgr := &mockQuotaLogManager{rankErr: errors.New("log store down")}
	h := &GovernanceHandler{configStore: store, logManager: logMgr}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-vk", "sk-bf-secret")

	h.getVirtualKeyQuota(ctx)

	if ctx.Response.StatusCode() != 500 {
		t.Fatalf("expected status 500 on rankings load error, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
}

// TestGetVirtualKeyQuota_EndToEndWithRealStore exercises the full round-trip against
// a real (SQLite) config store: create a VK, write its top-level and per-provider
// governance as VK-scoped wildcard model configs (the same shape the create path
// produces via syncVKGovernanceToModelConfigs), then hit the quota endpoint and
// assert the budget values come back correct. This is the integration counterpart to
// the mocked tests above — it fails against the unpatched handler because
// GetVirtualKeyQuotaByValue reads the VK's now-empty direct Budgets relationship.
func TestGetVirtualKeyQuota_EndToEndWithRealStore(t *testing.T) {
	SetLogger(&mockLogger{})
	ctx := context.Background()
	// Deterministic cycle start so the per-model usage query window can be asserted exactly.
	cycleStart := time.Date(2026, time.January, 2, 15, 4, 5, 0, time.UTC)

	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "quota_e2e.db")},
	}, &mockLogger{})
	if err != nil {
		t.Fatalf("failed to create config store: %v", err)
	}

	const vkID = "vk-e2e"
	active := true
	vk := &configstoreTables.TableVirtualKey{
		ID:       vkID,
		Name:     "Prod",
		Value:    *schemas.NewSecretVar("sk-bf-e2e-secret"),
		IsActive: &active,
		ProviderConfigs: []configstoreTables.TableVirtualKeyProviderConfig{
			{VirtualKeyID: vkID, Provider: "openai", AllowAllKeys: true, AllowedModels: schemas.WhiteList{"*"}},
		},
	}
	if err := store.CreateVirtualKey(ctx, vk); err != nil {
		t.Fatalf("failed to create VK: %v", err)
	}

	scopeID := vkID
	// VK top-level governance: (scope=virtual_key, model_name='*', provider=nil).
	vkMC := &configstoreTables.TableModelConfig{
		ID:        "mc-vk-e2e",
		ModelName: configstoreTables.ModelConfigAllModels,
		Scope:     configstoreTables.ModelConfigScopeVirtualKey,
		ScopeID:   &scopeID,
		Budgets: []configstoreTables.TableBudget{
			// CreatedAt precedes LastReset (the steady-state: budget has existed for
			// several cycles), so the window clamp keeps LastReset as the query start.
			{ID: "b-vk-e2e", MaxLimit: 100, CurrentUsage: 30, ResetDuration: "1d", LastReset: cycleStart, CreatedAt: cycleStart.Add(-24 * time.Hour)},
		},
	}
	if err := store.CreateModelConfig(ctx, vkMC); err != nil {
		t.Fatalf("failed to create VK-scoped model config: %v", err)
	}
	// Per-provider governance for openai: (scope=virtual_key, model_name='*', provider='openai').
	openai := "openai"
	provMC := &configstoreTables.TableModelConfig{
		ID:        "mc-openai-e2e",
		ModelName: configstoreTables.ModelConfigAllModels,
		Scope:     configstoreTables.ModelConfigScopeVirtualKey,
		ScopeID:   &scopeID,
		Provider:  &openai,
		Budgets: []configstoreTables.TableBudget{
			{ID: "b-openai-e2e", MaxLimit: 50, CurrentUsage: 10, ResetDuration: "1d"},
		},
	}
	if err := store.CreateModelConfig(ctx, provMC); err != nil {
		t.Fatalf("failed to create provider-scoped model config: %v", err)
	}
	// Per-model governance: (scope=virtual_key, model_name='gpt-4o', provider='openai').
	modelMC := &configstoreTables.TableModelConfig{
		ID:        "mc-gpt4o-e2e",
		ModelName: "gpt-4o",
		Scope:     configstoreTables.ModelConfigScopeVirtualKey,
		ScopeID:   &scopeID,
		Provider:  &openai,
		Budgets: []configstoreTables.TableBudget{
			{ID: "b-gpt4o-e2e", MaxLimit: 25, CurrentUsage: 7, ResetDuration: "1d"},
		},
	}
	if err := store.CreateModelConfig(ctx, modelMC); err != nil {
		t.Fatalf("failed to create model-scoped model config: %v", err)
	}

	// Exercise the log-manager path so per_model_usage and the cycle window are covered
	// against the real store (mirrors the mocked unit test).
	logMgr := &mockQuotaLogManager{
		rankings: &logstore.ModelRankingResult{
			Rankings: []logstore.ModelRankingWithTrend{
				{ModelRankingEntry: logstore.ModelRankingEntry{Model: "gpt-4o", Provider: "openai", TotalRequests: 3, TotalTokens: 900, TotalCost: 0.42}},
			},
		},
	}
	h := &GovernanceHandler{configStore: store, logManager: logMgr}

	// The real store query uses the RequestCtx as a context.Context (Done/Err), which
	// nil-derefs on a non-Init'd RequestCtx — so initialize it like a live request.
	var req fasthttp.Request
	req.Header.Set("x-bf-vk", "sk-bf-e2e-secret")
	reqCtx := &fasthttp.RequestCtx{}
	reqCtx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)
	h.getVirtualKeyQuota(reqCtx)

	if reqCtx.Response.StatusCode() != 200 {
		t.Fatalf("expected status 200, got %d: %s", reqCtx.Response.StatusCode(), string(reqCtx.Response.Body()))
	}

	var resp quotaResponse
	if err := json.Unmarshal(reqCtx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.VirtualKeyName != "Prod" || !resp.IsActive {
		t.Fatalf("unexpected identity fields: name=%q active=%v", resp.VirtualKeyName, resp.IsActive)
	}
	if len(resp.Budgets) != 1 {
		t.Fatalf("expected one VK budget, got %#v", resp.Budgets)
	}
	if b := resp.Budgets[0]; b.ID != "b-vk-e2e" || b.MaxLimit != 100 || b.CurrentUsage != 30 || b.ResetDuration != "1d" {
		t.Fatalf("unexpected VK budget values: %#v", b)
	}
	if len(resp.ProviderConfigs) != 1 {
		t.Fatalf("expected one provider config, got %#v", resp.ProviderConfigs)
	}
	pcBudgets := resp.ProviderConfigs[0].Budgets
	if len(pcBudgets) != 1 {
		t.Fatalf("expected one provider budget, got %#v", pcBudgets)
	}
	if b := pcBudgets[0]; b.ID != "b-openai-e2e" || b.MaxLimit != 50 || b.CurrentUsage != 10 {
		t.Fatalf("unexpected provider budget values: %#v", b)
	}
	// Per-model usage: only the specific-model config (gpt-4o), not the wildcard configs.
	if len(resp.Models) != 1 {
		t.Fatalf("expected one per-model usage entry, got %#v", resp.Models)
	}
	m := resp.Models[0]
	if m.ModelName != "gpt-4o" || m.Provider == nil || *m.Provider != "openai" {
		t.Fatalf("unexpected per-model identity: name=%q provider=%v", m.ModelName, m.Provider)
	}
	if len(m.Budgets) != 1 {
		t.Fatalf("expected one per-model budget, got %#v", m.Budgets)
	}
	if b := m.Budgets[0]; b.ID != "b-gpt4o-e2e" || b.MaxLimit != 25 || b.CurrentUsage != 7 {
		t.Fatalf("unexpected per-model budget values: %#v", b)
	}
	// per_model_usage: the VK budget carries the actual per-model spend from the log manager.
	if len(resp.Budgets[0].Models) != 1 {
		t.Fatalf("expected one per_model_usage entry on the VK budget, got %#v", resp.Budgets[0].Models)
	}
	if s := resp.Budgets[0].Models[0]; s.Model != "gpt-4o" || s.Provider != "openai" || s.TotalRequests != 3 || s.TotalTokens != 900 || s.TotalCost != 0.42 {
		t.Fatalf("unexpected per_model_usage spend: %#v", s)
	}
	// The usage query must be scoped to this VK and windowed at the budget's last
	// reset (the budget predates LastReset, so the creation-time clamp is a no-op here).
	if len(logMgr.calls) != 1 {
		t.Fatalf("expected GetModelRankings called once, got %d", len(logMgr.calls))
	}
	call := logMgr.calls[0]
	if len(call.VirtualKeyIDs) != 1 || call.VirtualKeyIDs[0] != vkID {
		t.Fatalf("expected usage query scoped to %q, got %#v", vkID, call.VirtualKeyIDs)
	}
	if call.StartTime == nil || !call.StartTime.Equal(cycleStart) {
		t.Fatalf("expected StartTime=%v (budget last reset), got %v", cycleStart, call.StartTime)
	}
	if call.EndTime == nil || call.EndTime.Before(cycleStart) {
		t.Fatalf("expected EndTime >= StartTime, got %v", call.EndTime)
	}
}

// TestGetVirtualKeyQuota_WindowClampedToBudgetCreation verifies that when a budget's
// LastReset is backdated to a calendar period start that predates the budget's
// creation (e.g. a "1d" budget created mid-day with LastReset at midnight), the
// per_model_usage query window starts at CreatedAt rather than LastReset — so the
// breakdown does not report spend that occurred before the budget existed and stays
// consistent with current_usage (which only accrues from creation).
func TestGetVirtualKeyQuota_WindowClampedToBudgetCreation(t *testing.T) {
	SetLogger(&mockLogger{})
	ctx := context.Background()
	periodStart := time.Date(2026, time.June, 24, 0, 0, 0, 0, time.UTC)  // backdated "1d" boundary (midnight)
	createdAt := time.Date(2026, time.June, 24, 11, 56, 42, 0, time.UTC) // budget created mid-day

	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "quota_clamp.db")},
	}, &mockLogger{})
	if err != nil {
		t.Fatalf("failed to create config store: %v", err)
	}

	const vkID = "vk-clamp"
	active := true
	vk := &configstoreTables.TableVirtualKey{
		ID:       vkID,
		Name:     "Clamp",
		Value:    *schemas.NewSecretVar("sk-bf-clamp-secret"),
		IsActive: &active,
	}
	if err := store.CreateVirtualKey(ctx, vk); err != nil {
		t.Fatalf("failed to create VK: %v", err)
	}

	scopeID := vkID
	vkMC := &configstoreTables.TableModelConfig{
		ID:        "mc-clamp",
		ModelName: configstoreTables.ModelConfigAllModels,
		Scope:     configstoreTables.ModelConfigScopeVirtualKey,
		ScopeID:   &scopeID,
		Budgets: []configstoreTables.TableBudget{
			{ID: "b-clamp", MaxLimit: 6, CurrentUsage: 0, ResetDuration: "1d", LastReset: periodStart, CreatedAt: createdAt},
		},
	}
	if err := store.CreateModelConfig(ctx, vkMC); err != nil {
		t.Fatalf("failed to create model config: %v", err)
	}

	logMgr := &mockQuotaLogManager{rankings: &logstore.ModelRankingResult{}}
	h := &GovernanceHandler{configStore: store, logManager: logMgr}

	var req fasthttp.Request
	req.Header.Set("x-bf-vk", "sk-bf-clamp-secret")
	reqCtx := &fasthttp.RequestCtx{}
	reqCtx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)
	h.getVirtualKeyQuota(reqCtx)

	if reqCtx.Response.StatusCode() != 200 {
		t.Fatalf("expected status 200, got %d: %s", reqCtx.Response.StatusCode(), string(reqCtx.Response.Body()))
	}
	if len(logMgr.calls) != 1 {
		t.Fatalf("expected GetModelRankings called once, got %d", len(logMgr.calls))
	}
	call := logMgr.calls[0]
	if call.StartTime == nil || !call.StartTime.Equal(createdAt) {
		t.Fatalf("expected StartTime=%v (budget creation, clamped above backdated last reset), got %v", createdAt, call.StartTime)
	}
}

// TestGetVirtualKeys_PaginatedEndpoint_ResponseShape verifies the JSON response
// from the paginated virtual keys endpoint contains all expected fields.
func TestGetVirtualKeys_PaginatedEndpoint_ResponseShape(t *testing.T) {
	SetLogger(&mockLogger{})

	h := &GovernanceHandler{
		configStore:       &mockConfigStoreForVK{},
		governanceManager: &mockGovernanceManagerForVK{},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/governance/virtual-keys?limit=10&offset=0")

	h.getVirtualKeys(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected status 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to parse JSON response: %v", err)
	}

	// Assert expected fields exist with correct types
	requiredFields := []struct {
		key      string
		wantType string
	}{
		{"virtual_keys", "array"},
		{"total_count", "number"},
		{"count", "number"},
		{"limit", "number"},
		{"offset", "number"},
	}

	for _, f := range requiredFields {
		val, ok := resp[f.key]
		if !ok {
			t.Errorf("response missing required field %q", f.key)
			continue
		}
		switch f.wantType {
		case "array":
			if _, ok := val.([]interface{}); !ok {
				// nil decodes as nil, which is fine — JSON null for empty array
				if val != nil {
					t.Errorf("field %q: expected array, got %T", f.key, val)
				}
			}
		case "number":
			if _, ok := val.(float64); !ok {
				t.Errorf("field %q: expected number, got %T", f.key, val)
			}
		}
	}

	// Verify no unexpected extra top-level fields
	allowedKeys := map[string]bool{
		"virtual_keys": true,
		"total_count":  true,
		"count":        true,
		"limit":        true,
		"offset":       true,
	}
	for key := range resp {
		if !allowedKeys[key] {
			t.Errorf("unexpected field %q in response", key)
		}
	}
}

// TestGetVirtualKeys_PaginatedEndpoint_QueryParams verifies query parameters are
// parsed and reflected in the response.
func TestGetVirtualKeys_PaginatedEndpoint_QueryParams(t *testing.T) {
	SetLogger(&mockLogger{})

	h := &GovernanceHandler{
		configStore:       &mockConfigStoreForVK{},
		governanceManager: &mockGovernanceManagerForVK{},
	}

	tests := []struct {
		name       string
		uri        string
		wantLimit  float64
		wantOffset float64
	}{
		{
			name:       "explicit limit and offset",
			uri:        "/api/governance/virtual-keys?limit=10&offset=5",
			wantLimit:  10,
			wantOffset: 5,
		},
		{
			name:       "no params uses defaults",
			uri:        "/api/governance/virtual-keys",
			wantLimit:  0,
			wantOffset: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.SetMethod("GET")
			ctx.Request.SetRequestURI(tt.uri)

			h.getVirtualKeys(ctx)

			if ctx.Response.StatusCode() != 200 {
				t.Fatalf("expected status 200, got %d", ctx.Response.StatusCode())
			}

			var resp map[string]interface{}
			if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
				t.Fatalf("failed to parse JSON: %v", err)
			}

			if got := resp["limit"].(float64); got != tt.wantLimit {
				t.Errorf("limit: got %v, want %v", got, tt.wantLimit)
			}
			if got := resp["offset"].(float64); got != tt.wantOffset {
				t.Errorf("offset: got %v, want %v", got, tt.wantOffset)
			}
		})
	}
}

// TestGetVirtualKeys_FromMemoryUsesGovernanceData verifies the from_memory
// flag serves virtual keys from the in-memory GovernanceData and bypasses the
// DB-backed ConfigStore entirely.
func TestGetVirtualKeys_FromMemoryUsesGovernanceData(t *testing.T) {
	SetLogger(&mockLogger{})

	store := &mockConfigStoreForVK{}
	manager := &mockGovernanceManagerForVK{
		data: &governance.GovernanceData{
			VirtualKeys: map[string]*configstoreTables.TableVirtualKey{},
		},
	}
	h := &GovernanceHandler{
		configStore:       store,
		governanceManager: manager,
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/governance/virtual-keys?from_memory=true")

	h.getVirtualKeys(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected status 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if manager.getGovernanceDataCalls != 1 {
		t.Fatalf("expected GetGovernanceData to be called once, got %d", manager.getGovernanceDataCalls)
	}
	if store.getVirtualKeysCalls != 0 {
		t.Fatalf("from_memory path called GetVirtualKeys %d times", store.getVirtualKeysCalls)
	}
	if store.getVirtualKeysPaginatedCalls != 0 {
		t.Fatalf("from_memory path called GetVirtualKeysPaginated %d times", store.getVirtualKeysPaginatedCalls)
	}
}

// TestGetVirtualKeys_FromMemoryTakesPrecedenceOverLimit verifies the
// from_memory flag is honored even when pagination parameters are present, so
// the in-memory path is used and the paginated ConfigStore query is skipped.
func TestGetVirtualKeys_FromMemoryTakesPrecedenceOverLimit(t *testing.T) {
	SetLogger(&mockLogger{})

	store := &mockConfigStoreForVK{}
	manager := &mockGovernanceManagerForVK{
		data: &governance.GovernanceData{
			VirtualKeys: map[string]*configstoreTables.TableVirtualKey{},
		},
	}
	h := &GovernanceHandler{
		configStore:       store,
		governanceManager: manager,
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/governance/virtual-keys?limit=0&from_memory=true")

	h.getVirtualKeys(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected status 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if manager.getGovernanceDataCalls != 1 {
		t.Fatalf("expected GetGovernanceData to be called once, got %d", manager.getGovernanceDataCalls)
	}
	if store.getVirtualKeysPaginatedCalls != 0 {
		t.Fatalf("from_memory path called GetVirtualKeysPaginated %d times", store.getVirtualKeysPaginatedCalls)
	}
	if store.getVirtualKeysCalls != 0 {
		t.Fatalf("from_memory path called GetVirtualKeys %d times", store.getVirtualKeysCalls)
	}
}

// Ensure mockLogger satisfies schemas.Logger (already defined in middlewares_test.go
// but we reference it here — same package, so no redeclaration needed).
var _ schemas.Logger = (*mockLogger)(nil)

func TestBudgetRemovalRequestDetection(t *testing.T) {
	tests := []struct {
		name string
		req  *UpdateBudgetRequest
		want bool
	}{
		{
			name: "nil request is not removal",
			req:  nil,
			want: false,
		},
		{
			name: "empty object is removal",
			req:  &UpdateBudgetRequest{},
			want: true,
		},
		{
			name: "max limit present is not removal",
			req:  &UpdateBudgetRequest{MaxLimit: schemas.Ptr(10.0)},
			want: false,
		},
		{
			name: "reset duration only is not removal",
			req:  &UpdateBudgetRequest{ResetDuration: schemas.Ptr("1h")},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBudgetRemovalRequest(tt.req); got != tt.want {
				t.Fatalf("isBudgetRemovalRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRateLimitRemovalRequestDetection(t *testing.T) {
	tests := []struct {
		name string
		req  *UpdateRateLimitRequest
		want bool
	}{
		{
			name: "nil request is not removal",
			req:  nil,
			want: false,
		},
		{
			name: "empty object is removal",
			req:  &UpdateRateLimitRequest{},
			want: true,
		},
		{
			name: "token limit present is not removal",
			req:  &UpdateRateLimitRequest{TokenMaxLimit: schemas.Ptr(int64(100))},
			want: false,
		},
		{
			name: "request limit present is not removal",
			req:  &UpdateRateLimitRequest{RequestMaxLimit: schemas.Ptr(int64(10))},
			want: false,
		},
		{
			name: "durations only is not removal",
			req: &UpdateRateLimitRequest{
				TokenResetDuration:   schemas.Ptr("1h"),
				RequestResetDuration: schemas.Ptr("1h"),
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRateLimitRemovalRequest(tt.req); got != tt.want {
				t.Fatalf("isRateLimitRemovalRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCollectProviderConfigDeleteIDs(t *testing.T) {
	budgetID := "budget-1"
	rateLimitID := "rate-limit-1"

	tests := []struct {
		name             string
		config           configstoreTables.TableVirtualKeyProviderConfig
		initialBudgetIDs []string
		initialRateIDs   []string
		wantBudgetIDs    []string
		wantRateIDs      []string
	}{
		{
			name: "collects both IDs",
			config: configstoreTables.TableVirtualKeyProviderConfig{
				Budgets:     []configstoreTables.TableBudget{{ID: budgetID}},
				RateLimitID: &rateLimitID,
			},
			wantBudgetIDs: []string{budgetID},
			wantRateIDs:   []string{rateLimitID},
		},
		{
			name: "appends to existing slices",
			config: configstoreTables.TableVirtualKeyProviderConfig{
				Budgets:     []configstoreTables.TableBudget{{ID: budgetID}},
				RateLimitID: &rateLimitID,
			},
			initialBudgetIDs: []string{"budget-0"},
			initialRateIDs:   []string{"rate-limit-0"},
			wantBudgetIDs:    []string{"budget-0", budgetID},
			wantRateIDs:      []string{"rate-limit-0", rateLimitID},
		},
		{
			name:   "ignores missing IDs",
			config: configstoreTables.TableVirtualKeyProviderConfig{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBudgetIDs, gotRateIDs := collectProviderConfigDeleteIDs(tt.config, tt.initialBudgetIDs, tt.initialRateIDs)

			if len(gotBudgetIDs) != len(tt.wantBudgetIDs) {
				t.Fatalf("budget IDs length = %d, want %d", len(gotBudgetIDs), len(tt.wantBudgetIDs))
			}
			for i := range gotBudgetIDs {
				if gotBudgetIDs[i] != tt.wantBudgetIDs[i] {
					t.Fatalf("budget IDs[%d] = %q, want %q", i, gotBudgetIDs[i], tt.wantBudgetIDs[i])
				}
			}

			if len(gotRateIDs) != len(tt.wantRateIDs) {
				t.Fatalf("rate limit IDs length = %d, want %d", len(gotRateIDs), len(tt.wantRateIDs))
			}
			for i := range gotRateIDs {
				if gotRateIDs[i] != tt.wantRateIDs[i] {
					t.Fatalf("rate limit IDs[%d] = %q, want %q", i, gotRateIDs[i], tt.wantRateIDs[i])
				}
			}
		})
	}
}

func TestCoerceLegacyBudget(t *testing.T) {
	existing := &configstoreTables.TableBudget{ID: "bud-1", MaxLimit: 50, ResetDuration: "1d"}

	tests := []struct {
		name     string
		req      *UpdateBudgetRequest
		existing *configstoreTables.TableBudget
		// nil wantResult means coerce returns nil (no actionable change)
		wantNil    bool
		wantEmpty  bool // non-nil but empty slice (removal)
		wantID     string
		wantLimit  float64
		wantPeriod string
	}{
		{
			name:      "empty object → removal, returns empty slice",
			req:       &UpdateBudgetRequest{},
			existing:  nil,
			wantEmpty: true,
		},
		{
			name:       "both fields set, no existing → new budget entry, no ID",
			req:        &UpdateBudgetRequest{MaxLimit: schemas.Ptr(100.0), ResetDuration: schemas.Ptr("1w")},
			existing:   nil,
			wantLimit:  100,
			wantPeriod: "1w",
		},
		{
			name:       "update max_limit only, existing budget → merges ID and reset_duration",
			req:        &UpdateBudgetRequest{MaxLimit: schemas.Ptr(200.0)},
			existing:   existing,
			wantID:     "bud-1",
			wantLimit:  200,
			wantPeriod: "1d",
		},
		{
			name:       "update reset_duration only, existing budget → merges ID and max_limit",
			req:        &UpdateBudgetRequest{ResetDuration: schemas.Ptr("1w")},
			existing:   existing,
			wantID:     "bud-1",
			wantLimit:  50,
			wantPeriod: "1w",
		},
		{
			name:     "max_limit only, no existing → cannot build valid budget, returns nil",
			req:      &UpdateBudgetRequest{MaxLimit: schemas.Ptr(100.0)},
			existing: nil,
			wantNil:  true,
		},
		{
			name:     "reset_duration only, no existing → cannot build valid budget, returns nil",
			req:      &UpdateBudgetRequest{ResetDuration: schemas.Ptr("1d")},
			existing: nil,
			wantNil:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coerceLegacyBudget(tt.req, tt.existing)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			if tt.wantEmpty {
				if len(*got) != 0 {
					t.Fatalf("expected empty slice, got %+v", *got)
				}
				return
			}
			if len(*got) != 1 {
				t.Fatalf("expected 1-element slice, got %d elements", len(*got))
			}
			b := (*got)[0]
			if b.ID != tt.wantID {
				t.Errorf("ID = %q, want %q", b.ID, tt.wantID)
			}
			if b.MaxLimit != tt.wantLimit {
				t.Errorf("MaxLimit = %v, want %v", b.MaxLimit, tt.wantLimit)
			}
			if b.ResetDuration != tt.wantPeriod {
				t.Errorf("ResetDuration = %q, want %q", b.ResetDuration, tt.wantPeriod)
			}
		})
	}
}

func TestModelConfigToProviderGovernanceNewFields(t *testing.T) {
	provider := "openai"
	base := configstoreTables.TableModelConfig{
		Scope:     configstoreTables.ModelConfigScopeGlobal,
		ModelName: configstoreTables.ModelConfigAllModels,
		Provider:  &provider,
	}

	t.Run("nil mc returns false", func(t *testing.T) {
		if _, ok := modelConfigToProviderGovernance(nil); ok {
			t.Fatal("expected false for nil mc")
		}
	})

	t.Run("wrong scope returns false", func(t *testing.T) {
		mc := base
		mc.Scope = "virtual_key"
		if _, ok := modelConfigToProviderGovernance(&mc); ok {
			t.Fatal("expected false for non-global scope")
		}
	})

	t.Run("no budgets: Budget nil, Budgets empty, CalendarAligned false", func(t *testing.T) {
		mc := base
		r, ok := modelConfigToProviderGovernance(&mc)
		if !ok {
			t.Fatal("expected ok")
		}
		if r.Budget != nil {
			t.Errorf("Budget should be nil, got %+v", r.Budget)
		}
		if len(r.Budgets) != 0 {
			t.Errorf("Budgets should be empty, got %+v", r.Budgets)
		}
		if r.CalendarAligned {
			t.Error("CalendarAligned should be false")
		}
	})

	t.Run("single budget: Budget points to first, Budgets has one entry", func(t *testing.T) {
		mc := base
		mc.Budgets = []configstoreTables.TableBudget{{ID: "b1", MaxLimit: 100, ResetDuration: "1d"}}
		r, ok := modelConfigToProviderGovernance(&mc)
		if !ok {
			t.Fatal("expected ok")
		}
		if r.Budget == nil || r.Budget.ID != "b1" {
			t.Errorf("Budget = %+v, want ID=b1", r.Budget)
		}
		if len(r.Budgets) != 1 || r.Budgets[0].ID != "b1" {
			t.Errorf("Budgets = %+v, want 1 entry with ID=b1", r.Budgets)
		}
	})

	t.Run("multiple budgets: Budget is first, Budgets contains all", func(t *testing.T) {
		mc := base
		mc.Budgets = []configstoreTables.TableBudget{
			{ID: "b1", MaxLimit: 100, ResetDuration: "1d"},
			{ID: "b2", MaxLimit: 500, ResetDuration: "1w"},
		}
		r, ok := modelConfigToProviderGovernance(&mc)
		if !ok {
			t.Fatal("expected ok")
		}
		if r.Budget == nil || r.Budget.ID != "b1" {
			t.Errorf("Budget should point to first budget, got %+v", r.Budget)
		}
		if len(r.Budgets) != 2 {
			t.Fatalf("Budgets len = %d, want 2", len(r.Budgets))
		}
		if r.Budgets[0].ID != "b1" || r.Budgets[1].ID != "b2" {
			t.Errorf("Budgets = %+v", r.Budgets)
		}
	})

	t.Run("calendar_aligned is propagated", func(t *testing.T) {
		mc := base
		mc.CalendarAligned = true
		r, ok := modelConfigToProviderGovernance(&mc)
		if !ok {
			t.Fatal("expected ok")
		}
		if !r.CalendarAligned {
			t.Error("CalendarAligned should be true")
		}
	})

	t.Run("Budgets slice is a copy, not a reference to mc.Budgets", func(t *testing.T) {
		mc := base
		mc.Budgets = []configstoreTables.TableBudget{{ID: "b1", MaxLimit: 100, ResetDuration: "1d"}}
		r, _ := modelConfigToProviderGovernance(&mc)
		r.Budgets[0].MaxLimit = 999
		if mc.Budgets[0].MaxLimit == 999 {
			t.Error("mutating response Budgets should not affect the original mc")
		}
	})
}

func TestUpdateProviderGovernance_BudgetMutualExclusion(t *testing.T) {
	SetLogger(&mockLogger{})

	h := &GovernanceHandler{}
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("provider_name", "openai")
	ctx.Request.SetBodyString(`{
		"budget":  {"max_limit": 100, "reset_duration": "1d"},
		"budgets": [{"max_limit": 100, "reset_duration": "1d"}]
	}`)

	h.updateProviderGovernance(ctx)

	if ctx.Response.StatusCode() != 400 {
		t.Fatalf("expected 400, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	var resp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to parse error response: %v", err)
	}
	if !strings.Contains(resp.Error.Message, "budget") {
		t.Errorf("error message should mention 'budget', got: %q", resp.Error.Message)
	}
}

func TestValidateRoutingFallbacks(t *testing.T) {

	tests := []struct {
		name    string
		fbs     []string
		wantErr bool
	}{
		{name: "nil", fbs: nil, wantErr: false},
		{name: "empty", fbs: []string{}, wantErr: false},
		{name: "provider model", fbs: []string{"openai/gpt-4o"}, wantErr: false},
		{name: "provider slash incoming model", fbs: []string{"azure/"}, wantErr: false},
		{name: "bare known provider name rejected", fbs: []string{"openrouter"}, wantErr: true},
		{name: "bare model rejected", fbs: []string{"gpt-4o"}, wantErr: true},
		{name: "empty element", fbs: []string{"openai/gpt-4o", ""}, wantErr: true},
		{name: "huggingface namespace not a provider prefix", fbs: []string{"meta-llama/Llama-3.1-8B"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRoutingFallbacks(tt.fbs)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// --- customer calendar_aligned handler tests ---

type mockCustomerStore struct {
	configstore.ConfigStore
	customers      map[string]*configstoreTables.TableCustomer
	createdBudgets []*configstoreTables.TableBudget
	updatedBudgets []*configstoreTables.TableBudget
	updatedRLs     []*configstoreTables.TableRateLimit
}

func newMockCustomerStore() *mockCustomerStore {
	return &mockCustomerStore{customers: make(map[string]*configstoreTables.TableCustomer)}
}

func (m *mockCustomerStore) ExecuteTransaction(_ context.Context, fn func(*gorm.DB) error) error {
	return fn(nil)
}
func (m *mockCustomerStore) GetCustomer(_ context.Context, id string) (*configstoreTables.TableCustomer, error) {
	c, ok := m.customers[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	clone := *c
	if len(c.Budgets) > 0 {
		clonedBudgets := make([]configstoreTables.TableBudget, len(c.Budgets))
		copy(clonedBudgets, c.Budgets)
		clone.Budgets = clonedBudgets
	}
	if c.RateLimit != nil {
		rl := *c.RateLimit
		clone.RateLimit = &rl
	}
	return &clone, nil
}
func (m *mockCustomerStore) CreateCustomer(_ context.Context, customer *configstoreTables.TableCustomer, _ ...*gorm.DB) error {
	m.customers[customer.ID] = customer
	return nil
}
func (m *mockCustomerStore) UpdateCustomer(_ context.Context, customer *configstoreTables.TableCustomer, _ ...*gorm.DB) error {
	m.customers[customer.ID] = customer
	return nil
}
func (m *mockCustomerStore) CreateBudget(_ context.Context, budget *configstoreTables.TableBudget, _ ...*gorm.DB) error {
	m.createdBudgets = append(m.createdBudgets, budget)
	return nil
}
func (m *mockCustomerStore) UpdateBudget(_ context.Context, budget *configstoreTables.TableBudget, _ ...*gorm.DB) error {
	m.updatedBudgets = append(m.updatedBudgets, budget)
	return nil
}
func (m *mockCustomerStore) CreateRateLimit(_ context.Context, rl *configstoreTables.TableRateLimit, _ ...*gorm.DB) error {
	return nil
}
func (m *mockCustomerStore) UpdateRateLimit(_ context.Context, rl *configstoreTables.TableRateLimit, _ ...*gorm.DB) error {
	m.updatedRLs = append(m.updatedRLs, rl)
	return nil
}
func (m *mockCustomerStore) DeleteBudget(_ context.Context, _ string, _ ...*gorm.DB) error {
	return nil
}

type mockCustomerGovernanceManager struct {
	GovernanceManager
}

func (m *mockCustomerGovernanceManager) ReloadCustomer(_ context.Context, _ string) (*configstoreTables.TableCustomer, error) {
	return nil, nil
}

// TestCreateCustomer_CalendarAligned_SnapsBudgetLastReset verifies that when
// calendar_aligned=true is set on create, the budget's LastReset is snapped to
// the calendar period start rather than time.Now().
func TestCreateCustomer_CalendarAligned_SnapsBudgetLastReset(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newMockCustomerStore()
	h := &GovernanceHandler{configStore: store, governanceManager: &mockCustomerGovernanceManager{}}

	body, _ := json.Marshal(map[string]any{
		"name":             "ACME",
		"calendar_aligned": true,
		"budget": map[string]any{
			"max_limit":      100.0,
			"reset_duration": "1M",
		},
	})
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBody(body)

	before := time.Now()
	h.createCustomer(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if len(store.createdBudgets) != 1 {
		t.Fatalf("expected 1 created budget, got %d", len(store.createdBudgets))
	}
	b := store.createdBudgets[0]
	// Calendar-aligned LastReset must be at the start of the calendar period,
	// which is always <= the beginning of the test, never a rolling time.Now().
	if b.LastReset.After(before) {
		t.Errorf("calendar-aligned budget LastReset %v should not be after test start %v (expected period start)", b.LastReset, before)
	}
	// Confirm the stored customer has CalendarAligned=true.
	var created *configstoreTables.TableCustomer
	for _, c := range store.customers {
		created = c
	}
	if created == nil || !created.CalendarAligned {
		t.Errorf("stored customer should have CalendarAligned=true")
	}
}

// TestCreateCustomer_CalendarAligned_False verifies that when calendar_aligned is
// not set, budget LastReset is a rolling time.Now() (not at a period boundary).
func TestCreateCustomer_CalendarAligned_False(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newMockCustomerStore()
	h := &GovernanceHandler{configStore: store, governanceManager: &mockCustomerGovernanceManager{}}

	body, _ := json.Marshal(map[string]any{
		"name": "Globex",
		"budget": map[string]any{
			"max_limit":      50.0,
			"reset_duration": "1M",
		},
	})
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBody(body)

	before := time.Now()
	h.createCustomer(ctx)
	after := time.Now()

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if len(store.createdBudgets) != 1 {
		t.Fatalf("expected 1 created budget, got %d", len(store.createdBudgets))
	}
	b := store.createdBudgets[0]
	// Rolling LastReset should be within the test window.
	if b.LastReset.Before(before) || b.LastReset.After(after) {
		t.Errorf("non-calendar-aligned budget LastReset %v should be between %v and %v", b.LastReset, before, after)
	}
}

// TestUpdateCustomer_CalendarAligned_SnapsExistingBudget verifies that toggling
// calendar_aligned from false to true snaps the existing budget's LastReset to the
// start of the current calendar period and resets CurrentUsage.
func TestUpdateCustomer_CalendarAligned_SnapsExistingBudget(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newMockCustomerStore()

	budgetID := "bud-snap"
	budgetID2 := "bud-snap-2"
	oldLastReset := time.Now().AddDate(0, -1, 0) // 1 month ago
	store.customers["cust-snap"] = &configstoreTables.TableCustomer{
		ID:              "cust-snap",
		Name:            "Initech",
		CalendarAligned: false,
		Budgets: []configstoreTables.TableBudget{
			{
				ID:            budgetID,
				MaxLimit:      200.0,
				ResetDuration: "1M",
				LastReset:     oldLastReset,
				CurrentUsage:  99.0,
			},
			{
				ID:            budgetID2,
				MaxLimit:      500.0,
				ResetDuration: "1Y",
				LastReset:     oldLastReset,
				CurrentUsage:  150.0,
			},
		},
	}
	h := &GovernanceHandler{configStore: store, governanceManager: &mockCustomerGovernanceManager{}}

	body, _ := json.Marshal(map[string]any{"calendar_aligned": true})
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBody(body)
	ctx.SetUserValue("customer_id", "cust-snap")

	snapBefore := time.Now()
	h.updateCustomer(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	// UpdateBudget must have been called once per budget (both snap).
	if len(store.updatedBudgets) != 2 {
		t.Fatalf("expected 2 UpdateBudget calls for snap, got %d", len(store.updatedBudgets))
	}
	snappedIDs := make(map[string]bool, 2)
	for _, snapped := range store.updatedBudgets {
		snappedIDs[snapped.ID] = true
		if snapped.LastReset.Equal(oldLastReset) {
			t.Errorf("budget %s LastReset was not snapped: still equals old value", snapped.ID)
		}
		if snapped.LastReset.After(snapBefore) {
			t.Errorf("budget %s snapped LastReset %v should be at the period start, not time.Now()", snapped.ID, snapped.LastReset)
		}
		if snapped.CurrentUsage != 0 {
			t.Errorf("budget %s expected CurrentUsage reset to 0, got %v", snapped.ID, snapped.CurrentUsage)
		}
	}
	if !snappedIDs[budgetID] || !snappedIDs[budgetID2] {
		t.Errorf("expected both %q and %q to be snapped, got IDs: %v", budgetID, budgetID2, snappedIDs)
	}
}

// TestUpdateCustomer_CalendarAligned_NoSnapWhenAlreadyEnabled verifies that if
// calendar_aligned is already true, no snap/UpdateBudget call occurs on update.
func TestUpdateCustomer_CalendarAligned_NoSnapWhenAlreadyEnabled(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newMockCustomerStore()

	store.customers["cust-already"] = &configstoreTables.TableCustomer{
		ID:              "cust-already",
		Name:            "Umbrella",
		CalendarAligned: true, // already enabled
		Budgets: []configstoreTables.TableBudget{
			{
				ID:            "bud-already-1",
				MaxLimit:      300.0,
				ResetDuration: "1M",
				LastReset:     time.Now().AddDate(0, -1, 0),
				CurrentUsage:  42.0,
			},
			{
				ID:            "bud-already-2",
				MaxLimit:      800.0,
				ResetDuration: "1Y",
				LastReset:     time.Now().AddDate(-1, 0, 0),
				CurrentUsage:  10.0,
			},
		},
	}
	h := &GovernanceHandler{configStore: store, governanceManager: &mockCustomerGovernanceManager{}}

	body, _ := json.Marshal(map[string]any{"calendar_aligned": true})
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBody(body)
	ctx.SetUserValue("customer_id", "cust-already")

	h.updateCustomer(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if len(store.updatedBudgets) != 0 {
		t.Errorf("expected no UpdateBudget call when calendar_aligned was already true, got %d", len(store.updatedBudgets))
	}
}

// TestApplyVKGovernanceFromModelConfigs_PreservesDirectlyAttachedBudget is a
// regression test for BF-1497: VKs provisioned via an access profile / config.json
// carry their global budget directly (TableBudget.VirtualKeyID set, preloaded into
// vk.Budgets) and have no VK-scoped model config. Hydration must not wipe that
// budget when no model config matches.
func TestApplyVKGovernanceFromModelConfigs_PreservesDirectlyAttachedBudget(t *testing.T) {
	directBudget := configstoreTables.TableBudget{
		ID:            "bud-direct",
		MaxLimit:      2500.0,
		ResetDuration: "1M",
		VirtualKeyID:  schemas.Ptr("vk-ap"),
		CurrentUsage:  120.0,
	}
	directRL := &configstoreTables.TableRateLimit{ID: "rl-direct"}
	// A config.json-provisioned VK can also carry directly-attached per-provider
	// budgets (TableBudget.ProviderConfigID set), which must survive hydration too.
	pcBudget := configstoreTables.TableBudget{
		ID:               "bud-direct-pc",
		MaxLimit:         500.0,
		ResetDuration:    "1M",
		ProviderConfigID: schemas.Ptr(uint(7)),
	}
	vk := &configstoreTables.TableVirtualKey{
		ID:          "vk-ap",
		Budgets:     []configstoreTables.TableBudget{directBudget},
		RateLimit:   directRL,
		RateLimitID: schemas.Ptr("rl-direct"),
		ProviderConfigs: []configstoreTables.TableVirtualKeyProviderConfig{
			{Provider: "anthropic", Budgets: []configstoreTables.TableBudget{pcBudget}},
		},
	}

	// No VK-scoped model config exists for this VK.
	applyVKGovernanceFromModelConfigs(vk, map[string]*configstoreTables.TableModelConfig{})

	if len(vk.Budgets) != 1 || vk.Budgets[0].ID != "bud-direct" {
		t.Fatalf("directly attached budget was wiped: got %+v", vk.Budgets)
	}
	if vk.RateLimit != directRL || vk.RateLimitID == nil || *vk.RateLimitID != "rl-direct" {
		t.Errorf("directly attached rate limit was wiped: rl=%v id=%v", vk.RateLimit, vk.RateLimitID)
	}
	if len(vk.ProviderConfigs[0].Budgets) != 1 || vk.ProviderConfigs[0].Budgets[0].ID != "bud-direct-pc" {
		t.Errorf("directly attached per-provider budget was wiped: got %+v", vk.ProviderConfigs[0].Budgets)
	}
}

// TestApplyVKGovernanceFromModelConfigs_OverlaysModelConfigGovernance verifies the
// existing overlay path: when a VK-scoped model config owns the governance
// (TableBudget.ModelConfigID set, not preloaded onto the VK), hydration overlays it.
func TestApplyVKGovernanceFromModelConfigs_OverlaysModelConfigGovernance(t *testing.T) {
	mcBudget := configstoreTables.TableBudget{
		ID:            "bud-mc",
		MaxLimit:      999.0,
		ResetDuration: "1M",
		ModelConfigID: schemas.Ptr("mc-top"),
	}
	mcRL := &configstoreTables.TableRateLimit{ID: "rl-mc"}
	vk := &configstoreTables.TableVirtualKey{ID: "vk-sheet"}

	byKey := map[string]*configstoreTables.TableModelConfig{
		vkModelConfigIndexKey("vk-sheet", nil): {
			ID:          "mc-top",
			Budgets:     []configstoreTables.TableBudget{mcBudget},
			RateLimit:   mcRL,
			RateLimitID: schemas.Ptr("rl-mc"),
		},
	}

	applyVKGovernanceFromModelConfigs(vk, byKey)

	if len(vk.Budgets) != 1 || vk.Budgets[0].ID != "bud-mc" {
		t.Fatalf("expected model-config budget overlaid, got %+v", vk.Budgets)
	}
	if vk.RateLimit != mcRL || vk.RateLimitID == nil || *vk.RateLimitID != "rl-mc" {
		t.Errorf("expected model-config rate limit overlaid, got rl=%v id=%v", vk.RateLimit, vk.RateLimitID)
	}
}

// newGovernanceProviderNameCtx builds a RequestCtx exactly as the fasthttp router
// would hand it to the handler: the {provider_name} path param is stored RAW
// (still percent-encoded), because the router does not decode path params. This
// is what exercises url.PathUnescape inside the handler.
func newGovernanceProviderNameCtx(encodedProviderName, body string) *fasthttp.RequestCtx {
	ctx := newTestRequestCtx(body)
	ctx.SetUserValue("provider_name", encodedProviderName)
	return ctx
}

// TestProviderGovernance_DecodesEncodedProviderName is a regression test for the
// 404 "Provider not found" that occurred when updating/deleting governance for a
// custom provider whose name contains a space (e.g. "OpenRouter Base"). The UI
// percent-encodes the name in the path ("OpenRouter%20Base"); the handler must
// url.PathUnescape it before matching against the stored provider name.
func TestProviderGovernance_DecodesEncodedProviderName(t *testing.T) {
	SetLogger(&mockLogger{})
	ctx := context.Background()
	store := setupPricingOverrideHandlerStore(t)
	handler := &GovernanceHandler{
		configStore:       store,
		governanceManager: pricingOverrideTestGovernanceManager{},
	}

	// Seed a custom provider whose name contains a space.
	const providerName = "OpenRouter Base"
	const encodedName = "OpenRouter%20Base"
	if err := store.AddProvider(ctx, schemas.ModelProvider(providerName), configstore.ProviderConfig{}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	// PUT a budget using the encoded name in the path param, exactly as the router
	// delivers it. Before the fix this returned 404 because "OpenRouter%20Base" was
	// compared raw against the stored name.
	putCtx := newGovernanceProviderNameCtx(encodedName, `{"budgets":[{"max_limit":10,"reset_duration":"1M"}],"calendar_aligned":false}`)
	handler.updateProviderGovernance(putCtx)
	if putCtx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("PUT status got %d, want 200; body=%s", putCtx.Response.StatusCode(), putCtx.Response.Body())
	}

	// The budget must be persisted against the decoded provider name.
	pn := providerName
	mc, err := store.GetModelConfig(ctx, configstoreTables.ModelConfigScopeGlobal, nil, configstoreTables.ModelConfigAllModels, &pn)
	if err != nil {
		t.Fatalf("expected persisted model config for %q, got err: %v", providerName, err)
	}
	if len(mc.Budgets) != 1 || mc.Budgets[0].MaxLimit != 10 {
		t.Fatalf("expected one budget with max_limit 10, got %+v", mc.Budgets)
	}

	// DELETE with the same encoded path param must also resolve and succeed.
	delCtx := newGovernanceProviderNameCtx(encodedName, "")
	handler.deleteProviderGovernance(delCtx)
	if delCtx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("DELETE status got %d, want 200; body=%s", delCtx.Response.StatusCode(), delCtx.Response.Body())
	}

	// The model config must actually be gone — a 200 alone could come from the
	// handler's idempotent ErrNotFound branch even if nothing was removed.
	if _, err := store.GetModelConfig(ctx, configstoreTables.ModelConfigScopeGlobal, nil, configstoreTables.ModelConfigAllModels, &pn); !errors.Is(err, configstore.ErrNotFound) {
		t.Fatalf("expected model config for %q to be removed (ErrNotFound), got err: %v", providerName, err)
	}
}

// TestProviderGovernance_UnknownProviderStill404 guards the inverse: a genuinely
// unknown provider must still 404, so the decode change didn't mask the check.
func TestProviderGovernance_UnknownProviderStill404(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	handler := &GovernanceHandler{
		configStore:       store,
		governanceManager: pricingOverrideTestGovernanceManager{},
	}

	putCtx := newGovernanceProviderNameCtx("Nope%20Missing", `{"budgets":[{"max_limit":10,"reset_duration":"1M"}]}`)
	handler.updateProviderGovernance(putCtx)
	if putCtx.Response.StatusCode() != fasthttp.StatusNotFound {
		t.Fatalf("PUT unknown provider status got %d, want 404; body=%s", putCtx.Response.StatusCode(), putCtx.Response.Body())
	}
}

// TestProviderGovernance_MalformedEncodingReturns400 locks in the fail-closed
// contract: when the provider name is not valid percent-encoding (e.g. a stray
// "%2"), url.PathUnescape fails and both handlers must respond 400 rather than
// matching against the raw string.
func TestProviderGovernance_MalformedEncodingReturns400(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	handler := &GovernanceHandler{
		configStore:       store,
		governanceManager: pricingOverrideTestGovernanceManager{},
	}

	const malformedName = "OpenRouter%2"

	putCtx := newGovernanceProviderNameCtx(malformedName, `{"budgets":[{"max_limit":10,"reset_duration":"1M"}]}`)
	handler.updateProviderGovernance(putCtx)
	if putCtx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("PUT malformed encoding status got %d, want 400; body=%s", putCtx.Response.StatusCode(), putCtx.Response.Body())
	}

	delCtx := newGovernanceProviderNameCtx(malformedName, "")
	handler.deleteProviderGovernance(delCtx)
	if delCtx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("DELETE malformed encoding status got %d, want 400; body=%s", delCtx.Response.StatusCode(), delCtx.Response.Body())
	}
}

// newGovernanceTeamIDCtx builds a request whose team_id path param carries the
// raw (still percent-encoded) value, exactly as the fasthttp router delivers it
// (it matches on URI().PathOriginal(), so no decoding happens before the handler).
func newGovernanceTeamIDCtx(encodedTeamID, body string) *fasthttp.RequestCtx {
	ctx := newTestRequestCtx(body)
	ctx.SetUserValue("team_id", encodedTeamID)
	return ctx
}

// TestTeam_DecodesEncodedTeamID is a regression test for #3106: SCIM/IdP-synced
// team IDs containing spaces or other URL-sensitive characters are listable but
// individual GET/DELETE returned "404 Team not found". The router delivers the
// team_id path segment still percent-encoded, so the handler must url.PathUnescape
// it before the config-store lookup (mirrors the provider_name handling).
func TestTeam_DecodesEncodedTeamID(t *testing.T) {
	SetLogger(&mockLogger{})
	ctx := context.Background()
	store := setupPricingOverrideHandlerStore(t)
	handler := &GovernanceHandler{
		configStore:       store,
		governanceManager: pricingOverrideTestGovernanceManager{},
	}

	cases := []struct {
		name    string
		teamID  string // stored (decoded) ID
		encoded string // what the router hands to the handler
	}{
		{"space", "SCIM Team Alpha", "SCIM%20Team%20Alpha"},
		{"punctuation", "Team (prod): eu-west", "Team%20%28prod%29%3A%20eu-west"},
		{"encoded-slash", "org/team/beta", "org%2Fteam%2Fbeta"},
		{"plus", "team+gamma", "team%2Bgamma"},
		{"unicode", "команда-δ", "%D0%BA%D0%BE%D0%BC%D0%B0%D0%BD%D0%B4%D0%B0-%CE%B4"},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			team := &configstoreTables.TableTeam{
				ID:   tc.teamID,
				Name: tc.name + "-" + string(rune('A'+i)), // Name has a unique index
			}
			if err := store.CreateTeam(ctx, team); err != nil {
				t.Fatalf("seed team %q: %v", tc.teamID, err)
			}

			// GET with the encoded path param must resolve the team.
			getCtx := newGovernanceTeamIDCtx(tc.encoded, "")
			handler.getTeam(getCtx)
			if getCtx.Response.StatusCode() != fasthttp.StatusOK {
				t.Fatalf("GET status got %d, want 200; body=%s", getCtx.Response.StatusCode(), getCtx.Response.Body())
			}
			var getResp struct {
				Team configstoreTables.TableTeam `json:"team"`
			}
			if err := json.Unmarshal(getCtx.Response.Body(), &getResp); err != nil {
				t.Fatalf("parse GET body: %v", err)
			}
			if getResp.Team.ID != tc.teamID {
				t.Fatalf("GET returned team id %q, want %q", getResp.Team.ID, tc.teamID)
			}

			// PUT with the encoded path param must resolve and apply the update.
			renamed := tc.name + "-renamed-" + string(rune('A'+i))
			putCtx := newGovernanceTeamIDCtx(tc.encoded, `{"name":"`+renamed+`"}`)
			handler.updateTeam(putCtx)
			if putCtx.Response.StatusCode() != fasthttp.StatusOK {
				t.Fatalf("PUT status got %d, want 200; body=%s", putCtx.Response.StatusCode(), putCtx.Response.Body())
			}
			updated, err := store.GetTeam(ctx, tc.teamID)
			if err != nil {
				t.Fatalf("re-fetch team %q after update: %v", tc.teamID, err)
			}
			if updated.Name != renamed {
				t.Fatalf("PUT did not apply: name %q, want %q", updated.Name, renamed)
			}

			// DELETE with the same encoded path param must resolve and succeed.
			delCtx := newGovernanceTeamIDCtx(tc.encoded, "")
			handler.deleteTeam(delCtx)
			if delCtx.Response.StatusCode() != fasthttp.StatusOK {
				t.Fatalf("DELETE status got %d, want 200; body=%s", delCtx.Response.StatusCode(), delCtx.Response.Body())
			}

			// The team must actually be gone — a 200 could otherwise come from an
			// idempotent ErrNotFound branch without deleting anything.
			if _, err := store.GetTeam(ctx, tc.teamID); !errors.Is(err, configstore.ErrNotFound) {
				t.Fatalf("expected team %q removed (ErrNotFound), got err: %v", tc.teamID, err)
			}
		})
	}
}

// TestTeam_MalformedEncodingReturns400 locks in the fail-closed contract: a team_id
// that is not valid percent-encoding (e.g. a stray "%2") must yield 400 rather than
// being matched raw against stored IDs.
func TestTeam_MalformedEncodingReturns400(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	handler := &GovernanceHandler{
		configStore:       store,
		governanceManager: pricingOverrideTestGovernanceManager{},
	}

	const malformedID = "Team%2"

	getCtx := newGovernanceTeamIDCtx(malformedID, "")
	handler.getTeam(getCtx)
	if getCtx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("GET malformed encoding status got %d, want 400; body=%s", getCtx.Response.StatusCode(), getCtx.Response.Body())
	}

	putCtx := newGovernanceTeamIDCtx(malformedID, `{"name":"irrelevant"}`)
	handler.updateTeam(putCtx)
	if putCtx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("PUT malformed encoding status got %d, want 400; body=%s", putCtx.Response.StatusCode(), putCtx.Response.Body())
	}

	delCtx := newGovernanceTeamIDCtx(malformedID, "")
	handler.deleteTeam(delCtx)
	if delCtx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("DELETE malformed encoding status got %d, want 400; body=%s", delCtx.Response.StatusCode(), delCtx.Response.Body())
	}
}
