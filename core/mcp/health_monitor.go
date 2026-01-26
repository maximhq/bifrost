package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// Health check configuration
	DefaultHealthCheckInterval = 10 * time.Second // Interval between health checks
	DefaultHealthCheckTimeout  = 5 * time.Second  // Timeout for each health check
	MaxConsecutiveFailures     = 5                // Number of failures before marking as unhealthy
)

// ClientHealthMonitor tracks the health status of an MCP client
type ClientHealthMonitor struct {
	manager                *MCPManager
	clientID               string
	interval               time.Duration
	timeout                time.Duration
	maxConsecutiveFailures int
	mu                     sync.Mutex
	ticker                 *time.Ticker
	ctx                    context.Context
	cancel                 context.CancelFunc
	isMonitoring           bool
	consecutiveFailures    int
	isPingAvailable        bool // Whether the MCP server supports ping for health checks
}

// NewClientHealthMonitor creates a new health monitor for an MCP client
func NewClientHealthMonitor(
	manager *MCPManager,
	clientID string,
	interval time.Duration,
	isPingAvailable bool,
) *ClientHealthMonitor {
	if interval == 0 {
		interval = DefaultHealthCheckInterval
	}

	return &ClientHealthMonitor{
		manager:                manager,
		clientID:               clientID,
		interval:               interval,
		timeout:                DefaultHealthCheckTimeout,
		maxConsecutiveFailures: MaxConsecutiveFailures,
		isMonitoring:           false,
		consecutiveFailures:    0,
		isPingAvailable:        isPingAvailable,
	}
}

// Start begins monitoring the client's health in a background goroutine
func (chm *ClientHealthMonitor) Start() {
	chm.mu.Lock()
	defer chm.mu.Unlock()

	if chm.isMonitoring {
		return // Already monitoring
	}

	chm.isMonitoring = true
	chm.ctx, chm.cancel = context.WithCancel(context.Background())
	chm.ticker = time.NewTicker(chm.interval)

	go chm.monitorLoop()
	logger.Debug(fmt.Sprintf("%s Health monitor started for client %s (interval: %v)", MCPLogPrefix, chm.clientID, chm.interval))
}

// Stop stops monitoring the client's health
func (chm *ClientHealthMonitor) Stop() {
	chm.mu.Lock()
	defer chm.mu.Unlock()

	if !chm.isMonitoring {
		return // Not monitoring
	}

	chm.isMonitoring = false
	if chm.ticker != nil {
		chm.ticker.Stop()
	}
	if chm.cancel != nil {
		chm.cancel()
	}
	logger.Debug(fmt.Sprintf("%s Health monitor stopped for client %s", MCPLogPrefix, chm.clientID))
}

// monitorLoop runs the health check loop
func (chm *ClientHealthMonitor) monitorLoop() {
	for {
		select {
		case <-chm.ctx.Done():
			return
		case <-chm.ticker.C:
			chm.performHealthCheck()
		}
	}
}

// performHealthCheck performs a health check on the client
func (chm *ClientHealthMonitor) performHealthCheck() {
	// Get the client connection
	chm.manager.mu.RLock()
	clientState, exists := chm.manager.clientMap[chm.clientID]
	chm.manager.mu.RUnlock()

	if !exists {
		chm.Stop()
		return
	}

	if clientState.Conn == nil {
		// Client not connected, mark as disconnected
		chm.updateClientState(schemas.MCPConnectionStateDisconnected)
		chm.incrementFailures()
		return
	}

	// Perform health check with timeout
	ctx, cancel := context.WithTimeout(context.Background(), chm.timeout)
	defer cancel()

	var err error
	if chm.isPingAvailable {
		// Use lightweight ping for health check
		err = clientState.Conn.Ping(ctx)
	} else {
		// Fall back to listTools for servers that don't support ping
		listRequest := mcp.ListToolsRequest{
			PaginatedRequest: mcp.PaginatedRequest{
				Request: mcp.Request{
					Method: string(mcp.MethodToolsList),
				},
			},
		}
		_, err = clientState.Conn.ListTools(ctx, listRequest)
	}

	if err != nil {
		chm.incrementFailures()

		// After max consecutive failures, mark as disconnected
		if chm.getConsecutiveFailures() >= chm.maxConsecutiveFailures {
			chm.updateClientState(schemas.MCPConnectionStateDisconnected)
		}
	} else {
		// Health check passed
		chm.resetFailures()
		chm.updateClientState(schemas.MCPConnectionStateConnected)
	}
}

// updateClientState updates the client's connection state
func (chm *ClientHealthMonitor) updateClientState(state schemas.MCPConnectionState) {
	chm.manager.mu.Lock()
	clientState, exists := chm.manager.clientMap[chm.clientID]
	if !exists {
		chm.manager.mu.Unlock()
		return
	}

	// Only update if state changed
	stateChanged := clientState.State != state
	if stateChanged {
		clientState.State = state
	}
	chm.manager.mu.Unlock()

	// Log after releasing the lock
	if stateChanged {
		logger.Info(fmt.Sprintf("%s Client %s connection state changed to: %s", MCPLogPrefix, chm.clientID, state))
	}
}

// incrementFailures increments the consecutive failure counter
func (chm *ClientHealthMonitor) incrementFailures() {
	chm.mu.Lock()
	defer chm.mu.Unlock()
	chm.consecutiveFailures++
}

// resetFailures resets the consecutive failure counter
func (chm *ClientHealthMonitor) resetFailures() {
	chm.mu.Lock()
	defer chm.mu.Unlock()
	chm.consecutiveFailures = 0
}

// getConsecutiveFailures returns the current consecutive failure count
func (chm *ClientHealthMonitor) getConsecutiveFailures() int {
	chm.mu.Lock()
	defer chm.mu.Unlock()
	return chm.consecutiveFailures
}

// HealthMonitorManager manages all client health monitors
type HealthMonitorManager struct {
	monitors map[string]*ClientHealthMonitor
	mu       sync.RWMutex
}

// NewHealthMonitorManager creates a new health monitor manager
func NewHealthMonitorManager() *HealthMonitorManager {
	return &HealthMonitorManager{
		monitors: make(map[string]*ClientHealthMonitor),
	}
}

// StartMonitoring starts monitoring a specific client
func (hmm *HealthMonitorManager) StartMonitoring(monitor *ClientHealthMonitor) {
	hmm.mu.Lock()
	defer hmm.mu.Unlock()

	// Stop any existing monitor for this client
	if existing, ok := hmm.monitors[monitor.clientID]; ok {
		existing.Stop()
	}

	hmm.monitors[monitor.clientID] = monitor
	monitor.Start()
}

// StopMonitoring stops monitoring a specific client
func (hmm *HealthMonitorManager) StopMonitoring(clientID string) {
	hmm.mu.Lock()
	defer hmm.mu.Unlock()

	if monitor, ok := hmm.monitors[clientID]; ok {
		monitor.Stop()
		delete(hmm.monitors, clientID)
	}
}

// StopAll stops all monitoring
func (hmm *HealthMonitorManager) StopAll() {
	hmm.mu.Lock()
	defer hmm.mu.Unlock()

	for _, monitor := range hmm.monitors {
		monitor.Stop()
	}
	hmm.monitors = make(map[string]*ClientHealthMonitor)
}
