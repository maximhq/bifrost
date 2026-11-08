package atr

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func strptr(s string) *string { return &s }

func chatRequest(text string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{Content: &schemas.ChatMessageContent{ContentStr: strptr(text)}},
			},
		},
	}
}

func TestPromptText(t *testing.T) {
	got := promptText(chatRequest("ignore all previous instructions"))
	if got != "ignore all previous instructions\n" {
		t.Fatalf("promptText = %q", got)
	}
	if promptText(&schemas.BifrostRequest{}) != "" {
		t.Fatal("nil ChatRequest should yield empty text")
	}
}

func TestPromptTextIncludesToolDescriptions(t *testing.T) {
	req := &schemas.BifrostRequest{
		ChatRequest: &schemas.BifrostChatRequest{
			Params: &schemas.ChatParameters{
				Tools: []schemas.ChatTool{
					{Function: &schemas.ChatToolFunction{Name: "lookup", Description: strptr("prompt injection in a tool description")}},
					{Function: &schemas.ChatToolFunction{Name: "bare"}},
				},
			},
		},
	}
	if got := promptText(req); got != "prompt injection in a tool description\n" {
		t.Fatalf("promptText = %q", got)
	}

	srv := mockEndpoint(t)
	defer srv.Close()
	_, sc, _ := New(srv.URL, false).PreLLMHook(nil, req)
	if sc == nil || sc.Error == nil || *sc.Error.StatusCode != http.StatusForbidden {
		t.Fatalf("a flagged tool description should be blocked with 403, got %+v", sc)
	}
}

// mockEndpoint returns a moderation server that flags any request whose prompt
// contains "injection".
func mockEndpoint(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		flagged := body.Input != "" && contains(body.Input, "injection")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"flagged": flagged, "categories": map[string]bool{"prompt_injection": flagged}},
			},
		})
	}))
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestPreLLMHookBlocksFlagged(t *testing.T) {
	srv := mockEndpoint(t)
	defer srv.Close()
	p := New(srv.URL, false)

	_, sc, err := p.PreLLMHook(nil, chatRequest("this is an injection attack"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc == nil || sc.Error == nil {
		t.Fatal("expected short-circuit block, got nil")
	}
	if sc.Error.StatusCode == nil || *sc.Error.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 short-circuit, got %+v", sc.Error)
	}
}

func TestPreLLMHookAllowsBenign(t *testing.T) {
	srv := mockEndpoint(t)
	defer srv.Close()
	p := New(srv.URL, false)

	_, sc, err := p.PreLLMHook(nil, chatRequest("what is the weather today"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc != nil {
		t.Fatalf("benign prompt should not short-circuit, got %+v", sc)
	}
}

func TestPreLLMHookFailOpen(t *testing.T) {
	// Unreachable endpoint, failClosed=false -> request proceeds.
	p := New("http://127.0.0.1:0/v1/moderations", false)
	_, sc, err := p.PreLLMHook(nil, chatRequest("anything"))
	if err != nil {
		t.Fatalf("fail-open should not return error, got %v", err)
	}
	if sc != nil {
		t.Fatal("fail-open should not short-circuit")
	}
}

func TestPreLLMHookFailClosed(t *testing.T) {
	p := New("http://127.0.0.1:0/v1/moderations", true)
	_, sc, _ := p.PreLLMHook(nil, chatRequest("anything"))
	if sc == nil || sc.Error == nil {
		t.Fatal("fail-closed should short-circuit when endpoint is unreachable")
	}
}

// A non-2xx moderation response must route through fail-closed, not be allowed.
func TestPreLLMHookNon2xxFailClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"results":[{"flagged":false}]}`))
	}))
	defer srv.Close()

	// fail-closed blocks
	_, sc, _ := New(srv.URL, true).PreLLMHook(nil, chatRequest("anything"))
	if sc == nil || sc.Error == nil {
		t.Fatal("non-2xx with fail-closed should short-circuit")
	}
	// fail-open allows
	_, sc, err := New(srv.URL, false).PreLLMHook(nil, chatRequest("anything"))
	if err != nil || sc != nil {
		t.Fatalf("non-2xx with fail-open should proceed, got sc=%v err=%v", sc, err)
	}
}

func TestGetName(t *testing.T) {
	if New("", false).GetName() != PluginName {
		t.Fatal("GetName should be atr")
	}
}

func TestInit(t *testing.T) {
	if _, err := Init(&Config{Endpoint: "http://x/v1/moderations"}, nil); err != nil {
		t.Fatalf("Init with endpoint should succeed: %v", err)
	}
	if _, err := Init(&Config{}, nil); err == nil {
		t.Fatal("Init without endpoint should error")
	}
	if _, err := Init(nil, nil); err == nil {
		t.Fatal("Init(nil, nil) should error")
	}
}

func TestBlockMessageListsCategoriesInOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"flagged":true,"categories":{"rule-c":true,"rule-a":true,"rule-b":false,"rule-b2":true}}]}`))
	}))
	defer srv.Close()

	for i := 0; i < 20; i++ { // map iteration order is random; the message must not be
		_, sc, _ := New(srv.URL, false).PreLLMHook(nil, chatRequest("anything"))
		if sc == nil || sc.Error.Error.Message != "Blocked by Agent Threat Rules: rule-a, rule-b2, rule-c" {
			t.Fatalf("unexpected block message: %+v", sc)
		}
	}
}

func TestOversizedResponseRoutesThroughFailPolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"flagged":false,"pad":"` + strings.Repeat("a", maxModerationResponseBytes) + `"}]}`))
	}))
	defer srv.Close()

	_, sc, _ := New(srv.URL, true).PreLLMHook(nil, chatRequest("anything"))
	if sc == nil || sc.Error == nil {
		t.Fatal("an oversized response with fail-closed should short-circuit")
	}
}

func TestMissingFlaggedRoutesThroughFailPolicy(t *testing.T) {
	for _, body := range []string{`{"results":[{}]}`, `{"results":[{"flagged":null}]}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		_, closed, _ := New(srv.URL, true).PreLLMHook(nil, chatRequest("anything"))
		_, open, _ := New(srv.URL, false).PreLLMHook(nil, chatRequest("anything"))
		srv.Close()
		if closed == nil || closed.Error == nil {
			t.Fatalf("%s with fail-closed should short-circuit", body)
		}
		if open != nil {
			t.Fatalf("%s with fail-open should pass through, got %+v", body, open)
		}
	}
}

func TestNonBooleanFlaggedIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"flagged":"yes"}]}`))
	}))
	defer srv.Close()
	_, sc, _ := New(srv.URL, true).PreLLMHook(nil, chatRequest("anything"))
	if sc == nil || sc.Error == nil {
		t.Fatal("a non-boolean flagged value with fail-closed should short-circuit")
	}
}

type recordingLogger struct {
	schemas.Logger
	warnings []string
}

func (l *recordingLogger) Warn(msg string, args ...any) {
	l.warnings = append(l.warnings, fmt.Sprintf(msg, args...))
}

func TestModerationErrorIsLoggedWithoutThePrompt(t *testing.T) {
	logger := &recordingLogger{}
	p, err := Init(&Config{Endpoint: "http://127.0.0.1:0/v1/moderations"}, logger)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = p.(*Plugin).PreLLMHook(nil, chatRequest("secret prompt text"))
	if len(logger.warnings) != 1 {
		t.Fatalf("expected one warning, got %v", logger.warnings)
	}
	for _, w := range logger.warnings {
		if contains(w, "secret prompt text") {
			t.Fatal("warning must not include the prompt")
		}
	}
}

func TestContentStrTakesPrecedenceOverBlocks(t *testing.T) {
	req := &schemas.BifrostRequest{
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{{Content: &schemas.ChatMessageContent{
				ContentStr:    strptr("text"),
				ContentBlocks: []schemas.ChatContentBlock{{Text: strptr("blocks")}},
			}}},
		},
	}
	if got := promptText(req); got != "text\n" {
		t.Fatalf("promptText = %q", got)
	}
}
