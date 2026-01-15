package testutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
)

// ModelTestResult tracks the result of testing a single model
type ModelTestResult struct {
	Model   string
	Passed  bool
	Error   error
	Message string
}

// MultiModelTestResult tracks results across all models for a scenario
type MultiModelTestResult struct {
	ScenarioName string
	ModelResults []ModelTestResult
	AllPassed    bool
	FailedModels []string
	PassedModels []string
}

// RunTestForEachChatModel executes a test function for each chat model and ensures all pass
func RunTestForEachChatModel(t *testing.T, scenarioName string, models []string, testFunc func(*testing.T, string) error) MultiModelTestResult {
	return runTestForEachModel(t, scenarioName, "ChatModel", models, testFunc)
}

// RunTestForEachTextModel executes a test function for each text model and ensures all pass
func RunTestForEachTextModel(t *testing.T, scenarioName string, models []string, testFunc func(*testing.T, string) error) MultiModelTestResult {
	return runTestForEachModel(t, scenarioName, "TextModel", models, testFunc)
}

// RunTestForEachVisionModel executes a test function for each vision model and ensures all pass
func RunTestForEachVisionModel(t *testing.T, scenarioName string, models []string, testFunc func(*testing.T, string) error) MultiModelTestResult {
	return runTestForEachModel(t, scenarioName, "VisionModel", models, testFunc)
}

// RunTestForEachReasoningModel executes a test function for each reasoning model and ensures all pass
func RunTestForEachReasoningModel(t *testing.T, scenarioName string, models []string, testFunc func(*testing.T, string) error) MultiModelTestResult {
	return runTestForEachModel(t, scenarioName, "ReasoningModel", models, testFunc)
}

// RunTestForEachEmbeddingModel executes a test function for each embedding model and ensures all pass
func RunTestForEachEmbeddingModel(t *testing.T, scenarioName string, models []string, testFunc func(*testing.T, string) error) MultiModelTestResult {
	return runTestForEachModel(t, scenarioName, "EmbeddingModel", models, testFunc)
}

// RunTestForEachTranscriptionModel executes a test function for each transcription model and ensures all pass
func RunTestForEachTranscriptionModel(t *testing.T, scenarioName string, models []string, testFunc func(*testing.T, string) error) MultiModelTestResult {
	return runTestForEachModel(t, scenarioName, "TranscriptionModel", models, testFunc)
}

// RunTestForEachSpeechSynthesisModel executes a test function for each speech synthesis model and ensures all pass
func RunTestForEachSpeechSynthesisModel(t *testing.T, scenarioName string, models []string, testFunc func(*testing.T, string) error) MultiModelTestResult {
	return runTestForEachModel(t, scenarioName, "SpeechSynthesisModel", models, testFunc)
}

// RunTestForEachChatAudioModel executes a test function for each chat audio model and ensures all pass
func RunTestForEachChatAudioModel(t *testing.T, scenarioName string, models []string, testFunc func(*testing.T, string) error) MultiModelTestResult {
	return runTestForEachModel(t, scenarioName, "ChatAudioModel", models, testFunc)
}

// RunTestForEachPromptCachingModel executes a test function for each prompt caching model and ensures all pass
func RunTestForEachPromptCachingModel(t *testing.T, scenarioName string, models []string, testFunc func(*testing.T, string) error) MultiModelTestResult {
	return runTestForEachModel(t, scenarioName, "PromptCachingModel", models, testFunc)
}

// runTestForEachModel is the core function that runs tests across multiple models
func runTestForEachModel(t *testing.T, scenarioName string, modelType string, models []string, testFunc func(*testing.T, string) error) MultiModelTestResult {
	result := MultiModelTestResult{
		ScenarioName: scenarioName,
		ModelResults: []ModelTestResult{},
		AllPassed:    true,
		FailedModels: []string{},
		PassedModels: []string{},
	}

	// If no models provided, skip the test
	if len(models) == 0 {
		t.Logf("⏭️  Skipping %s: no models configured for %s", scenarioName, modelType)
		result.AllPassed = false
		return result
	}

	t.Logf("🔄 Running %s scenario across %d model(s) for %s", scenarioName, len(models), modelType)

	// Run test for each model
	for i, model := range models {
		t.Logf("📌 [%d/%d] Testing %s with model: %s", i+1, len(models), scenarioName, model)

		modelResult := ModelTestResult{
			Model:  model,
			Passed: false,
		}

		// Run the test function
		err := testFunc(t, model)
		if err != nil {
			modelResult.Passed = false
			modelResult.Error = err
			modelResult.Message = fmt.Sprintf("Model %s failed: %v", model, err)
			result.FailedModels = append(result.FailedModels, model)
			result.AllPassed = false
			t.Logf("❌ Model %s FAILED: %v", model, err)
		} else {
			modelResult.Passed = true
			modelResult.Message = fmt.Sprintf("Model %s passed", model)
			result.PassedModels = append(result.PassedModels, model)
			t.Logf("✅ Model %s PASSED", model)
		}

		result.ModelResults = append(result.ModelResults, modelResult)
	}

	// Print summary
	t.Logf("\n%s", strings.Repeat("=", 80))
	t.Logf("📊 MULTI-MODEL TEST SUMMARY: %s", scenarioName)
	t.Logf("%s", strings.Repeat("=", 80))
	t.Logf("Total Models Tested: %d", len(models))
	t.Logf("✅ Passed: %d models - %v", len(result.PassedModels), result.PassedModels)
	t.Logf("❌ Failed: %d models - %v", len(result.FailedModels), result.FailedModels)
	t.Logf("%s\n", strings.Repeat("=", 80))

	return result
}

// AssertAllModelsPassed fails the test if any model failed
func AssertAllModelsPassed(t *testing.T, result MultiModelTestResult) {
	if !result.AllPassed {
		errorMessages := []string{}
		for _, mr := range result.ModelResults {
			if !mr.Passed {
				errorMessages = append(errorMessages, mr.Message)
			}
		}
		t.Fatalf("❌ %s scenario FAILED: %d/%d models failed\nFailed models: %v\nErrors:\n%s",
			result.ScenarioName,
			len(result.FailedModels),
			len(result.ModelResults),
			result.FailedModels,
			strings.Join(errorMessages, "\n"))
	}
	t.Logf("🎉 %s scenario PASSED: All %d model(s) succeeded!", result.ScenarioName, len(result.PassedModels))
}

// GetChatModelOrFirst returns the first chat model from the array (for backward compatibility)
func GetChatModelOrFirst(config ComprehensiveTestConfig) string {
	if len(config.ChatModels) > 0 {
		return config.ChatModels[0]
	}
	return ""
}

// GetTextModelOrFirst returns the first text model from the array (for backward compatibility)
func GetTextModelOrFirst(config ComprehensiveTestConfig) string {
	if len(config.TextModels) > 0 {
		return config.TextModels[0]
	}
	return ""
}

// GetVisionModelOrFirst returns the first vision model from the array (for backward compatibility)
func GetVisionModelOrFirst(config ComprehensiveTestConfig) string {
	if len(config.VisionModels) > 0 {
		return config.VisionModels[0]
	}
	return ""
}

// GetReasoningModelOrFirst returns the first reasoning model from the array (for backward compatibility)
func GetReasoningModelOrFirst(config ComprehensiveTestConfig) string {
	if len(config.ReasoningModels) > 0 {
		return config.ReasoningModels[0]
	}
	return ""
}

// GetEmbeddingModelOrFirst returns the first embedding model from the array (for backward compatibility)
func GetEmbeddingModelOrFirst(config ComprehensiveTestConfig) string {
	if len(config.EmbeddingModels) > 0 {
		return config.EmbeddingModels[0]
	}
	return ""
}

// GetTranscriptionModelOrFirst returns the first transcription model from the array (for backward compatibility)
func GetTranscriptionModelOrFirst(config ComprehensiveTestConfig) string {
	if len(config.TranscriptionModels) > 0 {
		return config.TranscriptionModels[0]
	}
	return ""
}

// GetSpeechSynthesisModelOrFirst returns the first speech synthesis model from the array (for backward compatibility)
func GetSpeechSynthesisModelOrFirst(config ComprehensiveTestConfig) string {
	if len(config.SpeechSynthesisModels) > 0 {
		return config.SpeechSynthesisModels[0]
	}
	return ""
}

// GetChatAudioModelOrFirst returns the first chat audio model from the array (for backward compatibility)
func GetChatAudioModelOrFirst(config ComprehensiveTestConfig) string {
	if len(config.ChatAudioModels) > 0 {
		return config.ChatAudioModels[0]
	}
	return ""
}

// GetPromptCachingModelOrFirst returns the first prompt caching model from the array (for backward compatibility)
func GetPromptCachingModelOrFirst(config ComprehensiveTestConfig) string {
	if len(config.PromptCachingModels) > 0 {
		return config.PromptCachingModels[0]
	}
	return ""
}

// CreateConfigWithSingleModel creates a single-model config for testing a specific model
// This is useful for compatibility with existing test functions
func CreateConfigWithSingleModel(baseConfig ComprehensiveTestConfig, modelType string, model string) ComprehensiveTestConfig {
	config := baseConfig

	switch modelType {
	case "ChatModel", "chat":
		config.ChatModels = []string{model}
	case "TextModel", "text":
		config.TextModels = []string{model}
	case "VisionModel", "vision":
		config.VisionModels = []string{model}
	case "ReasoningModel", "reasoning":
		config.ReasoningModels = []string{model}
	case "EmbeddingModel", "embedding":
		config.EmbeddingModels = []string{model}
	case "TranscriptionModel", "transcription":
		config.TranscriptionModels = []string{model}
	case "SpeechSynthesisModel", "speech_synthesis":
		config.SpeechSynthesisModels = []string{model}
	case "ChatAudioModel", "chat_audio":
		config.ChatAudioModels = []string{model}
	case "PromptCachingModel", "prompt_caching":
		config.PromptCachingModels = []string{model}
	}

	return config
}

// ModelType represents the type of model being tested
type ModelType string

const (
	ModelTypeChat            ModelType = "chat"
	ModelTypeText            ModelType = "text"
	ModelTypeVision          ModelType = "vision"
	ModelTypeReasoning       ModelType = "reasoning"
	ModelTypeEmbedding       ModelType = "embedding"
	ModelTypeTranscription   ModelType = "transcription"
	ModelTypeSpeechSynthesis ModelType = "speech_synthesis"
	ModelTypeChatAudio       ModelType = "chat_audio"
	ModelTypePromptCaching   ModelType = "prompt_caching"
)

// getModelsForType returns the model array for a given model type
func getModelsForType(config ComprehensiveTestConfig, modelType ModelType) []string {
	switch modelType {
	case ModelTypeChat:
		return config.ChatModels
	case ModelTypeText:
		return config.TextModels
	case ModelTypeVision:
		return config.VisionModels
	case ModelTypeReasoning:
		return config.ReasoningModels
	case ModelTypeEmbedding:
		return config.EmbeddingModels
	case ModelTypeTranscription:
		return config.TranscriptionModels
	case ModelTypeSpeechSynthesis:
		return config.SpeechSynthesisModels
	case ModelTypeChatAudio:
		return config.ChatAudioModels
	case ModelTypePromptCaching:
		return config.PromptCachingModels
	default:
		return []string{}
	}
}

// TestFunction defines the signature for test functions used with RunMultiModelTest
type TestFunction func(*testing.T, *bifrost.Bifrost, context.Context, ComprehensiveTestConfig) error

// RunMultiModelTest is a GENERIC wrapper that runs ANY test scenario across multiple models
// This is the main function you should use for all scenarios
//
// Parameters:
//   - t: testing.T instance
//   - client: Bifrost client
//   - ctx: context
//   - testConfig: test configuration with model arrays
//   - scenarioName: name of the scenario (e.g., "SimpleChat")
//   - modelType: type of model being tested (e.g., ModelTypeChat)
//   - testFunc: the actual test function that takes a config with a single model
//
// Example usage:
//
//	RunMultiModelTest(t, client, ctx, testConfig, "SimpleChat", ModelTypeChat,
//	    func(t *testing.T, client *bifrost.Bifrost, ctx context.Context, config ComprehensiveTestConfig) error {
//	        // Your test logic here using Get*ModelOrFirst(config)
//	        return nil
//	    })
func RunMultiModelTest(
	t *testing.T,
	client *bifrost.Bifrost,
	ctx context.Context,
	testConfig ComprehensiveTestConfig,
	scenarioName string,
	modelType ModelType,
	testFunc TestFunction,
) MultiModelTestResult {
	result := MultiModelTestResult{
		ScenarioName: scenarioName,
		ModelResults: []ModelTestResult{},
		AllPassed:    true,
		FailedModels: []string{},
		PassedModels: []string{},
	}

	// Get the models for this type
	models := getModelsForType(testConfig, modelType)

	// If no models provided, skip the test
	if len(models) == 0 {
		t.Logf("⏭️  Skipping %s: no models configured for %s", scenarioName, modelType)
		result.AllPassed = false
		return result
	}

	t.Logf("🔄 Running %s scenario across %d model(s) for %s", scenarioName, len(models), modelType)

	// Run test for each model
	for i, model := range models {
		t.Logf("📌 [%d/%d] Testing %s with model: %s", i+1, len(models), scenarioName, model)

		modelResult := ModelTestResult{
			Model:  model,
			Passed: false,
		}

		// Create a config with just this one model
		singleModelConfig := CreateConfigWithSingleModel(testConfig, string(modelType), model)

		// Run the test function
		err := testFunc(t, client, ctx, singleModelConfig)
		if err != nil {
			modelResult.Passed = false
			modelResult.Error = err
			modelResult.Message = fmt.Sprintf("Model %s failed: %v", model, err)
			result.FailedModels = append(result.FailedModels, model)
			result.AllPassed = false
			t.Logf("❌ Model %s FAILED: %v", model, err)
		} else {
			modelResult.Passed = true
			modelResult.Message = fmt.Sprintf("Model %s passed", model)
			result.PassedModels = append(result.PassedModels, model)
			t.Logf("✅ Model %s PASSED", model)
		}

		result.ModelResults = append(result.ModelResults, modelResult)
	}

	// Print summary
	t.Logf("\n%s", strings.Repeat("=", 80))
	t.Logf("📊 MULTI-MODEL TEST SUMMARY: %s", scenarioName)
	t.Logf("%s", strings.Repeat("=", 80))
	t.Logf("Total Models Tested: %d", len(models))
	t.Logf("✅ Passed: %d models - %v", len(result.PassedModels), result.PassedModels)
	t.Logf("❌ Failed: %d models - %v", len(result.FailedModels), result.FailedModels)
	t.Logf("%s\n", strings.Repeat("=", 80))

	return result
}

// WrapTestScenario is a convenience function that wraps a test scenario with multi-model support
// Use this to quickly convert existing tests - it handles parallel execution and assertion
//
// Example:
//
//	t.Run("ScenarioName", func(t *testing.T) {
//	    WrapTestScenario(t, client, ctx, testConfig, "ScenarioName", ModelTypeChat, runScenarioTestForModel)
//	})
func WrapTestScenario(
	t *testing.T,
	client *bifrost.Bifrost,
	ctx context.Context,
	testConfig ComprehensiveTestConfig,
	scenarioName string,
	modelType ModelType,
	testFunc TestFunction,
) {
	if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
		t.Parallel()
	}

	result := RunMultiModelTest(t, client, ctx, testConfig, scenarioName, modelType, testFunc)
	AssertAllModelsPassed(t, result)
}
