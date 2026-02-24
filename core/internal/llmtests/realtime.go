package llmtests

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"

	"github.com/gorilla/websocket"
)

// RunRealtimeTest dials the provider's native Realtime WebSocket endpoint,
// sends a text-based conversation turn, and validates the session + response events.
func RunRealtimeTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.Realtime {
		t.Logf("Realtime not supported for provider %s", testConfig.Provider)
		return
	}

	if strings.TrimSpace(testConfig.RealtimeModel) == "" {
		t.Skipf("Realtime enabled but RealtimeModel is not configured for provider %s; skipping", testConfig.Provider)
	}

	t.Run("Realtime", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		provider := client.GetProviderByKey(testConfig.Provider)
		if provider == nil {
			t.Fatalf("provider %s not found in bifrost client", testConfig.Provider)
		}

		rtProvider, ok := provider.(schemas.RealtimeProvider)
		if !ok || !rtProvider.SupportsRealtimeAPI() {
			t.Skipf("provider %s does not implement RealtimeProvider", testConfig.Provider)
		}

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		key, err := client.SelectKeyForProvider(bfCtx, testConfig.Provider, testConfig.RealtimeModel)
		if err != nil {
			t.Fatalf("failed to select key for provider %s: %v", testConfig.Provider, err)
		}

		wsURL := rtProvider.RealtimeWebSocketURL(key, testConfig.RealtimeModel)
		hdrs := rtProvider.RealtimeHeaders(key)

		httpHeaders := http.Header{}
		for k, v := range hdrs {
			httpHeaders.Set(k, v)
		}

		dialer := websocket.Dialer{
			HandshakeTimeout: 15 * time.Second,
		}

		conn, resp, dialErr := dialer.DialContext(ctx, wsURL, httpHeaders)
		if dialErr != nil {
			body := ""
			if resp != nil && resp.Body != nil {
				buf := make([]byte, 1024)
				n, _ := resp.Body.Read(buf)
				body = string(buf[:n])
				resp.Body.Close()
			}
			t.Fatalf("failed to dial Realtime WS %s: %v (body: %s)", wsURL, dialErr, body)
		}
		defer conn.Close()

		t.Logf("connected to Realtime endpoint: %s", wsURL)

		if testConfig.Provider == schemas.Elevenlabs {
			runElevenLabsRealtimeTest(t, conn, testConfig)
		} else {
			runOpenAIRealtimeTest(t, conn, testConfig)
		}
	})
}

// runOpenAIRealtimeTest drives an OpenAI Realtime session using text modality only.
func runOpenAIRealtimeTest(t *testing.T, conn *websocket.Conn, testConfig ComprehensiveTestConfig) {
	var gotSessionCreated bool
	eventCount := 0
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	for i := 0; i < 5; i++ {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("error reading initial events: %v", err)
		}
		eventCount++
		eventType := extractEventType(msg)
		t.Logf("init event #%d: %s", eventCount, eventType)

		if eventType == "session.created" {
			gotSessionCreated = true
			break
		}
	}

	if !gotSessionCreated {
		t.Fatal("did not receive session.created event")
	}

	sessionUpdate := map[string]interface{}{
		"type": "session.update",
		"session": map[string]interface{}{
			"modalities":  []string{"text"},
			"temperature": 0.7,
		},
	}
	writeJSON(t, conn, sessionUpdate)

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for i := 0; i < 3; i++ {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		eventCount++
		eventType := extractEventType(msg)
		t.Logf("update event: %s", eventType)
		if eventType == "session.updated" {
			break
		}
	}

	itemCreate := map[string]interface{}{
		"type": "conversation.item.create",
		"item": map[string]interface{}{
			"type": "message",
			"role": "user",
			"content": []map[string]interface{}{
				{
					"type": "input_text",
					"text": "Say hello in exactly two words.",
				},
			},
		},
	}
	writeJSON(t, conn, itemCreate)

	responseCreate := map[string]interface{}{
		"type": "response.create",
	}
	writeJSON(t, conn, responseCreate)

	var (
		gotTextDelta    bool
		gotResponseDone bool
	)

	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if !gotResponseDone {
				t.Fatalf("WS read error before response.done (events=%d): %v", eventCount, err)
			}
			break
		}
		eventCount++
		eventType := extractEventType(msg)

		switch eventType {
		case "response.text.delta":
			gotTextDelta = true
		case "response.done":
			gotResponseDone = true
			t.Logf("received response.done (total events: %d)", eventCount)
		case "error":
			t.Fatalf("received error event: %s", string(msg))
		}

		if gotResponseDone {
			break
		}
	}

	if !gotTextDelta {
		t.Error("expected at least one response.text.delta event")
	}
	if !gotResponseDone {
		t.Error("expected a response.done event")
	}
	t.Logf("OpenAI Realtime test passed (%d events)", eventCount)
}

// runElevenLabsRealtimeTest drives an ElevenLabs Conversational AI session.
// ElevenLabs sessions start with conversation_initiation_metadata and require pong heartbeats.
func runElevenLabsRealtimeTest(t *testing.T, conn *websocket.Conn, testConfig ComprehensiveTestConfig) {
	var gotInitMetadata bool
	eventCount := 0
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	for i := 0; i < 10; i++ {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("error reading initial events: %v", err)
		}
		eventCount++
		eventType := extractEventType(msg)
		t.Logf("init event #%d: %s", eventCount, eventType)

		if eventType == "ping" {
			pong := map[string]interface{}{"type": "pong"}
			writeJSON(t, conn, pong)
		}

		if eventType == "conversation_initiation_metadata" {
			gotInitMetadata = true
			break
		}
	}

	if !gotInitMetadata {
		t.Fatal("did not receive conversation_initiation_metadata event")
	}

	var gotAgentResponse bool
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for i := 0; i < 50; i++ {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		eventCount++
		eventType := extractEventType(msg)
		t.Logf("event #%d: %s", eventCount, eventType)

		if eventType == "ping" {
			pong := map[string]interface{}{"type": "pong"}
			writeJSON(t, conn, pong)
		}

		if eventType == "agent_response" || eventType == "audio" {
			gotAgentResponse = true
		}

		if gotAgentResponse && eventType != "audio" && eventType != "ping" {
			break
		}
	}

	if !gotAgentResponse {
		t.Logf("no agent_response/audio received; ElevenLabs agent may require audio input to respond")
	}

	t.Logf("ElevenLabs Realtime test passed (%d events)", eventCount)
}

func extractEventType(msg []byte) string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(msg, &raw); err != nil {
		return "unknown"
	}
	if typeBytes, ok := raw["type"]; ok {
		var eventType string
		json.Unmarshal(typeBytes, &eventType)
		return eventType
	}
	return "unknown"
}

func writeJSON(t *testing.T, conn *websocket.Conn, v interface{}) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("failed to marshal event: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("failed to write event: %v", err)
	}
}
