package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fasthttp/router"

	"github.com/maximhq/bifrost/framework/configstore"
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

// assertSessionSurvivedReset checks every field of the session block seeded by
// TestComplexityAnalyzerConfigResetPersistsDefaultsAndReloads. Reset must carry
// the block through whole: a field silently dropped here is a session control
// that reverts on the next reset, and asserting only some of them lets the rest
// regress unnoticed.
func assertSessionSurvivedReset(t *testing.T, where string, session *configstore.ComplexitySessionConfig) {
	t.Helper()
	if session == nil {
		t.Fatalf("expected the %s session config to survive reset, got nil", where)
	}
	if session.Mode != configstore.ComplexitySessionModePinned || session.TTL != 90*time.Minute {
		t.Fatalf("expected the %s session mode and ttl to survive reset, got %+v", where, session)
	}
	if len(session.IdentitySources) != 1 || session.IdentitySources[0] != configstore.ComplexitySessionIdentityHeader {
		t.Fatalf("expected the %s session identity ladder to survive reset, got %+v", where, session.IdentitySources)
	}
	if session.SwitchMinSimilarity != 0.85 || session.MaxSwitchesPerSession != 3 || !session.AlwaysAllowEscalation {
		t.Fatalf("expected the %s session switch gates to survive reset, got %+v", where, session)
	}
	if session.DowngradeAfterNTurns != 5 {
		t.Fatalf("expected the %s session switch thresholds to survive reset, got %+v", where, session)
	}
}

// assertRawSessionTTL pins the wire encoding of session.ttl, which typed
// decoding cannot: the decoder accepts both a duration string and a
// millisecond number, so a change of encoding would round-trip silently here
// while breaking the configuration UI that reads the raw field.
func assertRawSessionTTL(t *testing.T, body []byte, want any) {
	t.Helper()
	var raw struct {
		Session struct {
			TTL any `json:"ttl"`
		} `json:"session"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal raw response: %v", err)
	}
	if raw.Session.TTL != want {
		t.Fatalf("expected raw session ttl %v, got %#v", want, raw.Session.TTL)
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
	// Session behavior is deployment configuration for the same reason: losing it
	// silently unpins every live conversation. Seeded away from the defaults so a
	// wipe cannot be mistaken for a value that happens to match.
	custom.Session = &configstore.ComplexitySessionConfig{
		Mode:                  configstore.ComplexitySessionModePinned,
		TTL:                   90 * time.Minute,
		IdentitySources:       []string{configstore.ComplexitySessionIdentityHeader},
		SwitchMinSimilarity:   0.85,
		DowngradeAfterNTurns:  5,
		MaxSwitchesPerSession: 3,
		AlwaysAllowEscalation: true,
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
	assertSessionSurvivedReset(t, "stored", stored.Session)

	// The reload and the response body carry the same record: the plugin
	// reconfigures from one and the configuration UI reseeds its form from the
	// other, so an embedding block missing from either reads as "unconfigured"
	// until the next restart or refetch.
	if manager.reloadedConfig == nil || manager.reloadedConfig.Semantic == nil {
		t.Fatalf("expected reload with the embedding config retained, got %+v", manager.reloadedConfig)
	}
	assertSessionSurvivedReset(t, "reloaded", manager.reloadedConfig.Session)

	var resp complexity.AnalyzerConfig
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Semantic == nil || resp.Semantic.EmbeddingModel != "text-embedding-3-small" {
		t.Fatalf("expected the response to carry the embedding config, got %+v", resp.Semantic)
	}
	assertSessionSurvivedReset(t, "response", resp.Session)
	// The TTL leaves as a Go duration string, which is what the configuration UI
	// parses back into its milliseconds field. Decoding also accepts a plain
	// number, so only the raw body pins which of the two is actually emitted.
	assertRawSessionTTL(t, ctx.Response.Body(), "1h30m0s")
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
