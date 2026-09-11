package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// input_video content blocks (llama.cpp-style OpenAI-compatible dialect) must
// round-trip through schemas.Unmarshal/Marshal without losing their payload,
// so gateways can pass them through to backends that support video input.

func TestSonic_ChatContentBlock_InputVideo_RoundTrip(t *testing.T) {
	input := `{"type":"input_video","input_video":{"data":"AAAA","format":"mp4"}}`

	var b ChatContentBlock
	err := Unmarshal([]byte(input), &b)
	require.NoError(t, err)

	assert.Equal(t, ChatContentBlockTypeInputVideo, b.Type)
	require.NotNil(t, b.InputVideo)
	require.NotNil(t, b.InputVideo.Data)
	assert.Equal(t, "AAAA", *b.InputVideo.Data)
	require.NotNil(t, b.InputVideo.Format)
	assert.Equal(t, "mp4", *b.InputVideo.Format)
	assert.Nil(t, b.InputVideo.URL)

	output, err := Marshal(b)
	require.NoError(t, err)

	// Structural round-trip: re-unmarshal the marshaled output and verify the
	// nested payload survived (string containment alone can pass on type only).
	var b2 ChatContentBlock
	require.NoError(t, Unmarshal(output, &b2))
	assert.Equal(t, ChatContentBlockTypeInputVideo, b2.Type)
	require.NotNil(t, b2.InputVideo)
	require.NotNil(t, b2.InputVideo.Data)
	assert.Equal(t, "AAAA", *b2.InputVideo.Data)
	require.NotNil(t, b2.InputVideo.Format)
	assert.Equal(t, "mp4", *b2.InputVideo.Format)
}

func TestSonic_ChatContentBlock_InputVideo_URLVariant(t *testing.T) {
	input := `{"type":"input_video","input_video":{"url":"https://example.com/clip.mp4"}}`

	var b ChatContentBlock
	err := Unmarshal([]byte(input), &b)
	require.NoError(t, err)

	assert.Equal(t, ChatContentBlockTypeInputVideo, b.Type)
	require.NotNil(t, b.InputVideo)
	require.NotNil(t, b.InputVideo.URL)
	assert.Equal(t, "https://example.com/clip.mp4", *b.InputVideo.URL)
	assert.Nil(t, b.InputVideo.Data)

	output, err := Marshal(b)
	require.NoError(t, err)

	var b2 ChatContentBlock
	require.NoError(t, Unmarshal(output, &b2))
	require.NotNil(t, b2.InputVideo)
	require.NotNil(t, b2.InputVideo.URL)
	assert.Equal(t, "https://example.com/clip.mp4", *b2.InputVideo.URL)
	assert.Nil(t, b2.InputVideo.Data)
}

func TestChatToResponses_InputVideo_PayloadPreserved(t *testing.T) {
	data := "AAAA"
	format := "mp4"
	msg := ChatMessage{
		Role: ChatMessageRoleUser,
		Content: &ChatMessageContent{
			ContentBlocks: []ChatContentBlock{{
				Type:       ChatContentBlockTypeInputVideo,
				InputVideo: &ChatInputVideo{Data: &data, Format: &format},
			}},
		},
	}

	respMsgs := msg.ToResponsesMessages()
	require.Len(t, respMsgs, 1)
	require.NotNil(t, respMsgs[0].Content)
	require.Len(t, respMsgs[0].Content.ContentBlocks, 1)

	block := respMsgs[0].Content.ContentBlocks[0]
	assert.Equal(t, ResponsesInputMessageContentBlockTypeVideo, block.Type)
	require.NotNil(t, block.Video, "video payload must survive Chat→Responses conversion")
	require.NotNil(t, block.Video.Data)
	assert.Equal(t, "AAAA", *block.Video.Data)
	require.NotNil(t, block.Video.Format)
	assert.Equal(t, "mp4", *block.Video.Format)
}
