package agentcapabilityrouter

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestPreRequestHookDefaultsToShadowMode(t *testing.T) {
	plugin, err := Init(nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	req := chatRequest("agent-main-auto", "Design a migration architecture")
	if err := plugin.PreRequestHook(testContext(), req); err != nil {
		t.Fatalf("PreRequestHook() error = %v", err)
	}
	_, model, _ := req.GetRequestFields()
	if model != "agent-main-auto" {
		t.Fatalf("model = %q, want shadow-mode alias unchanged", model)
	}
}

func TestPreRequestHookRewritesOnlyModelLane(t *testing.T) {
	shadow := false
	plugin, err := Init(&Config{ShadowMode: &shadow})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	req := chatRequest("agent-worker-auto", "Fix the failing authentication test")
	req.ChatRequest.Provider = schemas.OpenAI
	if err := plugin.PreRequestHook(testContext(), req); err != nil {
		t.Fatalf("PreRequestHook() error = %v", err)
	}
	provider, model, _ := req.GetRequestFields()
	if provider != schemas.OpenAI {
		t.Fatalf("provider = %q, want unchanged %q", provider, schemas.OpenAI)
	}
	if model != "agent-worker-debug" {
		t.Fatalf("model = %q, want agent-worker-debug", model)
	}
}

func TestPreRequestHookUsesGeneralLaneBelowThreshold(t *testing.T) {
	shadow := false
	plugin, err := Init(&Config{ShadowMode: &shadow})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	req := chatRequest("agent-main-auto", "continue")
	if err := plugin.PreRequestHook(testContext(), req); err != nil {
		t.Fatalf("PreRequestHook() error = %v", err)
	}
	_, model, _ := req.GetRequestFields()
	if model != "agent-main-general" {
		t.Fatalf("model = %q, want agent-main-general", model)
	}
}

func TestPreRequestHookBypassesUnmanagedAndUnsupportedRequests(t *testing.T) {
	shadow := false
	plugin, err := Init(&Config{ShadowMode: &shadow})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	unmanaged := chatRequest("agent-main-max", "Fix the failure")
	if err := plugin.PreRequestHook(testContext(), unmanaged); err != nil {
		t.Fatalf("PreRequestHook() error = %v", err)
	}
	_, model, _ := unmanaged.GetRequestFields()
	if model != "agent-main-max" {
		t.Fatalf("unmanaged model = %q, want unchanged", model)
	}

	unsupported := &schemas.BifrostRequest{RequestType: schemas.EmbeddingRequest}
	if err := plugin.PreRequestHook(testContext(), unsupported); err != nil {
		t.Fatalf("PreRequestHook() unsupported error = %v", err)
	}
}

func TestPreRequestHookHonorsDisabledRole(t *testing.T) {
	shadow := false
	worker := false
	plugin, err := Init(&Config{
		ShadowMode:  &shadow,
		ActiveRoles: &ActiveRolesConfig{Worker: &worker},
	})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	req := chatRequest("agent-worker-auto", "Implement the feature")
	if err := plugin.PreRequestHook(testContext(), req); err != nil {
		t.Fatalf("PreRequestHook() error = %v", err)
	}
	_, model, _ := req.GetRequestFields()
	if model != "agent-worker-auto" {
		t.Fatalf("model = %q, want disabled role unchanged", model)
	}
}

func TestRoleForModel(t *testing.T) {
	plugin, err := Init(nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	tests := []struct {
		model   string
		role    string
		managed bool
	}{
		{"agent-main-auto", roleMain, true},
		{"agent-worker-auto", roleWorker, true},
		{"agent-main-max", "", false},
		{"agent-main-cheap", "", false},
		{"codex-main", "", false},
		{"bedrock/zai.glm-5", "", false},
	}
	for _, test := range tests {
		role, managed := plugin.roleForModel(test.model)
		if role != test.role || managed != test.managed {
			t.Errorf("model=%q got (%q,%t), want (%q,%t)", test.model, role, managed, test.role, test.managed)
		}
	}
}

func chatRequest(model, text string) *schemas.BifrostRequest {
	content := schemas.ChatMessageContent{ContentStr: schemas.Ptr(text)}
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Model: model,
			Input: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &content,
			}},
		},
	}
}

func testContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}
