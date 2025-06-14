package main

import (
	"context"
	"strings"
	"testing"

	"core-providers-test/config"
	scenarios "core-providers-test/scenarios"

	bifrost "github.com/maximhq/bifrost/core"
)

// runAllComprehensiveTests executes all comprehensive test scenarios for a given configuration
func runAllComprehensiveTests(t *testing.T, client *bifrost.Bifrost, ctx context.Context, config config.ComprehensiveTestConfig) {
	if config.SkipReason != "" {
		t.Skipf("Skipping %s: %s", config.Provider, config.SkipReason)
		return
	}

	t.Logf("🚀 Running comprehensive tests for provider: %s", config.Provider)

	// Run all test scenarios
	scenarios.RunTextCompletionTest(t, client, ctx, config)
	scenarios.RunSimpleChatTest(t, client, ctx, config)
	scenarios.RunMultiTurnConversationTest(t, client, ctx, config)
	scenarios.RunToolCallsTest(t, client, ctx, config)
	scenarios.RunMultipleToolCallsTest(t, client, ctx, config)
	scenarios.RunEnd2EndToolCallingTest(t, client, ctx, config)
	scenarios.RunAutomaticFunctionCallingTest(t, client, ctx, config)
	scenarios.RunImageURLTest(t, client, ctx, config)
	scenarios.RunImageBase64Test(t, client, ctx, config)
	scenarios.RunMultipleImagesTest(t, client, ctx, config)
	scenarios.RunCompleteEnd2EndTest(t, client, ctx, config)
	scenarios.RunProviderSpecificTest(t, client, ctx, config)

	// Print comprehensive summary based on configuration
	printTestSummary(t, config)
}

// printTestSummary prints a detailed summary of all test scenarios
func printTestSummary(t *testing.T, config config.ComprehensiveTestConfig) {
	testScenarios := []struct {
		name      string
		supported bool
	}{
		{"TextCompletion", config.Scenarios.TextCompletion && config.TextModel != ""},
		{"SimpleChat", config.Scenarios.SimpleChat},
		{"MultiTurnConversation", config.Scenarios.MultiTurnConversation},
		{"ToolCalls", config.Scenarios.ToolCalls},
		{"MultipleToolCalls", config.Scenarios.MultipleToolCalls},
		{"End2EndToolCalling", config.Scenarios.End2EndToolCalling},
		{"AutomaticFunctionCall", config.Scenarios.AutomaticFunctionCall},
		{"ImageURL", config.Scenarios.ImageURL},
		{"ImageBase64", config.Scenarios.ImageBase64},
		{"MultipleImages", config.Scenarios.MultipleImages},
		{"CompleteEnd2End", config.Scenarios.CompleteEnd2End},
		{"ProviderSpecific", config.Scenarios.ProviderSpecific},
	}

	supported := 0
	unsupported := 0

	t.Logf("\n%s", strings.Repeat("=", 80))
	t.Logf("COMPREHENSIVE TEST SUMMARY FOR PROVIDER: %s", strings.ToUpper(string(config.Provider)))
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
