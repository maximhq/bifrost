package integrations

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

var humeStreamBenchmarkSink interface{}

func BenchmarkHumeChatStreamResponseConverter(b *testing.B) {
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostCtx.SetValue(humeSessionContextKey{}, "hume-session")
	role := "assistant"
	content := "hello"
	toolType := "function"
	toolID := "call-1"
	toolName := "lookup"

	benchmarks := []struct {
		name     string
		response *schemas.BifrostChatResponse
	}{
		{
			name: "text",
			response: &schemas.BifrostChatResponse{
				ID:      "chatcmpl-text",
				Created: 123,
				Model:   "gpt-4o-mini",
				Choices: []schemas.BifrostResponseChoice{{
					Index: 0,
					ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: &schemas.ChatStreamResponseChoiceDelta{
						Role:    &role,
						Content: &content,
					}},
				}},
			},
		},
		{
			name: "tool_call",
			response: &schemas.BifrostChatResponse{
				ID:      "chatcmpl-tool",
				Created: 123,
				Model:   "gpt-4o-mini",
				Choices: []schemas.BifrostResponseChoice{{
					Index: 0,
					ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: &schemas.ChatStreamResponseChoiceDelta{
						ToolCalls: []schemas.ChatAssistantMessageToolCall{{
							Index: 0,
							Type:  &toolType,
							ID:    &toolID,
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      &toolName,
								Arguments: `{"query":"x"}`,
							},
						}},
					}},
				}},
			},
		},
	}

	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_, converted, err := humeChatStreamResponseConverter(bifrostCtx, benchmark.response)
				if err != nil {
					b.Fatal(err)
				}
				humeStreamBenchmarkSink = converted
			}
		})
	}
}
