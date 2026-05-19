package handlers

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// mockGovernanceManagerForVK embeds the interface so unimplemented methods panic.
// Only GetGovernanceData is needed for the getVirtualKeys handler path.
type mockGovernanceManagerForVK struct {
	GovernanceManager
}

func (m *mockGovernanceManagerForVK) GetGovernanceData(ctx context.Context) *governance.GovernanceData {
	return nil
}

// mockConfigStoreForVK embeds the interface so unimplemented methods panic.
// Only GetVirtualKeysPaginated is called in the non-from_memory path.
type mockConfigStoreForVK struct {
	configstore.ConfigStore
}

func (m *mockConfigStoreForVK) GetVirtualKeysPaginated(_ context.Context, _ configstore.VirtualKeyQueryParams) ([]configstoreTables.TableVirtualKey, int64, error) {
	return nil, 0, nil
}

func (m *mockConfigStoreForVK) GetVirtualKeys(_ context.Context) ([]configstoreTables.TableVirtualKey, error) {
	return nil, nil
}

type mockRotateConfigStore struct {
	configstore.ConfigStore
	virtualKeys map[string]*configstoreTables.TableVirtualKey
	updates     int
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

type mockRotateGovernanceManager struct {
	GovernanceManager
	store     *mockRotateConfigStore
	reloadIDs []string
}

func (m *mockRotateGovernanceManager) ReloadVirtualKey(ctx context.Context, id string) (*configstoreTables.TableVirtualKey, error) {
	m.reloadIDs = append(m.reloadIDs, id)
	return m.store.GetVirtualKey(ctx, id)
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

	byID, byDuration := buildBudgetLookup([]configstoreTables.TableBudget{monthlyBudget, dailyBudget})
	matched, found, err := findExistingBudget(CreateBudgetRequest{
		ID:            "budget-monthly",
		MaxLimit:      120,
		ResetDuration: "1d",
	}, byID, byDuration)
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
	byID, byDuration := buildBudgetLookup([]configstoreTables.TableBudget{
		{ID: "budget-1", ResetDuration: "1d"},
	})

	_, _, err := findExistingBudget(CreateBudgetRequest{
		ID:            "missing-budget",
		MaxLimit:      100,
		ResetDuration: "1d",
	}, byID, byDuration)
	if err == nil {
		t.Fatal("expected unknown budget ID to fail")
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

	byID, byDuration := buildBudgetLookup(existing)
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
			if err := inheritUsageFromClosestShorterBudget(&budget, reconciled, resetUsage); err != nil {
				return nil, err
			}
		}
		configChanged := budget.MaxLimit != request.MaxLimit || budget.ResetDuration != request.ResetDuration
		budget.MaxLimit = request.MaxLimit
		budget.ResetDuration = request.ResetDuration
		resetBudgetUsageIfRequested(&budget, resetUsage, false)
		if found && configChanged {
			if err := validatePreservedBudgetUsageWithinLimit(&budget, "preserved", "", resetUsage); err != nil {
				return nil, err
			}
		}
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

func TestExistingVirtualKeyBudgetLoweredBelowPreservedUsageFails(t *testing.T) {
	_, err := reconcileBudgetRequestsForTest(
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
	if err == nil {
		t.Fatal("expected lowering budget below preserved usage to fail")
	}
	if !strings.Contains(err.Error(), "reset usage while setting up the budget or increase the budget limit") {
		t.Fatalf("expected actionable error message, got %v", err)
	}
}

func TestExistingProviderBudgetLoweredBelowPreservedUsageFails(t *testing.T) {
	_, err := reconcileBudgetRequestsForTest(
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
	if err == nil {
		t.Fatal("expected lowering provider budget below preserved usage to fail")
	}
	if !strings.Contains(err.Error(), "reset usage while setting up the budget or increase the budget limit") {
		t.Fatalf("expected actionable error message, got %v", err)
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

func TestNewVirtualKeyBudgetInheritanceFailsWhenUsageExceedsLimit(t *testing.T) {
	_, err := reconcileBudgetRequestsForTest(
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
	if err == nil {
		t.Fatal("expected inherited usage above new budget limit to fail")
	}
	if !strings.Contains(err.Error(), "reset usage while setting up the budget or increase the budget limit") {
		t.Fatalf("expected actionable error message, got %v", err)
	}
}

func TestNewProviderBudgetInheritanceFailsWhenUsageExceedsLimit(t *testing.T) {
	_, err := reconcileBudgetRequestsForTest(
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
	if err == nil {
		t.Fatal("expected inherited usage equal to new budget limit to fail")
	}
	if !strings.Contains(err.Error(), "reset usage while setting up the budget or increase the budget limit") {
		t.Fatalf("expected actionable error message, got %v", err)
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
				Value:       "sk-bf-old",
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
	if updated.Value == "sk-bf-old" {
		t.Fatal("expected virtual key value to rotate")
	}
	if !strings.HasPrefix(updated.Value, governance.VirtualKeyPrefix) {
		t.Fatalf("expected rotated value to use %q prefix, got %q", governance.VirtualKeyPrefix, updated.Value)
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
	if resp.VirtualKey.Value != updated.Value {
		t.Fatalf("response value = %q, want %q", resp.VirtualKey.Value, updated.Value)
	}
}

func TestRotateVirtualKeys_PartialSuccess(t *testing.T) {
	SetLogger(&mockLogger{})

	store := &mockRotateConfigStore{
		virtualKeys: map[string]*configstoreTables.TableVirtualKey{
			"vk-1": {ID: "vk-1", Name: "One", Value: "sk-bf-old-1"},
			"vk-2": {ID: "vk-2", Name: "Two", Value: "sk-bf-old-2"},
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
	if store.virtualKeys["vk-1"].Value == "sk-bf-old-1" || store.virtualKeys["vk-2"].Value == "sk-bf-old-2" {
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
