package testutil

import (
	"context"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
)

// TestScenarioFunc defines the function signature for test scenario functions
type TestScenarioFunc func(*testing.T, *bifrost.Bifrost, context.Context, ComprehensiveTestConfig)

// RunAllComprehensiveTests executes all comprehensive test scenarios for a given configuration
func RunAllComprehensiveTests(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if testConfig.SkipReason != "" {
		t.Skipf("Skipping %s: %s", testConfig.Provider, testConfig.SkipReason)
		return
	}

	t.Logf("🚀 Running comprehensive tests for provider: %s", testConfig.Provider)

	// Define all test scenario functions in a slice
	testScenarios := []TestScenarioFunc{
		RunTextCompletionTest,
		RunTextCompletionStreamTest,
		RunSimpleChatTest,
		RunChatCompletionStreamTest,
		RunResponsesStreamTest,
		RunMultiTurnConversationTest,
		RunToolCallsTest,
		RunToolCallsStreamingTest,
		RunMultipleToolCallsTest,
		RunEnd2EndToolCallingTest,
		RunAutomaticFunctionCallingTest,
		RunImageURLTest,
		RunImageBase64Test,
		RunMultipleImagesTest,
		RunCompleteEnd2EndTest,
		RunSpeechSynthesisTest,
		RunSpeechSynthesisAdvancedTest,
		RunSpeechSynthesisStreamTest,
		RunSpeechSynthesisStreamAdvancedTest,
		RunTranscriptionTest,
		RunTranscriptionAdvancedTest,
		RunTranscriptionStreamTest,
		RunTranscriptionStreamAdvancedTest,
		RunEmbeddingTest,
		RunChatCompletionReasoningTest,
		RunResponsesReasoningTest,
		RunListModelsTest,
		RunListModelsPaginationTest,
		RunPromptCachingTest,
		RunBatchCreateTest,
		RunBatchListTest,
		RunBatchRetrieveTest,
		RunBatchCancelTest,
		RunBatchResultsTest,
		RunBatchUnsupportedTest,
		RunFileUploadTest,
		RunFileListTest,
		RunFileRetrieveTest,
		RunFileDeleteTest,
		RunFileContentTest,
		RunFileUnsupportedTest,
		RunFileAndBatchIntegrationTest,
	}

	// Execute all test scenarios
	for _, scenarioFunc := range testScenarios {
		scenarioFunc(t, client, ctx, testConfig)
	}

	// Print comprehensive summary based on configuration
	printTestSummary(t, testConfig)
}

// printTestSummary prints a detailed summary of all test scenarios
func printTestSummary(t *testing.T, testConfig ComprehensiveTestConfig) {
	testScenarios := []struct {
		name      string
		supported bool
	}{
		{"TextCompletion", testConfig.Scenarios.TextCompletion && testConfig.TextModel != ""},
		{"SimpleChat", testConfig.Scenarios.SimpleChat},
		{"CompletionStream", testConfig.Scenarios.CompletionStream},
		{"MultiTurnConversation", testConfig.Scenarios.MultiTurnConversation},
		{"ToolCalls", testConfig.Scenarios.ToolCalls},
		{"ToolCallsStreaming", testConfig.Scenarios.ToolCallsStreaming},
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
		{"ChatCompletionReasoning", testConfig.Scenarios.Reasoning && testConfig.ReasoningModel != ""},
		{"ResponsesReasoning", testConfig.Scenarios.Reasoning && testConfig.ReasoningModel != ""},
		{"ListModels", testConfig.Scenarios.ListModels},
		{"PromptCaching", testConfig.Scenarios.SimpleChat && testConfig.PromptCachingModel != ""},
		{"BatchCreate", testConfig.Scenarios.BatchCreate},
		{"BatchList", testConfig.Scenarios.BatchList},
		{"BatchRetrieve", testConfig.Scenarios.BatchRetrieve},
		{"BatchCancel", testConfig.Scenarios.BatchCancel},
		{"BatchResults", testConfig.Scenarios.BatchResults},
		{"BatchUnsupported", !testConfig.Scenarios.BatchCreate && !testConfig.Scenarios.BatchList && !testConfig.Scenarios.BatchRetrieve && !testConfig.Scenarios.BatchCancel && !testConfig.Scenarios.BatchResults},
		{"FileUpload", testConfig.Scenarios.FileUpload},
		{"FileList", testConfig.Scenarios.FileList},
		{"FileRetrieve", testConfig.Scenarios.FileRetrieve},
		{"FileDelete", testConfig.Scenarios.FileDelete},
		{"FileContent", testConfig.Scenarios.FileContent},
		{"FileUnsupported", !testConfig.Scenarios.FileUpload && !testConfig.Scenarios.FileList && !testConfig.Scenarios.FileRetrieve && !testConfig.Scenarios.FileDelete && !testConfig.Scenarios.FileContent},
		{"FileAndBatchIntegration", testConfig.Scenarios.FileBatchInput},
	}

	supported := 0
	unsupported := 0

	t.Logf("\n%s", strings.Repeat("=", 80))
	t.Logf("COMPREHENSIVE TEST SUMMARY FOR PROVIDER: %s", strings.ToUpper(string(testConfig.Provider)))
	t.Logf("%s", strings.Repeat("=", 80))

	for _, scenario := range testScenarios {
		if scenario.supported {
			supported++
			t.Logf("[ENABLED]  SUPPORTED:   %-25s [ENABLED]  Configured to run", scenario.name)
		} else {
			unsupported++
			t.Logf("[SKIPPED]  UNSUPPORTED: %-25s [SKIPPED]  Not supported by provider", scenario.name)
		}
	}

	t.Logf("%s", strings.Repeat("-", 80))
	t.Logf("CONFIGURATION SUMMARY:")
	t.Logf("  [ENABLED]  Supported Tests:   %d", supported)
	t.Logf("  [SKIPPED]  Unsupported Tests: %d", unsupported)
	t.Logf("  [TOTAL]    Total Test Types:  %d", len(testScenarios))
	t.Logf("")
	t.Logf("%s\n", strings.Repeat("=", 80))
}
