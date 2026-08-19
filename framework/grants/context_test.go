package grants

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

// The context is how resolved access reaches everything downstream of the layer that resolved it,
// so this pair is the seam every consumer reads through.
func TestRecordAndReadEffectiveAccess(t *testing.T) {
	base := grantWithProviders(GrantTypeVirtualKey, "vk1", "Caller Key", "openai")

	t.Run("what was recorded is what is read", func(t *testing.T) {
		ctx := newCtx()
		access := NewEffectiveAccess(base, nil, "", nil, nil)

		RecordEffectiveAccess(ctx, access)

		read := EffectiveAccessFromContext(ctx)
		require.NotNil(t, read)
		assert.Same(t, access, read, "the same value, not a copy — consumers compare identity")
		assert.True(t, read.IsProviderAllowed("openai"))
	})

	t.Run("nothing recorded reads as nothing resolved", func(t *testing.T) {
		assert.Nil(t, EffectiveAccessFromContext(newCtx()))
	})

	// The rule that keeps "no access resolved" distinguishable from "access that permits nothing".
	// A consumer branches on the difference: the first means it is running before resolution, or on
	// a request that carries no grant, and it must not read as a grant permitting nothing.
	t.Run("recording nothing is a no-op", func(t *testing.T) {
		ctx := newCtx()

		RecordEffectiveAccess(ctx, nil)

		assert.Nil(t, EffectiveAccessFromContext(ctx),
			"a nil access must leave the request indistinguishable from one nobody resolved")
	})

	t.Run("recording nothing does not erase what was recorded", func(t *testing.T) {
		ctx := newCtx()
		access := NewEffectiveAccess(base, nil, "", nil, nil)
		RecordEffectiveAccess(ctx, access)

		RecordEffectiveAccess(ctx, nil)

		assert.Same(t, access, EffectiveAccessFromContext(ctx))
	})

	t.Run("re-recording replaces", func(t *testing.T) {
		// Resolution runs once per request, but a caller that resolves again must not end up with
		// two answers on one request.
		ctx := newCtx()
		first := NewEffectiveAccess(base, nil, "", nil, nil)
		second := NewEffectiveAccess(grantWithProviders(GrantTypeVirtualKey, "vk2", "Other", "anthropic"), nil, "", nil, nil)

		RecordEffectiveAccess(ctx, first)
		RecordEffectiveAccess(ctx, second)

		read := EffectiveAccessFromContext(ctx)
		assert.Same(t, second, read)
		assert.False(t, read.IsProviderAllowed("openai"))
		assert.True(t, read.IsProviderAllowed("anthropic"))
	})

	// Both sides take a nil context because they are called from paths that may not have one, and a
	// missing context is not a reason to panic on a request that was going to be refused anyway.
	t.Run("no context", func(t *testing.T) {
		assert.Nil(t, EffectiveAccessFromContext(nil))
		assert.NotPanics(t, func() {
			RecordEffectiveAccess(nil, NewEffectiveAccess(base, nil, "", nil, nil))
			RecordEffectiveAccess(nil, nil)
		})
	})

	// The value travels under a key declared with every other context key rather than privately
	// here, so a consumer outside this package can find it at all.
	t.Run("recorded under the declared key", func(t *testing.T) {
		ctx := newCtx()
		access := NewEffectiveAccess(base, nil, "", nil, nil)
		RecordEffectiveAccess(ctx, access)

		raw := ctx.Value(schemas.BifrostContextKeyGovernanceEffectiveAccess)
		require.NotNil(t, raw)
		assert.Same(t, access, raw)
	})

	// Anything else stored under that key is not access, and reading must not mistake it for some.
	t.Run("something else under the key reads as nothing", func(t *testing.T) {
		ctx := newCtx()
		ctx.SetValue(schemas.BifrostContextKeyGovernanceEffectiveAccess, "not access")

		assert.Nil(t, EffectiveAccessFromContext(ctx))
	})

	// Access is resolved once and read many times, including from the goroutines a streaming
	// request fans out into, so reading must not race with itself.
	t.Run("concurrent readers", func(t *testing.T) {
		ctx := newCtx()
		access := NewEffectiveAccess(base, nil, "", nil, nil)
		RecordEffectiveAccess(ctx, access)

		done := make(chan struct{})
		for range 8 {
			go func() {
				defer func() { done <- struct{}{} }()
				for range 50 {
					if EffectiveAccessFromContext(ctx) != access {
						panic("read did not return the recorded access")
					}
				}
			}()
		}
		for range 8 {
			<-done
		}
	})
}

// Resolution runs from inside a plugin hook, and core blocks writes to the keys it owns for as long as
// a batch is running. Recording has to work there for the plugin the deployment named, and nowhere else:
// the recorded access is what the request is enforced and billed against, so a plugin able to overwrite
// it would widen what the request reaches with nothing re-checking, and one able to erase it would leave
// a checked request looking unchecked, which is billed nothing.
func TestAccessIsRecordedOnlyByThePluginNamedForIt(t *testing.T) {
	const resolvesAccess = "the-plugin-that-resolves-access"
	const bystander = "a-plugin-nobody-named"

	schemas.RegisterReservedKeyWriter(resolvesAccess, schemas.BifrostContextKeyGovernanceEffectiveAccess)

	base := grantWithProviders(GrantTypeVirtualKey, "vk1", "Caller Key", "openai")
	wider := grantWithProviders(GrantTypeVirtualKey, "vk2", "Wider Key", "openai", "anthropic")

	// Mid-batch, from inside the named plugin's hook: the ordinary path on every request.
	inHookAs := func(t *testing.T, plugin string) *schemas.BifrostContext {
		t.Helper()
		ctx := newCtx()
		ctx.BlockRestrictedWrites()
		t.Cleanup(ctx.UnblockRestrictedWrites)
		name := plugin
		scoped := ctx.WithPluginScope(&name)
		t.Cleanup(scoped.ReleasePluginScope)
		return scoped
	}

	t.Run("the plugin that resolves access records it", func(t *testing.T) {
		ctx := inHookAs(t, resolvesAccess)
		access := NewEffectiveAccess(base, nil, "", nil, nil)

		RecordEffectiveAccess(ctx, access)

		assert.Same(t, access, EffectiveAccessFromContext(ctx),
			"resolution happens inside a hook, so this is the window that has to work")
	})

	t.Run("another plugin cannot widen what was resolved", func(t *testing.T) {
		resolved := NewEffectiveAccess(base, nil, "", nil, nil)
		ctx := inHookAs(t, resolvesAccess)
		RecordEffectiveAccess(ctx, resolved)

		elsewhere := inHookAs(t, bystander)
		RecordEffectiveAccess(elsewhere, NewEffectiveAccess(wider, nil, "", nil, nil))

		read := EffectiveAccessFromContext(ctx)
		assert.Same(t, resolved, read)
		assert.False(t, read.IsProviderAllowed("anthropic"),
			"a provider this request never resolved access to")
	})

	t.Run("another plugin cannot unbill a checked request", func(t *testing.T) {
		resolved := NewEffectiveAccess(base, nil, "", nil, nil)
		ctx := inHookAs(t, resolvesAccess)
		RecordEffectiveAccess(ctx, resolved)

		elsewhere := inHookAs(t, bystander)
		elsewhere.ClearValue(schemas.BifrostContextKeyGovernanceEffectiveAccess)

		assert.Same(t, resolved, EffectiveAccessFromContext(ctx),
			"a cleared access reads as never resolved, and a request nobody resolved is charged nothing")
	})
}
