package handlers

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func terminalChatChunk(usage *schemas.BifrostLLMUsage) *schemas.BifrostChatResponse {
	content := "hello"
	stop := "stop"
	return &schemas.BifrostChatResponse{
		ID:      "chatcmpl-test",
		Object:  "chat.completion.chunk",
		Created: 1,
		Model:   "test-model",
		Choices: []schemas.BifrostResponseChoice{{
			Index:        0,
			FinishReason: &stop,
			ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
				Delta: &schemas.ChatStreamResponseChoiceDelta{Content: &content},
			},
		}},
		Usage: usage,
	}
}

func decodeJSONMap(t *testing.T, encoded []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatalf("unmarshal wire event: %v", err)
	}
	return value
}

func TestMarshalChatCompletionStreamEvents_DefaultOmitsUsage(t *testing.T) {
	response := terminalChatChunk(&schemas.BifrostLLMUsage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3})
	primary, usage, err := marshalChatCompletionStreamEvents(response, false)
	if err != nil {
		t.Fatal(err)
	}
	if usage != nil {
		t.Fatalf("unexpected usage event: %s", usage)
	}
	wire := decodeJSONMap(t, primary)
	if _, ok := wire["usage"]; ok {
		t.Fatalf("default stream exposed usage: %s", primary)
	}
	choices := wire["choices"].([]any)
	choice := choices[0].(map[string]any)
	if _, ok := choice["delta"]; !ok {
		t.Fatalf("terminal choice is not a delta: %s", primary)
	}
	if _, ok := choice["message"]; ok {
		t.Fatalf("terminal choice contains message: %s", primary)
	}
	if response.Usage == nil {
		t.Fatal("wire normalization mutated internal accounting response")
	}
}

func TestMarshalChatCompletionStreamEvents_IncludeUsageSplitsTerminalChunk(t *testing.T) {
	response := terminalChatChunk(&schemas.BifrostLLMUsage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3})
	primary, usage, err := marshalChatCompletionStreamEvents(response, true)
	if err != nil {
		t.Fatal(err)
	}
	primaryWire := decodeJSONMap(t, primary)
	if value, ok := primaryWire["usage"]; !ok || value != nil {
		t.Fatalf("ordinary include_usage chunk must carry usage:null: %s", primary)
	}
	if len(primaryWire["choices"].([]any)) != 1 {
		t.Fatalf("ordinary terminal chunk lost its choice: %s", primary)
	}

	usageWire := decodeJSONMap(t, usage)
	if len(usageWire["choices"].([]any)) != 0 {
		t.Fatalf("usage event choices must be empty: %s", usage)
	}
	usageValue, ok := usageWire["usage"].(map[string]any)
	if !ok || usageValue["total_tokens"] != float64(3) {
		t.Fatalf("usage event missing totals: %s", usage)
	}
}

func TestMarshalChatCompletionStreamEvents_IncludeUsageAddsNullToOrdinaryChunk(t *testing.T) {
	response := terminalChatChunk(nil)
	response.Choices[0].FinishReason = nil
	primary, usage, err := marshalChatCompletionStreamEvents(response, true)
	if err != nil {
		t.Fatal(err)
	}
	if usage != nil {
		t.Fatalf("unexpected usage event: %s", usage)
	}
	wire := decodeJSONMap(t, primary)
	if value, ok := wire["usage"]; !ok || value != nil {
		t.Fatalf("ordinary include_usage chunk must carry usage:null: %s", primary)
	}
}

func TestMarshalChatCompletionStreamEvents_UsageOnlyChunkHonorsRequest(t *testing.T) {
	response := terminalChatChunk(&schemas.BifrostLLMUsage{TotalTokens: 3})
	response.Choices = []schemas.BifrostResponseChoice{}

	primary, usage, err := marshalChatCompletionStreamEvents(response, false)
	if err != nil {
		t.Fatal(err)
	}
	if primary != nil || usage != nil {
		t.Fatalf("default stream exposed usage-only event: primary=%s usage=%s", primary, usage)
	}

	primary, usage, err = marshalChatCompletionStreamEvents(response, true)
	if err != nil {
		t.Fatal(err)
	}
	if primary != nil {
		t.Fatalf("usage-only response produced ordinary event: %s", primary)
	}
	wire := decodeJSONMap(t, usage)
	if len(wire["choices"].([]any)) != 0 {
		t.Fatalf("usage-only event choices must be empty: %s", usage)
	}
}
