package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fasthttp/router"
	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/valyala/fasthttp"
)

type mockRoutingManager struct {
	RoutingManager
	reloadedConfig *complexity.AnalyzerConfig
	reloadCalls    int
	reloadErr      error
}

func (m *mockRoutingManager) ReloadComplexityAnalyzerConfig(_ context.Context, config *complexity.AnalyzerConfig) error {
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

// unreachableConfigStore fails the complexity read the way an unreachable
// database does, and delegates everything else. The embedded interface is nil,
// so any other call panics rather than quietly returning a zero value.
type unreachableConfigStore struct {
	configstore.ConfigStore
}

func (unreachableConfigStore) GetComplexityAnalyzerConfig(context.Context) (*configstore.ComplexityAnalyzerConfig, error) {
	return nil, errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")
}

// TestComplexityAnalyzerConfigGetDegradesOnUnreadableConfig covers a stored
// config this version cannot parse — after a rollback, for instance. The
// analyzer has already fallen back to defaults for the same reason, logging a
// warning, so the page must show what is actually running instead of failing.
func TestComplexityAnalyzerConfigGetDegradesOnUnreadableConfig(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)

	// Well-formed JSON, but the boundaries are out of order, so it fails
	// validation on the way out of the store.
	require.NoError(t, store.UpdateConfig(context.Background(), &tables.TableGovernanceConfig{
		Key:   tables.ConfigComplexityAnalyzerConfigKey,
		Value: `{"tier_boundaries":{"simple_medium":0.9,"medium_complex":0.1}}`,
	}))

	_, err := store.GetComplexityAnalyzerConfig(context.Background())
	require.ErrorIs(t, err, configstore.ErrConfigUnreadable,
		"the store must mark this as unreadable, not as an infrastructure failure")

	handler := &RoutingHandler{configStore: store, routingManager: &mockRoutingManager{}}
	ctx := newTestRequestCtx("")
	handler.getComplexityAnalyzerConfig(ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(),
		"an unreadable stored config must not take the page down: %s", string(ctx.Response.Body()))

	var resp complexity.AnalyzerConfig
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &resp))
	require.Equal(t, complexity.DefaultTierBoundaries(), resp.TierBoundaries)
}

// TestComplexityAnalyzerConfigGetStillFailsWhenStoreUnreachable is the other
// half: defaults are only correct when the config is unreadable. Serving them
// when the store is down would report a broken installation as a working one.
func TestComplexityAnalyzerConfigGetStillFailsWhenStoreUnreachable(t *testing.T) {
	SetLogger(&mockLogger{})
	handler := &RoutingHandler{
		configStore:    unreachableConfigStore{},
		routingManager: &mockRoutingManager{},
	}

	ctx := newTestRequestCtx("")
	handler.getComplexityAnalyzerConfig(ctx)

	require.Equal(t, fasthttp.StatusInternalServerError, ctx.Response.StatusCode(),
		"an unreachable store must surface as an error, not as defaults")
}

func TestComplexityAnalyzerConfigGetReturnsDefaultsWhenUnset(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	handler := &RoutingHandler{
		configStore:    store,
		routingManager: &mockRoutingManager{},
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
	manager := &mockRoutingManager{}
	handler := &RoutingHandler{
		configStore:    store,
		routingManager: manager,
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
	handler := &RoutingHandler{
		configStore:    store,
		routingManager: &mockRoutingManager{},
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
	manager := &mockRoutingManager{}
	handler := &RoutingHandler{
		configStore:    store,
		routingManager: manager,
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

// TestRoutingRoutesServeCanonicalAndLegacyPaths pins the backwards-compatibility contract:
// every routing endpoint answers on both its /api/routing path and the /api/governance path
// it shipped under before routing became its own plugin, and each pair resolves to the same
// handler so the two can never drift.
func TestRoutingRoutesServeCanonicalAndLegacyPaths(t *testing.T) {
	r := router.New()
	h := &RoutingHandler{}
	h.RegisterRoutes(r)

	pairs := []struct {
		method    string
		canonical string
		legacy    string
	}{
		{fasthttp.MethodGet, "/api/routing/rules", "/api/governance/routing-rules"},
		{fasthttp.MethodPost, "/api/routing/rules", "/api/governance/routing-rules"},
		{fasthttp.MethodGet, "/api/routing/rules/{rule_id}", "/api/governance/routing-rules/{rule_id}"},
		{fasthttp.MethodPut, "/api/routing/rules/{rule_id}", "/api/governance/routing-rules/{rule_id}"},
		{fasthttp.MethodDelete, "/api/routing/rules/{rule_id}", "/api/governance/routing-rules/{rule_id}"},
		{fasthttp.MethodGet, "/api/routing/complexity-analyzer-config", "/api/governance/complexity-analyzer-config"},
		{fasthttp.MethodPut, "/api/routing/complexity-analyzer-config", "/api/governance/complexity-analyzer-config"},
		{fasthttp.MethodPost, "/api/routing/complexity-analyzer-config/reset", "/api/governance/complexity-analyzer-config/reset"},
	}

	for _, pair := range pairs {
		for _, path := range []string{pair.canonical, pair.legacy} {
			if got := countRegisteredRoute(r, pair.method, path); got != 1 {
				t.Fatalf("%s %s registrations = %d, want 1", pair.method, path, got)
			}
		}
	}
}
