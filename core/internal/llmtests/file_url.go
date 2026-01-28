package llmtests

import (
	"context"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// CreateFileURLChatMessage creates a ChatMessage with a file URL
func CreateFileURLChatMessage(text, fileURL string) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role: schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{
			ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: bifrost.Ptr(text)},
				{
					Type: schemas.ChatContentBlockTypeFile,
					File: &schemas.ChatInputFile{
						FileURL: bifrost.Ptr(fileURL),
					},
				},
			},
		},
	}
}

// CreateFileURLResponsesMessage creates a ResponsesMessage with a file URL
func CreateFileURLResponsesMessage(text, fileURL string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: bifrost.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: bifrost.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: bifrost.Ptr(text)},
				{
					Type: schemas.ResponsesInputMessageContentBlockTypeFile,
					ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
						FileURL: bifrost.Ptr(fileURL),
					},
				},
			},
		},
	}
}

// RunFileURLTest executes the file URL input test scenario with separate subtests for each API
func RunFileURLTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.FileURL {
		t.Logf("File URL not supported for provider %s", testConfig.Provider)
		return
	}

	// Run Chat Completions subtest
	RunFileURLChatCompletionsTest(t, client, ctx, testConfig)

	// Run Responses API subtest
	RunFileURLResponsesTest(t, client, ctx, testConfig)
}

// RunFileURLChatCompletionsTest executes the file URL test using Chat Completions API
func RunFileURLChatCompletionsTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.FileURL {
		t.Logf("File URL not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("FileURL-ChatCompletions", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Skip Chat Completions for OpenAI and OpenRouter (file URL not supported)
		if testConfig.Provider == schemas.OpenAI || testConfig.Provider == schemas.OpenRouter {
			t.Skipf("Skipping FileURL Chat Completions test for provider %s (file URL not supported)", testConfig.Provider)
			return
		}

		// Create messages for Chat Completions API with file URL
		chatMessages := []schemas.ChatMessage{
			CreateFileURLChatMessage("What is this document about? Please provide a summary of its main topics.", TestFileURL),
		}

		// Use retry framework for file URL requests
		retryConfig := GetTestRetryConfigForScenario("FileInput", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "FileURL-ChatCompletions",
			ExpectedBehavior: map[string]interface{}{
				"should_fetch_url":       true,
				"should_read_document":   true,
				"should_extract_content": true,
				"document_understanding": true,
			},
			TestMetadata: map[string]interface{}{
				"provider":          testConfig.Provider,
				"model":             testConfig.ChatModel,
				"file_type":         "pdf",
				"source":            "url",
				"test_url":          TestFileURL,
				"expected_keywords": []string{"berkshire", "hathaway", "shareholders"},
			},
		}

		// Enhanced validation for file URL processing
		expectations := GetExpectationsForScenario("FileInput", testConfig, map[string]interface{}{})
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
		// The test PDF is a Berkshire Hathaway shareholder letter - flexible keywords
		expectations.ShouldContainKeywords = []string{} // Clear default keywords
		expectations.ShouldNotContainWords = append(expectations.ShouldNotContainWords, []string{
			"cannot process", "invalid format", "decode error",
			"unable to read", "no file", "corrupted", "unsupported",
			"cannot fetch", "download failed", "url not found",
		}...) // File URL processing failure indicators

		chatRetryConfig := ChatRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []ChatRetryCondition{},
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		response, chatError := WithChatTestRetry(t, chatRetryConfig, retryContext, expectations, "FileURL", func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			chatReq := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    chatMessages,
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(500),
				},
				Fallbacks: testConfig.Fallbacks,
			}
			return client.ChatCompletionRequest(bfCtx, chatReq)
		})

		if chatError != nil {
			t.Fatalf("❌ FileURL Chat Completions test failed: %v", GetErrorMessage(chatError))
		}

		// Additional validation for file URL processing
		content := GetChatContent(response)
		validateFileURLContent(t, content, "Chat Completions")

		t.Logf("🎉 Chat Completions API passed FileURL test!")
	})
}

// RunFileURLResponsesTest executes the file URL test using Responses API
func RunFileURLResponsesTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.FileURL {
		t.Logf("File URL not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("FileURL-Responses", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Create messages for Responses API with file URL
		responsesMessages := []schemas.ResponsesMessage{
			CreateFileURLResponsesMessage("What is this document about? Please provide a summary of its main topics.", TestFileURL),
		}

		// Set up retry context for file URL requests
		retryContext := TestRetryContext{
			ScenarioName: "FileURL-Responses",
			ExpectedBehavior: map[string]interface{}{
				"should_fetch_url":       true,
				"should_read_document":   true,
				"should_extract_content": true,
				"document_understanding": true,
			},
			TestMetadata: map[string]interface{}{
				"provider":          testConfig.Provider,
				"model":             testConfig.ChatModel,
				"file_type":         "pdf",
				"source":            "url",
				"test_url":          TestFileURL,
				"expected_keywords": []string{"berkshire", "hathaway", "shareholders"},
			},
		}

		// Enhanced validation for file URL processing
		expectations := GetExpectationsForScenario("FileInput", testConfig, map[string]interface{}{})
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
		// The test PDF is a Berkshire Hathaway shareholder letter - flexible keywords
		expectations.ShouldContainKeywords = []string{} // Clear default keywords
		expectations.ShouldNotContainWords = append(expectations.ShouldNotContainWords, []string{
			"cannot process", "invalid format", "decode error",
			"unable to read", "no file", "corrupted", "unsupported",
			"cannot fetch", "download failed", "url not found",
		}...) // File URL processing failure indicators

		responsesRetryConfig := FileInputResponsesRetryConfig()

		response, responsesError := WithResponsesTestRetry(t, responsesRetryConfig, retryContext, expectations, "FileURL", func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    responsesMessages,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(500),
				},
				Fallbacks: testConfig.Fallbacks,
			}
			return client.ResponsesRequest(bfCtx, responsesReq)
		})

		if responsesError != nil {
			t.Fatalf("❌ FileURL Responses test failed: %v", GetErrorMessage(responsesError))
		}

		// Additional validation for file URL processing
		content := GetResponsesContent(response)
		validateFileURLContent(t, content, "Responses")

		t.Logf("🎉 Responses API passed FileURL test!")
	})
}

func validateFileURLContent(t *testing.T, content string, apiName string) {
	t.Helper()
	lowerContent := strings.ToLower(content)

	if len(content) < 20 {
		t.Errorf("❌ %s response is too short for document description (got %d chars): %s", apiName, len(content), content)
		return
	}

	// Berkshire Hathaway related keywords
	primaryKeywords := []string{"berkshire", "hathaway", "shareholder", "mistake", "murphy", "munger"}

	// Generic document-related keywords
	documentKeywords := []string{"document", "pdf", "letter", "report", "annual", "company"}

	// Check if any primary keywords are found
	foundPrimary := false
	for _, keyword := range primaryKeywords {
		if strings.Contains(lowerContent, keyword) {
			foundPrimary = true
			break
		}
	}

	// Check if any document keywords are found
	foundDocument := false
	for _, keyword := range documentKeywords {
		if strings.Contains(lowerContent, keyword) {
			foundDocument = true
			break
		}
	}

	// Pass if we find any relevant content indicators
	if foundPrimary || foundDocument {
		if foundPrimary {
			t.Logf("✅ %s model successfully extracted Berkshire Hathaway content from PDF file URL", apiName)
		} else {
			t.Logf("✅ %s model processed PDF from URL and generated relevant response", apiName)
		}
		t.Logf("   Response preview: %s", truncateString(content, 200))
	} else {
		t.Errorf("❌ %s model failed to process file from URL - response doesn't reference expected content. Response: %s", apiName, truncateString(content, 300))
		return
	}
}
