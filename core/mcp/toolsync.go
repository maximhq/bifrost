package mcp

import (
	"context"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// Tool sync configuration
	DefaultToolSyncInterval = 10 * time.Minute // Default interval for syncing tools from MCP servers
	ToolSyncTimeout         = 10 * time.Second // Timeout for each sync operation
)

// ClientToolSyncer periodically syncs tools from an MCP server
type ClientToolSyncer struct {
	manager    *MCPManager
	clientID   string
	clientName string
	interval   time.Duration
	timeout    time.Duration
	logger     schemas.Logger
	mu         sync.Mutex
	ticker     *time.Ticker
	ctx        context.Context
	cancel     context.CancelFunc
	isSyncing  bool
}

// NewClientToolSyncer creates a new tool syncer for an MCP client
func NewClientToolSyncer(
	manager *MCPManager,
	clientID string,
	clientName string,
	interval time.Duration,
	logger schemas.Logger,
) *ClientToolSyncer {
	if interval <= 0 {
		interval = DefaultToolSyncInterval
	}

	if logger == nil {
		logger = defaultLogger
	}

	return &ClientToolSyncer{
		manager:    manager,
		clientID:   clientID,
		clientName: clientName,
		interval:   interval,
		timeout:    ToolSyncTimeout,
		logger:     logger,
		isSyncing:  false,
	}
}

// Start begins syncing tools in a background goroutine
func (cts *ClientToolSyncer) Start() {
	cts.mu.Lock()
	defer cts.mu.Unlock()

	if cts.isSyncing {
		return // Already syncing
	}

	cts.isSyncing = true
	cts.ctx, cts.cancel = context.WithCancel(context.Background())
	cts.ticker = time.NewTicker(cts.interval)

	go cts.syncLoop()
	cts.logger.Debug("%s Tool syncer started for client %s (interval: %v)", MCPLogPrefix, cts.clientID, cts.interval)
}

// Stop stops syncing tools
func (cts *ClientToolSyncer) Stop() {
	cts.mu.Lock()
	defer cts.mu.Unlock()

	if !cts.isSyncing {
		return // Not syncing
	}

	cts.isSyncing = false
	if cts.ticker != nil {
		cts.ticker.Stop()
	}
	if cts.cancel != nil {
		cts.cancel()
	}
	cts.logger.Debug("%s Tool syncer stopped for client %s", MCPLogPrefix, cts.clientID)
}

// syncLoop runs the tool sync loop
func (cts *ClientToolSyncer) syncLoop() {
	for {
		select {
		case <-cts.ctx.Done():
			return
		case <-cts.ticker.C:
			cts.performSync()
		}
	}
}

// performSync performs a tool sync for the client
func (cts *ClientToolSyncer) performSync() {
	// Get the client connection (read lock)
	cts.manager.mu.RLock()
	clientState, exists := cts.manager.clientMap[cts.clientID]
	if !exists {
		cts.manager.mu.RUnlock()
		cts.Stop()
		return
	}

	if clientState.Conn == nil {
		cts.manager.mu.RUnlock()
		cts.logger.Debug("%s Skipping tool sync for %s: client not connected", MCPLogPrefix, cts.clientID)
		return
	}

	// Get the connection reference while holding the lock
	conn := clientState.Conn
	clientName := clientState.ExecutionConfig.Name
	cts.manager.mu.RUnlock()

	// Perform tool sync with timeout (outside of lock)
	ctx, cancel := context.WithTimeout(context.Background(), cts.timeout)
	defer cancel()

	newTools, newMapping, err := retrieveExternalTools(ctx, conn, clientName, cts.logger)
	if err != nil {
		// On failure, keep existing tools intact
		cts.logger.Warn("%s Tool sync failed for %s, keeping existing tools: %v", MCPLogPrefix, cts.clientID, err)
		return
	}

	// Update tools atomically (write lock)
	cts.manager.mu.Lock()
	clientState, exists = cts.manager.clientMap[cts.clientID]
	if !exists {
		cts.manager.mu.Unlock()
		return
	}

	// Check if tools have changed
	oldToolCount := len(clientState.ToolMap)
	newToolCount := len(newTools)

	clientState.ToolMap = newTools
	clientState.ToolNameMapping = newMapping
	cts.manager.mu.Unlock()

	if oldToolCount != newToolCount {
		cts.logger.Info("%s Tool sync completed for %s: %d -> %d tools", MCPLogPrefix, cts.clientID, oldToolCount, newToolCount)
	} else {
		cts.logger.Debug("%s Tool sync completed for %s: %d tools (no change)", MCPLogPrefix, cts.clientID, newToolCount)
	}
}

// ToolSyncManager manages all client tool syncers
type ToolSyncManager struct {
	syncers        map[string]*ClientToolSyncer
	globalInterval time.Duration
	mu             sync.RWMutex
}

// NewToolSyncManager creates a new tool sync manager
func NewToolSyncManager(globalInterval time.Duration) *ToolSyncManager {
	if globalInterval <= 0 {
		globalInterval = DefaultToolSyncInterval
	}

	return &ToolSyncManager{
		syncers:        make(map[string]*ClientToolSyncer),
		globalInterval: globalInterval,
	}
}

// GetGlobalInterval returns the global tool sync interval
func (tsm *ToolSyncManager) GetGlobalInterval() time.Duration {
	return tsm.globalInterval
}

// StartSyncing starts syncing for a specific client
func (tsm *ToolSyncManager) StartSyncing(syncer *ClientToolSyncer) {
	tsm.mu.Lock()
	defer tsm.mu.Unlock()

	// Stop any existing syncer for this client
	if existing, ok := tsm.syncers[syncer.clientID]; ok {
		existing.Stop()
	}

	tsm.syncers[syncer.clientID] = syncer
	syncer.Start()
}

// StopSyncing stops syncing for a specific client
func (tsm *ToolSyncManager) StopSyncing(clientID string) {
	tsm.mu.Lock()
	defer tsm.mu.Unlock()

	if syncer, ok := tsm.syncers[clientID]; ok {
		syncer.Stop()
		delete(tsm.syncers, clientID)
	}
}

// StopAll stops all syncing
func (tsm *ToolSyncManager) StopAll() {
	tsm.mu.Lock()
	defer tsm.mu.Unlock()

	for _, syncer := range tsm.syncers {
		syncer.Stop()
	}
	tsm.syncers = make(map[string]*ClientToolSyncer)
}

// ResolveToolSyncInterval determines the effective tool sync interval for a client.
// Priority: per-client override > global setting > default
//
// Per-client semantics:
//   - Negative value: disabled for this client
//   - Zero: use global setting
//   - Positive value: use this interval
//
// Returns 0 if sync is disabled for this client.
func ResolveToolSyncInterval(clientConfig *schemas.MCPClientConfig, globalInterval time.Duration) time.Duration {
	// Per-client explicitly disabled (negative value)
	if clientConfig.ToolSyncInterval < 0 {
		return 0 // Disabled for this client
	}

	// Per-client override (positive value)
	if clientConfig.ToolSyncInterval > 0 {
		return clientConfig.ToolSyncInterval
	}

	// Use global interval (or default if global is 0)
	if globalInterval > 0 {
		return globalInterval
	}

	return DefaultToolSyncInterval
}
