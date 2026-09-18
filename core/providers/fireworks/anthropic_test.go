package fireworks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// anthropicMessagesStub serves Fireworks' Anthropic-compatible Messages endpoint
// and captures the outbound body so a test can assert on what Bifrost sent.
func anthropicMessagesStub(t *testing.T, captured *map[string]any) *httptest.Server {
	t.Helper()
	// The handler runs on its own goroutine, so it reports with t.Errorf and a
	// non-200 rather than t.Fatalf: FailNow is only legal on the test's own
	// goroutine, and aborting mid-write surfaces an opaque transport error
	// instead of the assertion message.
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %q, want /v1/messages", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, captured); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, "decode body", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"accounts/fireworks/models/deepseek-v4p1-flash","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
}

func outboundToolTypes(t *testing.T, captured map[string]any) []string {
	t.Helper()
	raw, ok := captured["tools"].([]any)
	if !ok {
		return nil
	}
	types := make([]string, 0, len(raw))
	for _, entry := range raw {
		tool, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		// Function tools carry no "type" on the Anthropic wire; identify them by name.
		if tpe, ok := tool["type"].(string); ok {
			types = append(types, tpe)
			continue
		}
		if name, ok := tool["name"].(string); ok {
			types = append(types, "function:"+name)
		}
	}
	return types
}

func webSearchTool() schemas.ResponsesTool {
	return schemas.ResponsesTool{Type: schemas.ResponsesToolTypeWebSearch}
}

func lookupTool() schemas.ResponsesTool {
	return schemas.ResponsesTool{
		Type: schemas.ResponsesToolTypeFunction,
		Name: schemas.Ptr("lookup"),
		ResponsesToolFunction: &schemas.ResponsesToolFunction{
			Parameters: &schemas.ToolFunctionParameters{Type: "object"},
		},
	}
}

// TestResponses_AnthropicEndpointDropsServerWebSearch pins the fix for the
// Fireworks 400 'tools: server-side web search ("web_search_20250305") is not
// supported on this endpoint'. A client whose built-in web search is on by
// default (Codex, for one) sends a Responses web_search tool on every request;
// Fireworks' Anthropic-compatible endpoint has no Anthropic-hosted server tools,
// so Bifrost must drop it and keep the caller's function tools instead of
// letting the whole request fail.
func TestResponses_AnthropicEndpointDropsServerWebSearch(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := anthropicMessagesStub(t, &captured)
	defer server.Close()

	provider := newTestFireworksProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("test-key"), UseAnthropicEndpoints: schemas.Ptr(true)}

	resp, bifrostErr := provider.Responses(ctx, key, &schemas.BifrostResponsesRequest{
		Provider: schemas.Fireworks,
		Model:    "accounts/fireworks/models/deepseek-v4p1-flash",
		Input: []schemas.ResponsesMessage{{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{
				ContentStr: schemas.Ptr("hello"),
			},
		}},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{webSearchTool(), lookupTool()},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("Responses: %v", bifrostErr.Error.Message)
	}
	if resp == nil {
		t.Fatal("expected a responses payload")
	}

	types := outboundToolTypes(t, captured)
	for _, tpe := range types {
		if tpe == "web_search_20250305" || tpe == "web_search_20260209" {
			t.Fatalf("web_search reached the Fireworks endpoint: outbound tools = %v", types)
		}
	}
	if !slices.Contains(types, "function:lookup") {
		t.Fatalf("caller's function tool was dropped: outbound tools = %v", types)
	}
}

// TestChatCompletion_AnthropicEndpointDropsServerWebSearch is the Chat-path
// mirror. The chat converter validates unconditionally, so this covers the
// ProviderFeatures entry on its own, with no ValidateTools flag involved.
func TestChatCompletion_AnthropicEndpointDropsServerWebSearch(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := anthropicMessagesStub(t, &captured)
	defer server.Close()

	provider := newTestFireworksProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("test-key"), UseAnthropicEndpoints: schemas.Ptr(true)}

	resp, bifrostErr := provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{
		Provider: schemas.Fireworks,
		Model:    "accounts/fireworks/models/deepseek-v4p1-flash",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{
				{Type: schemas.ChatToolType("web_search_20250305"), Name: "web_search"},
				{
					Type: schemas.ChatToolTypeFunction,
					Function: &schemas.ChatToolFunction{
						Name:       "lookup",
						Parameters: &schemas.ToolFunctionParameters{Type: "object"},
					},
				},
			},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	if resp == nil {
		t.Fatal("expected a chat payload")
	}

	types := outboundToolTypes(t, captured)
	if slices.Contains(types, "web_search_20250305") {
		t.Fatalf("web_search reached the Fireworks endpoint: outbound tools = %v", types)
	}
	if !slices.Contains(types, "function:lookup") {
		t.Fatalf("caller's function tool was dropped: outbound tools = %v", types)
	}
}
