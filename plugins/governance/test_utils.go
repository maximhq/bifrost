package governance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grants"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/stretchr/testify/assert"
)

// MockLogger implements schemas.Logger for testing
type MockLogger struct {
	mu       sync.Mutex
	logs     []string
	errors   []string
	debugs   []string
	infos    []string
	warnings []string
}

func NewMockLogger() *MockLogger {
	return &MockLogger{
		logs:     make([]string, 0),
		errors:   make([]string, 0),
		debugs:   make([]string, 0),
		infos:    make([]string, 0),
		warnings: make([]string, 0),
	}
}

func (ml *MockLogger) SetLevel(level schemas.LogLevel) {}

func (ml *MockLogger) SetOutputType(outputType schemas.LoggerOutputType) {}

func (ml *MockLogger) Error(format string, args ...interface{}) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	ml.errors = append(ml.errors, format)
}

func (ml *MockLogger) Warn(format string, args ...interface{}) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	ml.warnings = append(ml.warnings, format)
}

func (ml *MockLogger) Info(format string, args ...interface{}) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	ml.infos = append(ml.infos, format)
}

func (ml *MockLogger) Debug(format string, args ...interface{}) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	ml.debugs = append(ml.debugs, format)
}

func (ml *MockLogger) Fatal(format string, args ...interface{}) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	ml.errors = append(ml.errors, format)
}

func (ml *MockLogger) LogHTTPRequest(level schemas.LogLevel, msg string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// Test data builders

func buildVirtualKey(id, value, name string, isActive bool) *configstoreTables.TableVirtualKey {
	return &configstoreTables.TableVirtualKey{
		ID:       id,
		Value:    *schemas.NewSecretVar(value),
		Name:     name,
		IsActive: &isActive,
	}
}

func buildVirtualKeyWithBudget(id, value, name string, budget *configstoreTables.TableBudget) *configstoreTables.TableVirtualKey {
	vk := buildVirtualKey(id, value, name, true)
	vkID := id
	budget.VirtualKeyID = &vkID
	vk.Budgets = []configstoreTables.TableBudget{*budget}
	// Add a default provider config so the resolver doesn't block at provider check
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}
	return vk
}

func buildVirtualKeyWithRateLimit(id, value, name string, rateLimit *configstoreTables.TableRateLimit) *configstoreTables.TableVirtualKey {
	vk := buildVirtualKey(id, value, name, true)
	vk.RateLimit = rateLimit
	rateLimitID := rateLimit.ID
	vk.RateLimitID = &rateLimitID
	// Add a default provider config so the resolver doesn't block at provider check
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}
	return vk
}

func buildVirtualKeyWithProviders(id, value, name string, providers []configstoreTables.TableVirtualKeyProviderConfig) *configstoreTables.TableVirtualKey {
	vk := buildVirtualKey(id, value, name, true)
	vk.ProviderConfigs = providers
	return vk
}

func buildBudget(id string, maxLimit float64, resetDuration string) *configstoreTables.TableBudget {
	return &configstoreTables.TableBudget{
		ID:            id,
		MaxLimit:      maxLimit,
		CurrentUsage:  0,
		ResetDuration: resetDuration,
		LastReset:     time.Now(),
	}
}

func buildBudgetWithUsage(id string, maxLimit, currentUsage float64, resetDuration string) *configstoreTables.TableBudget {
	return &configstoreTables.TableBudget{
		ID:            id,
		MaxLimit:      maxLimit,
		CurrentUsage:  currentUsage,
		ResetDuration: resetDuration,
		LastReset:     time.Now(),
	}
}

func buildRateLimit(id string, tokenMaxLimit, requestMaxLimit int64) *configstoreTables.TableRateLimit {
	duration := "1m"
	return &configstoreTables.TableRateLimit{
		ID:                   id,
		TokenMaxLimit:        &tokenMaxLimit,
		TokenCurrentUsage:    0,
		TokenResetDuration:   &duration,
		TokenLastReset:       time.Now(),
		RequestMaxLimit:      &requestMaxLimit,
		RequestCurrentUsage:  0,
		RequestResetDuration: &duration,
		RequestLastReset:     time.Now(),
	}
}

func buildRateLimitWithUsage(id string, tokenMaxLimit, tokenUsage, requestMaxLimit, requestUsage int64) *configstoreTables.TableRateLimit {
	duration := "1m"
	return &configstoreTables.TableRateLimit{
		ID:                   id,
		TokenMaxLimit:        &tokenMaxLimit,
		TokenCurrentUsage:    tokenUsage,
		TokenResetDuration:   &duration,
		TokenLastReset:       time.Now(),
		RequestMaxLimit:      &requestMaxLimit,
		RequestCurrentUsage:  requestUsage,
		RequestResetDuration: &duration,
		RequestLastReset:     time.Now(),
	}
}

func buildTeam(id, name string, budget *configstoreTables.TableBudget) *configstoreTables.TableTeam {
	team := &configstoreTables.TableTeam{
		ID:   id,
		Name: name,
	}
	if budget != nil {
		budget.TeamID = &team.ID
		team.Budgets = []configstoreTables.TableBudget{*budget}
	}
	return team
}

func buildCustomer(id, name string, budget *configstoreTables.TableBudget) *configstoreTables.TableCustomer {
	customer := &configstoreTables.TableCustomer{
		ID:   id,
		Name: name,
	}
	if budget != nil {
		budget.CustomerID = &customer.ID
		customer.Budgets = []configstoreTables.TableBudget{*budget}
	}
	return customer
}

func buildProviderConfig(provider string, allowedModels []string) configstoreTables.TableVirtualKeyProviderConfig {
	return configstoreTables.TableVirtualKeyProviderConfig{
		Provider:      provider,
		AllowedModels: allowedModels,
		Weight:        bifrost.Ptr(1.0),
		RateLimit:     nil,
		Keys:          []configstoreTables.TableKey{},
	}
}

func buildProviderConfigWithBudgets(provider string, allowedModels []string, budgets []configstoreTables.TableBudget) configstoreTables.TableVirtualKeyProviderConfig {
	pc := buildProviderConfig(provider, allowedModels)
	pc.Budgets = budgets
	return pc
}

func buildVirtualKeyWithMultiBudgets(id, value, name string, budgets []configstoreTables.TableBudget) *configstoreTables.TableVirtualKey {
	vk := buildVirtualKey(id, value, name, true)
	for i := range budgets {
		vkID := id
		budgets[i].VirtualKeyID = &vkID
	}
	vk.Budgets = budgets
	return vk
}

func buildProviderConfigWithRateLimit(provider string, allowedModels []string, rateLimit *configstoreTables.TableRateLimit) configstoreTables.TableVirtualKeyProviderConfig {
	pc := buildProviderConfig(provider, allowedModels)
	pc.RateLimit = rateLimit
	if rateLimit != nil {
		pc.RateLimitID = &rateLimit.ID
	}
	return pc
}

// Test helpers

// resolverCtx returns a context carrying a virtual key and the access resolved for it — the
// state the request path has already established by the time evaluation runs, since evaluation
// reads what a request may reach rather than working it out.
func resolverCtx(store GovernanceStore, virtualKeyValue string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, virtualKeyValue)
	if store != nil {
		base, scoping, mode := store.GetGrantSet(ctx)
		if base != nil || scoping != nil {
			grants.RecordEffectiveAccess(ctx, grants.NewEffectiveAccess(base, scoping, mode, nil, nil))
		}
	}
	return ctx
}

func assertDecision(t *testing.T, expected Decision, result *EvaluationResult) {
	t.Helper()
	assert.NotNil(t, result, "EvaluationResult should not be nil")
	assert.Equal(t, expected, result.Decision, "Decision mismatch. Reason: %s", result.Reason)
}

func buildModelConfig(id, modelName string, provider *string, budget *configstoreTables.TableBudget, rateLimit *configstoreTables.TableRateLimit) *configstoreTables.TableModelConfig {
	mc := &configstoreTables.TableModelConfig{
		ID:        id,
		ModelName: modelName,
		Provider:  provider,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if budget != nil {
		// Model configs now own budgets via TableBudget.ModelConfigID (multi-budget).
		budget.ModelConfigID = &mc.ID
		mc.Budgets = []configstoreTables.TableBudget{*budget}
	}
	if rateLimit != nil {
		mc.RateLimit = rateLimit
		mc.RateLimitID = &rateLimit.ID
	}
	return mc
}

// buildVKScopedModelConfig builds a model config scoped to a specific virtual key.
func buildVKScopedModelConfig(id, modelName string, provider *string, vkID string, budget *configstoreTables.TableBudget, rateLimit *configstoreTables.TableRateLimit) *configstoreTables.TableModelConfig {
	mc := buildModelConfig(id, modelName, provider, budget, rateLimit)
	mc.Scope = configstoreTables.ModelConfigScopeVirtualKey
	mc.ScopeID = &vkID
	return mc
}

func buildProviderWithGovernance(name string, budget *configstoreTables.TableBudget, rateLimit *configstoreTables.TableRateLimit) *configstoreTables.TableProvider {
	provider := &configstoreTables.TableProvider{
		Name:      name,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if budget != nil {
		provider.Budget = budget
		provider.BudgetID = &budget.ID
	}
	if rateLimit != nil {
		provider.RateLimit = rateLimit
		provider.RateLimitID = &rateLimit.ID
	}
	return provider
}

func boolPtr(b bool) *bool {
	return &b
}

// Datasheet is fetched once per test binary run via sync.Once.
var (
	datasheetOnce      sync.Once
	datasheetBaseIndex map[string]string
	datasheetErr       error
)

// fetchDatasheetBaseIndex downloads the default datasheet and builds a
// model → base_model index, mirroring ModelCatalog.populateModelPoolFromPricingData.
func fetchDatasheetBaseIndex() {
	client := &http.Client{Timeout: modelcatalog.DefaultPricingTimeout}
	resp, err := client.Get(modelcatalog.DefaultPricingURL)
	if err != nil {
		datasheetErr = err
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		datasheetErr = fmt.Errorf("datasheet HTTP %d", resp.StatusCode)
		return
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		datasheetErr = err
		return
	}

	var entries map[string]modelcatalog.PricingEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		datasheetErr = err
		return
	}

	index := make(map[string]string, len(entries))
	for modelKey, entry := range entries {
		if entry.BaseModel == "" {
			continue
		}
		// Strip provider prefix (same as convertPricingDataToTableModelPricing)
		modelName := modelKey
		if strings.Contains(modelKey, "/") {
			parts := strings.Split(modelKey, "/")
			if len(parts) > 1 {
				modelName = strings.Join(parts[1:], "/")
			}
		}
		index[modelName] = entry.BaseModel
	}

	datasheetBaseIndex = index
}

// newTestModelCatalog creates a test ModelCatalog using the fetched datasheet base model index.
// This provides proper nil-pointer semantics (unlike an interface wrapper).
func newTestModelCatalog(t *testing.T) *modelcatalog.ModelCatalog {
	t.Helper()
	datasheetOnce.Do(fetchDatasheetBaseIndex)
	if datasheetErr != nil {
		t.Skipf("skipping: failed to fetch datasheet for test model catalog: %v", datasheetErr)
	}
	return modelcatalog.NewTestCatalog(datasheetBaseIndex)
}

// evaluateGrantedRequest runs a request through the two steps the plugin's evaluation funnel runs it
// through once its access is resolved — what the request may reach, then what it can afford — so a
// test can assert the verdict end to end. It takes the access rather than a key: the funnel composes
// the same two steps for every request, keyed or not.
func evaluateGrantedRequest(r *BudgetResolver, ctx *schemas.BifrostContext, access *grants.EffectiveAccess, provider schemas.ModelProvider, model string, requestType schemas.RequestType) *EvaluationResult {
	evaluationRequest := &EvaluationRequest{
		RequestType: requestType,
		Provider:    provider,
		Model:       model,
	}
	if result := r.evaluateAccess(ctx, evaluationRequest, access); result.Decision != DecisionAllow {
		return result
	}
	// Evaluate settles the limits onto the access before any check runs; a test reaching the resolver
	// directly has to do the same, or it checks an access nothing has been resolved for.
	return r.evaluateLimits(ctx, evaluationRequest, resolveLimits(ctx, r.store, provider, model))
}

// evaluateDeploymentLimits runs the limits that apply to every request regardless of what granted
// it — the deployment's provider and model-config limits — through the single check the funnel uses.
// It stands in for the per-holder entry points those limits used to have.
func evaluateDeploymentLimits(r *BudgetResolver, ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string) *EvaluationResult {
	return r.evaluateLimits(ctx, &EvaluationRequest{Provider: provider, Model: model}, nil)
}

// resolveLimitsForTest settles the limits onto whatever access ctx carries, as Evaluate does, and
// hands it back for a test that then calls the resolver itself.
func resolveLimitsForTest(r *BudgetResolver, ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string) *grants.EffectiveAccess {
	return resolveLimits(ctx, r.store, provider, model)
}

// evaluateHolderLimits runs the limits a grant carries, plus the deployment's, through that same
// check — what the funnel does once a request's access is resolved.
func evaluateHolderLimits(r *BudgetResolver, ctx *schemas.BifrostContext, access *grants.EffectiveAccess, provider schemas.ModelProvider, model string) *EvaluationResult {
	return r.evaluateLimits(ctx, &EvaluationRequest{Provider: provider, Model: model},
		resolveLimitsForTest(r, ctx, provider, model))
}

// evaluateVirtualKey runs a request made with a virtual key through the funnel's two post-resolution
// steps. The key is named only to say what kind of request this is — the evaluation reads the access
// already recorded on ctx by resolverCtx, exactly as production code does, and never looks the key up.
func evaluateVirtualKey(r *BudgetResolver, ctx *schemas.BifrostContext, virtualKeyValue string, provider schemas.ModelProvider, model string, requestType schemas.RequestType, skipRateLimitsAndBudgets bool, skipProviderCheck bool) *EvaluationResult {
	if skipRateLimitsAndBudgets {
		ctx.SetValue(schemas.BifrostContextKeySkipBudgetAndRateLimits, true)
	}
	if skipProviderCheck {
		ctx.SetValue(schemas.BifrostContextKeySkipProviderCheck, true)
	}
	return evaluateGrantedRequest(r, ctx, grants.EffectiveAccessFromContext(ctx), provider, model, requestType)
}

// emptyCtx is a request context with nothing on it, for tests that build a grant directly and do not
// care about the per-request state grant assembly reads.
func emptyCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

// The helpers below stand in for the per-holder check methods the store used to expose. Each
// assembles the limits that holder contributes and runs them through the one check, so a test still
// exercises exactly what it did before — the assembly is now the caller's half, and the evaluation
// is shared.

// checkDeploymentBudgets checks the limits that apply to every request regardless of what granted
// it: the provider's own and the global model configs that cover the pair.
func checkDeploymentBudgets(gs *LocalGovernanceStore, ctx context.Context, provider schemas.ModelProvider, model string, baselines map[string]float64) (Decision, error) {
	budgets, _ := gs.ProviderAndModelLimits(ctx, nil, provider, model)
	return gs.CheckBudgets(ctx, budgets, baselines)
}

// checkDeploymentRateLimits is checkDeploymentBudgets for rate limits.
func checkDeploymentRateLimits(gs *LocalGovernanceStore, ctx context.Context, provider schemas.ModelProvider, model string, tokens, requests map[string]int64) (Decision, error) {
	_, rateLimits := gs.ProviderAndModelLimits(ctx, nil, provider, model)
	return gs.CheckRateLimits(ctx, rateLimits, tokens, requests)
}

// checkScopedBudgets checks the model configs a named holder set for its own traffic, which is what
// a grant of that identity resolves to.
func checkScopedBudgets(gs *LocalGovernanceStore, ctx context.Context, grantType grants.GrantType, scopeID string, provider schemas.ModelProvider, model string, baselines map[string]float64) (Decision, error) {
	budgets, _ := gs.ProviderAndModelLimits(ctx, &grants.Grant{Type: grantType, ID: scopeID, IsActive: true}, provider, model)
	return gs.CheckBudgets(ctx, budgets, baselines)
}

// checkScopedRateLimits is checkScopedBudgets for rate limits.
func checkScopedRateLimits(gs *LocalGovernanceStore, ctx context.Context, grantType grants.GrantType, scopeID string, provider schemas.ModelProvider, model string, tokens, requests map[string]int64) (Decision, error) {
	_, rateLimits := gs.ProviderAndModelLimits(ctx, &grants.Grant{Type: grantType, ID: scopeID, IsActive: true}, provider, model)
	return gs.CheckRateLimits(ctx, rateLimits, tokens, requests)
}

// checkGrantBudgets checks what a key's grant is funded by — the key's own, its provider configs' for
// this provider, and the team and customer it belongs to. Both halves, since the grant carries only
// what is per-provider and the rest comes from the store.
func checkGrantBudgets(gs *LocalGovernanceStore, bifrostCtx *schemas.BifrostContext, vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider, model string, baselines map[string]float64) (Decision, error) {
	grant := gs.grantForVirtualKey(bifrostCtx, vk)
	heldBudgets, _ := gs.HolderLimits(bifrostCtx, grant)
	return gs.CheckBudgets(bifrostCtx, append(heldBudgets, grant.BudgetsFor(string(provider))...), baselines)
}
