package configstore

import (
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGenerateRoutingRuleHash_StickySessions checks the two halves of the sticky_sessions
// contract: turning it on changes the hash, so a config.json edit is seen as a change, and
// leaving it off adds nothing to the hash input, so every rule stored before the field
// existed keeps its digest and an upgrade needs no config_hash backfill.
// TestGenerateRoutingRuleHash_IDDigestIsPinned pins the off case byte for byte.
func TestGenerateRoutingRuleHash_StickySessions(t *testing.T) {
	rule := tables.TableRoutingRule{
		ID:            "rule-1",
		Name:          "split",
		CelExpression: `model == "gpt-4o"`,
		Targets: []tables.TableRoutingTarget{
			{Provider: strPtr("openai"), Model: strPtr("gpt-4o"), Weight: 0.7},
			{Provider: strPtr("anthropic"), Model: strPtr("claude-sonnet-4"), Weight: 0.3},
		},
		Scope: "global",
	}

	off, err := GenerateRoutingRuleHash(rule)
	require.NoError(t, err)

	rule.StickySessions = true
	on, err := GenerateRoutingRuleHash(rule)
	require.NoError(t, err)

	assert.NotEqual(t, off, on, "turning sticky_sessions on must change the rule hash")
}
