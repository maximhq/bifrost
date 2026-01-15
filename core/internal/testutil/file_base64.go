package testutil

import (
	"context"
	"fmt"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// HelloWorldPDFBase64 is a base64 encoded PDF file containing "Hello World!" text.
// This is a minimal valid PDF for testing document input functionality.
const HelloWorldPDFBase64 = "data:application/pdf;base64,JVBERi0xLjcKCjEgMCBvYmogICUgZW50cnkgcG9pbnQKPDwKICAvVHlwZSAvQ2F0YWxvZwogIC" +
	"9QYWdlcyAyIDAgUgo+PgplbmRvYmoKCjIgMCBvYmoKPDwKICAvVHlwZSAvUGFnZXwKICAvTWV" +
	"kaWFCb3ggWyAwIDAgMjAwIDIwMCBdCiAgL0NvdW50IDEKICAvS2lkcyBbIDMgMCBSIF0KPj4K" +
	"ZW5kb2JqCgozIDAgb2JqCjw8CiAgL1R5cGUgL1BhZ2UKICAvUGFyZW50IDIgMCBSCiAgL1Jlc" +
	"291cmNlcyA8PAogICAgL0ZvbnQgPDwKICAgICAgL0YxIDQgMCBSCj4+CiAgPj4KICAvQ29udG" +
	"VudHMgNSAwIFIKPj4KZW5kb2JqCgo0IDAgb2JqCjw8CiAgL1R5cGUgL0ZvbnQKICAvU3VidHl" +
	"wZSAvVHlwZTEKICAvQmFzZUZvbnQgL1RpbWVzLVJvbWFuCj4+CmVuZG9iagoKNSAwIG9iago8" +
	"PAogIC9MZW5ndGggNDQKPj4Kc3RyZWFtCkJUCjcwIDUwIFRECi9GMSAxMiBUZgooSGVsbG8gV" +
	"29ybGQhKSBUagpFVAplbmRzdHJlYW0KZW5kb2JqCgp4cmVmCjAgNgowMDAwMDAwMDAwIDY1NT" +
	"M1IGYgCjAwMDAwMDAwMTAgMDAwMDAgbiAKMDAwMDAwMDA2MCAwMDAwMCBuIAowMDAwMDAwMTU" +
	"3IDAwMDAwIG4gCjAwMDAwMDAyNTUgMDAwMDAgbiAKMDAwMDAwMDM1MyAwMDAwMCBuIAp0cmFp" +
	"bGVyCjw8CiAgL1NpemUgNgogIC9Sb290IDEgMCBSCj4+CnN0YXJ0eHJlZgo0NDkKJSVFT0YK"

// CreateDocumentChatMessage creates a ChatMessage with a PDF document in base64 format
func CreateDocumentChatMessage(text, documentBase64 string) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role: schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{
			ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: bifrost.Ptr(text)},
				{
					Type: schemas.ChatContentBlockTypeFile,
					File: &schemas.ChatInputFile{
						FileData: bifrost.Ptr(documentBase64),
						Filename: bifrost.Ptr("test_document.pdf"),
					},
				},
			},
		},
	}
}

// CreateDocumentResponsesMessage creates a ResponsesMessage with a PDF document in base64 format
func CreateDocumentResponsesMessage(text, documentBase64 string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: bifrost.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: bifrost.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: bifrost.Ptr(text)},
				{
					Type: schemas.ResponsesInputMessageContentBlockTypeFile,
					ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
						FileData: bifrost.Ptr(documentBase64),
						Filename: bifrost.Ptr("test_document.pdf"),
					},
				},
			},
		},
	}
}

// RunFileBase64Test executes the PDF file input test scenario with separate subtests for each API
// This function now supports testing multiple vision models - the test passes only if ALL models pass
func RunFileBase64Test(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.FileBase64 {
		t.Logf("File base64 not supported for provider %s", testConfig.Provider)
		return
	}

	// Run Chat Completions subtest
	RunFileBase64ChatCompletionsTest(t, client, ctx, testConfig)

	// Run Responses API subtest
	RunFileBase64ResponsesTest(t, client, ctx, testConfig)
}

// RunFileBase64ChatCompletionsTest executes the file base64 test using Chat Completions API
func RunFileBase64ChatCompletionsTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.FileBase64 {
		t.Logf("File base64 not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("FileBase64-ChatCompletions", func(t *testing.T) {
		WrapTestScenario(t, client, ctx, testConfig, "FileBase64-ChatCompletions", ModelTypeVision, runFileBase64ChatCompletionsTestForModel)
	})
}

// runFileBase64ChatCompletionsTestForModel runs the file base64 Chat Completions test for a specific model
func runFileBase64ChatCompletionsTestForModel(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) error {
	// Get the single model from the config
	model := GetVisionModelOrFirst(testConfig)

	// Create messages for Chat Completions API with base64 PDF document
	chatMessages := []schemas.ChatMessage{
		CreateDocumentChatMessage("What is the main content of this PDF document? Summarize it.", HelloWorldPDFBase64),
	}

	// Use retry framework for document input requests
	retryConfig := GetTestRetryConfigForScenario("FileInput", testConfig)
	retryContext := TestRetryContext{
		ScenarioName: "FileBase64-ChatCompletions",
		ExpectedBehavior: map[string]interface{}{
			"should_process_pdf":     true,
			"should_read_document":   true,
			"should_extract_content": true,
			"document_understanding": true,
		},
		TestMetadata: map[string]interface{}{
			"provider":          testConfig.Provider,
			"model":             model,
			"file_type":         "pdf",
			"encoding":          "base64",
			"test_content":      "Hello World!",
			"expected_keywords": []string{"hello", "world", "pdf", "document"},
		},
	}

	// Enhanced validation for PDF document processing
	expectations := GetExpectationsForScenario("FileInput", testConfig, map[string]interface{}{})
	expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
	expectations.ShouldContainKeywords = append(expectations.ShouldContainKeywords, "hello", "world")
	expectations.ShouldNotContainWords = append(expectations.ShouldNotContainWords, []string{
		"cannot process", "invalid format", "decode error",
		"unable to read", "no file", "corrupted", "unsupported",
	}...) // PDF processing failure indicators

	chatRetryConfig := ChatRetryConfig{
		MaxAttempts: retryConfig.MaxAttempts,
		BaseDelay:   retryConfig.BaseDelay,
		MaxDelay:    retryConfig.MaxDelay,
		Conditions:  []ChatRetryCondition{},
		OnRetry:     retryConfig.OnRetry,
		OnFinalFail: retryConfig.OnFinalFail,
	}

	response, chatError := WithChatTestRetry(t, chatRetryConfig, retryContext, expectations, "FileBase64", func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		chatReq := &schemas.BifrostChatRequest{
			Provider: testConfig.Provider,
			Model:    model,
			Input:    chatMessages,
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: bifrost.Ptr(500),
			},
			Fallbacks: testConfig.Fallbacks,
		}
		return client.ChatCompletionRequest(bfCtx, chatReq)
	})

	if chatError != nil {
		return fmt.Errorf("FileBase64 Chat Completions test failed: %v", GetErrorMessage(chatError))
	}

	// Additional validation for PDF document processing
	content := GetChatContent(response)
	err := validateDocumentContentWithError(content, "Chat Completions")
	if err != nil {
		return err
	}

	t.Logf("🎉 Chat Completions API passed FileBase64 test!")
	return nil
}

// RunFileBase64ResponsesTest executes the file base64 test using Responses API
func RunFileBase64ResponsesTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.FileBase64 {
		t.Logf("File base64 not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("FileBase64-Responses", func(t *testing.T) {
		WrapTestScenario(t, client, ctx, testConfig, "FileBase64-Responses", ModelTypeVision, runFileBase64ResponsesTestForModel)
	})
}

// runFileBase64ResponsesTestForModel runs the file base64 Responses test for a specific model
func runFileBase64ResponsesTestForModel(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) error {
	// Get the single model from the config
	model := GetVisionModelOrFirst(testConfig)

	// Create messages for Responses API with base64 PDF document
	responsesMessages := []schemas.ResponsesMessage{
		CreateDocumentResponsesMessage("What is the main content of this PDF document? Summarize it.", HelloWorldPDFBase64),
	}

	// Set up retry context for document input requests
	retryContext := TestRetryContext{
		ScenarioName: "FileBase64-Responses",
		ExpectedBehavior: map[string]interface{}{
			"should_process_pdf":     true,
			"should_read_document":   true,
			"should_extract_content": true,
			"document_understanding": true,
		},
		TestMetadata: map[string]interface{}{
			"provider":          testConfig.Provider,
			"model":             model,
			"file_type":         "pdf",
			"encoding":          "base64",
			"test_content":      "Hello World!",
			"expected_keywords": []string{"hello", "world", "pdf", "document"},
		},
	}

	// Enhanced validation for PDF document processing
	expectations := GetExpectationsForScenario("FileInput", testConfig, map[string]interface{}{})
	expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
	expectations.ShouldContainKeywords = append(expectations.ShouldContainKeywords, "hello", "world")
	expectations.ShouldNotContainWords = append(expectations.ShouldNotContainWords, []string{
		"cannot process", "invalid format", "decode error",
		"unable to read", "no file", "corrupted", "unsupported",
	}...) // PDF processing failure indicators

	retryConfig := GetTestRetryConfigForScenario("FileInput", testConfig)
	responsesRetryConfig := ResponsesRetryConfig{
		MaxAttempts: retryConfig.MaxAttempts,
		BaseDelay:   retryConfig.BaseDelay,
		MaxDelay:    retryConfig.MaxDelay,
		Conditions:  []ResponsesRetryCondition{},
		OnRetry:     retryConfig.OnRetry,
		OnFinalFail: retryConfig.OnFinalFail,
	}

	response, responsesError := WithResponsesTestRetry(t, responsesRetryConfig, retryContext, expectations, "FileBase64", func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		responsesReq := &schemas.BifrostResponsesRequest{
			Provider: testConfig.Provider,
			Model:    model,
			Input:    responsesMessages,
			Params: &schemas.ResponsesParameters{
				MaxOutputTokens: bifrost.Ptr(500),
			},
			Fallbacks: testConfig.Fallbacks,
		}
		return client.ResponsesRequest(bfCtx, responsesReq)
	})

	if responsesError != nil {
		return fmt.Errorf("FileBase64 Responses test failed: %v", GetErrorMessage(responsesError))
	}

	// Additional validation for PDF document processing
	content := GetResponsesContent(response)
	err := validateDocumentContentWithError(content, "Responses")
	if err != nil {
		return err
	}

	t.Logf("🎉 Responses API passed FileBase64 test!")
	return nil
}

// validateDocumentContentWithError validates document content and returns error instead of failing test
func validateDocumentContentWithError(content string, apiName string) error {
	lowerContent := strings.ToLower(content)
	foundHelloWorld := strings.Contains(lowerContent, "hello") && strings.Contains(lowerContent, "world")
	foundDocument := strings.Contains(lowerContent, "document") || strings.Contains(lowerContent, "pdf") ||
		strings.Contains(lowerContent, "file") || strings.Contains(lowerContent, "text")

	if len(content) < 10 {
		return fmt.Errorf("%s response is too short for document description (got %d chars): %s", apiName, len(content), content)
	}

	if !foundHelloWorld && !foundDocument {
		return fmt.Errorf("%s model failed to process PDF document - response doesn't reference expected content or document-related terms. Response: %s", apiName, content)
	}

	return nil
}

func validateDocumentContent(t *testing.T, content string, apiName string) {
	t.Helper()
	lowerContent := strings.ToLower(content)
	foundHelloWorld := strings.Contains(lowerContent, "hello") && strings.Contains(lowerContent, "world")
	foundDocument := strings.Contains(lowerContent, "document") || strings.Contains(lowerContent, "pdf") ||
		strings.Contains(lowerContent, "file") || strings.Contains(lowerContent, "text")

	if len(content) < 10 {
		t.Errorf("❌ %s response is too short for document description (got %d chars): %s", apiName, len(content), content)
		return
	}

	if !foundHelloWorld && !foundDocument {
		t.Errorf("❌ %s model failed to process PDF document - response doesn't reference expected content or document-related terms. Response: %s", apiName, content)
		return
	}

	if foundHelloWorld {
		t.Logf("✅ %s model successfully extracted 'Hello World' content from PDF document", apiName)
	} else if foundDocument {
		t.Logf("✅ %s model processed PDF document but may not have clearly identified the exact text", apiName)
	} else {
		t.Errorf("❌ %s response doesn't reference document content or expected keywords: %s", apiName, content)
		return
	}

	t.Logf("✅ %s PDF document processing completed: %s", apiName, content)
}
