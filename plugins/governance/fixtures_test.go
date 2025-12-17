package governance

import (
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// Test data builders

func buildVirtualKey(id, value, name string, isActive bool) *configstoreTables.TableVirtualKey {
	return &configstoreTables.TableVirtualKey{
		ID:       id,
		Value:    value,
		Name:     name,
		IsActive: isActive,
	}
}

func buildVirtualKeyWithBudget(id, value, name string, budget *configstoreTables.TableBudget) *configstoreTables.TableVirtualKey {
	vk := buildVirtualKey(id, value, name, true)
	vk.Budget = budget
	budgetID := budget.ID
	vk.BudgetID = &budgetID
	return vk
}

func buildVirtualKeyWithRateLimit(id, value, name string, rateLimit *configstoreTables.TableRateLimit) *configstoreTables.TableVirtualKey {
	vk := buildVirtualKey(id, value, name, true)
	vk.RateLimit = rateLimit
	rateLimitID := rateLimit.ID
	vk.RateLimitID = &rateLimitID
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
		team.Budget = budget
		team.BudgetID = &budget.ID
	}
	return team
}

func buildCustomer(id, name string, budget *configstoreTables.TableBudget) *configstoreTables.TableCustomer {
	customer := &configstoreTables.TableCustomer{
		ID:   id,
		Name: name,
	}
	if budget != nil {
		customer.Budget = budget
		customer.BudgetID = &budget.ID
	}
	return customer
}

func buildProviderConfig(provider string, allowedModels []string) configstoreTables.TableVirtualKeyProviderConfig {
	return configstoreTables.TableVirtualKeyProviderConfig{
		Provider:      provider,
		AllowedModels: allowedModels,
		Weight:        bifrost.Ptr(1.0),
		RateLimit:     nil,
		Budget:        nil,
		Keys:          []configstoreTables.TableKey{},
	}
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

func assertDecision(t *testing.T, expected Decision, result *EvaluationResult) {
	t.Helper()
	assert.NotNil(t, result, "EvaluationResult should not be nil")
	assert.Equal(t, expected, result.Decision, "Decision mismatch. Reason: %s", result.Reason)
}

func assertVirtualKeyFound(t *testing.T, result *EvaluationResult) {
	t.Helper()
	assert.NotNil(t, result.VirtualKey, "VirtualKey should be found in result")
}

func assertRateLimitInfo(t *testing.T, result *EvaluationResult) {
	t.Helper()
	assert.NotNil(t, result.RateLimitInfo, "RateLimitInfo should be present in result")
}

func requireNoError(t *testing.T, err error, msg string) {
	t.Helper()
	require.NoError(t, err, msg)
}

func requireError(t *testing.T, err error, msg string) {
	t.Helper()
	require.Error(t, err, msg)
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
		mc.Budget = budget
		mc.BudgetID = &budget.ID
	}
	if rateLimit != nil {
		mc.RateLimit = rateLimit
		mc.RateLimitID = &rateLimit.ID
	}
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
