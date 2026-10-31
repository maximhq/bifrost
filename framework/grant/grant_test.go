package grant

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The envelope a request carries its sections in. What is covered here is the contract every
// reader relies on: a nil section means not settled, and a section is replaced whole or not at all.

// grantSections builds one value of each section, for tests that need something to record.
func grantSections() (*Identity, *Access, *Limits) {
	identity := NewIdentity(NewCredential(CredentialVirtualKey, "sk-1"), nil, nil, nil, nil, nil, nil)
	access := NewAccess(held(permitWithProviders(PermitVirtualKey, "vk1", "Caller Key", "openai")), nil, "", nil)
	limits := NewLimits([]schemas.Limit{{ID: "b-1", HolderKind: string(LimitHolderVirtualKey), HolderID: "vk1"}}, nil)
	return identity, access, limits
}

func TestGrant_StartsWithNothingSettled(t *testing.T) {
	g := New()

	assert.Nil(t, g.Identity())
	assert.Nil(t, g.Access())
	assert.Nil(t, g.Limits())
}

func TestGrant_WhatIsRecordedIsWhatIsRead(t *testing.T) {
	g := New()
	identity, access, limits := grantSections()

	assert.True(t, g.SetIdentity(identity))
	assert.True(t, g.SetAccess(access))
	assert.True(t, g.SetLimits(limits))

	assert.Same(t, identity, g.Identity(), "the same value, not a copy: consumers compare identity")
	assert.Same(t, access, g.Access())
	assert.Same(t, limits, g.Limits())

	t.Run("re-recording replaces", func(t *testing.T) {
		// Resolution runs once per attempt, but a caller that resolves again must not end up with
		// two answers on one request.
		second := NewAccess(held(permitWithProviders(PermitVirtualKey, "vk2", "Other", "anthropic")), nil, "", nil)
		assert.True(t, g.SetAccess(second))

		read := g.Access()
		assert.Same(t, second, read)
		assert.False(t, read.IsProviderAllowed("openai"))
		assert.True(t, read.IsProviderAllowed("anthropic"))
	})
}

// The rule that keeps "nothing settled" distinguishable from "settled as nothing". A section can
// be replaced by a later answer but never emptied by one, because a reader cannot tell an emptied
// section from one that was never settled.
func TestGrant_RecordingNothingIsANoOp(t *testing.T) {
	t.Run("a bare nil is refused and leaves an empty section empty", func(t *testing.T) {
		g := New()

		assert.False(t, g.SetIdentity(nil))
		assert.False(t, g.SetAccess(nil))
		assert.False(t, g.SetLimits(nil))

		assert.Nil(t, g.Identity())
		assert.Nil(t, g.Access())
		assert.Nil(t, g.Limits())
	})

	t.Run("a typed nil pointer is nothing too", func(t *testing.T) {
		// A section holding a nil pointer would read as settled while answering nothing, which is
		// exactly the confusion a nil section exists to prevent.
		g := New()

		assert.False(t, g.SetIdentity((*Identity)(nil)))
		assert.False(t, g.SetAccess((*Access)(nil)))
		assert.False(t, g.SetLimits((*Limits)(nil)))

		assert.Nil(t, g.Identity())
		assert.Nil(t, g.Access())
		assert.Nil(t, g.Limits())
	})

	t.Run("recording nothing does not erase what was recorded", func(t *testing.T) {
		g := New()
		identity, access, limits := grantSections()
		require.True(t, g.SetIdentity(identity))
		require.True(t, g.SetAccess(access))
		require.True(t, g.SetLimits(limits))

		assert.False(t, g.SetIdentity(nil))
		assert.False(t, g.SetAccess((*Access)(nil)))
		assert.False(t, g.SetLimits(nil))

		assert.Same(t, identity, g.Identity())
		assert.Same(t, access, g.Access())
		assert.Same(t, limits, g.Limits())
	})
}

// Every method takes a nil receiver: a grant reaches its readers through a context that may not
// carry one, and a missing grant is not a reason to panic on a request that was going to be
// refused or left ungoverned anyway.
func TestGrant_NilReceiver(t *testing.T) {
	var g *Grant
	identity, access, limits := grantSections()

	assert.Nil(t, g.Identity())
	assert.Nil(t, g.Access())
	assert.Nil(t, g.Limits())

	assert.False(t, g.SetIdentity(identity))
	assert.False(t, g.SetAccess(access))
	assert.False(t, g.SetLimits(limits))

}

// isNil is the one place "nothing" is decided for the setters, and it has to see through every
// kind of nil that can arrive through an interface.
func TestIsNil(t *testing.T) {
	assert.True(t, isNil(nil))
	assert.True(t, isNil((*Access)(nil)))
	assert.True(t, isNil((*Identity)(nil)))
	assert.True(t, isNil((*Limits)(nil)))
	assert.True(t, isNil(([]string)(nil)))
	assert.True(t, isNil((map[string]string)(nil)))
	assert.True(t, isNil((func())(nil)))

	assert.False(t, isNil(New()))
	assert.False(t, isNil(""))
	assert.False(t, isNil(0))
	assert.False(t, isNil([]string{}))
	assert.False(t, isNil(schemas.Credential{}))
}
