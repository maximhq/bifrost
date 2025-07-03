// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains WebSocket handlers for real-time log streaming.
package handlers

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/fasthttp/router"
	"github.com/fasthttp/websocket"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/plugins/logging"
	"github.com/valyala/fasthttp"
)

// WebSocketHandler manages WebSocket connections for real-time updates
type WebSocketHandler struct {
	logManager logging.LogManager
	logger     schemas.Logger
	clients    map[*websocket.Conn]bool
	mu         sync.RWMutex
}

// NewWebSocketHandler creates a new WebSocket handler instance
func NewWebSocketHandler(logManager logging.LogManager, logger schemas.Logger) *WebSocketHandler {
	return &WebSocketHandler{
		logManager: logManager,
		logger:     logger,
		clients:    make(map[*websocket.Conn]bool),
	}
}

// RegisterRoutes registers all WebSocket-related routes
func (h *WebSocketHandler) RegisterRoutes(r *router.Router) {
	r.GET("/ws/logs", h.HandleLogStream)
}

// WebSocket upgrader configuration
var upgrader = websocket.FastHTTPUpgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(ctx *fasthttp.RequestCtx) bool {
		return true // Allow all origins for now, can be restricted in production
	},
}

// HandleLogStream handles WebSocket connections for real-time log streaming
func (h *WebSocketHandler) HandleLogStream(ctx *fasthttp.RequestCtx) {
	err := upgrader.Upgrade(ctx, func(ws *websocket.Conn) {
		// Register new client
		h.mu.Lock()
		h.clients[ws] = true
		h.mu.Unlock()

		// Clean up on disconnect
		defer func() {
			h.mu.Lock()
			delete(h.clients, ws)
			h.mu.Unlock()
			ws.Close()
		}()

		// Keep connection alive and handle client messages
		for {
			_, _, err := ws.ReadMessage()
			if err != nil {
				// Only log unexpected close errors
				if websocket.IsUnexpectedCloseError(err,
					websocket.CloseNormalClosure,
					websocket.CloseGoingAway,
					websocket.CloseAbnormalClosure,
					websocket.CloseNoStatusReceived) {
					h.logger.Error(fmt.Errorf("websocket read error: %v", err))
				}
				break
			}
		}
	})

	if err != nil {
		h.logger.Error(fmt.Errorf("websocket upgrade error: %v", err))
		return
	}
}

// BroadcastLogUpdate sends a log update to all connected WebSocket clients
func (h *WebSocketHandler) BroadcastLogUpdate(logEntry *logging.LogEntry) {
	message := struct {
		Type    string            `json:"type"`
		Payload *logging.LogEntry `json:"payload"`
	}{
		Type:    "log",
		Payload: logEntry,
	}

	data, err := json.Marshal(message)
	if err != nil {
		h.logger.Error(fmt.Errorf("failed to marshal log entry: %v", err))
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for client := range h.clients {
		err := client.WriteMessage(websocket.TextMessage, data)
		if err != nil {
			h.logger.Error(fmt.Errorf("failed to send message to client: %v", err))
			continue
		}
	}
}

// StartHeartbeat starts sending periodic heartbeat messages to keep connections alive
func (h *WebSocketHandler) StartHeartbeat() {
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		for range ticker.C {
			h.mu.RLock()
			for client := range h.clients {
				err := client.WriteMessage(websocket.PingMessage, nil)
				if err != nil {
					h.logger.Error(fmt.Errorf("failed to send heartbeat: %v", err))
				}
			}
			h.mu.RUnlock()
		}
	}()
}
