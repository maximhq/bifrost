package integrations

import (
	"io"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestAnthropicStreamPrefersResponsesChunkWhenChatResponseAlsoPresent(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 1)
	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{},
		BifrostResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeCompleted,
		},
	}
	close(stream)

	config := RouteConfig{
		Type: RouteConfigTypeAnthropic,
		StreamConfig: &StreamConfig{
			ResponsesStreamResponseConverter: func(_ *schemas.BifrostContext, resp *schemas.BifrostResponsesStreamResponse) (string, interface{}, error) {
				return "message_stop", map[string]string{"type": "message_stop"}, nil
			},
		},
	}
	router := NewGenericRouter(nil, &mockHandlerStore{}, nil, nil, nil, bifrost.NewNoOpLogger())
	ctx := &fasthttp.RequestCtx{}
	router.handleStreaming(ctx, nil, config, stream, func() {})
	body, err := io.ReadAll(ctx.Response.BodyStream())
	require.NoError(t, err)
	require.True(t, strings.Contains(string(body), "event: message_stop"), string(body))
}

func TestAnthropicStreamConvertsChatOnlyChunks(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 2)
	stream <- &schemas.BifrostStreamChunk{BifrostChatResponse: &schemas.BifrostChatResponse{
		ID: "msg_test", Model: "claude-sonnet-4-5",
		Choices: []schemas.BifrostResponseChoice{{ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: &schemas.ChatStreamResponseChoiceDelta{Role: schemas.Ptr("assistant")}}}},
	}}
	stop := "stop"
	stream <- &schemas.BifrostStreamChunk{BifrostChatResponse: &schemas.BifrostChatResponse{
		ID: "msg_test", Model: "claude-sonnet-4-5",
		Choices: []schemas.BifrostResponseChoice{{FinishReason: &stop, ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: &schemas.ChatStreamResponseChoiceDelta{}}}},
	}}
	close(stream)
	config := RouteConfig{Type: RouteConfigTypeAnthropic, StreamConfig: &StreamConfig{
		ResponsesStreamResponseConverter: func(_ *schemas.BifrostContext, resp *schemas.BifrostResponsesStreamResponse) (string, interface{}, error) {
			if resp.Type == schemas.ResponsesStreamResponseTypeCompleted {
				return "message_stop", map[string]string{"type": "message_stop"}, nil
			}
			return "", nil, nil
		},
	}}
	router := NewGenericRouter(nil, &mockHandlerStore{}, nil, nil, nil, bifrost.NewNoOpLogger())
	ctx := &fasthttp.RequestCtx{}
	router.handleStreaming(ctx, nil, config, stream, func() {})
	body, err := io.ReadAll(ctx.Response.BodyStream())
	require.NoError(t, err)
	require.Contains(t, string(body), "event: message_stop")
}
