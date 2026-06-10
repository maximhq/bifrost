package gemini_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestToGeminiChatCompletionRequestDownloadsAudioURL(t *testing.T) {
	restore := providerUtils.AllowPrivateAudioURLsForTest()
	defer restore()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("audio-bytes"))
	}))
	defer server.Close()

	format := "wav"
	result, err := gemini.ToGeminiChatCompletionRequest(context.Background(), &schemas.BifrostChatRequest{
		Model: "gemini-2.0-flash",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
					{
						Type: schemas.ChatContentBlockTypeInputAudio,
						InputAudio: &schemas.ChatInputAudio{
							URL:    server.URL,
							Format: &format,
						},
					},
				}},
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, result.Contents, 1)
	require.Len(t, result.Contents[0].Parts, 1)
	require.NotNil(t, result.Contents[0].Parts[0].InlineData)
	require.Equal(t, "audio/wav", result.Contents[0].Parts[0].InlineData.MIMEType)
	require.NotEmpty(t, result.Contents[0].Parts[0].InlineData.Data)
}

func TestToGeminiResponsesRequestDownloadsAudioURL(t *testing.T) {
	restore := providerUtils.AllowPrivateAudioURLsForTest()
	defer restore()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("audio-bytes"))
	}))
	defer server.Close()

	role := schemas.ResponsesInputMessageRoleUser
	result, err := gemini.ToGeminiResponsesRequest(context.Background(), &schemas.BifrostResponsesRequest{
		Model: "gemini-2.0-flash",
		Input: []schemas.ResponsesMessage{
			{
				Role: &role,
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{
						Type: schemas.ResponsesInputMessageContentBlockTypeAudio,
						Audio: &schemas.ResponsesInputMessageContentBlockAudio{
							URL:    server.URL,
							Format: "wav",
						},
					},
				}},
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, result.Contents, 1)
	require.Len(t, result.Contents[0].Parts, 1)
	require.NotNil(t, result.Contents[0].Parts[0].InlineData)
	require.Equal(t, "audio/wav", result.Contents[0].Parts[0].InlineData.MIMEType)
	require.NotEmpty(t, result.Contents[0].Parts[0].InlineData.Data)
}

func TestToGeminiChatCompletionRequestPropagatesAudioDownloadErrors(t *testing.T) {
	// Guard active: an http URL to a non-loopback host should error and the
	// error must propagate up through the conversion path.
	format := "wav"
	_, err := gemini.ToGeminiChatCompletionRequest(context.Background(), &schemas.BifrostChatRequest{
		Model: "gemini-2.0-flash",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
					{
						Type: schemas.ChatContentBlockTypeInputAudio,
						InputAudio: &schemas.ChatInputAudio{
							URL:    "http://169.254.169.254/audio.mp3",
							Format: &format,
						},
					},
				}},
			},
		},
	})
	require.Error(t, err, "audio download errors must propagate up the chat path")
}
