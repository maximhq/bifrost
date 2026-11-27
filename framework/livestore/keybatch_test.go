package livestore

import (
	"context"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/clause"
)

// manyKeys is more keys than one SQLite statement may bind (32,766). Every key the store reads is
// bound again by its grant preloads and its budget and assignment reads.
const manyKeys = 40_000

// TestRefreshReadsKeysInBatches checks that refreshes reaching more keys than one statement may
// bind read every key: a team's refresh and a customer's re-read all the keys under them, a
// refresh names the keys by ID, and a removed team's keys are re-read with the team cleared.
func TestRefreshReadsKeysInBatches(t *testing.T) {
	ctx := context.Background()
	store := governanceDB(t)
	db := store.DB()
	require.NoError(t, db.Omit(clause.Associations).Create(&tables.TableCustomer{ID: "cust-a", Name: "Acme"}).Error)
	require.NoError(t, db.Omit(clause.Associations).Create(&tables.TableTeam{ID: "team-a", Name: "Apps"}).Error)
	keys := make([]tables.TableVirtualKey, 0, 2*manyKeys)
	teamKeys := make([]Ref, 0, manyKeys+1)
	for i := range manyKeys {
		team, customer := "team-a", "cust-a"
		id := fmt.Sprintf("vk-t-%05d", i)
		keys = append(keys, tables.TableVirtualKey{ID: id, Name: id, Value: *schemas.NewSecretVar("sk-" + id), TeamID: &team})
		teamKeys = append(teamKeys, Ref{Kind: KindVirtualKey, ID: id})
		id = fmt.Sprintf("vk-c-%05d", i)
		keys = append(keys, tables.TableVirtualKey{ID: id, Name: id, Value: *schemas.NewSecretVar("sk-" + id), CustomerID: &customer})
	}
	require.NoError(t, db.Omit(clause.Associations).CreateInBatches(keys, 500).Error)
	s := loadedGovernance(t, db)
	cols := s.Collections

	rename := func(id, name string) {
		t.Helper()
		require.NoError(t, db.Model(&tables.TableVirtualKey{}).Where("id = ?", id).UpdateColumn("name", name).Error)
	}
	rename("vk-t-01234", "renamed through the team")
	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: KindTeam, ID: "team-a"}}), "a team's refresh")
	k, ok := cols.VirtualKeys.Get("vk-t-01234")
	require.True(t, ok)
	require.Equal(t, "renamed through the team", k.Name)

	rename("vk-c-01234", "renamed through the customer")
	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: KindCustomer, ID: "cust-a"}}), "a customer's refresh")
	k, ok = cols.VirtualKeys.Get("vk-c-01234")
	require.True(t, ok)
	require.Equal(t, "renamed through the customer", k.Name)

	rename("vk-t-04321", "renamed by ID")
	require.NoError(t, s.Refresh(ctx, append(teamKeys, Ref{Kind: KindVirtualKey, ID: "missing"})), "a refresh by ID")
	k, ok = cols.VirtualKeys.Get("vk-t-04321")
	require.True(t, ok)
	require.Equal(t, "renamed by ID", k.Name)

	require.NoError(t, db.Model(&tables.TableVirtualKey{}).Where("team_id = ?", "team-a").UpdateColumn("team_id", nil).Error)
	require.NoError(t, db.Delete(&tables.TableTeam{ID: "team-a"}).Error)
	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: KindTeam, ID: "team-a", Remove: true}}), "a removed team's re-read")
	held := 0
	cols.VirtualKeys.Range(func(k *registry.VirtualKey) bool {
		if k.TeamID == "team-a" {
			t.Fatalf("%s still names the removed team", k.ID)
		}
		held++
		return true
	})
	require.Equal(t, 2*manyKeys+2, held, "every key stays, the fixture's two included")
}
