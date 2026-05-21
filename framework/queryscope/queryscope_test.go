package queryscope

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// recordingScope is a sentinel QueryScope that flips a flag when invoked
// so tests can assert whether the wrapper actually called it.
type recordingScope struct{ called bool }

func (r *recordingScope) apply(db *gorm.DB) *gorm.DB {
	r.called = true
	return db
}

func TestWithQueryScope_NilScopeIsNoOp(t *testing.T) {
	ctx := context.Background()
	out := WithQueryScope(ctx, nil)
	// Nil scope must not stash anything, so FromContext on the returned
	// ctx must report no scope present.
	assert.Nil(t, FromContext(out), "nil scope should not be retrievable as a scope")
}

func TestWithQueryScope_StashesScope(t *testing.T) {
	r := &recordingScope{}
	ctx := WithQueryScope(context.Background(), r.apply)

	got := FromContext(ctx)
	if assert.NotNil(t, got, "scope should be retrievable from ctx") {
		got(nil)
		assert.True(t, r.called, "retrieved scope should be the same closure that was stashed")
	}
}

func TestFromContext_NilCtxReturnsNil(t *testing.T) {
	assert.Nil(t, FromContext(nil))
}

func TestFromContext_MissingKeyReturnsNil(t *testing.T) {
	assert.Nil(t, FromContext(context.Background()))
}

func TestFromContext_WrongTypeReturnsNil(t *testing.T) {
	ctx := context.WithValue(context.Background(),
		schemas.BifrostContextKeyQueryScope, "not a closure")
	assert.Nil(t, FromContext(ctx),
		"a value of the wrong type at the scope key must not be treated as a scope")
}

func TestFromContext_NilInterfaceValueReturnsNil(t *testing.T) {
	// A typed nil QueryScope stashed on ctx must be safe to retrieve.
	// The retrieved scope can be nil, but FromContext must not panic.
	ctx := context.WithValue(context.Background(),
		schemas.BifrostContextKeyQueryScope, QueryScope(nil))
	got := FromContext(ctx)
	assert.Nil(t, got, "typed-nil QueryScope must be returned as a usable nil")
}

func TestWithQueryScope_NilCtxIsSafe(t *testing.T) {
	// Defensive: callers should pass a real ctx, but nil ctx must
	// not panic. Go's context.WithValue panics on nil parent so
	// WithQueryScope short-circuits via the nil-scope guard.
	out := WithQueryScope(nil, nil)
	assert.Nil(t, out, "nil ctx + nil scope must propagate nil cleanly")
}

func TestFromContext_CancelledCtxStillReturnsScope(t *testing.T) {
	// Cancellation must not affect scope retrieval; the scope is a
	// value on ctx, not a lifecycle resource.
	parent, cancel := context.WithCancel(context.Background())
	r := &recordingScope{}
	ctx := WithQueryScope(parent, r.apply)
	cancel()
	got := FromContext(ctx)
	if assert.NotNil(t, got, "cancellation should not strip the scope value") {
		got(nil)
		assert.True(t, r.called)
	}
}
