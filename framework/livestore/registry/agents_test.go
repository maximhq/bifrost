package registry

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

// TestAgentsPageAndByName checks that agent pages walk every registration in name order with its
// enabled and allow-by-default flags, that a disabled registration is still read, and that a name
// with no row is left out.
func TestAgentsPageAndByName(t *testing.T) {
	ctx := context.Background()
	db := newProviderDB(t, nil).DB()
	for _, a := range []*tables.TableAgentRegistration{
		{Name: "gamma", AgentCardURL: "https://gamma.example/card", Enabled: true},
		{Name: "alpha", AgentCardURL: "https://alpha.example/card", Enabled: true, AllowByDefault: true},
		{Name: "beta", AgentCardURL: "https://beta.example/card", Enabled: true},
	} {
		require.NoError(t, db.Create(a).Error)
	}
	require.NoError(t, db.Model(&tables.TableAgentRegistration{}).Where("name = ?", "beta").Update("enabled", false).Error)

	var got []Agent
	after := ""
	for {
		rows, next, err := AgentsPage(ctx, db, after, 2)
		require.NoError(t, err)
		for _, a := range rows {
			got = append(got, *a)
		}
		if next == "" {
			break
		}
		after = next
	}
	require.Equal(t, []Agent{
		{Name: "alpha", Enabled: true, AllowByDefault: true},
		{Name: "beta"},
		{Name: "gamma", Enabled: true},
	}, got)

	rows, err := AgentsByName(ctx, db, []string{"gamma", "gone"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "gamma", rows[0].Name)
}
