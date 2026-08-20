package vertex_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/gemini"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVertex(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("VERTEX_API_KEY")) == "" && (strings.TrimSpace(os.Getenv("VERTEX_PROJECT_ID")) == "" || strings.TrimSpace(os.Getenv("VERTEX_CREDENTIALS")) == "") {
		t.Skip("Skipping Vertex tests because VERTEX_API_KEY is not set and VERTEX_PROJECT_ID or VERTEX_CREDENTIALS is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	rerankModel := strings.TrimSpace(os.Getenv("VERTEX_RERANK_MODEL"))

	// Vertex file operations are GCS-backed: the bucket/prefix are passed via the typed
	// StorageConfig (VERTEX_GCS_BUCKET, optional VERTEX_GCS_PREFIX), not extra_params.
	var fileStorageConfig *schemas.FileStorageConfig
	var batchOutputFolder *schemas.BatchOutputFolder
	if gcsBucket := strings.TrimSpace(os.Getenv("VERTEX_GCS_BUCKET")); gcsBucket != "" {
		gcsPrefix := strings.TrimSpace(os.Getenv("VERTEX_GCS_PREFIX"))
		fileStorageConfig = &schemas.FileStorageConfig{
			GCS: &schemas.GCSStorageConfig{
				Bucket: gcsBucket,
				Prefix: gcsPrefix,
			},
		}
		// Batch output is a gs:// prefix; Vertex writes results into its own subdirectory under it.
		outputURI := "gs://" + gcsBucket
		if gcsPrefix != "" {
			outputURI += "/" + strings.Trim(gcsPrefix, "/")
		}
		outputURI += "/batch-output"
		batchOutputFolder = &schemas.BatchOutputFolder{URL: outputURI}
	}

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:             schemas.Vertex,
		ChatModel:            "gemini-2.5-pro",
		PromptCachingModel:   "claude-sonnet-4-5",
		VisionModel:          "claude-sonnet-4-5",
		TextModel:            "", // Vertex doesn't support text completion in newer models
		EmbeddingModel:       "text-multilingual-embedding-002",
		RerankModel:          rerankModel,
		ReasoningModel:       "claude-4.5-haiku",
		ImageGenerationModel: "gemini-2.5-flash-image",
		ImageEditModel:       "imagen-3.0-capability-001",
		VideoGenerationModel: "veo-3.1-generate-preview",
		FileStorageConfig:    fileStorageConfig,
		BatchOutputFolder:    batchOutputFolder,
		Scenarios: llmtests.TestScenarios{
			TextCompletion:               false, // Not supported
			SimpleChat:                   true,
			CompletionStream:             true,
			MultiTurnConversation:        true,
			ToolCalls:                    true,
			ToolCallsStreaming:           true,
			MultipleToolCalls:            true,
			MultipleToolCallsStreaming:   true,
			End2EndToolCalling:           true,
			AutomaticFunctionCall:        true,
			ImageURL:                     false,
			ImageBase64:                  true,
			ImageGeneration:              true,
			ImageGenerationStream:        false,
			ImageEdit:                    true,
			VideoGeneration:              false, // disabled for now because of long running operations
			VideoRetrieve:                false,
			VideoRemix:                   false,
			VideoDownload:                false,
			VideoList:                    false,
			VideoDelete:                  false,
			MultipleImages:               true,
			CompleteEnd2End:              true,
			FileBase64:                   true,
			Embedding:                    true,
			Rerank:                       rerankModel != "",
			Reasoning:                    true,
			PromptCaching:                true,
			ListModels:                   false,
			CountTokens:                  true,
			StructuredOutputs:            true, // Structured outputs with nullable enum support
			InterleavedThinking:          true,
			EagerInputStreaming:          true, // fine-grained-tool-streaming-2025-05-14 (GA on Vertex)
			ServerToolsViaOpenAIEndpoint: true, // web_search only on Vertex per Table 20 (web_fetch/code_execution skip)
			FileUpload:                   true,
			FileList:                     true,
			FileRetrieve:                 true,
			FileDelete:                   true,
			FileContent:                  true,
			FileBatchInput:               true,
			BatchCreate:                  true,
			BatchList:                    true,
			BatchRetrieve:                true,
			BatchCancel:                  true,
			BatchResults:                 true,
		},
	}

	t.Run("VertexTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

func TestVertexGeminiChatCompletionPreservesGCSImageURL(t *testing.T) {
	result, err := gemini.ToGeminiChatCompletionRequestWithImageURLSchemes(nil, &schemas.BifrostChatRequest{
		Model: "gemini-3-flash-preview",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentBlocks: []schemas.ChatContentBlock{
						{
							Type: schemas.ChatContentBlockTypeText,
							Text: schemas.Ptr("Describe this image."),
						},
						{
							Type: schemas.ChatContentBlockTypeImage,
							ImageURLStruct: &schemas.ChatInputImage{
								URL: "gs://my-bucket/xxx.png",
							},
						},
					},
				},
			},
		},
	}, "http", "https", "gs")

	require.NoError(t, err)
	require.Len(t, result.Contents, 1)
	require.Len(t, result.Contents[0].Parts, 2)
	require.NotNil(t, result.Contents[0].Parts[1].FileData)
	assert.Equal(t, "gs://my-bucket/xxx.png", result.Contents[0].Parts[1].FileData.FileURI)
	assert.Equal(t, "image/png", result.Contents[0].Parts[1].FileData.MIMEType)
}

func TestVertexGeminiResponsesPreservesGCSImageURL(t *testing.T) {
	result, err := gemini.ToGeminiResponsesRequestWithImageURLSchemes(nil, &schemas.BifrostResponsesRequest{
		Provider: schemas.Vertex,
		Model:    "gemini-3-flash-preview",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesInputMessageContentBlockTypeText,
							Text: schemas.Ptr("Describe this image."),
						},
						{
							Type: schemas.ResponsesInputMessageContentBlockTypeImage,
							ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
								ImageURL: schemas.Ptr("gs://my-bucket/xxx.png"),
							},
						},
					},
				},
			},
		},
	}, "http", "https", "gs")

	require.NoError(t, err)
	require.Len(t, result.Contents, 1)
	require.Len(t, result.Contents[0].Parts, 2)
	require.NotNil(t, result.Contents[0].Parts[1].FileData)
	assert.Equal(t, "gs://my-bucket/xxx.png", result.Contents[0].Parts[1].FileData.FileURI)
	assert.Equal(t, "image/png", result.Contents[0].Parts[1].FileData.MIMEType)
}

// TestVertexGeminiRejectsUnsupportedScheme guards the upper bound of the Vertex
// scheme allowlist: even though Vertex extends Gemini's defaults with "gs", schemes
// outside that set (file://, ftp://, ...) must still be rejected.
func TestVertexGeminiRejectsUnsupportedScheme(t *testing.T) {
	_, err := gemini.ToGeminiChatCompletionRequestWithImageURLSchemes(nil, &schemas.BifrostChatRequest{
		Model: "gemini-3-flash-preview",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentBlocks: []schemas.ChatContentBlock{
						{
							Type: schemas.ChatContentBlockTypeImage,
							ImageURLStruct: &schemas.ChatInputImage{
								URL: "file:///etc/passwd",
							},
						},
					},
				},
			},
		},
	}, "http", "https", "gs")

	require.Error(t, err)
	assert.Contains(t, err.Error(), `URL scheme "file" is not allowed`)
}
