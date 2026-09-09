package configstore

import (
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGenerateMCPClientHash_NeedsSessionStickiness_NilVsFalseVsTrue pins the
// three-way distinction the config.json reconciliation-mismatch fix depends
// on:
//
//   - nil must hash identically to how the field hashed before it existed at
//     all (zero contribution) — this is what lets a pre-existing config.json
//     entry that still omits the field coincidentally match a migration-era
//     stale DB hash, so the migration's true backfill isn't clobbered by a
//     forced resync on the next restart.
//   - true and false must each hash distinctly from nil AND from each other —
//     an admin's explicit `needs_session_stickiness: false` in config.json
//     must drift the hash so the mismatch is actually detected, instead of
//     silently colliding with a stale hash and never taking effect.
func TestGenerateMCPClientHash_NeedsSessionStickiness_NilVsFalseVsTrue(t *testing.T) {
	base := func(stickiness *bool) tables.TableMCPClient {
		return tables.TableMCPClient{
			Name:                   "client",
			ConnectionType:         "http",
			AuthType:               "headers",
			NeedsSessionStickiness: stickiness,
		}
	}
	truePtr := true
	falsePtr := false

	nilHash, err := GenerateMCPClientHash(base(nil))
	require.NoError(t, err)
	trueHash, err := GenerateMCPClientHash(base(&truePtr))
	require.NoError(t, err)
	falseHash, err := GenerateMCPClientHash(base(&falsePtr))
	require.NoError(t, err)

	assert.NotEqual(t, nilHash, trueHash, "true must hash differently from nil")
	assert.NotEqual(t, nilHash, falseHash, "false must hash differently from nil — otherwise a genuine config.json edit to false is silently ignored")
	assert.NotEqual(t, trueHash, falseHash, "true and false must hash differently from each other")
}
