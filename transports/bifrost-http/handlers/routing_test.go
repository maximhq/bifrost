package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	retryStatus    complexity.SemanticStatusInfo
	retryStarted   bool
	retryErr       error
}

func (m *mockRoutingManager) ValidateComplexityAnalyzerConfig(_ context.Context, _ *complexity.AnalyzerConfig) error {
	return nil
}

func (m *mockRoutingManager) GetComplexitySemanticStatus(_ context.Context) (complexity.SemanticStatusInfo, error) {
	return complexity.SemanticStatusInfo{State: complexity.SemanticStatusDisabled}, nil
}

// RetryComplexitySemanticWarmup records a retry request for routing handler tests.
func (m *mockRoutingManager) RetryComplexitySemanticWarmup(_ context.Context) (complexity.SemanticStatusInfo, bool, error) {
	return m.retryStatus, m.retryStarted, m.retryErr
}

func (m *mockRoutingManager) GetComplexityLLMStatus(_ context.Context) (complexity.LLMStatusInfo, error) {
	return complexity.LLMStatusInfo{State: complexity.LLMStatusDisabled}, nil
}

func (m *mockRoutingManager) ReloadComplexityAnalyzerConfig(_ context.Context, config *complexity.AnalyzerConfig) error {
	m.reloadCalls++
	m.reloadedConfig = config
	return m.reloadErr
}

func (m *mockRoutingManager) ReloadRoutingRule(_ context.Context, _ string) error {
	return nil
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

// TestRetryComplexitySemanticWarmup verifies the retry endpoint accepts only a
// failed classifier retry and returns the new asynchronous warmup state.
func TestRetryComplexitySemanticWarmup(t *testing.T) {
	SetLogger(&mockLogger{})
	t.Run("accepts a failed warmup", func(t *testing.T) {
		handler := &RoutingHandler{
			configStore: setupPricingOverrideHandlerStore(t),
			routingManager: &mockRoutingManager{
				retryStarted: true,
				retryStatus:  complexity.SemanticStatusInfo{State: complexity.SemanticStatusWarming, Total: 3},
			},
		}
		ctx := newTestRequestCtx("")

		handler.retryComplexitySemanticWarmup(ctx)

		require.Equal(t, fasthttp.StatusAccepted, ctx.Response.StatusCode())
		var status complexity.SemanticStatusInfo
		require.NoError(t, json.Unmarshal(ctx.Response.Body(), &status))
		require.Equal(t, complexity.SemanticStatusWarming, status.State)
	})

	t.Run("rejects a non-failed warmup", func(t *testing.T) {
		handler := &RoutingHandler{
			configStore:    setupPricingOverrideHandlerStore(t),
			routingManager: &mockRoutingManager{retryStatus: complexity.SemanticStatusInfo{State: complexity.SemanticStatusReady}},
		}
		ctx := newTestRequestCtx("")

		handler.retryComplexitySemanticWarmup(ctx)

		require.Equal(t, fasthttp.StatusConflict, ctx.Response.StatusCode())
	})
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
	cfg.Semantic = &complexity.SemanticConfig{
		Provider:       "openai",
		EmbeddingModel: "text-embedding-3-small",
		MinSimilarity:  0.65,
	}
	cfg.Session = &complexity.SessionConfig{Enabled: true}

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
	if stored.Semantic == nil || stored.Semantic.MinSimilarity != 0.65 {
		t.Fatalf("expected semantic threshold to persist, got %+v", stored.Semantic)
	}
	if stored.Session == nil || !stored.Session.Enabled {
		t.Fatalf("expected enabled session config to persist, got %+v", stored.Session)
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
	tooManyPhrases := valid
	tooManyPhrases.Semantic = &complexity.SemanticConfig{
		Provider:       "openai",
		EmbeddingModel: "text-embedding-3-small",
	}
	tooManyPhrases.Keywords.SimpleKeywords = make([]string, configstore.MaxComplexitySemanticPhrases-1)
	for index := range tooManyPhrases.Keywords.SimpleKeywords {
		tooManyPhrases.Keywords.SimpleKeywords[index] = fmt.Sprintf("simple-%d", index)
	}
	tooManyPhrases.Keywords.MediumKeywords = []string{"medium"}
	tooManyPhrases.Keywords.ComplexKeywords = []string{"complex"}

	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "unknown field", body: strings.TrimSuffix(validBody, "}") + `,"extra":true}`, want: "Invalid request payload"},
		{name: "multiple json values", body: validBody + `{}`, want: "multiple JSON values"},
		{name: "invalid boundaries", body: testComplexityAnalyzerPayload(t, invalidBoundaries), want: "tier boundaries"},
		{name: "empty keywords", body: testComplexityAnalyzerPayload(t, emptyKeywords), want: "keyword lists must be non-empty"},
		{name: "too many semantic phrases", body: testComplexityAnalyzerPayload(t, tooManyPhrases), want: "contains 751 phrases"},
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

// reloadRecordingRoutingManager accepts rule reloads so the CRUD handlers can run end to end.
type reloadRecordingRoutingManager struct {
	RoutingManager
	reloaded []string
}

func (m *reloadRecordingRoutingManager) ReloadRoutingRule(_ context.Context, id string) error {
	m.reloaded = append(m.reloaded, id)
	return nil
}

// ruleFallbacksJSON returns the "fallbacks" array of a handler response exactly as the client sees it.
func ruleFallbacksJSON(t *testing.T, ctx *fasthttp.RequestCtx) string {
	t.Helper()
	require.Less(t, ctx.Response.StatusCode(), 300, "unexpected status: %s", string(ctx.Response.Body()))
	var resp struct {
		Rule struct {
			ID        string          `json:"id"`
			Fallbacks json.RawMessage `json:"fallbacks"`
		} `json:"rule"`
	}
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &resp))
	if len(resp.Rule.Fallbacks) == 0 {
		return ""
	}
	return string(resp.Rule.Fallbacks)
}

// TestRoutingRuleFallbacksRoundTrip pins the fallbacks a client sends, reads back and edits through
// the routing rule API: both forms survive create, GET and the stored column; a PUT without
// fallbacks keeps them, a PUT with fallbacks replaces them, an empty list clears them, and an
// invalid PUT leaves them untouched.
func TestRoutingRuleFallbacksRoundTrip(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	manager := &reloadRecordingRoutingManager{}
	handler := &RoutingHandler{configStore: store, routingManager: manager}

	created := `["anthropic/claude-sonnet-4","azure/",{"provider":"vertex","model":"gemini-2.5-pro","key_id":"k-1"},{"provider":"groq","model":"llama-3.1-8b-instant"},{"provider":"bedrock","model":"","key_id":"k-2"}]`
	// An unpinned object is normalized to its legacy string; everything else comes back as sent.
	wantCreated := `["anthropic/claude-sonnet-4","azure/",{"provider":"vertex","model":"gemini-2.5-pro","key_id":"k-1"},"groq/llama-3.1-8b-instant",{"provider":"bedrock","model":"","key_id":"k-2"}]`

	createCtx := newTestRequestCtx(`{"name":"fb-roundtrip","cel_expression":"model == \"gpt-4o\"","targets":[{"provider":"openai","weight":1}],"fallbacks":` + created + `}`)
	handler.createRoutingRule(createCtx)
	require.JSONEq(t, wantCreated, ruleFallbacksJSON(t, createCtx))
	var createResp struct {
		Rule struct {
			ID string `json:"id"`
		} `json:"rule"`
	}
	require.NoError(t, json.Unmarshal(createCtx.Response.Body(), &createResp))
	ruleID := createResp.Rule.ID
	require.Equal(t, []string{ruleID}, manager.reloaded)

	get := func() string {
		ctx := newTestRequestCtx("")
		ctx.SetUserValue("rule_id", ruleID)
		handler.getRoutingRule(ctx)
		return ruleFallbacksJSON(t, ctx)
	}
	put := func(body string) *fasthttp.RequestCtx {
		ctx := newTestRequestCtx(body)
		ctx.SetUserValue("rule_id", ruleID)
		handler.updateRoutingRule(ctx)
		return ctx
	}
	storedColumn := func() string {
		rule, err := store.GetRoutingRule(context.Background(), ruleID)
		require.NoError(t, err)
		if rule.Fallbacks == nil {
			return ""
		}
		return *rule.Fallbacks
	}

	require.Equal(t, wantCreated, get(), "GET must return the fallbacks byte-for-byte")
	require.Equal(t, wantCreated, storedColumn(), "the stored column must hold the same bytes")

	t.Run("PUT without fallbacks keeps them", func(t *testing.T) {
		ctx := put(`{"description":"renamed"}`)
		require.Equal(t, wantCreated, ruleFallbacksJSON(t, ctx))
		require.Equal(t, wantCreated, get())
	})

	t.Run("invalid PUT is rejected and leaves them untouched", func(t *testing.T) {
		for _, body := range []string{
			`{"fallbacks":["gpt-4o"]}`,
			`{"fallbacks":[{"key_id":"k-1"}]}`,
			`{"fallbacks":[{"provider":"vertex","provider_key_name":"prod"}]}`,
			`{"fallbacks":[""]}`,
			`{"fallbacks":[null]}`,
		} {
			ctx := put(body)
			require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), "%s: %s", body, string(ctx.Response.Body()))
			require.Equal(t, wantCreated, get(), "%s changed the stored fallbacks", body)
		}
	})

	t.Run("PUT with fallbacks replaces them", func(t *testing.T) {
		replaced := `[{"provider":"anthropic","model":"claude-sonnet-4","key_id":"k-3"},"openai/gpt-4o-mini"]`
		ctx := put(`{"fallbacks":` + replaced + `}`)
		require.Equal(t, replaced, ruleFallbacksJSON(t, ctx))
		require.Equal(t, replaced, get())
		require.Equal(t, replaced, storedColumn())
	})

	t.Run("PUT with an empty list clears them", func(t *testing.T) {
		ctx := put(`{"fallbacks":[]}`)
		require.Equal(t, "", ruleFallbacksJSON(t, ctx))
		require.Equal(t, "", get())
		require.Equal(t, "", storedColumn())
	})
}

// TestCreateRoutingRuleRejectsInvalidFallbacks pins that a create with a bad fallback returns 400
// and stores nothing.
func TestCreateRoutingRuleRejectsInvalidFallbacks(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	manager := &reloadRecordingRoutingManager{}
	handler := &RoutingHandler{configStore: store, routingManager: manager}

	for _, fallbacks := range []string{
		`["gpt-4o"]`,
		`["meta-llama/Llama-3.1-8B"]`,
		`[""]`,
		`[{"key_id":"k-1"}]`,
		`[{"provider":"vertex","provider_key_name":"prod"}]`,
		`[{"provider":"unregistered","model":"claude-sonnet-5","key_id":"k-1"}]`,
		`[{"provider":" ","model":"claude-sonnet-5"}]`,
		`[1]`,
	} {
		t.Run(fallbacks, func(t *testing.T) {
			ctx := newTestRequestCtx(`{"name":"bad-fb","cel_expression":"true","targets":[{"provider":"openai","weight":1}],"fallbacks":` + fallbacks + `}`)
			handler.createRoutingRule(ctx)
			require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		})
	}
	rules, err := store.GetRoutingRules(context.Background())
	require.NoError(t, err)
	require.Empty(t, rules, "a rejected create must not store a rule")
	require.Empty(t, manager.reloaded)
}

// TestRoutingRuleTTFTTimeoutValidation pins ttft_timeout_ms on create and
// update: 1..300000 is stored, 0 means "off" (and clears it on update),
// omitting it on update keeps the stored value, and anything else is a 400.
func TestRoutingRuleTTFTTimeoutValidation(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	handler := &RoutingHandler{configStore: store, routingManager: &mockRoutingManager{}}

	create := func(t *testing.T, name, ttft string) (int, string) {
		t.Helper()
		body := fmt.Sprintf(`{"name":%q,"cel_expression":"true","targets":[{"provider":"openai","model":"gpt-4o-mini","weight":1}],"priority":%d%s}`, name, len(name), ttft)
		ctx := newTestRequestCtx(body)
		handler.createRoutingRule(ctx)
		var resp struct {
			Rule tables.TableRoutingRule `json:"rule"`
		}
		_ = json.Unmarshal(ctx.Response.Body(), &resp)
		return ctx.Response.StatusCode(), resp.Rule.ID
	}
	update := func(t *testing.T, id, body string) int {
		t.Helper()
		ctx := newTestRequestCtx(body)
		ctx.SetUserValue("rule_id", id)
		handler.updateRoutingRule(ctx)
		return ctx.Response.StatusCode()
	}
	stored := func(t *testing.T, id string) *int {
		t.Helper()
		rule, err := store.GetRoutingRule(context.Background(), id)
		require.NoError(t, err)
		return rule.TTFTTimeoutMs
	}

	status, id := create(t, "ttft-set", `,"ttft_timeout_ms":1500`)
	require.Equal(t, fasthttp.StatusOK, status)
	require.NotNil(t, stored(t, id))
	require.Equal(t, 1500, *stored(t, id))

	status, offID := create(t, "ttft-zero", `,"ttft_timeout_ms":0`)
	require.Equal(t, fasthttp.StatusOK, status)
	require.Nil(t, stored(t, offID), "0 must mean no TTFT deadline")

	for _, bad := range []string{`,"ttft_timeout_ms":-1`, `,"ttft_timeout_ms":300001`} {
		status, _ = create(t, "ttft-bad"+bad[len(bad)-2:], bad)
		require.Equal(t, fasthttp.StatusBadRequest, status, "create with %s", bad)
	}

	require.Equal(t, fasthttp.StatusOK, update(t, id, `{"description":"no ttft field"}`))
	require.Equal(t, 1500, *stored(t, id), "an update without the field must keep it")

	require.Equal(t, fasthttp.StatusBadRequest, update(t, id, `{"ttft_timeout_ms":999999}`))
	require.Equal(t, 1500, *stored(t, id), "a rejected update must not change it")

	require.Equal(t, fasthttp.StatusOK, update(t, id, `{"ttft_timeout_ms":250}`))
	require.Equal(t, 250, *stored(t, id))

	require.Equal(t, fasthttp.StatusOK, update(t, id, `{"ttft_timeout_ms":0}`))
	require.Nil(t, stored(t, id), "0 on update must clear the deadline")
}
