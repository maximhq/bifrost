package openai

import (
	"testing"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// The Anthropic integration maps tool_choice {"type": "any"} to the
// provider-generic string "any". OpenAI accepts only "none", "auto" and
// "required" as string tool choices, so "any" must be serialized as "required"
// on both OpenAI egress paths while every other choice is left untouched and
// destinations that accept "any" natively keep it.

// responsesToolChoiceRequest builds a minimal Responses request with one user
// message so conversion reaches the tool-choice handling.
func responsesToolChoiceRequest(provider schemas.ModelProvider, model string, toolChoice *schemas.ResponsesToolChoice) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    model,
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("What is the weather in Tokyo?")},
		}},
		Params: &schemas.ResponsesParameters{ToolChoice: toolChoice},
	}
}

// chatToolChoiceRequest builds a minimal Chat request with one user message so
// conversion reaches the tool-choice handling.
func chatToolChoiceRequest(provider schemas.ModelProvider, model string, toolChoice *schemas.ChatToolChoice) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What is the weather in Tokyo?")},
		}},
		Params: &schemas.ChatParameters{ToolChoice: toolChoice},
	}
}

// marshalToolChoice returns the wire JSON for a converted tool choice.
func marshalToolChoice(t *testing.T, toolChoice any) string {
	t.Helper()
	got, err := sonic.Marshal(toolChoice)
	if err != nil {
		t.Fatalf("marshal tool_choice: %v", err)
	}
	return string(got)
}

// TestToOpenAIResponsesRequest_ToolChoiceAnyBecomesRequired verifies the
// Responses egress maps "any" to "required" for OpenAI, keeps every other
// choice as is, and leaves "any" intact for Mistral destinations.
func TestToOpenAIResponsesRequest_ToolChoiceAnyBecomesRequired(t *testing.T) {
	tests := []struct {
		name       string
		provider   schemas.ModelProvider
		model      string
		toolChoice *schemas.ResponsesToolChoice
		wantJSON   string
	}{
		{
			name:       "any is mapped to required",
			provider:   schemas.OpenAI,
			model:      "gpt-4o",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("any")},
			wantJSON:   `"required"`,
		},
		{
			name:       "required is preserved",
			provider:   schemas.OpenAI,
			model:      "gpt-4o",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("required")},
			wantJSON:   `"required"`,
		},
		{
			name:       "auto is preserved",
			provider:   schemas.OpenAI,
			model:      "gpt-4o",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("auto")},
			wantJSON:   `"auto"`,
		},
		{
			name:       "none is preserved",
			provider:   schemas.OpenAI,
			model:      "gpt-4o",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("none")},
			wantJSON:   `"none"`,
		},
		{
			name:     "named function is preserved",
			provider: schemas.OpenAI,
			model:    "gpt-4o",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
				Type: schemas.ResponsesToolChoiceTypeFunction,
				Name: schemas.Ptr("get_weather"),
			}},
			wantJSON: `{"type":"function","name":"get_weather"}`,
		},
		{
			name:       "Mistral keeps any",
			provider:   schemas.Mistral,
			model:      "mistral-large-latest",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("any")},
			wantJSON:   `"any"`,
		},
		{
			name:       "Mistral on Vertex keeps any",
			provider:   schemas.Vertex,
			model:      "mistral-large",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("any")},
			wantJSON:   `"any"`,
		},
		{
			name:       "Fireworks keeps any",
			provider:   schemas.Fireworks,
			model:      "accounts/fireworks/models/kimi-k2-instruct",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("any")},
			wantJSON:   `"any"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ToOpenAIResponsesRequest(nil, responsesToolChoiceRequest(tt.provider, tt.model, tt.toolChoice))
			if result == nil {
				t.Fatal("ToOpenAIResponsesRequest returned nil")
			}
			if got := marshalToolChoice(t, result.ToolChoice); got != tt.wantJSON {
				t.Fatalf("tool_choice on the wire = %s, want %s", got, tt.wantJSON)
			}
		})
	}
}

// TestToOpenAIResponsesRequest_ToolChoiceAnyDoesNotMutateInput verifies the
// Responses egress rewrites a copy of the tool choice, not the caller's value.
func TestToOpenAIResponsesRequest_ToolChoiceAnyDoesNotMutateInput(t *testing.T) {
	toolChoice := &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("any")}
	bifrostReq := responsesToolChoiceRequest(schemas.OpenAI, "gpt-4o", toolChoice)

	result := ToOpenAIResponsesRequest(nil, bifrostReq)
	if result == nil {
		t.Fatal("ToOpenAIResponsesRequest returned nil")
	}
	if got := marshalToolChoice(t, result.ToolChoice); got != `"required"` {
		t.Fatalf("conversion did not run: tool_choice on the wire = %s", got)
	}
	if got := *bifrostReq.Params.ToolChoice.ResponsesToolChoiceStr; got != "any" {
		t.Fatalf("caller's tool_choice was mutated to %q", got)
	}
	if bifrostReq.Params.ToolChoice != toolChoice {
		t.Fatal("caller's tool_choice pointer was replaced")
	}
}

// TestToOpenAIChatRequest_ToolChoiceAnyBecomesRequired verifies the Chat
// egress maps "any" to "required" for OpenAI, keeps every other choice as is,
// and leaves "any" intact for Mistral destinations.
func TestToOpenAIChatRequest_ToolChoiceAnyBecomesRequired(t *testing.T) {
	tests := []struct {
		name       string
		provider   schemas.ModelProvider
		model      string
		toolChoice *schemas.ChatToolChoice
		wantJSON   string
	}{
		{
			name:       "any is mapped to required",
			provider:   schemas.OpenAI,
			model:      "gpt-4o",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("any")},
			wantJSON:   `"required"`,
		},
		{
			name:       "required is preserved",
			provider:   schemas.OpenAI,
			model:      "gpt-4o",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("required")},
			wantJSON:   `"required"`,
		},
		{
			name:       "auto is preserved",
			provider:   schemas.OpenAI,
			model:      "gpt-4o",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("auto")},
			wantJSON:   `"auto"`,
		},
		{
			name:       "none is preserved",
			provider:   schemas.OpenAI,
			model:      "gpt-4o",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("none")},
			wantJSON:   `"none"`,
		},
		{
			name:     "named function is preserved",
			provider: schemas.OpenAI,
			model:    "gpt-4o",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
				Type:     schemas.ChatToolChoiceTypeFunction,
				Function: &schemas.ChatToolChoiceFunction{Name: "get_weather"},
			}},
			wantJSON: `{"type":"function","function":{"name":"get_weather"}}`,
		},
		{
			name:       "Mistral keeps any",
			provider:   schemas.Mistral,
			model:      "mistral-large-latest",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("any")},
			wantJSON:   `"any"`,
		},
		{
			name:       "Mistral on Vertex keeps any",
			provider:   schemas.Vertex,
			model:      "mistral-large",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("any")},
			wantJSON:   `"any"`,
		},
		{
			name:       "Fireworks keeps any",
			provider:   schemas.Fireworks,
			model:      "accounts/fireworks/models/kimi-k2-instruct",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("any")},
			wantJSON:   `"any"`,
		},
		{
			name:       "non-Mistral model on Vertex is mapped to required",
			provider:   schemas.Vertex,
			model:      "moonshotai/kimi-k2-thinking-maas",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("any")},
			wantJSON:   `"required"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline), chatToolChoiceRequest(tt.provider, tt.model, tt.toolChoice))
			if result == nil {
				t.Fatal("ToOpenAIChatRequest returned nil")
			}
			if got := marshalToolChoice(t, result.ToolChoice); got != tt.wantJSON {
				t.Fatalf("tool_choice on the wire = %s, want %s", got, tt.wantJSON)
			}
		})
	}
}

// TestToOpenAIChatRequest_ToolChoiceAnyDoesNotMutateInput verifies the Chat
// egress rewrites a copy of the tool choice, not the caller's value.
func TestToOpenAIChatRequest_ToolChoiceAnyDoesNotMutateInput(t *testing.T) {
	toolChoice := &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("any")}
	bifrostReq := chatToolChoiceRequest(schemas.OpenAI, "gpt-4o", toolChoice)

	result := ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline), bifrostReq)
	if result == nil {
		t.Fatal("ToOpenAIChatRequest returned nil")
	}
	if got := marshalToolChoice(t, result.ToolChoice); got != `"required"` {
		t.Fatalf("conversion did not run: tool_choice on the wire = %s", got)
	}
	if got := *bifrostReq.Params.ToolChoice.ChatToolChoiceStr; got != "any" {
		t.Fatalf("caller's tool_choice was mutated to %q", got)
	}
	if bifrostReq.Params.ToolChoice != toolChoice {
		t.Fatal("caller's tool_choice pointer was replaced")
	}
}

// setToolChoiceCaps installs a resolver answering for one (provider, model)
// pair, so the converter takes its datasheet branch rather than the name-based
// fallback in toolChoiceAnySupported / DefaultSupportsForcedToolChoice.
func setToolChoiceCaps(t *testing.T, provider schemas.ModelProvider, model string, ov schemas.ModelCapabilities) {
	t.Helper()
	providerUtils.SetCapabilityResolver(func(p schemas.ModelProvider, m string) *schemas.ModelCapabilities {
		if p == provider && m == model {
			return &ov
		}
		return nil
	})
	t.Cleanup(func() { providerUtils.SetCapabilityResolver(nil) })
}

// TestToolChoiceAny_DatasheetOutranksFallback verifies the datasheet answers
// first in both directions: a row can keep "any" on a provider whose fallback
// would rewrite it, and rewrite it on one whose fallback would keep it.
func TestToolChoiceAny_DatasheetOutranksFallback(t *testing.T) {
	t.Run("row keeps any where the fallback would rewrite", func(t *testing.T) {
		yes := true
		setToolChoiceCaps(t, schemas.OpenAI, "gpt-4o", schemas.ModelCapabilities{ToolChoiceAnySupported: &yes})

		result := ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline),
			chatToolChoiceRequest(schemas.OpenAI, "gpt-4o", &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("any")}))
		if got := marshalToolChoice(t, result.ToolChoice); got != `"any"` {
			t.Fatalf("tool_choice on the wire = %s, want \"any\"", got)
		}
	})

	t.Run("row rewrites any where the fallback would keep it", func(t *testing.T) {
		no := false
		setToolChoiceCaps(t, schemas.Mistral, "mistral-large-latest", schemas.ModelCapabilities{ToolChoiceAnySupported: &no})

		result := ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline),
			chatToolChoiceRequest(schemas.Mistral, "mistral-large-latest", &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("any")}))
		if got := marshalToolChoice(t, result.ToolChoice); got != `"required"` {
			t.Fatalf("tool_choice on the wire = %s, want \"required\"", got)
		}
	})
}

// TestForcedToolChoiceDroppedForFable51 covers Claude Fable 5.1 reached over an
// OpenAI-compatible surface (Databricks, Bedrock Mantle): forced tool use
// returns a 400 there, so the choice is dropped and the model answers under
// the default "auto". Fable 5 still supports it and is untouched.
func TestForcedToolChoiceDroppedForFable51(t *testing.T) {
	forced := []struct {
		name string
		chat *schemas.ChatToolChoice
		resp *schemas.ResponsesToolChoice
	}{
		{
			name: "required",
			chat: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("required")},
			resp: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("required")},
		},
		{
			name: "any",
			chat: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("any")},
			resp: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("any")},
		},
		{
			name: "named function",
			chat: &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
				Type: schemas.ChatToolChoiceTypeFunction, Function: &schemas.ChatToolChoiceFunction{Name: "get_weather"}}},
			resp: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
				Type: schemas.ResponsesToolChoiceTypeFunction, Name: schemas.Ptr("get_weather")}},
		},
	}

	for _, tt := range forced {
		t.Run("fable-5-1 drops "+tt.name, func(t *testing.T) {
			chat := ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline),
				chatToolChoiceRequest(schemas.Databricks, "claude-fable-5-1", tt.chat))
			if chat.ToolChoice != nil {
				t.Errorf("chat tool_choice = %s, want it dropped", marshalToolChoice(t, chat.ToolChoice))
			}
			resp := ToOpenAIResponsesRequest(nil, responsesToolChoiceRequest(schemas.Databricks, "claude-fable-5-1", tt.resp))
			if resp.ToolChoice != nil {
				t.Errorf("responses tool_choice = %s, want it dropped", marshalToolChoice(t, resp.ToolChoice))
			}
		})

		t.Run("fable-5 keeps "+tt.name, func(t *testing.T) {
			chat := ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline),
				chatToolChoiceRequest(schemas.Databricks, "claude-fable-5", tt.chat))
			if chat.ToolChoice == nil {
				t.Error("chat tool_choice was dropped for fable-5, which supports forced tool use")
			}
		})
	}

	t.Run("auto survives on fable-5-1", func(t *testing.T) {
		chat := ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline),
			chatToolChoiceRequest(schemas.Databricks, "claude-fable-5-1", &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("auto")}))
		if got := marshalToolChoice(t, chat.ToolChoice); got != `"auto"` {
			t.Fatalf("tool_choice on the wire = %s, want \"auto\"", got)
		}
	})

	t.Run("datasheet can re-enable forced tool use", func(t *testing.T) {
		yes := true
		setToolChoiceCaps(t, schemas.Databricks, "claude-fable-5-1", schemas.ModelCapabilities{SupportsForcedToolChoice: &yes})

		chat := ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline),
			chatToolChoiceRequest(schemas.Databricks, "claude-fable-5-1", &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("required")}))
		if got := marshalToolChoice(t, chat.ToolChoice); got != `"required"` {
			t.Fatalf("tool_choice on the wire = %s, want \"required\"", got)
		}
	})
}
