package schemas

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

var NoDeadline time.Time

var reservedKeys = []any{
	BifrostContextKeyVirtualKey,
	BifrostContextKeyAPIKeyName,
	BifrostContextKeyAPIKeyID,
	BifrostContextKeyRequestID,
	BifrostContextKeyFallbackRequestID,
	BifrostContextKeySelectedKeyID,
	BifrostContextKeySelectedKeyName,
	BifrostContextKeyNumberOfRetries,
	BifrostContextKeyFallbackIndex,
	BifrostContextKeySkipKeySelection,
	BifrostContextKeyURLPath,
	BifrostContextKeyDeferTraceCompletion,
	BifrostContextKeyAttemptTrail,
}

// pluginLogStore holds plugin log entries accumulated during request processing.
// It is shared between the root BifrostContext and all scoped contexts derived from it.
// Uses a flat slice (not map) to minimize heap allocations.
type pluginLogStore struct {
	mu   sync.Mutex
	logs []PluginLogEntry
}

// pluginLogStorePool pools pluginLogStore instances to reduce per-request allocations.
var pluginLogStorePool = sync.Pool{
	New: func() any {
		return &pluginLogStore{logs: make([]PluginLogEntry, 0, 8)}
	},
}

// pluginScopePool pools BifrostContext instances used as scoped plugin contexts.
var pluginScopePool = sync.Pool{
	New: func() any {
		return &BifrostContext{}
	},
}

// BifrostContext is a custom context.Context implementation that tracks user-set values.
// It supports deadlines, can be derived from other contexts, and provides layered
// value inheritance when derived from another BifrostContext.
type BifrostContext struct {
	parent                context.Context
	deadline              time.Time
	hasDeadline           bool
	done                  chan struct{}
	doneOnce              sync.Once
	err                   error
	errMu                 sync.RWMutex
	userValues            map[any]any
	valuesMu              sync.RWMutex
	blockRestrictedWrites atomic.Bool

	// Plugin scoping fields
	pluginScope   *string                        // Non-nil when this is a scoped plugin context
	pluginLogs    atomic.Pointer[pluginLogStore] // Shared log store; lazily initialized on root, shared by scoped contexts
	valueDelegate *BifrostContext                // For scoped contexts: delegate Value/SetValue to this root context
}

// NewBifrostContext creates a new BifrostContext with the given parent context and deadline.
// If the deadline is zero, no deadline is set on this context (though the parent may have one).
// The context will be cancelled when the deadline expires or when the parent context is cancelled.
func NewBifrostContext(parent context.Context, deadline time.Time) *BifrostContext {
	if parent == nil {
		parent = context.Background()
	}
	ctx := &BifrostContext{
		parent:                parent,
		deadline:              deadline,
		hasDeadline:           !deadline.IsZero(),
		done:                  make(chan struct{}),
		userValues:            make(map[any]any),
		blockRestrictedWrites: atomic.Bool{},
	}
	ctx.blockRestrictedWrites.Store(false)
	// Only start goroutine if there's something to watch:
	// - If we have a deadline, we need the timer
	// - If parent can be cancelled (Done() != nil) AND is not a non-cancelling context
	// - If parent has a deadline, we need a timer (parent may not properly cancel via Done())
	_, parentHasDeadline := parent.Deadline()
	parentCanCancel := parent.Done() != nil && !isNonCancellingContext(parent)
	if ctx.hasDeadline || parentCanCancel || parentHasDeadline {
		go ctx.watchCancellation()
	}
	return ctx
}

// NewBifrostContextWithValue creates a new BifrostContext with the given value set.
func NewBifrostContextWithValue(parent context.Context, deadline time.Time, key any, value any) *BifrostContext {
	ctx := NewBifrostContext(parent, deadline)
	ctx.SetValue(key, value)
	return ctx
}

// NewBifrostContextWithTimeout creates a new BifrostContext with a timeout duration.
// This is a convenience wrapper around NewBifrostContext.
// Returns the context and a cancel function that should be called to release resources.
func NewBifrostContextWithTimeout(parent context.Context, timeout time.Duration) (*BifrostContext, context.CancelFunc) {
	ctx := NewBifrostContext(parent, time.Now().Add(timeout))
	return ctx, func() { ctx.Cancel() }
}

// NewBifrostContextWithCancel creates a new BifrostContext with a cancel function.
// This is a convenience wrapper around NewBifrostContext.
// Returns the context and a cancel function that should be called to release resources.
func NewBifrostContextWithCancel(parent context.Context) (*BifrostContext, context.CancelFunc) {
	ctx := NewBifrostContext(parent, NoDeadline)
	return ctx, func() { ctx.Cancel() }
}

// WithValue returns a new context with the given value set.
func (bc *BifrostContext) WithValue(key any, value any) *BifrostContext {
	bc.SetValue(key, value)
	return bc
}

// BlockRestrictedWrites returns true if restricted writes are blocked.
func (bc *BifrostContext) BlockRestrictedWrites() {
	bc.blockRestrictedWrites.Store(true)
}

// UnblockRestrictedWrites unblocks restricted writes.
func (bc *BifrostContext) UnblockRestrictedWrites() {
	bc.blockRestrictedWrites.Store(false)
}

// Cancel cancels the context, closing the Done channel and setting the error to context.Canceled.
func (bc *BifrostContext) Cancel() {
	bc.cancel(context.Canceled)
}

// watchCancellation monitors for deadline expiration and parent cancellation.
func (bc *BifrostContext) watchCancellation() {
	var timer <-chan time.Time

	// Use effective deadline (considers both own and parent deadlines)
	// This handles cases where parent has a deadline but doesn't properly
	// cancel via Done() (e.g., fasthttp.RequestCtx)
	if effectiveDeadline, hasDeadline := bc.Deadline(); hasDeadline {
		duration := time.Until(effectiveDeadline)
		if duration <= 0 {
			// Deadline already passed
			bc.cancel(context.DeadlineExceeded)
			return
		}
		t := time.NewTimer(duration)
		defer t.Stop()
		timer = t.C
	}

	// Don't watch parent.Done() for contexts known to never close it
	// (e.g., fasthttp.RequestCtx pools contexts and never cancels them)
	if isNonCancellingContext(bc.parent) {
		select {
		case <-timer:
			bc.cancel(context.DeadlineExceeded)
		case <-bc.done:
			// Already cancelled
		}
		return
	}

	select {
	case <-bc.parent.Done():
		bc.cancel(bc.parent.Err())
	case <-timer:
		bc.cancel(context.DeadlineExceeded)
	case <-bc.done:
		// Already cancelled
	}
}

// cancel closes the done channel and sets the error.
func (bc *BifrostContext) cancel(err error) {
	bc.doneOnce.Do(func() {
		bc.errMu.Lock()
		bc.err = err
		bc.errMu.Unlock()
		close(bc.done)
	})
}

// Deadline returns the deadline for this context.
// For scoped contexts, delegates to the root context.
// If both this context and the parent have deadlines, the earlier one is returned.
func (bc *BifrostContext) Deadline() (time.Time, bool) {
	if bc.valueDelegate != nil {
		return bc.valueDelegate.Deadline()
	}
	parentDeadline, parentHasDeadline := bc.parent.Deadline()

	if !bc.hasDeadline && !parentHasDeadline {
		return time.Time{}, false
	}

	if !bc.hasDeadline {
		return parentDeadline, true
	}

	if !parentHasDeadline {
		return bc.deadline, true
	}

	// Both have deadlines, return the earlier one
	if bc.deadline.Before(parentDeadline) {
		return bc.deadline, true
	}
	return parentDeadline, true
}

// Done returns a channel that is closed when the context is cancelled.
func (bc *BifrostContext) Done() <-chan struct{} {
	return bc.done
}

// Err returns the error explaining why the context was cancelled.
// For scoped contexts, delegates to the root context.
// Returns nil if the context has not been cancelled.
func (bc *BifrostContext) Err() error {
	if bc.valueDelegate != nil {
		return bc.valueDelegate.Err()
	}
	bc.errMu.RLock()
	defer bc.errMu.RUnlock()
	return bc.err
}

// Value returns the value associated with the key.
// For scoped contexts, delegates to the root context via valueDelegate.
// Otherwise checks the internal userValues map, then delegates to the parent context.
func (bc *BifrostContext) Value(key any) any {
	if bc.valueDelegate != nil {
		return bc.valueDelegate.Value(key)
	}
	bc.valuesMu.RLock()
	if val, ok := bc.userValues[key]; ok {
		bc.valuesMu.RUnlock()
		return val
	}
	bc.valuesMu.RUnlock()

	if bc.parent == nil {
		return nil
	}

	return bc.parent.Value(key)
}

// SetValue sets a value in the internal userValues map.
// For scoped contexts, delegates to the root context via valueDelegate.
// This is thread-safe and can be called concurrently.
func (bc *BifrostContext) SetValue(key, value any) {
	if bc.valueDelegate != nil {
		bc.valueDelegate.SetValue(key, value)
		return
	}
	// Check if the key is a reserved key
	if bc.blockRestrictedWrites.Load() && slices.Contains(reservedKeys, key) {
		// we silently drop writes for these reserved keys
		return
	}
	bc.valuesMu.Lock()
	defer bc.valuesMu.Unlock()
	if bc.userValues == nil {
		bc.userValues = make(map[any]any)
	}
	bc.userValues[key] = value
}

// ClearValue clears a value from the internal userValues map.
// For scoped contexts, delegates to the root context via valueDelegate.
func (bc *BifrostContext) ClearValue(key any) {
	if bc.valueDelegate != nil {
		bc.valueDelegate.ClearValue(key)
		return
	}
	// Check if the key is a reserved key
	if bc.blockRestrictedWrites.Load() && slices.Contains(reservedKeys, key) {
		// we silently drop writes for these reserved keys
		return
	}
	bc.valuesMu.Lock()
	defer bc.valuesMu.Unlock()
	if bc.userValues != nil {
		bc.userValues[key] = nil
	}
}

// GetAndSetValue gets a value from the internal userValues map and sets it.
// For scoped contexts, delegates to the root context via valueDelegate.
func (bc *BifrostContext) GetAndSetValue(key any, value any) any {
	if bc.valueDelegate != nil {
		return bc.valueDelegate.GetAndSetValue(key, value)
	}
	bc.valuesMu.Lock()
	defer bc.valuesMu.Unlock()
	// Check if the key is a reserved key
	if bc.blockRestrictedWrites.Load() && slices.Contains(reservedKeys, key) {
		// we silently drop writes for these reserved keys
		return bc.userValues[key]
	}
	if bc.userValues == nil {
		bc.userValues = make(map[any]any)
	}
	oldValue := bc.userValues[key]
	bc.userValues[key] = value
	return oldValue
}

// GetUserValues returns a copy of all user-set values in this context.
// If the parent is also a PluginContext, the values are merged with parent values
// (this context's values take precedence over parent values).
func (bc *BifrostContext) GetUserValues() map[any]any {
	result := make(map[any]any)

	// First, get parent's user values if parent is a PluginContext
	if parentCtx, ok := bc.parent.(*BifrostContext); ok {
		for k, v := range parentCtx.GetUserValues() {
			result[k] = v
		}
	}

	// Then overlay with our own values (our values take precedence)
	bc.valuesMu.RLock()
	for k, v := range bc.userValues {
		result[k] = v
	}
	bc.valuesMu.RUnlock()

	return result
}

// GetParentCtxWithUserValues returns a copy of the parent context with all user-set values merged in.
func (bc *BifrostContext) GetParentCtxWithUserValues() context.Context {
	parentCtx := bc.parent
	bc.valuesMu.RLock()
	for k, v := range bc.userValues {
		parentCtx = context.WithValue(parentCtx, k, v)
	}
	bc.valuesMu.RUnlock()
	return parentCtx
}

// AppendRoutingEngineLog appends a routing engine log entry to the context.
// Parameters:
//   - ctx: The Bifrost context
//   - engineName: Name of the routing engine (e.g., "governance", "routing-rule")
//   - message: Human-readable log message describing the decision/action
func (bc *BifrostContext) AppendRoutingEngineLog(engineName string, level LogLevel, message string) {
	entry := RoutingEngineLogEntry{
		Engine:    engineName,
		Level:     level,
		Message:   message,
		Timestamp: time.Now().UnixMilli(),
	}
	AppendToContextList(bc, BifrostContextKeyRoutingEngineLogs, entry)
}

// GetRoutingEngineLogs retrieves all routing engine logs from the context.
// Parameters:
//   - ctx: The Bifrost context
//
// Returns:
//   - []RoutingEngineLogEntry: Slice of routing engine log entries (nil if none)
func (bc *BifrostContext) GetRoutingEngineLogs() []RoutingEngineLogEntry {
	if val := bc.Value(BifrostContextKeyRoutingEngineLogs); val != nil {
		if logs, ok := val.([]RoutingEngineLogEntry); ok {
			return logs
		}
	}
	return nil
}

// AppendToContextList appends a value to the context list value.
// Parameters:
//   - ctx: The Bifrost context
//   - key: The key to append the value to
//   - value: The value to append
func AppendToContextList[T any](ctx *BifrostContext, key BifrostContextKey, value T) {
	if ctx == nil {
		return
	}
	existingValues, ok := ctx.Value(key).([]T)
	if !ok {
		existingValues = []T{}
	}
	ctx.SetValue(key, append(existingValues, value))
}

// WithPluginScope returns a lightweight scoped BifrostContext from the pool.
// The scoped context shares the root's pluginLogs store and delegates all
// Value/SetValue operations to the root context.
// Call ReleasePluginScope() when done to return the scoped context to the pool.
func (bc *BifrostContext) WithPluginScope(name *string) *BifrostContext {
	// Lazily initialize the plugin log store on the root context (CAS to avoid race)
	if bc.pluginLogs.Load() == nil {
		newStore := pluginLogStorePool.Get().(*pluginLogStore)
		if !bc.pluginLogs.CompareAndSwap(nil, newStore) {
			// Another goroutine initialized first — return unused store to pool
			pluginLogStorePool.Put(newStore)
		}
	}

	scoped := pluginScopePool.Get().(*BifrostContext)
	scoped.parent = bc.parent
	scoped.done = bc.done
	scoped.pluginScope = name
	scoped.pluginLogs.Store(bc.pluginLogs.Load())
	scoped.valueDelegate = bc
	return scoped
}

// ReleasePluginScope returns a scoped context to the pool.
// Safe no-op if called on a non-scoped context.
// Do not use the scoped context after calling this method.
func (bc *BifrostContext) ReleasePluginScope() {
	if bc.valueDelegate == nil {
		return // not a scoped context
	}
	bc.parent = nil
	bc.done = nil
	bc.pluginScope = nil
	bc.pluginLogs.Store(nil)
	bc.valueDelegate = nil
	pluginScopePool.Put(bc)
}

// AddSpanAttribute adds an attribute to the span.
// For scoped contexts, delegates to the root context via valueDelegate.
// This is thread-safe and can be called concurrently.
func (bc *BifrostContext) SetTraceAttribute(key string, value any) {
	tr, _ := bc.Value(BifrostContextKeyTracer).(Tracer)
	tid, _ := bc.Value(BifrostContextKeyTraceID).(string)
	if tr == nil || tid == "" {
		return
	}
	tr.SetAttribute(tid, key, value)
}

// Log appends a structured log entry for the current plugin scope.
// No-op if the context is not scoped to a plugin or has no log store.
func (bc *BifrostContext) Log(level LogLevel, msg string) {
	store := bc.pluginLogs.Load()
	if bc.pluginScope == nil || store == nil {
		return
	}
	store.mu.Lock()
	store.logs = append(store.logs, PluginLogEntry{
		PluginName: *bc.pluginScope,
		Level:      level,
		Message:    msg,
		Timestamp:  time.Now().UnixMilli(),
	})
	store.mu.Unlock()
}

// GetPluginLogs returns a deep copy of all accumulated plugin log entries.
// Thread-safe. Returns nil if no logs have been recorded.
func (bc *BifrostContext) GetPluginLogs() []PluginLogEntry {
	store := bc.pluginLogs.Load()
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.logs) == 0 {
		return nil
	}
	copied := make([]PluginLogEntry, len(store.logs))
	copy(copied, store.logs)
	return copied
}

// DrainPluginLogs transfers ownership of the plugin log slice to the caller.
// The internal log store is returned to the pool after draining.
// Returns nil if no logs have been recorded.
// This should be called once on the root context after all plugin hooks have completed.
func (bc *BifrostContext) DrainPluginLogs() []PluginLogEntry {
	if bc.valueDelegate != nil {
		return nil // scoped contexts must not drain the shared log store
	}
	store := bc.pluginLogs.Load()
	if store == nil {
		return nil
	}
	bc.pluginLogs.Store(nil)

	store.mu.Lock()
	logs := store.logs
	// Reset with fresh pre-allocated slice before returning to pool
	store.logs = make([]PluginLogEntry, 0, 8)
	store.mu.Unlock()

	// Return the store to the pool for reuse
	pluginLogStorePool.Put(store)

	if len(logs) == 0 {
		return nil
	}
	return logs
}
