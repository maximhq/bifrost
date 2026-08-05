package realtime_test

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	ws "github.com/fasthttp/websocket"
)

// TestBifrostRealtimeWebSocket drives a full Realtime session through Bifrost's
// WS proxy (GET /v1/realtime): session.update, conversation.item.create,
// response.create, then asserts a streamed text response comes back.
//
// Requires a running Bifrost instance with a Realtime-capable provider
// configured. Configure via env vars:
//
//	BIFROST_BASE_URL  default "http://localhost:8080"
//	BIFROST_API_KEY   Authorization bearer sent to Bifrost (provider key or virtual key); required
//	REALTIME_MODEL    default "openai/gpt-realtime"
//
// Run:
//
//	BIFROST_API_KEY=sk-... go test ./transports/bifrost-http/tests/realtime/... -run TestBifrostRealtimeWebSocket -v
func TestBifrostRealtimeWebSocket(t *testing.T) {
	apiKey := os.Getenv("BIFROST_API_KEY")
	if apiKey == "" {
		t.Skip("BIFROST_API_KEY not set; skipping realtime WS test")
	}

	baseURL := os.Getenv("BIFROST_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	model := os.Getenv("REALTIME_MODEL")
	if model == "" {
		model = "openai/gpt-realtime"
	}

	wsURL := strings.Replace(baseURL, "http", "ws", 1) + "/v1/realtime?model=" + model

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+apiKey)

	dialer := ws.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, resp, err := dialer.Dial(wsURL, headers)
	if err != nil {
		body := ""
		if resp != nil && resp.Body != nil {
			buf := make([]byte, 2048)
			n, _ := resp.Body.Read(buf)
			body = string(buf[:n])
			resp.Body.Close()
		}
		t.Fatalf("failed to connect to Bifrost realtime endpoint %s: %v (body: %s)", wsURL, err, body)
	}
	defer conn.Close()
	t.Logf("connected to %s", wsURL)

	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	if !waitForEvent(t, conn, "session.created", 10) {
		t.Fatal("did not receive session.created")
	}

	send(t, conn, map[string]any{
		"type": "session.update",
		"session": map[string]any{
			"type":              "realtime",
			"output_modalities": []string{"text"},
		},
	})

	send(t, conn, map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type": "message",
			"role": "user",
			"content": []map[string]any{
				{"type": "input_text", "text": "Reply with exactly: Bifrost realtime works."},
			},
		},
	})
	send(t, conn, map[string]any{"type": "response.create"})

	var transcript strings.Builder
	gotDelta, gotDone := false, false
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for i := 0; i < 200 && !gotDone; i++ {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read error before response.done: %v", err)
		}
		switch eventType(msg) {
		case "response.output_text.delta", "response.text.delta":
			gotDelta = true
			var delta struct {
				Delta string `json:"delta"`
			}
			json.Unmarshal(msg, &delta)
			transcript.WriteString(delta.Delta)
		case "response.done":
			gotDone = true
		case "error":
			t.Fatalf("realtime error event: %s", string(msg))
		}
	}

	if !gotDelta {
		t.Error("expected at least one text delta event")
	}
	if !gotDone {
		t.Error("expected a response.done event")
	}
	t.Logf("model replied: %q", transcript.String())
}

func waitForEvent(t *testing.T, conn *ws.Conn, wantType string, maxEvents int) bool {
	t.Helper()
	for i := 0; i < maxEvents; i++ {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read error waiting for %s: %v", wantType, err)
		}
		got := eventType(msg)
		t.Logf("event: %s", got)
		if got == wantType {
			return true
		}
		if got == "error" {
			t.Fatalf("realtime error event: %s", string(msg))
		}
	}
	return false
}

func eventType(msg []byte) string {
	var raw struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(msg, &raw); err != nil {
		return "unknown"
	}
	return raw.Type
}

func send(t *testing.T, conn *ws.Conn, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if err := conn.WriteMessage(ws.TextMessage, data); err != nil {
		t.Fatalf("write event: %v", err)
	}
}
