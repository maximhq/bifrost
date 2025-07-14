// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains WebSocket handlers for real-time log streaming.
package handlers

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
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
	stopChan   chan struct{} // Channel to signal heartbeat goroutine to stop
	done       chan struct{} // Channel to signal when heartbeat goroutine has stopped
}

// NewWebSocketHandler creates a new WebSocket handler instance
func NewWebSocketHandler(logManager logging.LogManager, logger schemas.Logger) *WebSocketHandler {
	return &WebSocketHandler{
		logManager: logManager,
		logger:     logger,
		clients:    make(map[*websocket.Conn]bool),
		stopChan:   make(chan struct{}),
		done:       make(chan struct{}),
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
		// Only allow connections from localhost for security
		origin := string(ctx.Request.Header.Peek("Origin"))
		if origin == "" {
			// If no Origin header, check the Host header for direct connections
			host := string(ctx.Request.Header.Peek("Host"))
			return isLocalhost(host)
		}

		// Parse the origin URL
		originURL, err := url.Parse(origin)
		if err != nil {
			return false
		}

		return isLocalhost(originURL.Host)
	},
}

// isLocalhost checks if the given host is localhost
func isLocalhost(host string) bool {
	// Remove port if present
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}

	// Check for localhost variations
	return host == "localhost" ||
		host == "127.0.0.1" ||
		host == "::1" ||
		host == ""
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
		// This loop continuously reads and discards incoming WebSocket messages to:
		// 1. Keep the connection alive by processing client pings and control frames
		// 2. Detect when the client disconnects by watching for close frames or errors
		// 3. Maintain proper WebSocket protocol handling without accumulating messages
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
	// Determine operation type based on log status and timestamp
	operationType := "update"
	if logEntry.Status == "processing" && logEntry.CreatedAt.Equal(logEntry.Timestamp) {
		operationType = "create"
	}

	message := struct {
		Type      string            `json:"type"`
		Operation string            `json:"operation"` // "create" or "update"
		Payload   *logging.LogEntry `json:"payload"`
	}{
		Type:      "log",
		Operation: operationType,
		Payload:   logEntry,
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
		defer func() {
			ticker.Stop()
			close(h.done)
		}()

		for {
			select {
			case <-ticker.C:
				h.mu.RLock()
				for client := range h.clients {
					err := client.WriteMessage(websocket.PingMessage, nil)
					if err != nil {
						h.logger.Error(fmt.Errorf("failed to send heartbeat: %v", err))
					}
				}
				h.mu.RUnlock()
			case <-h.stopChan:
				return
			}
		}
	}()
}

// Stop gracefully shuts down the WebSocket handler
func (h *WebSocketHandler) Stop() {
	close(h.stopChan) // Signal heartbeat goroutine to stop
	<-h.done          // Wait for heartbeat goroutine to finish

	// Close all client connections
	h.mu.Lock()
	for client := range h.clients {
		client.Close()
	}
	h.clients = make(map[*websocket.Conn]bool)
	h.mu.Unlock()
}
