// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains WebSocket handlers for real-time log streaming.
package handlers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/fasthttp/websocket"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// websocketWriteTimeout bounds a single write to a client connection.
const websocketWriteTimeout = 10 * time.Second

// errWebSocketClientClosed is returned when a write targets a client whose
// connection has already been handed back to the transport.
var errWebSocketClientClosed = errors.New("websocket client is closed")

// WebSocketClient represents a connected WebSocket client with its own mutex
type WebSocketClient struct {
	conn       *websocket.Conn
	mu         sync.Mutex // Per-connection mutex for thread-safe writes
	roleID     uint
	hasRole    bool
	localAdmin bool

	// closed is set under mu once the connection's owning handler is done with
	// it. It cannot be inferred from conn: fasthttp hands the upgrade handler a
	// pooled *hijackConn wrapper, so the connection stays non-nil while its
	// underlying net.Conn has been nil'd out from under it.
	closed bool
}

// close marks the client unusable and releases its connection.
//
// It blocks until any in-flight write finishes, which is what lets the caller
// safely return from the upgrade handler afterwards: fasthttp recycles the
// hijacked connection the moment that handler returns, nils its net.Conn and
// leases the wrapper to the next client. A write racing that recycle either
// panics on a nil dereference or, when the wrapper has already been re-leased,
// silently delivers to an unrelated client's socket.
func (c *WebSocketClient) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeLocked()
}

// closeLocked is close with c.mu already held.
func (c *WebSocketClient) closeLocked() {
	if c.closed {
		return
	}
	c.closed = true
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

// WebSocketHandler manages WebSocket connections for real-time updates
type WebSocketHandler struct {
	ctx            context.Context
	allowedOrigins []string
	clients        map[*websocket.Conn]*WebSocketClient
	mu             sync.RWMutex
	stopChan       chan struct{} // Channel to signal heartbeat goroutine to stop
	done           chan struct{} // Channel to signal when heartbeat goroutine has stopped
}

// NewWebSocketHandler creates a new WebSocket handler instance
func NewWebSocketHandler(ctx context.Context, allowedOrigins []string) *WebSocketHandler {
	return &WebSocketHandler{
		ctx:            ctx,
		allowedOrigins: allowedOrigins,
		clients:        make(map[*websocket.Conn]*WebSocketClient),
		stopChan:       make(chan struct{}),
		done:           make(chan struct{}),
	}
}

// RegisterRoutes registers all WebSocket-related routes
func (h *WebSocketHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/ws", lib.ChainMiddlewares(h.connectStream, middlewares...))
}

// getUpgrader returns a WebSocket upgrader configured with the current allowed origins
func (h *WebSocketHandler) getUpgrader() websocket.FastHTTPUpgrader {
	return websocket.FastHTTPUpgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(ctx *fasthttp.RequestCtx) bool {
			origin := string(ctx.Request.Header.Peek("Origin"))
			if origin == "" {
				// If no Origin header, check the Host header for direct connections
				host := string(ctx.Request.Header.Peek("Host"))
				return isLocalhost(host)
			}
			// Check if origin is allowed (localhost always allowed + configured origins)
			return IsOriginAllowed(origin, h.allowedOrigins)
		},
	}
}

// isLocalhost checks if the given host is localhost
func isLocalhost(host string) bool {
	// Remove port if present; SplitHostPort also unwraps IPv6 brackets ("[::1]:8080" -> "::1")
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	// Bare bracketed IPv6 literal without a port ("[::1]"); reject half-bracketed hosts
	if strings.HasPrefix(host, "[") || strings.HasSuffix(host, "]") {
		if !strings.HasPrefix(host, "[") || !strings.HasSuffix(host, "]") {
			return false
		}
		host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	}

	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// connectStream handles WebSocket connections for real-time streaming
func (h *WebSocketHandler) connectStream(ctx *fasthttp.RequestCtx) {
	upgrader := h.getUpgrader()
	err := upgrader.Upgrade(ctx, func(ws *websocket.Conn) {
		// Read safety & liveness
		ws.SetReadLimit(50 << 20) // 50 MiB
		ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		ws.SetPongHandler(func(string) error {
			ws.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})
		// Create a new client with its own mutex
		client := &WebSocketClient{
			conn: ws,
		}
		client.roleID, client.hasRole = notificationRoleID(ctx)
		client.localAdmin, _ = ctx.UserValue(schemas.IsLocalAdminContextKey).(bool)

		// Register new client
		h.mu.Lock()
		h.clients[ws] = client
		h.mu.Unlock()

		// Clean up on disconnect.
		//
		// The client is deregistered first so no new broadcast picks it up, then
		// closed. close() waits on the client's write lock, so this returns only
		// once every in-flight write has finished — required, because returning
		// from this function lets fasthttp recycle ws underneath any goroutine
		// still holding it from an earlier snapshot.
		//
		// h.mu is released before taking the client lock: writes hold the client
		// lock and take h.mu to deregister themselves, so holding both here in
		// the opposite order would deadlock.
		defer func() {
			h.mu.Lock()
			delete(h.clients, ws)
			h.mu.Unlock()

			client.close()
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
					logger.Error("websocket read error: %v", err)
				}
				break
			}
		}
	})

	if err != nil {
		logger.Error("websocket upgrade error: %v", err)
		return
	}
}

// writeSafely runs write against the client's connection under the client's
// write lock, with the client discarded on any failure.
//
// Holding the lock for the whole write is what gives close() something to wait
// on, so a client can never be written to after its handler has released the
// underlying connection.
func (h *WebSocketHandler) writeSafely(client *WebSocketClient, write func(conn *websocket.Conn) error) (writeErr error) {
	if client == nil {
		return fmt.Errorf("websocket client is nil")
	}

	client.mu.Lock()
	defer client.mu.Unlock()

	if client.closed || client.conn == nil {
		return errWebSocketClientClosed
	}
	conn := client.conn

	// discard drops the client on any failure. It runs with client.mu held, and
	// takes h.mu via removeClient — never the reverse.
	discard := func() {
		client.closeLocked()
		h.removeClient(conn)
	}

	defer func() {
		if r := recover(); r != nil {
			writeErr = fmt.Errorf("panic while writing websocket message: %v", r)
			discard()
		}
	}()

	if err := write(conn); err != nil {
		discard()
		return err
	}

	return nil
}

// sendMessageSafely sends a message to a client with proper locking and error handling
func (h *WebSocketHandler) sendMessageSafely(client *WebSocketClient, messageType int, data []byte) error {
	return h.writeSafely(client, func(conn *websocket.Conn) error {
		// Set a write deadline to prevent hanging connections
		if err := conn.SetWriteDeadline(time.Now().Add(websocketWriteTimeout)); err != nil {
			return err
		}
		if err := conn.WriteMessage(messageType, data); err != nil {
			return err
		}
		// Clear write deadline on successful write.
		return conn.SetWriteDeadline(time.Time{})
	})
}

// sendPingSafely sends a keepalive ping to a client.
//
// Pings go out via WriteControl rather than WriteMessage: WriteControl frames
// the message on its own buffer, so a failed ping cannot leave the connection's
// concurrent-write detector latched and turn every later write on that
// connection into a spurious "concurrent write" panic.
func (h *WebSocketHandler) sendPingSafely(client *WebSocketClient) error {
	return h.writeSafely(client, func(conn *websocket.Conn) error {
		return conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(websocketWriteTimeout))
	})
}

func (h *WebSocketHandler) removeClient(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	h.mu.Lock()
	delete(h.clients, conn)
	h.mu.Unlock()
}

// BroadcastUpdatesToClients sends a store update notification to all connected WebSocket clients
// The tags parameter should match RTK Query tagTypes (e.g., "Providers", "VirtualKeys", "MCPClients")
func (h *WebSocketHandler) BroadcastUpdatesToClients(tags []string) {
	message := struct {
		Type string   `json:"type"`
		Tags []string `json:"tags"`
	}{
		Type: "store_update",
		Tags: tags,
	}

	data, err := sonic.Marshal(message)
	if err != nil {
		logger.Error("failed to marshal store update: %v", err)
		return
	}

	h.BroadcastMarshaledMessage(data)
}

// BroadcastEvent sends a typed event to all connected WebSocket clients.
// Any subsystem can use this to push real-time updates to the frontend.
func (h *WebSocketHandler) BroadcastEvent(eventType string, data interface{}) {
	message := struct {
		Type string      `json:"type"`
		Data interface{} `json:"data"`
	}{
		Type: eventType,
		Data: data,
	}

	bytes, err := sonic.Marshal(message)
	if err != nil {
		logger.Error("failed to marshal event %s: %v", eventType, err)
		return
	}

	h.BroadcastMarshaledMessage(bytes)
}

// BroadcastNotification sends a dedicated notification event only to clients
// whose authenticated role is part of the notification audience.
func (h *WebSocketHandler) BroadcastNotification(notification *schemas.Notification) {
	message := struct {
		Type string                `json:"type"`
		Data *schemas.Notification `json:"data"`
	}{Type: "notification", Data: notification}
	data, err := sonic.Marshal(message)
	if err != nil {
		logger.Error("failed to marshal notification: %v", err)
		return
	}
	h.mu.RLock()
	clients := make([]*WebSocketClient, 0, len(h.clients))
	for _, client := range h.clients {
		if client.localAdmin || notificationVisibleToRole(notification, client.roleID, client.hasRole) {
			clients = append(clients, client)
		}
	}
	h.mu.RUnlock()
	for _, client := range clients {
		if err := h.sendMessageSafely(client, websocket.TextMessage, data); err != nil && !errors.Is(err, errWebSocketClientClosed) {
			logger.Error("failed to send notification to client: %v", err)
		}
	}
}

// BroadcastMarshaledMessage sends an adaptive routing update to all connected WebSocket clients
func (h *WebSocketHandler) BroadcastMarshaledMessage(data []byte) {
	// Get a snapshot of clients to avoid holding the lock during writes
	h.mu.RLock()
	clients := make([]*WebSocketClient, 0, len(h.clients))
	for _, client := range h.clients {
		clients = append(clients, client)
	}
	h.mu.RUnlock()

	// Send message to each client safely. A client that disconnected between the
	// snapshot and the write is expected, not an error worth logging.
	for _, client := range clients {
		if err := h.sendMessageSafely(client, websocket.TextMessage, data); err != nil && !errors.Is(err, errWebSocketClientClosed) {
			logger.Error("failed to send message to client: %v", err)
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
			case <-h.ctx.Done():
				logger.Info("got context cancel(), stopping webserver")
				return
			case <-ticker.C:
				// Get a snapshot of clients to avoid holding the lock during writes
				h.mu.RLock()
				clients := make([]*WebSocketClient, 0, len(h.clients))
				for _, client := range h.clients {
					clients = append(clients, client)
				}
				h.mu.RUnlock()

				// Send heartbeat to each client safely
				for _, client := range clients {
					if err := h.sendPingSafely(client); err != nil && !errors.Is(err, errWebSocketClientClosed) {
						logger.Error("failed to send heartbeat: %v", err)
					}
				}
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

	// Close all client connections.
	//
	// Detach the map under h.mu, then close outside it: close() waits on each
	// client's write lock, and a write holding that lock takes h.mu to
	// deregister itself. Closing while holding h.mu would deadlock.
	h.mu.Lock()
	clients := make([]*WebSocketClient, 0, len(h.clients))
	for _, client := range h.clients {
		clients = append(clients, client)
	}
	h.clients = make(map[*websocket.Conn]*WebSocketClient)
	h.mu.Unlock()

	for _, client := range clients {
		client.close()
	}
}
