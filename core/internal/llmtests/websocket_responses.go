package llmtests

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"

	"github.com/gorilla/websocket"
)

// RunWebSocketResponsesTest dials the provider's native WebSocket Responses endpoint,
// sends a response.create event, and validates the streaming events that come back.
func RunWebSocketResponsesTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.WebSocketResponses || testConfig.ChatModel == "" {
		t.Logf("WebSocketResponses not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("WebSocketResponses", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		provider := client.GetProviderByKey(testConfig.Provider)
		if provider == nil {
			t.Fatalf("provider %s not found in bifrost client", testConfig.Provider)
		}

		wsProvider, ok := provider.(schemas.WebSocketCapableProvider)
		if !ok || !wsProvider.SupportsWebSocketMode() {
			t.Skipf("provider %s does not implement WebSocketCapableProvider", testConfig.Provider)
		}

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		key, err := client.SelectKeyForProvider(bfCtx, testConfig.Provider, testConfig.ChatModel)
		if err != nil {
			t.Fatalf("failed to select key for provider %s: %v", testConfig.Provider, err)
		}

		wsURL := wsProvider.WebSocketResponsesURL(key)
		hdrs := wsProvider.WebSocketHeaders(key)

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
				buf := make([]byte, 512)
				n, _ := resp.Body.Read(buf)
				body = string(buf[:n])
				resp.Body.Close()
			}
			t.Fatalf("failed to dial WS %s: %v (body: %s)", wsURL, dialErr, body)
		}
		defer conn.Close()

		t.Logf("connected to WebSocket Responses endpoint: %s", wsURL)

		event := map[string]interface{}{
			"type":  "response.create",
			"model": testConfig.ChatModel,
			"input": []map[string]interface{}{
				{
					"role": "user",
					"content": []map[string]interface{}{
						{
							"type": "input_text",
							"text": "Say hello in exactly two words.",
						},
					},
				},
			},
			"max_output_tokens": 64,
		}

		eventBytes, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			t.Fatalf("failed to marshal response.create event: %v", marshalErr)
		}

		if writeErr := conn.WriteMessage(websocket.TextMessage, eventBytes); writeErr != nil {
			t.Fatalf("failed to send response.create: %v", writeErr)
		}
		t.Logf("sent response.create event")

		var (
			gotDelta     bool
			gotCompleted bool
			eventCount   int
		)

		readDeadline := time.Now().Add(30 * time.Second)
		conn.SetReadDeadline(readDeadline)

		for {
			_, msg, readErr := conn.ReadMessage()
			if readErr != nil {
				if !gotCompleted {
					t.Fatalf("WS read error before response.completed (events=%d): %v", eventCount, readErr)
				}
				break
			}
			eventCount++

			var raw map[string]json.RawMessage
			if jsonErr := json.Unmarshal(msg, &raw); jsonErr != nil {
				t.Logf("event #%d: non-JSON message: %s", eventCount, string(msg))
				continue
			}

			var eventType string
			if typeBytes, ok := raw["type"]; ok {
				json.Unmarshal(typeBytes, &eventType)
			}

			switch eventType {
			case "response.output_text.delta":
				gotDelta = true
			case "response.completed":
				gotCompleted = true
				t.Logf("received response.completed (total events: %d)", eventCount)
			case "error":
				t.Fatalf("received error event: %s", string(msg))
			}

			if gotCompleted {
				break
			}
		}

		if !gotDelta {
			t.Error("expected at least one response.output_text.delta event")
		}
		if !gotCompleted {
			t.Error("expected a response.completed event")
		}
		t.Logf("WebSocket Responses test passed (%d events received)", eventCount)
	})
}
