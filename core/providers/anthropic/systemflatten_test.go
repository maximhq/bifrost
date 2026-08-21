package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

// Regression test for issue #6245: an Anthropic-format request with a multi-block
// top-level `system` field (what Claude Code sends) must reach chat-style providers
// (e.g. ollama, which routes Responses through ToChatRequest) as exactly ONE system
// message at position 0 with plain string content. OpenAI-compatible servers like
// ollama expand a content-parts array into one message per part, so a multi-part
// system message becomes [system, system, system] upstream and is rejected with
// "system message must be at the beginning".
func TestMultiBlockSystemToChatRequest(t *testing.T) {
	rawReq := []byte(`{
		"model": "ollama/llama3.1",
		"max_tokens": 512,
		"system": [
			{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
			{"type": "text", "text": "Block B: follow the project conventions.", "cache_control": {"type": "ephemeral"}},
			{"type": "text", "text": "Block C: answer concisely."}
		],
		"messages": [
			{"role": "user", "content": "hello"}
		]
	}`)

	var anthropicReq AnthropicMessageRequest
	if err := json.Unmarshal(rawReq, &anthropicReq); err != nil {
		t.Fatalf("failed to unmarshal anthropic request: %v", err)
	}

	// Ingress conversion (what /anthropic/v1/messages does), then the responses→chat
	// fallback used by chat-style providers (core/providers/ollama/ollama.go).
	chatReq := anthropicReq.ToBifrostResponsesRequest(nil).ToChatRequest()

	dumpChat, _ := json.MarshalIndent(chatReq.Input, "", "  ")

	chatSysCount := 0
	for _, m := range chatReq.Input {
		if m.Role == schemas.ChatMessageRoleSystem {
			chatSysCount++
		}
	}
	if chatSysCount != 1 {
		t.Errorf("expected exactly 1 system message in chat request, got %d:\n%s", chatSysCount, dumpChat)
	}
	if len(chatReq.Input) == 0 || chatReq.Input[0].Role != schemas.ChatMessageRoleSystem {
		t.Fatalf("expected system message at position 0, got messages:\n%s", dumpChat)
	}
	if len(chatReq.Input) != 2 {
		t.Errorf("expected 2 chat messages total (system + user), got %d", len(chatReq.Input))
	}

	sys := chatReq.Input[0]
	if sys.Content == nil || sys.Content.ContentStr == nil {
		t.Fatalf("expected multi-block system prompt to be flattened to string content, got:\n%s", dumpChat)
	}
	want := "You are Claude Code, Anthropic's official CLI for Claude.\n\nBlock B: follow the project conventions.\n\nBlock C: answer concisely."
	if *sys.Content.ContentStr != want {
		t.Errorf("flattened system prompt mismatch:\ngot:  %q\nwant: %q", *sys.Content.ContentStr, want)
	}

	// The outbound openai-compatible payload must carry the same single string prompt.
	wireMessages := openai.ConvertBifrostMessagesToOpenAIMessages(chatReq.Input)
	dumpWire, err := json.Marshal(wireMessages[0])
	if err != nil {
		t.Fatalf("marshal wire message: %v", err)
	}
	var wireSys struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	if err := json.Unmarshal(dumpWire, &wireSys); err != nil {
		t.Fatalf("unmarshal wire message: %v", err)
	}
	if wireSys.Role != "system" {
		t.Errorf("expected wire role system, got %q", wireSys.Role)
	}
	if _, isString := wireSys.Content.(string); !isString {
		t.Errorf("expected wire system content to be a string, got %T: %s", wireSys.Content, dumpWire)
	}
}

// One text block already collapsed to string content before the fix; multi-block user
// messages must keep their content array (image/file parts depend on it).
func TestSystemFlattenShapes(t *testing.T) {
	buildChat := func(t *testing.T, bodyJSON string) *schemas.BifrostChatRequest {
		t.Helper()
		var req AnthropicMessageRequest
		if err := json.Unmarshal([]byte(bodyJSON), &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return req.ToBifrostResponsesRequest(nil).ToChatRequest()
	}

	t.Run("single system block stays plain string", func(t *testing.T) {
		chatReq := buildChat(t, `{
			"model": "ollama/llama3.1",
			"max_tokens": 512,
			"system": [{"type":"text","text":"only block"}],
			"messages": [{"role": "user", "content": "hello"}]
		}`)
		sys := chatReq.Input[0]
		if sys.Role != schemas.ChatMessageRoleSystem {
			t.Fatalf("expected system first, got %s", sys.Role)
		}
		if sys.Content == nil || sys.Content.ContentStr == nil || *sys.Content.ContentStr != "only block" {
			t.Fatalf("expected single-block system to be plain string content, got: %+v", sys.Content)
		}
	})

	t.Run("multi-block developer message flattens to string", func(t *testing.T) {
		devRole := schemas.ResponsesInputMessageRoleDeveloper
		chatMessages := schemas.ToChatMessages([]schemas.ResponsesMessage{
			{
				Role: &devRole,
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: schemas.Ptr("dev block A")},
						{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: schemas.Ptr("dev block B")},
					},
				},
			},
		})
		if len(chatMessages) != 1 {
			t.Fatalf("expected 1 chat message, got %d", len(chatMessages))
		}
		dev := chatMessages[0]
		if dev.Role != schemas.ChatMessageRoleDeveloper {
			t.Fatalf("expected developer role, got %s", dev.Role)
		}
		if dev.Content == nil || dev.Content.ContentStr == nil {
			t.Fatalf("expected multi-block developer message to flatten to string content, got: %+v", dev.Content)
		}
		if want := "dev block A\n\ndev block B"; *dev.Content.ContentStr != want {
			t.Errorf("flattened developer prompt mismatch:\ngot:  %q\nwant: %q", *dev.Content.ContentStr, want)
		}
	})

	t.Run("multi-block user message keeps content array", func(t *testing.T) {
		// Flattening applies to system/developer roles only: user messages keep their
		// content array through ToChatMessages (image/file parts depend on it).
		userRole := schemas.ResponsesInputMessageRoleUser
		chatMessages := schemas.ToChatMessages([]schemas.ResponsesMessage{
			{
				Role: &userRole,
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: schemas.Ptr("part one")},
						{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: schemas.Ptr("part two")},
					},
				},
			},
		})
		if len(chatMessages) != 1 {
			t.Fatalf("expected 1 chat message, got %d", len(chatMessages))
		}
		user := chatMessages[0]
		if user.Role != schemas.ChatMessageRoleUser {
			t.Fatalf("expected user role, got %s", user.Role)
		}
		if user.Content == nil || user.Content.ContentBlocks == nil {
			t.Fatalf("expected multi-block user message to keep content array, got: %+v", user.Content)
		}
		if len(user.Content.ContentBlocks) != 2 {
			t.Fatalf("expected 2 user content blocks, got %d", len(user.Content.ContentBlocks))
		}
	})
}
