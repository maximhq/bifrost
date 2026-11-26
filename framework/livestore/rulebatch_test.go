package livestore

import (
	"context"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/clause"
)

// manyRules is more routing rules than one SQLite statement may bind (32,766).
const manyRules = 40_000

// TestRefreshReadsRoutingRulesInBatches checks that a refresh naming more routing rules than one
// statement may bind reads every one: reading a rule preloads its targets, which binds the IDs of
// every rule read again, so the store reads the rules in batches.
func TestRefreshReadsRoutingRulesInBatches(t *testing.T) {
	ctx := context.Background()
	db := governanceDB(t).DB()
	rules := make([]tables.TableRoutingRule, 0, manyRules)
	refs := make([]Ref, 0, manyRules+1)
	for i := range manyRules {
		id := fmt.Sprintf("r-%05d", i)
		rules = append(rules, tables.TableRoutingRule{ID: id, Name: id, Scope: "global", CelExpression: "true"})
		refs = append(refs, Ref{Kind: KindRoutingRule, ID: id})
	}
	require.NoError(t, db.Omit(clause.Associations).CreateInBatches(rules, 500).Error)
	s := loadedGovernance(t, db)

	require.NoError(t, db.Model(&tables.TableRoutingRule{}).Where("id = ?", "r-01234").UpdateColumn("cel_expression", "false").Error)
	require.NoError(t, s.Refresh(ctx, append(refs, Ref{Kind: KindRoutingRule, ID: "missing"})))
	r, ok := s.Collections.RoutingRules.Get("r-01234")
	require.True(t, ok)
	require.Equal(t, "false", r.CelExpression)
	n := 0
	s.Collections.RoutingRules.Range(func(*registry.RoutingRule) bool { n++; return true })
	require.Equal(t, manyRules, n)
}
