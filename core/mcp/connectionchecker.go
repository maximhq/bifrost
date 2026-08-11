package mcp

import (
	"context"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// DefaultConnectionCheckInterval is the steady-state cadence once a
	// client is Healthy — a discovery-freshness concern (keep the tool list
	// current), not a failure-detection one.
	DefaultConnectionCheckInterval = 10 * time.Minute
	// UnstableConnectionCheckInterval is the tight cadence used while a
	// client is Unstable. Recovery detection depends entirely on this tick
	// now — there's no faster opportunistic path via real tool-call traffic
	// anymore (see prepareToolExecution / exec.go) — so it has to be fast.
	// Not load-bearing for correctness (nothing gates on Unstable), just for
	// how quickly the state reflects reality; the UI surfaces "next check in
	// Xs" so a stale-looking badge during this window reads as "nothing to
	// do, self-resolving" rather than confusing.
	UnstableConnectionCheckInterval = 10 * time.Second
	// ConnectionCheckTimeout bounds each individual ping/list_tools attempt.
	ConnectionCheckTimeout = 5 * time.Second
)

// ClientConnectionChecker is the single periodic mechanism behind a client's
// Healthy/Unstable/NeedsReauth state — it replaces the old separate
// ClientHealthMonitor (liveness ping, consecutive-failure counting) and
// ClientToolSyncer (discovery refresh) with one ticker doing both jobs.
//
// It is Bifrost's SOLE authority over these state transitions. Nothing else
// — a live tool call succeeding or failing — ever touches State; see
// exec.go's prepareToolExecution, which gates only on NeedsReauth and lets
// Unstable clients keep serving calls normally.
//
// Three check shapes depending on what the client currently looks like:
//   - Live connection (Conn != nil, sticky clients): ping (if supported) +
//     list_tools over it. One call now serves both liveness-check and
//     tool-discovery-refresh duty. A failure here also triggers a
//     background reconnect (ReconnectClient already dedupes concurrent
//     attempts per client via its own exclusive-op guard) — the reconnect's
//     own outcome, once it lands, supersedes whatever this tick marked.
//   - No connection, but a shared/sticky auth type (Conn == nil, not
//     per-call): attempt a reconnect directly. connectToMCPClient already
//     sets Healthy/Unstable/NeedsReauth itself on success/failure — this
//     branch doesn't need its own state-setting logic.
//   - Per-call auth types (RequiresPerCallConnection == true): no
//     persistent connection ever exists to ping or reconnect. list_tools
//     only, via an ephemeral connect-discover-close cycle
//     (performAdminToolDiscovery) — Bifrost's own check, not derived from
//     real caller traffic.
//
// Interval is adaptive (DefaultConnectionCheckInterval once Healthy,
// UnstableConnectionCheckInterval while Unstable), using a Timer rather
// than a Ticker so it can be reset to a different duration each cycle.
type ClientConnectionChecker struct {
	manager         *MCPManager
	clientID        string
	healthyInterval time.Duration
	timeout         time.Duration
	logger          schemas.Logger
	isPingAvailable bool

	mu        sync.Mutex
	timer     *time.Timer
	ctx       context.Context
	cancel    context.CancelFunc
	isRunning bool
}

// NewClientConnectionChecker creates a new connection checker for a client.
// healthyInterval is the steady-state (post-recovery) cadence — typically
// ResolveToolSyncInterval's result; pass 0 to use
// DefaultConnectionCheckInterval.
func NewClientConnectionChecker(
	manager *MCPManager,
	clientID string,
	healthyInterval time.Duration,
	isPingAvailable bool,
	logger schemas.Logger,
) *ClientConnectionChecker {
	if healthyInterval <= 0 {
		healthyInterval = DefaultConnectionCheckInterval
	}
	if logger == nil {
		logger = defaultLogger
	}
	return &ClientConnectionChecker{
		manager:         manager,
		clientID:        clientID,
		healthyInterval: healthyInterval,
		timeout:         ConnectionCheckTimeout,
		logger:          logger,
		isPingAvailable: isPingAvailable,
	}
}

// Start begins the periodic check in a background goroutine. First tick
// fires after healthyInterval if the client is currently Healthy (just
// connected, no reason to re-check immediately) — but after the tight
// UnstableConnectionCheckInterval if it's anything else (e.g. the boot-time
// and EnableClient failure-fallback paths both start a checker for a client
// they've just parked in Unstable specifically to recover it automatically;
// waiting a full healthyInterval — potentially 10 minutes — before the
// first recovery attempt would defeat that).
func (c *ClientConnectionChecker) Start() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isRunning {
		return
	}

	c.manager.mu.RLock()
	clientState, exists := c.manager.clientMap[c.clientID]
	c.manager.mu.RUnlock()
	if !exists {
		c.logger.Error("%s Connection checker failed to start for client %s, client not found in manager", MCPLogPrefix, c.clientID)
		return
	}

	firstInterval := c.healthyInterval
	if clientState.State != schemas.MCPConnectionStateHealthy {
		firstInterval = UnstableConnectionCheckInterval
	}

	c.isRunning = true
	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.timer = time.NewTimer(firstInterval)

	go c.checkLoop()
	c.logger.Debug("%s Connection checker started for client %s", MCPLogPrefix, clientState.ExecutionConfig.Name)
}

// Stop stops the periodic check.
func (c *ClientConnectionChecker) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.isRunning {
		return
	}
	c.isRunning = false
	if c.timer != nil {
		c.timer.Stop()
	}
	if c.cancel != nil {
		c.cancel()
	}
	c.logger.Debug("%s Connection checker stopped for client %s", MCPLogPrefix, c.clientID)
}

func (c *ClientConnectionChecker) checkLoop() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.timer.C:
			next := c.performCheck()
			c.mu.Lock()
			if c.isRunning {
				c.timer.Reset(next)
			}
			c.mu.Unlock()
		}
	}
}

// performCheck runs one check cycle and returns the interval the next one
// should fire after.
func (c *ClientConnectionChecker) performCheck() time.Duration {
	c.manager.mu.RLock()
	clientState, exists := c.manager.clientMap[c.clientID]
	var isDisabled, needsReauth bool
	var conn *client.Client
	var config *schemas.MCPClientConfig
	var connGeneration uint64
	if exists && clientState != nil {
		conn = clientState.Conn
		isDisabled = clientState.State == schemas.MCPConnectionStateDisabled
		needsReauth = clientState.State == schemas.MCPConnectionStateNeedsReauth
		config = clientState.ExecutionConfig
		connGeneration = clientState.ConnGeneration
	}
	c.manager.mu.RUnlock()

	if !exists {
		c.Stop()
		return c.healthyInterval
	}
	if isDisabled {
		// Health monitoring is already stopped for disabled clients
		// elsewhere (DisableClient) — this is just a sanity check.
		c.Stop()
		return c.healthyInterval
	}
	if needsReauth {
		// Stay quiet: the credential is confirmed permanently dead (typed
		// classification, see connectToMCPClient), so a check here would
		// just rediscover what's already known and burn a reconnect
		// attempt for nothing. Keep the timer alive at the relaxed
		// interval so a stalled reauthorize eventually gets picked up by
		// something, but do no work — only an explicit reauthorize (or a
		// direct UpdateClientCredentials success) moves this out.
		return c.healthyInterval
	}
	if config == nil {
		return c.healthyInterval
	}

	clientName := config.Name

	switch {
	case conn != nil:
		return c.checkLiveConnection(conn, clientName, connGeneration)
	case c.manager.credStore.RequiresPerCallConnection(config):
		return c.checkPerCall(config, connGeneration)
	default:
		// Sticky client with no live connection (e.g. still Unstable from a
		// prior failed connect). connectToMCPClient sets Healthy/Unstable/
		// NeedsReauth itself on this attempt's outcome — nothing further to
		// do here. "Already in progress" (another trigger, e.g. the
		// reactive per-request path, beat this tick to it) is expected and
		// fine to ignore; whichever attempt finishes is authoritative.
		go func() {
			if err := c.manager.ReconnectClient(c.clientID); err != nil {
				c.logger.Debug("%s Connection checker's reconnect attempt for %s did not complete: %v", MCPLogPrefix, clientName, err)
			}
		}()
		return UnstableConnectionCheckInterval
	}
}

// checkLiveConnection runs ping (if supported) + list_tools over an
// existing shared connection — one call now serving both liveness-check and
// tool-discovery-refresh duty.
//
// ExecuteWithRetry is given context.Background() as its outer ctx, not a
// short-lived one — mirrors connectToMCPClient's own retry usage (its outer
// ctx is the long-lived m.ctx; the short per-attempt deadline is built fresh
// inside the retry closure for each individual attempt). A single shared
// c.timeout-bounded ctx wrapping the whole retry sequence would cut
// ProbeRetryConfig's own backoff (up to ~3.5s already, before any attempt's
// own wall-clock time) off before it ever got to retry — defeating the
// entire point of adding retry coverage here.
func (c *ClientConnectionChecker) checkLiveConnection(conn *client.Client, clientName string, connGeneration uint64) time.Duration {
	if c.isPingAvailable {
		pingErr := ExecuteWithRetry(context.Background(), func() error {
			attemptCtx, cancel := context.WithTimeout(context.Background(), c.timeout)
			defer cancel()
			return c.manager.runPingWithHooks(c.markAsCheck(attemptCtx), conn, clientName)
		}, ProbeRetryConfig, c.logger)
		if pingErr != nil {
			c.recordFailure(clientName, "ping", pingErr, connGeneration)
			return UnstableConnectionCheckInterval
		}
	}

	var newTools map[string]schemas.ChatTool
	var newMapping map[string]string
	listErr := ExecuteWithRetry(context.Background(), func() error {
		attemptCtx, cancel := context.WithTimeout(context.Background(), c.timeout)
		defer cancel()
		var err error
		newTools, newMapping, err = c.manager.runListToolsWithHooks(c.markAsCheck(attemptCtx), conn, clientName)
		return err
	}, ProbeRetryConfig, c.logger)
	if listErr != nil {
		c.recordFailure(clientName, "list_tools", listErr, connGeneration)
		return UnstableConnectionCheckInterval
	}

	c.writeBackTools(connGeneration, newTools, newMapping)
	c.recordSuccess(clientName, connGeneration)
	return c.healthyInterval
}

// checkPerCall runs the per-call/ephemeral discovery cycle for auth types
// with no persistent connection to ping or reconnect. Same
// per-attempt-timeout reasoning as checkLiveConnection above.
func (c *ClientConnectionChecker) checkPerCall(config *schemas.MCPClientConfig, connGeneration uint64) time.Duration {
	var newTools map[string]schemas.ChatTool
	var newMapping map[string]string
	err := ExecuteWithRetry(context.Background(), func() error {
		attemptCtx, cancel := context.WithTimeout(context.Background(), c.timeout)
		defer cancel()
		var innerErr error
		newTools, newMapping, innerErr = c.manager.performAdminToolDiscovery(attemptCtx, config)
		return innerErr
	}, ProbeRetryConfig, c.logger)
	if err != nil {
		c.recordFailure(config.Name, "admin tool discovery", err, connGeneration)
		return UnstableConnectionCheckInterval
	}

	c.writeBackTools(connGeneration, newTools, newMapping)
	c.recordSuccess(config.Name, connGeneration)
	return c.healthyInterval
}

// markAsCheck wraps ctx as a BifrostContext with the health-check marker set,
// so plugins/hooks can distinguish these internal checks from
// caller-initiated requests (runListToolsWithHooks/runPingWithHooks wrap
// this further into their own gate context internally).
func (c *ClientConnectionChecker) markAsCheck(ctx context.Context) *schemas.BifrostContext {
	bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	bfCtx.SetValue(schemas.BifrostContextKeyMCPHealthCheckRequest, true)
	return bfCtx
}

// writeBackTools mirrors the old tool-syncer's generation-guarded
// write-back: if a reconnect swapped in a fresh connection while this check
// was in flight, the fresh generation no longer matches what was captured
// before the check ran, and these (now stale) results are dropped silently
// — the next tick syncs against whatever is current.
func (c *ClientConnectionChecker) writeBackTools(connGeneration uint64, newTools map[string]schemas.ChatTool, newMapping map[string]string) {
	c.manager.mu.Lock()

	clientState, exists := c.manager.clientMap[c.clientID]
	if !exists {
		c.manager.mu.Unlock()
		return
	}
	if clientState.ConnGeneration != connGeneration {
		c.manager.mu.Unlock()
		c.logger.Debug("%s Skipping tool write-back for %s: connection was replaced during check", MCPLogPrefix, c.clientID)
		return
	}
	clientState.ToolMap = newTools
	clientState.ToolNameMapping = newMapping
	fire := c.manager.toolsChangedCallback(clientState, c.clientID, newTools, newMapping)
	c.manager.mu.Unlock()

	// Fired outside the lock — see toolsChangeCallback's field doc. Covers
	// the periodic checker's own refresh for both sticky (checkLiveConnection)
	// and per-call (checkPerCall) clients — previously the one path where a
	// client's tools could drift out of sync with the DB indefinitely, since
	// nothing else revisits a per-call client after its first discovery.
	// Gated on genuine content change: this is the highest-frequency firing
	// point (every checker tick), and most ticks rediscover identical tools.
	if fire != nil {
		fire()
	}
}

// recordFailure marks the client Unstable — a single transient-classified
// failure is enough, no consecutive-failure counter: the retry-with-backoff
// each check already went through (ProbeRetryConfig) is what absorbs an
// ordinary blip before it ever reaches here. connGeneration is the value
// captured at the start of this check (see performCheck) — see setState for
// why it must be threaded through even though this write, unlike
// writeBackTools, isn't about the connection itself.
func (c *ClientConnectionChecker) recordFailure(clientName, stage string, err error, connGeneration uint64) {
	c.logger.Warn("%s Connection check (%s) failed for %s: %v", MCPLogPrefix, stage, clientName, err)
	c.setState(schemas.MCPConnectionStateUnstable, connGeneration)
}

// recordSuccess marks the client Healthy. A single success is enough to
// recover — asymmetric on purpose: slow and deliberate into Unstable
// (respecting the retry budget), instant back out of it once a check
// actually proves the connection is fine again.
func (c *ClientConnectionChecker) recordSuccess(clientName string, connGeneration uint64) {
	c.setState(schemas.MCPConnectionStateHealthy, connGeneration)
}

// setState is the guarded writer every transition in this file funnels
// through — Disabled and NeedsReauth are authoritative and never silently
// overwritten by a check result racing against a DisableClient call or a
// credential already confirmed dead.
//
// connGeneration is dropped if it no longer matches the client's current
// ConnGeneration, mirroring writeBackTools' own guard: StopChecking does not
// cancel an in-flight check, so a check started just before a
// stickiness-transition (which bumps ConnGeneration when it closes the
// persistent connection) can still complete afterward. Without this guard
// its state write would land on an entry that has since moved on — e.g.
// marking Unstable a client that just became per-call and has no connection
// left to be unstable about.
func (c *ClientConnectionChecker) setState(state schemas.MCPConnectionState, connGeneration uint64) {
	c.manager.mu.Lock()
	clientState, exists := c.manager.clientMap[c.clientID]
	if !exists {
		c.manager.mu.Unlock()
		return
	}
	if clientState.ConnGeneration != connGeneration {
		c.manager.mu.Unlock()
		c.logger.Debug("%s Skipping state write for %s: connection was replaced during check", MCPLogPrefix, c.clientID)
		return
	}
	if clientState.State == schemas.MCPConnectionStateDisabled || clientState.State == schemas.MCPConnectionStateNeedsReauth {
		c.manager.mu.Unlock()
		return
	}
	oldState := clientState.State
	stateChanged := oldState != state
	name := ""
	if clientState.ExecutionConfig != nil {
		name = clientState.ExecutionConfig.Name
	}
	if stateChanged {
		clientState.State = state
	}
	cb := c.manager.stateChangeCallback
	c.manager.mu.Unlock()

	if stateChanged {
		c.logger.Info("%s Client %s connection state changed to: %s", MCPLogPrefix, name, state)
		// Fired outside the lock — a registered callback is caller-supplied
		// and may do arbitrary work (including I/O), which must never run
		// while holding m.mu.
		if cb != nil {
			cb(c.clientID, name, oldState, state)
		}
	}
}

// ConnectionCheckerManager manages one ClientConnectionChecker per client —
// replaces the old separate HealthMonitorManager + ToolSyncManager.
type ConnectionCheckerManager struct {
	checkers       map[string]*ClientConnectionChecker
	globalInterval time.Duration
	mu             sync.RWMutex
}

// NewConnectionCheckerManager creates a new connection checker manager.
// globalInterval is the fallback steady-state interval when a client has no
// per-client override — see ResolveToolSyncInterval.
func NewConnectionCheckerManager(globalInterval time.Duration) *ConnectionCheckerManager {
	if globalInterval <= 0 {
		globalInterval = DefaultConnectionCheckInterval
	}
	return &ConnectionCheckerManager{
		checkers:       make(map[string]*ClientConnectionChecker),
		globalInterval: globalInterval,
	}
}

// GetGlobalInterval returns the global steady-state check interval.
func (m *ConnectionCheckerManager) GetGlobalInterval() time.Duration {
	return m.globalInterval
}

// StartChecking starts checking a specific client, stopping any pre-existing
// checker for the same client first — no leaked duplicate timers.
//
// checker.Start() runs after m.mu is released, not under it: Start acquires
// MCPManager.mu.RLock internally, and DisableClient/removeClientUnsafe hold
// MCPManager.mu while calling StopChecking (which needs m.mu here) — nesting
// m.mu -> MCPManager.mu on this path while those hold the opposite order is a
// lock-order inversion that can deadlock the two goroutines against each
// other. Registering the checker before releasing the lock still prevents a
// concurrent StartChecking/StopChecking for the same client from racing the
// registry itself; only the (already lock-free) Start() call moves outside.
func (m *ConnectionCheckerManager) StartChecking(checker *ClientConnectionChecker) {
	m.mu.Lock()
	if existing, ok := m.checkers[checker.clientID]; ok {
		existing.Stop()
	}
	m.checkers[checker.clientID] = checker
	m.mu.Unlock()

	checker.Start()
}

// StopChecking stops checking a specific client.
func (m *ConnectionCheckerManager) StopChecking(clientID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if checker, ok := m.checkers[clientID]; ok {
		checker.Stop()
		delete(m.checkers, clientID)
	}
}

// StopAll stops every checker.
func (m *ConnectionCheckerManager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, checker := range m.checkers {
		checker.Stop()
	}
	m.checkers = make(map[string]*ClientConnectionChecker)
}

// ResolveToolSyncInterval determines the effective steady-state check
// interval for a client (the discovery-freshness cadence used once
// Healthy — UnstableConnectionCheckInterval always applies while Unstable
// regardless of this).
// Priority: per-client override > global setting > default.
//
// Per-client semantics:
//   - Negative value: disabled for this client
//   - Zero: use global setting
//   - Positive value: use this interval
//
// Returns 0 if checking is disabled for this client.
func ResolveToolSyncInterval(clientConfig *schemas.MCPClientConfig, globalInterval time.Duration) time.Duration {
	if clientConfig.ToolSyncInterval < 0 {
		return 0
	}
	if clientConfig.ToolSyncInterval > 0 {
		return clientConfig.ToolSyncInterval
	}
	if globalInterval > 0 {
		return globalInterval
	}
	return DefaultConnectionCheckInterval
}
