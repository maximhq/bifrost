package logging

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/grant"
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

// The log store resolves every content-logging layer through the shared tiers, not the virtual key
// alone: a higher org layer outranks the key in both directions, and peers in the credential tier
// resolve with any "off" winning.
func TestResolveContentPolicyFollowsLayerTiers(t *testing.T) {
	type stamp struct {
		layer    schemas.BifrostContextKey
		disabled bool
	}
	for _, tc := range []struct {
		name       string
		clientOff  bool
		stamps     []stamp
		wantStored bool
	}{
		{
			name:      "team off outranks a key that turns content on",
			clientOff: false,
			stamps: []stamp{
				{schemas.BifrostContextKeyTeamDisableContentLogging, true},
				{schemas.BifrostContextKeyGovernanceDisableContentLogging, false},
			},
			wantStored: false,
		},
		{
			name:      "business unit on outranks a key that turns content off",
			clientOff: false,
			stamps: []stamp{
				{schemas.BifrostContextKeyBusinessUnitDisableContentLogging, false},
				{schemas.BifrostContextKeyGovernanceDisableContentLogging, true},
			},
			wantStored: true,
		},
		{
			name:      "a provider key off beats a virtual key on in the same tier",
			clientOff: false,
			stamps: []stamp{
				{schemas.BifrostContextKeyGovernanceDisableContentLogging, false},
				{schemas.BifrostContextKeyProviderKeyDisableContentLogging, true},
			},
			wantStored: false,
		},
		{
			name:      "a user on overrides a client flag that is off",
			clientOff: true,
			stamps: []stamp{
				{schemas.BifrostContextKeyUserDisableContentLogging, false},
			},
			wantStored: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := policyTestPlugin(boolPtr(tc.clientOff), nil, true)
			ctx := policyCtx(false, nil)
			for _, s := range tc.stamps {
				schemas.StampContentLoggingDecision(ctx, s.layer, s.disabled)
			}
			assert.Equal(t, tc.wantStored, p.resolveContentPolicy(ctx).storeContent)
		})
	}
}

// A pre-hook's content decision is pending while a layer that is stamped after it could still change
// the result: the caller's layers until governance marks them resolved, and on the LLM path the
// provider key, which core picks after every PreLLMHook. An allowed header ends it, since it outranks
// every layer.
func TestContentDecisionPending(t *testing.T) {
	withIdentity := func(kind grant.CredentialKind) func(*schemas.BifrostContext) {
		return func(ctx *schemas.BifrostContext) {
			ctx.Grant().SetIdentity(grant.NewIdentity(grant.NewCredential(kind, "presented"), nil, nil, nil, nil, nil, nil))
		}
	}
	resolved := func(ctx *schemas.BifrostContext) { schemas.MarkCallerContentLoggingResolved(ctx) }
	stamp := func(layer schemas.BifrostContextKey, disabled bool) func(*schemas.BifrostContext) {
		return func(ctx *schemas.BifrostContext) { schemas.StampContentLoggingDecision(ctx, layer, disabled) }
	}
	for _, tc := range []struct {
		name    string
		setup   []func(*schemas.BifrostContext)
		wantMCP bool
		wantLLM bool
	}{
		{
			name:    "no caller: only the provider key is still to come",
			wantMCP: false, wantLLM: true,
		},
		{
			name:    "an unresolved virtual key",
			setup:   []func(*schemas.BifrostContext){withIdentity(grant.CredentialVirtualKey)},
			wantMCP: true, wantLLM: true,
		},
		{
			name:    "an unresolved user token with no virtual key",
			setup:   []func(*schemas.BifrostContext){withIdentity(grant.CredentialMCPToken)},
			wantMCP: true, wantLLM: true,
		},
		{
			name:    "a resolved caller whose layers all inherit",
			setup:   []func(*schemas.BifrostContext){withIdentity(grant.CredentialIdentityToken), resolved},
			wantMCP: false, wantLLM: true,
		},
		{
			name: "a resolved caller whose team decided leaves the provider key nothing to change",
			setup: []func(*schemas.BifrostContext){
				withIdentity(grant.CredentialVirtualKey),
				stamp(schemas.BifrostContextKeyTeamDisableContentLogging, false),
				resolved,
			},
			wantMCP: false, wantLLM: false,
		},
		{
			name: "a resolved key that turned content off leaves the provider key nothing to change",
			setup: []func(*schemas.BifrostContext){
				withIdentity(grant.CredentialVirtualKey),
				stamp(schemas.BifrostContextKeyGovernanceDisableContentLogging, true),
				resolved,
			},
			wantMCP: false, wantLLM: false,
		},
		{
			name: "an allowed header outranks every layer still to come",
			setup: []func(*schemas.BifrostContext){
				withIdentity(grant.CredentialVirtualKey),
				func(ctx *schemas.BifrostContext) {
					ctx.SetValue(schemas.BifrostContextKeyAllowPerRequestStorageOverride, true)
					ctx.SetValue(schemas.BifrostContextKeyDisableContentLogging, true)
				},
			},
			wantMCP: false, wantLLM: false,
		},
		{
			name: "a header the deployment does not allow ends nothing",
			setup: []func(*schemas.BifrostContext){
				withIdentity(grant.CredentialVirtualKey),
				func(ctx *schemas.BifrostContext) { ctx.SetValue(schemas.BifrostContextKeyDisableContentLogging, true) },
			},
			wantMCP: true, wantLLM: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
			ctx.SetGrant(grant.New())
			for _, s := range tc.setup {
				s(ctx)
			}
			assert.Equal(t, tc.wantMCP, contentDecisionPending(ctx, contentScopeMCP), "MCP scope")
			assert.Equal(t, tc.wantLLM, contentDecisionPending(ctx, contentScopeLLM), "LLM scope")
		})
	}
	assert.False(t, contentDecisionPending(nil, contentScopeLLM), "nil context")
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
