package schemas

import (
	"context"
	"slices"
	"sync"
	"time"
)

var NoDeadline time.Time

var reservedKeys = []any{
	BifrostContextKeyVirtualKey,
	BifrostContextKeyAPIKeyName,
	BifrostContextKeyRequestID,
	BifrostContextKeyFallbackRequestID,
	BifrostContextKeyDirectKey,
	BifrostContextKeySelectedKeyID,
	BifrostContextKeySelectedKeyName,
	BifrostContextKeyNumberOfRetries,
	BifrostContextKeyFallbackIndex,
	BifrostContextKeyStreamEndIndicator,
	BifrostContextKeySkipKeySelection,
	BifrostContextKeyExtraHeaders,
	BifrostContextKeyURLPath,
}

// BifrostContext is a custom context.Context implementation that tracks user-set values.
// It supports deadlines, can be derived from other contexts, and provides layered
// value inheritance when derived from another BifrostContext.
type BifrostContext struct {
	parent      context.Context
	deadline    time.Time
	hasDeadline bool
	done        chan struct{}
	doneOnce    sync.Once
	err         error
	errMu       sync.RWMutex
	userValues  map[any]any
	valuesMu    sync.RWMutex
}

// NewBifrostContext creates a new PluginContext with the given parent context and deadline.
// If the deadline is zero, no deadline is set on this context (though the parent may have one).
// The context will be cancelled when the deadline expires or when the parent context is cancelled.
func NewBifrostContext(parent context.Context, deadline time.Time) *BifrostContext {
	if parent == nil {
		parent = context.Background()
	}
	ctx := &BifrostContext{
		parent:      parent,
		deadline:    deadline,
		hasDeadline: !deadline.IsZero(),
		done:        make(chan struct{}),
		userValues:  make(map[any]any),
	}
	// Only start goroutine if there's something to watch:
	// - If we have a deadline, we need the timer
	// - If parent can be cancelled (Done() != nil), we need to propagate cancellation
	if ctx.hasDeadline || parent.Done() != nil {
		go ctx.watchCancellation()
	}
	return ctx
}

// NewBifrostContextWithTimeout creates a new PluginContext with a timeout duration.
// This is a convenience wrapper around NewPluginContext.
// Returns the context and a cancel function that should be called to release resources.
func NewBifrostContextWithTimeout(parent context.Context, timeout time.Duration) (*BifrostContext, context.CancelFunc) {
	ctx := NewBifrostContext(parent, time.Now().Add(timeout))
	return ctx, func() { ctx.Cancel() }
}

// Cancel cancels the context, closing the Done channel and setting the error to context.Canceled.
func (bc *BifrostContext) Cancel() {
	bc.cancel(context.Canceled)
}

// watchCancellation monitors for deadline expiration and parent cancellation.
func (bc *BifrostContext) watchCancellation() {
	var timer <-chan time.Time
	if bc.hasDeadline {
		duration := time.Until(bc.deadline)
		if duration <= 0 {
			// Deadline already passed
			bc.cancel(context.DeadlineExceeded)
			return
		}
		t := time.NewTimer(duration)
		defer t.Stop()
		timer = t.C
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

	return bc.parent.Value(key)
}

// SetValue sets a value in the internal userValues map.
// This is thread-safe and can be called concurrently.
func (bc *BifrostContext) SetValue(key, value any) {
	// Check if the key is a reserved key
	if slices.Contains(reservedKeys, key) {
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
