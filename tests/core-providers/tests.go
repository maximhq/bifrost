package tests

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"
	"github.com/maximhq/bifrost/tests/core-providers/scenarios"

	bifrost "github.com/maximhq/bifrost/core"
)

// TestScenarioFunc defines the function signature for test scenario functions
type TestScenarioFunc func(*testing.T, *bifrost.Bifrost, context.Context, config.ComprehensiveTestConfig)

// runAllComprehensiveTests executes all comprehensive test scenarios for a given configuration
func runAllComprehensiveTests(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if testConfig.SkipReason != "" {
		t.Skipf("Skipping %s: %s", testConfig.Provider, testConfig.SkipReason)
		return
	}

	t.Logf("🚀 Running comprehensive tests for provider: %s", testConfig.Provider)

	// Define all test scenario functions in a slice
	testScenarios := []TestScenarioFunc{
		scenarios.RunTextCompletionTest,
		scenarios.RunTextCompletionStreamTest,
		scenarios.RunSimpleChatTest,
		scenarios.RunChatCompletionStreamTest,
		scenarios.RunResponsesStreamTest,
		scenarios.RunMultiTurnConversationTest,
		scenarios.RunToolCallsTest,
		scenarios.RunMultipleToolCallsTest,
		scenarios.RunEnd2EndToolCallingTest,
		scenarios.RunAutomaticFunctionCallingTest,
		scenarios.RunImageURLTest,
		scenarios.RunImageBase64Test,
		scenarios.RunMultipleImagesTest,
		scenarios.RunCompleteEnd2EndTest,
		scenarios.RunSpeechSynthesisTest,
		scenarios.RunSpeechSynthesisAdvancedTest,
		scenarios.RunSpeechSynthesisStreamTest,
		scenarios.RunSpeechSynthesisStreamAdvancedTest,
		scenarios.RunTranscriptionTest,
		scenarios.RunTranscriptionAdvancedTest,
		scenarios.RunTranscriptionStreamTest,
		scenarios.RunTranscriptionStreamAdvancedTest,
		scenarios.RunEmbeddingTest,
		scenarios.RunReasoningTest,
		scenarios.RunListModelsTest,
		scenarios.RunListModelsPaginationTest,
	}

	// Execute all test scenarios
	for _, scenarioFunc := range testScenarios {
		scenarioFunc(t, client, ctx, testConfig)
	}

	// Print comprehensive summary based on configuration
	printTestSummary(t, testConfig)
}

// printTestSummary prints a detailed summary of all test scenarios
func printTestSummary(t *testing.T, testConfig config.ComprehensiveTestConfig) {
	testScenarios := []struct {
		name      string
		supported bool
	}{
		{"TextCompletion", testConfig.Scenarios.TextCompletion && testConfig.TextModel != ""},
		{"SimpleChat", testConfig.Scenarios.SimpleChat},
		{"CompletionStream", testConfig.Scenarios.CompletionStream},
		{"MultiTurnConversation", testConfig.Scenarios.MultiTurnConversation},
		{"ToolCalls", testConfig.Scenarios.ToolCalls},
		{"MultipleToolCalls", testConfig.Scenarios.MultipleToolCalls},
		{"End2EndToolCalling", testConfig.Scenarios.End2EndToolCalling},
		{"AutomaticFunctionCall", testConfig.Scenarios.AutomaticFunctionCall},
		{"ImageURL", testConfig.Scenarios.ImageURL},
		{"ImageBase64", testConfig.Scenarios.ImageBase64},
		{"MultipleImages", testConfig.Scenarios.MultipleImages},
		{"CompleteEnd2End", testConfig.Scenarios.CompleteEnd2End},
		{"SpeechSynthesis", testConfig.Scenarios.SpeechSynthesis},
		{"SpeechSynthesisStream", testConfig.Scenarios.SpeechSynthesisStream},
		{"Transcription", testConfig.Scenarios.Transcription},
		{"TranscriptionStream", testConfig.Scenarios.TranscriptionStream},
		{"Embedding", testConfig.Scenarios.Embedding && testConfig.EmbeddingModel != ""},
		{"Reasoning", testConfig.Scenarios.Reasoning && testConfig.ReasoningModel != ""},
		{"ListModels", testConfig.Scenarios.ListModels},
	}

	supported := 0
	unsupported := 0

	t.Logf("\n%s", strings.Repeat("=", 80))
	t.Logf("COMPREHENSIVE TEST SUMMARY FOR PROVIDER: %s", strings.ToUpper(string(testConfig.Provider)))
	t.Logf("%s", strings.Repeat("=", 80))

	for _, scenario := range testScenarios {
		if scenario.supported {
			supported++
			t.Logf("✅ SUPPORTED:   %-25s ✅ Configured to run", scenario.name)
		} else {
			unsupported++
			t.Logf("❌ UNSUPPORTED: %-25s ❌ Not supported by provider", scenario.name)
		}
	}

	t.Logf("%s", strings.Repeat("-", 80))
	t.Logf("CONFIGURATION SUMMARY:")
	t.Logf("  ✅ Supported Tests:   %d", supported)
	t.Logf("  ❌ Unsupported Tests: %d", unsupported)
	t.Logf("  📊 Total Test Types:  %d", len(testScenarios))
	t.Logf("")
	t.Logf("ℹ️  NOTE: Actual PASS/FAIL results are shown in the individual test output above.")
	t.Logf("ℹ️  Look for individual test results like 'PASS: TestOpenAI/SimpleChat' or 'FAIL: TestOpenAI/ToolCalls'")
	t.Logf("%s\n", strings.Repeat("=", 80))
}
