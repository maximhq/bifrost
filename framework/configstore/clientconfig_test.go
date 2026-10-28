package configstore

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

func TestGenerateVirtualKeyHashCoversRelayAccess(t *testing.T) {
	hash := func(vk tables.TableVirtualKey) string {
		t.Helper()
		h, err := GenerateVirtualKeyHash(vk)
		require.NoError(t, err)
		return h
	}
	base := tables.TableVirtualKey{ID: "vk-1", Name: "prod", Value: *schemas.NewSecretVar("sk-bf-1")}
	plain := hash(base)

	granted := base
	granted.RelayGrants = []tables.TableVirtualKeyRelayGrant{{RelayName: "tavily"}, {RelayName: "exa"}}
	reordered := base
	reordered.RelayGrants = []tables.TableVirtualKeyRelayGrant{{RelayName: "exa"}, {RelayName: "tavily"}}
	require.NotEqual(t, plain, hash(granted), "declared grants are a change")
	require.Equal(t, hash(granted), hash(reordered), "file order does not move the hash")

	cleared := base
	cleared.RelayGrants = []tables.TableVirtualKeyRelayGrant{}
	require.NotEqual(t, hash(granted), hash(cleared), "clearing the grants is a change")
}
