package grant

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Who a request is made by, as this package holds it: built whole, read through getters, and
// never edited in place.

func TestNewIdentity(t *testing.T) {
	credential := NewCredential(CredentialVirtualKey, "sk-1")
	user := &schemas.UserRef{ID: "u1", Name: "Ada", Email: "ada@example.com"}
	virtualKey := &schemas.EntityRef{ID: "vk1", Name: "Caller Key"}
	teams := []schemas.EntityRef{{ID: "t1", Name: "Platform"}, {ID: "t2", Name: "Data"}}
	customers := []schemas.EntityRef{{ID: "c1", Name: "Acme"}}
	businessUnits := []schemas.EntityRef{{ID: "bu1", Name: "Engineering"}}
	project := &schemas.EntityRef{ID: "p1", Name: "Some Project"}

	t.Run("what it was built from is what the getters return", func(t *testing.T) {
		identity := NewIdentity(credential, user, virtualKey, teams, customers, businessUnits, project)

		assert.Equal(t, credential, identity.Credential())
		assert.True(t, identity.Presented())
		assert.Same(t, user, identity.User())
		assert.Same(t, virtualKey, identity.VirtualKey())
		assert.Equal(t, teams, identity.Teams())
		assert.Equal(t, customers, identity.Customers())
		assert.Equal(t, businessUnits, identity.BusinessUnits())
		assert.Same(t, project, identity.Project())
	})

	t.Run("nothing known is nothing", func(t *testing.T) {
		identity := NewIdentity(schemas.Credential{}, nil, nil, nil, nil, nil, nil)

		assert.Equal(t, schemas.Credential{}, identity.Credential())
		assert.False(t, identity.Presented())
		assert.Nil(t, identity.User())
		assert.Nil(t, identity.VirtualKey())
		assert.Empty(t, identity.Teams())
		assert.Empty(t, identity.Customers())
		assert.Empty(t, identity.BusinessUnits())
		assert.Nil(t, identity.Project())
	})

	// A caller that keeps its slices cannot alter what the request is attributed to.
	t.Run("the lists are copied", func(t *testing.T) {
		myTeams := append([]schemas.EntityRef(nil), teams...)
		myCustomers := append([]schemas.EntityRef(nil), customers...)
		myBusinessUnits := append([]schemas.EntityRef(nil), businessUnits...)
		identity := NewIdentity(schemas.Credential{}, nil, nil, myTeams, myCustomers, myBusinessUnits, nil)

		myTeams[0].ID = "mutated"
		myCustomers[0].ID = "mutated"
		myBusinessUnits[0].ID = "mutated"

		assert.Equal(t, "t1", identity.Teams()[0].ID)
		assert.Equal(t, "c1", identity.Customers()[0].ID)
		assert.Equal(t, "bu1", identity.BusinessUnits()[0].ID)
	})

	t.Run("the attributed one comes first", func(t *testing.T) {
		// Order is the caller's and is kept as given: whoever resolves the identity puts the
		// team the request is attributed to first, and readers rely on finding it there.
		identity := NewIdentity(schemas.Credential{}, nil, nil, teams, nil, nil, nil)
		require.Len(t, identity.Teams(), 2)
		assert.Equal(t, "t1", identity.Teams()[0].ID)
	})
}

// Presented is about whether anything was presented at all, which is a different question from
// whether what was presented resolved to something.
func TestIdentity_Presented(t *testing.T) {
	assert.True(t, NewIdentity(NewCredential(CredentialVirtualKey, "sk-1"), nil, nil, nil, nil, nil, nil).Presented())
	assert.True(t, NewIdentity(NewCredential(CredentialSessionToken, "session-1"), nil, nil, nil, nil, nil, nil).Presented())
	assert.False(t, NewIdentity(schemas.Credential{}, nil, nil, nil, nil, nil, nil).Presented())

	// A resolved user with nothing presented is still nothing presented: the two are settled by
	// different layers and answer different questions.
	resolvedOnly := NewIdentity(schemas.Credential{}, &schemas.UserRef{ID: "u1"}, nil, nil, nil, nil, nil)
	assert.False(t, resolvedOnly.Presented())
	assert.NotNil(t, resolvedOnly.User())
}

func TestNewCredential(t *testing.T) {
	credential := NewCredential(CredentialVirtualKey, "sk-1")

	assert.Equal(t, "virtual_key", credential.Kind, "the kind travels as a plain string")
	assert.Equal(t, "sk-1", credential.Value)
	assert.Equal(t, schemas.Credential{Kind: string(CredentialAPIKey), Value: "key-id"}, NewCredential(CredentialAPIKey, "key-id"))
}

// The credential kinds are one vocabulary for telling what was presented, so two kinds must never
// share a value, and none may be empty: an empty kind would read as nothing presented.
func TestCredentialKindsAreDistinct(t *testing.T) {
	kinds := []CredentialKind{
		CredentialVirtualKey, CredentialSessionToken, CredentialIdentityToken, CredentialMCPToken, CredentialAPIKey,
	}
	seen := make(map[CredentialKind]bool, len(kinds))
	for _, k := range kinds {
		assert.NotEmpty(t, string(k), "a credential kind with no value cannot name what was presented")
		assert.False(t, seen[k], "%q is declared twice", k)
		seen[k] = true
	}
	assert.Len(t, seen, len(kinds))
}

// A identity that is not there is nobody, and every reader has to be able to ask without first
// checking: a request nothing has settled yet still reaches the code that attributes it.
func TestIdentity_NilReceiver(t *testing.T) {
	var identity *Identity

	assert.Equal(t, schemas.Credential{}, identity.Credential())
	assert.False(t, identity.Presented())
	assert.Nil(t, identity.User())
	assert.Nil(t, identity.VirtualKey())
	assert.Nil(t, identity.Teams())
	assert.Nil(t, identity.Customers())
	assert.Nil(t, identity.BusinessUnits())
	assert.Nil(t, identity.Project())
}
