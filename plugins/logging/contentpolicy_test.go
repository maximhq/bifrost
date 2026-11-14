package logging

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

func policyTestPlugin(disableContentLogging, retainContent *bool, objectStorageEnabled bool) *LoggerPlugin {
	return &LoggerPlugin{
		disableContentLogging:        disableContentLogging,
		retainContentInObjectStorage: retainContent,
		objectStorageEnabled:         objectStorageEnabled,
		logger:                       testLogger{},
	}
}

func policyCtx(overridesAllowed bool, values map[schemas.BifrostContextKey]bool) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	if overridesAllowed {
		ctx.SetValue(schemas.BifrostContextKeyAllowPerRequestStorageOverride, true)
	}
	for k, v := range values {
		ctx.SetValue(k, v)
	}
	return ctx
}

func boolPtr(b bool) *bool { return &b }

func TestResolveContentPolicyDefaults(t *testing.T) {
	p := policyTestPlugin(nil, nil, true)

	policy := p.resolveContentPolicy(policyCtx(false, nil))
	assert.True(t, policy.storeContent)
	assert.False(t, policy.hidden)
	assert.True(t, policy.visible())

	// Nil context falls back to static config.
	policy = p.resolveContentPolicy(nil)
	assert.True(t, policy.storeContent)
	assert.False(t, policy.hidden)
}

func TestResolveContentPolicyStaticDisable(t *testing.T) {
	p := policyTestPlugin(boolPtr(true), nil, true)

	// Retention off: content is dropped entirely.
	policy := p.resolveContentPolicy(policyCtx(false, nil))
	assert.False(t, policy.storeContent)
	assert.False(t, policy.hidden)
	assert.False(t, policy.visible())

	// Per-request header re-enables content when overrides are allowed.
	policy = p.resolveContentPolicy(policyCtx(true, map[schemas.BifrostContextKey]bool{
		schemas.BifrostContextKeyDisableContentLogging: false,
	}))
	assert.True(t, policy.storeContent)
	assert.False(t, policy.hidden)
}

// The virtual key's decision (stamped by governance) sits between the client flag and the
// per-request header: it overrides the client flag in both directions, and the header, when the
// deployment allows per-request overrides at all, still has the final word.
func TestResolveContentPolicyVirtualKeyOverridesClientFlag(t *testing.T) {
	t.Run("key forces content off while the client flag is on", func(t *testing.T) {
		p := policyTestPlugin(nil, nil, true)
		policy := p.resolveContentPolicy(policyCtx(false, map[schemas.BifrostContextKey]bool{
			schemas.BifrostContextKeyGovernanceDisableContentLogging: true,
		}))
		assert.False(t, policy.storeContent)
		assert.False(t, policy.visible())
	})

	t.Run("key forces content on while the client flag is off", func(t *testing.T) {
		p := policyTestPlugin(boolPtr(true), nil, true)
		policy := p.resolveContentPolicy(policyCtx(false, map[schemas.BifrostContextKey]bool{
			schemas.BifrostContextKeyGovernanceDisableContentLogging: false,
		}))
		assert.True(t, policy.storeContent)
		assert.True(t, policy.visible())
	})

	t.Run("key decision does not need the per-request override gate", func(t *testing.T) {
		// The header is caller input and is gated; the key's decision is admin configuration
		// and applies whether or not callers may override anything.
		p := policyTestPlugin(nil, nil, true)
		policy := p.resolveContentPolicy(policyCtx(false, map[schemas.BifrostContextKey]bool{
			schemas.BifrostContextKeyGovernanceDisableContentLogging: true,
		}))
		assert.False(t, policy.storeContent)
	})

	t.Run("header still wins over the key when overrides are allowed", func(t *testing.T) {
		p := policyTestPlugin(nil, nil, true)
		policy := p.resolveContentPolicy(policyCtx(true, map[schemas.BifrostContextKey]bool{
			schemas.BifrostContextKeyGovernanceDisableContentLogging: true,
			schemas.BifrostContextKeyDisableContentLogging:           false,
		}))
		assert.True(t, policy.storeContent, "an allowed header re-enables content the key turned off")

		policy = p.resolveContentPolicy(policyCtx(true, map[schemas.BifrostContextKey]bool{
			schemas.BifrostContextKeyGovernanceDisableContentLogging: false,
			schemas.BifrostContextKeyDisableContentLogging:           true,
		}))
		assert.False(t, policy.storeContent, "an allowed header disables content the key turned on")
	})

	t.Run("header is ignored over the key when overrides are not allowed", func(t *testing.T) {
		p := policyTestPlugin(nil, nil, true)
		policy := p.resolveContentPolicy(policyCtx(false, map[schemas.BifrostContextKey]bool{
			schemas.BifrostContextKeyGovernanceDisableContentLogging: true,
			schemas.BifrostContextKeyDisableContentLogging:           false,
		}))
		assert.False(t, policy.storeContent, "without the gate the header cannot undo the key")
	})

	t.Run("key forcing content off with retention on stores hidden", func(t *testing.T) {
		p := policyTestPlugin(nil, boolPtr(true), true)
		policy := p.resolveContentPolicy(policyCtx(false, map[schemas.BifrostContextKey]bool{
			schemas.BifrostContextKeyGovernanceDisableContentLogging: true,
		}))
		assert.True(t, policy.storeContent)
		assert.True(t, policy.hidden)
		assert.False(t, policy.visible())
	})
}

func TestResolveContentPolicyHeaderDisableWithoutGateIgnored(t *testing.T) {
	p := policyTestPlugin(nil, nil, true)

	// Without the override gate the header key is ignored.
	policy := p.resolveContentPolicy(policyCtx(false, map[schemas.BifrostContextKey]bool{
		schemas.BifrostContextKeyDisableContentLogging: true,
	}))
	assert.True(t, policy.storeContent)
	assert.False(t, policy.hidden)
}

func TestResolveContentPolicyRetainMakesDisabledHidden(t *testing.T) {
	p := policyTestPlugin(nil, boolPtr(true), true)

	// Header-disabled + retention on → content stored hidden.
	policy := p.resolveContentPolicy(policyCtx(true, map[schemas.BifrostContextKey]bool{
		schemas.BifrostContextKeyDisableContentLogging: true,
	}))
	assert.True(t, policy.storeContent)
	assert.True(t, policy.hidden)
	assert.False(t, policy.visible())
}

func TestResolveContentPolicyRetainAppliesToStaticDisable(t *testing.T) {
	p := policyTestPlugin(boolPtr(true), boolPtr(true), true)

	// Static disable + retention on → every request stored hidden, no headers needed.
	policy := p.resolveContentPolicy(policyCtx(false, nil))
	assert.True(t, policy.storeContent)
	assert.True(t, policy.hidden)
}

func TestResolveContentPolicyRetainWithoutObjectStorageDrops(t *testing.T) {
	p := policyTestPlugin(nil, boolPtr(true), false)

	// Retention configured but no object storage → degrade to dropped.
	policy := p.resolveContentPolicy(policyCtx(true, map[schemas.BifrostContextKey]bool{
		schemas.BifrostContextKeyDisableContentLogging: true,
	}))
	assert.False(t, policy.storeContent)
	assert.False(t, policy.hidden)
}

func TestResolveContentPolicyRetainDoesNotAffectNormalRequests(t *testing.T) {
	p := policyTestPlugin(nil, boolPtr(true), true)

	// Retention on but content logging not disabled → normal visible logging.
	policy := p.resolveContentPolicy(policyCtx(true, nil))
	assert.True(t, policy.storeContent)
	assert.False(t, policy.hidden)
	assert.True(t, policy.visible())
}

func TestResolveContentPolicyRetainOffKeepsCurrentBehaviour(t *testing.T) {
	p := policyTestPlugin(nil, boolPtr(false), true)

	// Explicitly-off retention behaves exactly like nil: disabled means dropped.
	policy := p.resolveContentPolicy(policyCtx(true, map[schemas.BifrostContextKey]bool{
		schemas.BifrostContextKeyDisableContentLogging: true,
	}))
	assert.False(t, policy.storeContent)
	assert.False(t, policy.hidden)
}
