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
	BifrostContextKeyDirectKey,
	BifrostContextKeySelectedKeyID,
	BifrostContextKeySelectedKeyName,
	BifrostContextKeyNumberOfRetries,
	BifrostContextKeyFallbackIndex,
	BifrostContextKeySkipKeySelection,
	BifrostContextKeyURLPath,
	BifrostContextKeyDeferTraceCompletion,
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
	pluginScope           *string          // non-nil when this is a plugin-scoped context (pointer avoids string comparison)
	valueDelegate         *BifrostContext  // non-nil for scoped contexts; write operations delegate here
	pluginLogs            *pluginLogStore  // shared log store (pointer, shared across scoped contexts)
}

// pluginLogStore is a thread-safe store for plugin log entries.
// It is shared between a root BifrostContext and its plugin-scoped derivatives.
// Uses a flat slice instead of map to avoid heap map allocations on every Log() call.
type pluginLogStore struct {
	mu   sync.Mutex
	logs []PluginLogEntry
}

// pluginScopePool pools scoped BifrostContext objects to avoid per-hook heap allocations.
var pluginScopePool = sync.Pool{
	New: func() any { return &BifrostContext{} },
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
	if bc.valueDelegate != nil {
		bc.valueDelegate.Cancel()
		return
	}
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
// If both this context and the parent have deadlines, the earlier one is returned.
func (bc *BifrostContext) Deadline() (time.Time, bool) {
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
// It first checks the internal userValues map, then delegates to the parent context.
func (bc *BifrostContext) Value(key any) any {
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

// GetAndSetValue gets a value from the internal userValues map and sets it
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
	if bc.valueDelegate != nil {
		return bc.valueDelegate.GetUserValues()
	}
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
	if bc.valueDelegate != nil {
		return bc.valueDelegate.GetParentCtxWithUserValues()
	}
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
func (bc *BifrostContext) AppendRoutingEngineLog(engineName string, message string) {
	entry := RoutingEngineLogEntry{
		Engine:    engineName,
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

// WithPluginScope returns a lightweight derived context where Log() always attributes
// entries to the given plugin name. The scoped context shares the root context's
// user values, cancellation, and plugin log store. It is safe to pass to goroutines.
// Scoped contexts are obtained from a pool. Call ReleasePluginScope when done to return
// the context to the pool. If not released, the GC will collect it normally.
// The reason we have chosen this flow is - pluginName matters when we are collecting logs
// because we want to be able to filter logs by plugin name in the UI.
// And plugins can call Log() methods in async manner till the context is alive
// This method is thread-safe - and would allow plugins to log till context is alive
func (bc *BifrostContext) WithPluginScope(pluginName string) *BifrostContext {
	root := bc
	if bc.valueDelegate != nil {
		root = bc.valueDelegate
	}
	// Lazily initialize the log store on the root context
	if root.pluginLogs == nil {
		root.pluginLogs = &pluginLogStore{}
	}
	scoped := pluginScopePool.Get().(*BifrostContext)
	scoped.parent = root
	scoped.done = root.done
	scoped.pluginScope = &pluginName
	scoped.valueDelegate = root
	scoped.pluginLogs = root.pluginLogs
	return scoped
}

// ReleasePluginScope returns a scoped context to the pool.
// After release, any calls to Log() on this context are safe no-ops.
// Must only be called on contexts created via WithPluginScope.
func (bc *BifrostContext) ReleasePluginScope() {
	if bc.valueDelegate == nil {
		return // not a scoped context
	}
	bc.parent = nil
	bc.done = nil
	bc.pluginScope = nil
	bc.valueDelegate = nil
	bc.pluginLogs = nil
	pluginScopePool.Put(bc)
}

// Log appends a plugin log entry to the shared log store.
// If this context was not created via WithPluginScope, the log is silently dropped.
// This method is safe for concurrent use.
func (bc *BifrostContext) Log(level LogLevel, msg string) {
	if bc.pluginScope == nil || bc.pluginLogs == nil {
		return
	}
	bc.pluginLogs.mu.Lock()
	bc.pluginLogs.logs = append(bc.pluginLogs.logs, PluginLogEntry{
		PluginName: *bc.pluginScope,
		Level:      level,
		Message:    msg,
		Timestamp:  time.Now().UnixMilli(),
	})
	bc.pluginLogs.mu.Unlock()
}

// GetPluginLogs retrieves all plugin logs from the context.
// Returns a deep copy of the plugin logs as a flat slice. Returns nil if no plugin has logged anything.
// This method is safe for concurrent use.
func (bc *BifrostContext) GetPluginLogs() []PluginLogEntry {
	if bc.valueDelegate != nil {
		return bc.valueDelegate.GetPluginLogs()
	}
	store := bc.pluginLogs
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.logs) == 0 {
		return nil
	}
	result := make([]PluginLogEntry, len(store.logs))
	copy(result, store.logs)
	return result
}

// DrainPluginLogs retrieves all plugin logs and transfers ownership to the caller.
// Unlike GetPluginLogs which returns a deep copy, this method returns the internal slice
// directly and nils out the store — zero allocation. Entries are already in chronological
// order from sequential appends. Safe to call when all hooks are done and no more Log()
// calls will be made (e.g., at trace completion time).
// Returns nil if no plugin has logged anything.
func (bc *BifrostContext) DrainPluginLogs() []PluginLogEntry {
	if bc.valueDelegate != nil {
		return bc.valueDelegate.DrainPluginLogs()
	}
	store := bc.pluginLogs
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	logs := store.logs
	store.logs = nil
	return logs
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
