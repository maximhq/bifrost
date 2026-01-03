package testutil

import (
	"context"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// Base64 encoded PDF file containing "Hello World!" text
// This is a minimal valid PDF for testing document input functionality
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

// RunFileBase64Test executes the PDF file input test scenario using dual API testing framework
func RunFileBase64Test(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.FileBase64 {
		t.Logf("File base64 not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("FileBase64", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Create messages for both APIs with base64 PDF document
		chatMessages := []schemas.ChatMessage{
			CreateDocumentChatMessage("What is the main content of this PDF document? Summarize it.", HelloWorldPDFBase64),
		}
		responsesMessages := []schemas.ResponsesMessage{
			CreateDocumentResponsesMessage("What is the main content of this PDF document? Summarize it.", HelloWorldPDFBase64),
		}

		// Use retry framework for document input requests
		retryConfig := GetTestRetryConfigForScenario("FileBase64", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "FileBase64",
			ExpectedBehavior: map[string]interface{}{
				"should_process_pdf":     true,
				"should_read_document":   true,
				"should_extract_content": true,
				"document_understanding": true,
			},
			TestMetadata: map[string]interface{}{
				"provider":          testConfig.Provider,
				"model":             testConfig.VisionModel,
				"file_type":         "pdf",
				"encoding":          "base64",
				"test_content":      "Hello World!",
				"expected_keywords": []string{"hello", "world", "pdf", "document"},
			},
		}

		// Enhanced validation for PDF document processing (same for both APIs)
		expectations := GetExpectationsForScenario("FileInput", testConfig, map[string]interface{}{})
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
		expectations.ShouldContainKeywords = append(expectations.ShouldContainKeywords, "hello", "world")
		expectations.ShouldNotContainWords = append(expectations.ShouldNotContainWords, []string{
			"cannot process", "invalid format", "decode error",
			"unable to read", "no file", "corrupted", "unsupported",
		}...) // PDF processing failure indicators

		// Create operations for both Chat Completions and Responses API
		chatOperation := func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			chatReq := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.VisionModel,
				Input:    chatMessages,
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(500),
				},
				Fallbacks: testConfig.Fallbacks,
			}
			return client.ChatCompletionRequest(ctx, chatReq)
		}

		responsesOperation := func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.VisionModel,
				Input:    responsesMessages,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(500),
				},
				Fallbacks: testConfig.Fallbacks,
			}
			return client.ResponsesRequest(ctx, responsesReq)
		}

		// Execute dual API test - passes only if BOTH APIs succeed
		result := WithDualAPITestRetry(t,
			retryConfig,
			retryContext,
			expectations,
			"FileBase64",
			chatOperation,
			responsesOperation)

		// Validate both APIs succeeded
		if !result.BothSucceeded {
			var errors []string
			if result.ChatCompletionsError != nil {
				errors = append(errors, "Chat Completions: "+GetErrorMessage(result.ChatCompletionsError))
			}
			if result.ResponsesAPIError != nil {
				errors = append(errors, "Responses API: "+GetErrorMessage(result.ResponsesAPIError))
			}
			if len(errors) == 0 {
				errors = append(errors, "One or both APIs failed validation (see logs above)")
			}
			t.Fatalf("❌ FileBase64 dual API test failed: %v", errors)
		}

		// Additional validation for PDF document processing using universal content extraction
		validateChatDocumentProcessing := func(response *schemas.BifrostChatResponse, apiName string) {
			content := GetChatContent(response)
			validateDocumentContent(t, content, apiName)
		}

		validateResponsesDocumentProcessing := func(response *schemas.BifrostResponsesResponse, apiName string) {
			content := GetResponsesContent(response)
			validateDocumentContent(t, content, apiName)
		}

		// Validate both API responses
		if result.ChatCompletionsResponse != nil {
			validateChatDocumentProcessing(result.ChatCompletionsResponse, "Chat Completions")
		}

		if result.ResponsesAPIResponse != nil {
			validateResponsesDocumentProcessing(result.ResponsesAPIResponse, "Responses")
		}

		t.Logf("🎉 Both Chat Completions and Responses APIs passed FileBase64 test!")
	})
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
