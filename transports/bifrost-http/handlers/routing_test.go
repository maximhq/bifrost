package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

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

func (m *mockRoutingManager) ValidateComplexityAnalyzerConfig(_ context.Context, _ *complexity.AnalyzerConfig) error {
	return nil
}

func (m *mockRoutingManager) GetComplexitySemanticStatus(_ context.Context) (complexity.SemanticStatusInfo, error) {
	return complexity.SemanticStatusInfo{State: complexity.SemanticStatusDisabled}, nil
}

func (m *mockRoutingManager) GetComplexityLLMStatus(_ context.Context) (complexity.LLMStatusInfo, error) {
	return complexity.LLMStatusInfo{State: complexity.LLMStatusDisabled}, nil
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
	if len(resp.Keywords.MediumKeywords) == 0 {
		t.Fatalf("expected default medium keywords")
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
	cfg.Keywords.MediumKeywords = []string{" Function ", "api", "API"}

	ctx := newTestRequestCtx(testComplexityAnalyzerPayload(t, cfg))
	handler.updateComplexityAnalyzerConfig(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if manager.reloadCalls != 1 {
		t.Fatalf("expected one reload, got %d", manager.reloadCalls)
	}
	if manager.reloadedConfig == nil || manager.reloadedConfig.TierBoundaries.MediumComplex != 0.34 {
		t.Fatalf("expected reload with normalized config, got %+v", manager.reloadedConfig)
	}

	stored, err := store.GetComplexityAnalyzerConfig(context.Background())
	if err != nil {
		t.Fatalf("get stored config: %v", err)
	}
	if stored == nil || len(stored.Keywords.MediumKeywords) != 2 {
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
	emptyKeywords.Keywords.MediumKeywords = nil

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
	custom.TierBoundaries.MediumComplex = 0.55
	custom.Keywords.MediumKeywords = []string{"summarize this document"}
	// Seeded because reset must not touch it: the embedding block is deployment
	// configuration, and losing it takes the classifier down rather than
	// restoring phrases. Without it here the endpoint could wipe the block and
	// this test would still pass.
	custom.Semantic = &complexity.SemanticConfig{
		Provider:       "openai",
		EmbeddingModel: "text-embedding-3-small",
		MinSimilarity:  0.42,
		VectorStore:    "vector_store",
	}
	// The llm fallback block is deployment configuration for the same reason:
	// its prompt is the operator's own text, so reset must restore the shipped
	// phrase lists, not a prompt someone wrote.
	custom.LLM = &complexity.LLMConfig{
		Provider:            "openai",
		Model:               "gpt-4.1-mini",
		Timeout:             3 * time.Second,
		Prompt:              "route legal work to COMPLEX",
		MessageHistoryCount: 4,
		CountTowardBudgets:  true,
	}
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
	defaultMedium := complexity.DefaultAnalyzerConfig().Keywords.MediumKeywords
	if len(stored.Keywords.MediumKeywords) != len(defaultMedium) {
		t.Fatalf("expected default medium keywords, got %+v", stored.Keywords.MediumKeywords)
	}
	if stored.Semantic == nil {
		t.Fatalf("expected the embedding config to survive reset, got %+v", stored)
	}
	if stored.Semantic.Provider != "openai" || stored.Semantic.EmbeddingModel != "text-embedding-3-small" {
		t.Fatalf("expected the embedding provider and model to survive reset, got %+v", stored.Semantic)
	}
	if stored.Semantic.VectorStore != "vector_store" || stored.Semantic.MinSimilarity != 0.42 {
		t.Fatalf("expected the storage selection and similarity floor to survive reset, got %+v", stored.Semantic)
	}
	if stored.LLM == nil {
		t.Fatalf("expected the llm fallback config to survive reset, got %+v", stored)
	}
	if stored.LLM.Provider != "openai" || stored.LLM.Model != "gpt-4.1-mini" || stored.LLM.Timeout != 3*time.Second {
		t.Fatalf("expected the llm provider, model, and timeout to survive reset, got %+v", stored.LLM)
	}
	if stored.LLM.Prompt != "route legal work to COMPLEX" || stored.LLM.MessageHistoryCount != 4 {
		t.Fatalf("expected the operator's classifier prompt and history window to survive reset, got %+v", stored.LLM)
	}
	// Asserted separately because its zero value is a legal setting: a dropped
	// flag reads as a deliberate "don't bill classifications", not as loss.
	if !stored.LLM.CountTowardBudgets {
		t.Fatalf("expected the llm budget attribution flag to survive reset, got %+v", stored.LLM)
	}

	// The reload and the response body carry the same record: the plugin
	// reconfigures from one and the configuration UI reseeds its form from the
	// other, so an embedding block missing from either reads as "unconfigured"
	// until the next restart or refetch.
	if manager.reloadedConfig == nil || manager.reloadedConfig.Semantic == nil || manager.reloadedConfig.LLM == nil {
		t.Fatalf("expected reload with the embedding and llm config retained, got %+v", manager.reloadedConfig)
	}
	var resp complexity.AnalyzerConfig
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Semantic == nil || resp.Semantic.EmbeddingModel != "text-embedding-3-small" {
		t.Fatalf("expected the response to carry the embedding config, got %+v", resp.Semantic)
	}
	if resp.LLM == nil || resp.LLM.Model != "gpt-4.1-mini" {
		t.Fatalf("expected the response to carry the llm fallback config, got %+v", resp.LLM)
	}
	if resp.TierBoundaries != complexity.DefaultTierBoundaries() {
		t.Fatalf("expected the response to carry default boundaries, got %+v", resp.TierBoundaries)
	}
}

// TestComplexityAnalyzerConfigResetReportsReloadFailure pins what a failed in-memory reload
// leaves behind. The reset is already committed at that point and is deliberately not rolled
// back — matching the update handler, and because a compensating write can fail the same way
// the first one did. What the operator gets instead is the persisted state plus a message
// naming the one action that reconciles the two, so the contract is worth holding still.
func TestComplexityAnalyzerConfigResetReportsReloadFailure(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	manager := &mockRoutingManager{reloadErr: errors.New("plugin is not wired")}
	handler := &RoutingHandler{
		configStore:    store,
		routingManager: manager,
	}

	custom := complexity.DefaultAnalyzerConfig()
	custom.TierBoundaries.MediumComplex = 0.55
	custom.Semantic = &complexity.SemanticConfig{
		Provider:       "openai",
		EmbeddingModel: "text-embedding-3-small",
	}
	if err := store.UpdateComplexityAnalyzerConfig(context.Background(), &custom); err != nil {
		t.Fatalf("seed custom config: %v", err)
	}

	ctx := newTestRequestCtx("")
	handler.resetComplexityAnalyzerConfig(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if !strings.Contains(string(ctx.Response.Body()), "restart bifrost") {
		t.Fatalf("expected the response to name the reconciling action, got %s", string(ctx.Response.Body()))
	}

	// The write landed before the reload was attempted, so the stored record is the reset one
	// and the embedding block it preserves is still intact.
	stored, err := store.GetComplexityAnalyzerConfig(context.Background())
	if err != nil {
		t.Fatalf("get stored config: %v", err)
	}
	if stored == nil || stored.TierBoundaries != complexity.DefaultTierBoundaries() {
		t.Fatalf("expected the reset to stay persisted, got %+v", stored)
	}
	if stored.Semantic == nil || stored.Semantic.EmbeddingModel != "text-embedding-3-small" {
		t.Fatalf("expected the embedding config to survive a failed reload, got %+v", stored.Semantic)
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
