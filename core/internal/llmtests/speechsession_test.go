package llmtests

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// RunAllComprehensiveTests runs every scenario on its own test, so a scenario that skips its caller skips the rest.
func TestRunSpeechSessionTestWithoutAModelLeavesTheCallerRunning(t *testing.T) {
	reached := false
	t.Run("caller", func(t *testing.T) {
		RunSpeechSessionTest(t, nil, context.Background(), ComprehensiveTestConfig{Scenarios: TestScenarios{SpeechSession: true}})
		reached = true
	})
	assert.True(t, reached, "the scenario skipped the test that called it, and every scenario after it")
}
