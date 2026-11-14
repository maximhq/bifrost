package rules

import (
	"context"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/governance"
)

// MockLogger is a no-op logger that records what it was asked to log.
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
	ml.mu.Lock()
	defer ml.mu.Unlock()
	ml.logs = append(ml.logs, msg)
	return schemas.NoopLogEvent
}

// MockGovernanceStore serves canned virtual keys and usage status in place of the real
// governance store.
type MockGovernanceStore struct {
	VirtualKeys map[string]*configstoreTables.TableVirtualKey
	// Status is returned for every provider/model pair. A nil Status stands for a request
	// with no budgets or rate limits configured.
	Status *governance.BudgetAndRateLimitStatus
}

func NewMockGovernanceStore() *MockGovernanceStore {
	return &MockGovernanceStore{VirtualKeys: map[string]*configstoreTables.TableVirtualKey{}}
}

func (m *MockGovernanceStore) GetBudgetAndRateLimitStatus(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, budgetBaselines map[string]float64, tokenBaselines map[string]int64, requestBaselines map[string]int64) *governance.BudgetAndRateLimitStatus {
	return m.Status
}

// newTestRuleStore builds an empty rule cache backed by no config store; tests add rules
// through UpsertRule.
func newTestRuleStore() (*LocalStore, error) {
	return NewLocalStore(context.Background(), NewMockLogger(), nil)
}

// newTestEngine builds a rule store and an engine over it, returning both so tests can add
// rules and then evaluate them.
func newTestEngine(chainMaxDepth int) (*LocalStore, *Engine, error) {
	rules, err := newTestRuleStore()
	if err != nil {
		return nil, nil, err
	}
	engine, err := NewEngine(rules, NewMockGovernanceStore(), NewMockLogger(), &chainMaxDepth)
	if err != nil {
		return nil, nil, err
	}
	return rules, engine, nil
}
