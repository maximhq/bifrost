package relay

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUpdateRefusesRepeatedRouteIDs checks an update sending one route ID twice is refused, as a
// create is, rather than having the repeat quietly replaced.
func TestUpdateRefusesRepeatedRouteIDs(t *testing.T) {
	m := NewManager(newMemRelayStore(), nil)
	cfg := bearerConfig("tavily", "https://api.example.com")
	cfg.Routes = []schemas.RelayRouteConfig{{Method: "POST", Path: "/search"}}
	created, err := m.Create(context.Background(), cfg)
	require.NoError(t, err)
	id := created.Routes[0].ID
	edit := bearerConfig("tavily", "https://api.example.com")
	edit.Routes = []schemas.RelayRouteConfig{{ID: id, Method: "POST", Path: "/search"}, {ID: id, Method: "POST", Path: "/extract"}}
	_, err = m.Update(context.Background(), "tavily", edit)
	assert.ErrorIs(t, err, ErrInvalidConfig)
}
