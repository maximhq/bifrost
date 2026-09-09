// Standalone Realtime WS smoke test for Bifrost.
//
//	cd scripts/realtime-test
//	go run . [virtual-key]
//
// The virtual key is read, in order, from: the first CLI arg, the
// VIRTUAL_KEY env var, or an interactive stdin prompt.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	ws "github.com/fasthttp/websocket"
)

const (
	// Bifrost base URL. Override with BASE_URL.
	baseURL = "http://localhost:8080"

	// provider/model to route to. Override with MODEL.
	model = "openai/gpt-realtime"

	// Message sent as the user turn. Override with MESSAGE.
	message = "Say a couple sentences about how you're doing."
)

func main() {
	vk := readVirtualKey()
	base := envOr("BASE_URL", baseURL)
	mdl := envOr("MODEL", model)
	msg := envOr("MESSAGE", message)

	wsURL := strings.Replace(base, "http", "ws", 1) + "/v1/realtime?model=" + mdl

	headers := http.Header{}
	headers.Set("x-bf-vk", vk)

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
		log.Fatalf("failed to connect to %s: %v (body: %s)", wsURL, err, body)
	}
	defer conn.Close()
	fmt.Printf("Connected to %s\n", wsURL)

	send(conn, map[string]any{
		"type": "session.update",
		"session": map[string]any{
			"type":              "realtime",
			"output_modalities": []string{"text"},
		},
	})

	send(conn, map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type": "message",
			"role": "user",
			"content": []map[string]any{
				{"type": "input_text", "text": msg},
			},
		},
	})

	send(conn, map[string]any{"type": "response.create"})

	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			log.Fatalf("read error: %v", err)
		}

		var event struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			continue
		}

		switch event.Type {
		case "session.created":
			fmt.Println("session created")
		case "response.output_text.delta", "response.text.delta":
			fmt.Print(event.Delta)
		case "response.output_text.done", "response.text.done":
			fmt.Println()
		case "response.done":
			fmt.Println("\nresponse done, closing connection")
			return
		case "error":
			log.Fatalf("realtime error: %s", string(raw))
		}
	}
}

func send(conn *ws.Conn, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		log.Fatalf("marshal event: %v", err)
	}
	if err := conn.WriteMessage(ws.TextMessage, data); err != nil {
		log.Fatalf("write event: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func readVirtualKey() string {
	if len(os.Args) > 1 && os.Args[1] != "" {
		return os.Args[1]
	}
	if v := os.Getenv("VIRTUAL_KEY"); v != "" {
		return v
	}

	fmt.Print("Enter Bifrost virtual key: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		log.Fatalf("failed to read virtual key: %v", err)
	}
	vk := strings.TrimSpace(line)
	if vk == "" {
		log.Fatal("no virtual key provided")
	}
	return vk
}
