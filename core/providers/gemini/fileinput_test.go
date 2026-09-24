package gemini

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newGeminiFileResolverTestProvider(t *testing.T, responseBody string) *GeminiProvider {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v1beta/files/abc123" {
			t.Errorf("path = %s, want /v1beta/files/abc123", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(server.Close)

	return NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL + "/v1beta",
			DefaultRequestTimeoutInSeconds: 5,
		},
	}, testNoopLogger{})
}

func geminiFileResolverTestKey() schemas.Key {
	return schemas.Key{Value: *schemas.NewSecretVar("test-key")}
}

func geminiFileResolverTestContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

func requireGeminiFileURI(t *testing.T, request *GeminiGenerationRequest, wantURI string) {
	t.Helper()

	for _, content := range request.Contents {
		for _, part := range content.Parts {
			if part != nil && part.FileData != nil {
				assert.Equal(t, wantURI, part.FileData.FileURI)
				return
			}
		}
	}
	t.Fatalf("converted request did not contain a fileData part")
}

func TestPrepareChatRequestWithResolvedFilesUsesStorageURI(t *testing.T) {
	fileID := "abc123"
	fileType := "application/pdf"
	request := &schemas.BifrostChatRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-2.5-flash",
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{
				ContentBlocks: []schemas.ChatContentBlock{{
					Type: schemas.ChatContentBlockTypeFile,
					File: &schemas.ChatInputFile{
						FileID:   &fileID,
						FileType: &fileType,
					},
				}},
			},
		}},
	}
	provider := newGeminiFileResolverTestProvider(t, "{\"name\":\"files/abc123\",\"uri\":\"https://generativelanguage.googleapis.com/v1beta/files/abc123\",\"mimeType\":\"application/pdf\",\"state\":\"ACTIVE\"}")

	prepared, err := provider.prepareChatRequestWithResolvedFiles(
		geminiFileResolverTestContext(),
		geminiFileResolverTestKey(),
		request,
	)
	require.NoError(t, err)
	require.NotNil(t, prepared.Input[0].Content.ContentBlocks[0].File.FileURL)
	assert.Equal(t, "https://generativelanguage.googleapis.com/v1beta/files/abc123", *prepared.Input[0].Content.ContentBlocks[0].File.FileURL)
	assert.Nil(t, request.Input[0].Content.ContentBlocks[0].File.FileURL, "the caller request must remain unchanged")

	converted, err := ToGeminiChatCompletionRequest(geminiFileResolverTestContext(), prepared)
	require.NoError(t, err)
	requireGeminiFileURI(t, converted, "https://generativelanguage.googleapis.com/v1beta/files/abc123")
}

func TestPrepareResponsesRequestWithResolvedFilesUsesStorageURI(t *testing.T) {
	fileID := "abc123"
	role := schemas.ResponsesInputMessageRoleUser
	request := &schemas.BifrostResponsesRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-2.5-flash",
		Input: []schemas.ResponsesMessage{{
			Role: &role,
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{{
					Type:   schemas.ResponsesInputMessageContentBlockTypeFile,
					FileID: &fileID,
				}},
			},
		}},
	}
	provider := newGeminiFileResolverTestProvider(t, "{\"name\":\"files/abc123\",\"uri\":\"https://generativelanguage.googleapis.com/v1beta/files/abc123\",\"mimeType\":\"application/pdf\",\"state\":\"ACTIVE\"}")

	prepared, err := provider.prepareResponsesRequestWithResolvedFiles(
		geminiFileResolverTestContext(),
		geminiFileResolverTestKey(),
		request,
	)
	require.NoError(t, err)
	require.NotNil(t, prepared.Input[0].Content.ContentBlocks[0].ResponsesInputMessageContentBlockFile)
	file := prepared.Input[0].Content.ContentBlocks[0].ResponsesInputMessageContentBlockFile
	require.NotNil(t, file.FileURL)
	assert.Equal(t, "https://generativelanguage.googleapis.com/v1beta/files/abc123", *file.FileURL)
	assert.Nil(t, request.Input[0].Content.ContentBlocks[0].ResponsesInputMessageContentBlockFile)

	converted, err := ToGeminiResponsesRequest(geminiFileResolverTestContext(), prepared)
	require.NoError(t, err)
	requireGeminiFileURI(t, converted, "https://generativelanguage.googleapis.com/v1beta/files/abc123")
}

func TestPrepareChatRequestWithResolvedFilesPrefersInlineFileData(t *testing.T) {
	fileID := "abc123"
	fileData := "JVBERi0xLjQK"
	request := &schemas.BifrostChatRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-2.5-flash",
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{
				ContentBlocks: []schemas.ChatContentBlock{{
					Type: schemas.ChatContentBlockTypeFile,
					File: &schemas.ChatInputFile{
						FileID:   &fileID,
						FileData: &fileData,
					},
				}},
			},
		}},
	}
	provider := newGeminiFileResolverTestProvider(t, "{\"name\":\"files/abc123\",\"uri\":\"https://generativelanguage.googleapis.com/v1beta/files/abc123\",\"mimeType\":\"application/pdf\",\"state\":\"ACTIVE\"}")

	prepared, err := provider.prepareChatRequestWithResolvedFiles(
		geminiFileResolverTestContext(),
		geminiFileResolverTestKey(),
		request,
	)
	require.NoError(t, err)
	file := prepared.Input[0].Content.ContentBlocks[0].File
	require.NotNil(t, file)
	require.NotNil(t, file.FileData)
	assert.Equal(t, fileData, *file.FileData)
	assert.Nil(t, file.FileURL, "inline file data must take precedence over file ID resolution")
}

func TestPrepareResponsesRequestWithResolvedFilesPrefersInlineFileData(t *testing.T) {
	fileID := "abc123"
	fileData := "JVBERi0xLjQK"
	role := schemas.ResponsesInputMessageRoleUser
	request := &schemas.BifrostResponsesRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-2.5-flash",
		Input: []schemas.ResponsesMessage{{
			Role: &role,
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{{
					Type:   schemas.ResponsesInputMessageContentBlockTypeFile,
					FileID: &fileID,
					ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
						FileData: &fileData,
					},
				}},
			},
		}},
	}
	provider := newGeminiFileResolverTestProvider(t, "{\"name\":\"files/abc123\",\"uri\":\"https://generativelanguage.googleapis.com/v1beta/files/abc123\",\"mimeType\":\"application/pdf\",\"state\":\"ACTIVE\"}")

	prepared, err := provider.prepareResponsesRequestWithResolvedFiles(
		geminiFileResolverTestContext(),
		geminiFileResolverTestKey(),
		request,
	)
	require.NoError(t, err)
	file := prepared.Input[0].Content.ContentBlocks[0].ResponsesInputMessageContentBlockFile
	require.NotNil(t, file)
	require.NotNil(t, file.FileData)
	assert.Equal(t, fileData, *file.FileData)
	assert.Nil(t, file.FileURL, "inline file data must take precedence over file ID resolution")
}
func TestPrepareChatRequestWithResolvedFilesRejectsMissingURI(t *testing.T) {
	fileID := "abc123"
	request := &schemas.BifrostChatRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-2.5-flash",
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{
				ContentBlocks: []schemas.ChatContentBlock{{
					Type: schemas.ChatContentBlockTypeFile,
					File: &schemas.ChatInputFile{FileID: &fileID},
				}},
			},
		}},
	}
	provider := newGeminiFileResolverTestProvider(t, "{\"name\":\"files/abc123\",\"mimeType\":\"application/pdf\",\"state\":\"ACTIVE\"}")

	_, err := provider.prepareChatRequestWithResolvedFiles(
		geminiFileResolverTestContext(),
		geminiFileResolverTestKey(),
		request,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not return a storage URI")
}
