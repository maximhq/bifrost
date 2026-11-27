package routing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/rules"
)

func testEnabledSessionAnalyzerConfig() *complexity.AnalyzerConfig {
	cfg := complexity.DefaultAnalyzerConfig()
	cfg.Semantic = &configstore.ComplexitySemanticConfig{
		Provider:       schemas.OpenAI,
		EmbeddingModel: "text-embedding-3-small",
	}
	cfg.Session = &configstore.ComplexitySessionConfig{Enabled: true}
	return &cfg
}

func TestRoutingPluginRequiresSessionStoreForSessionMode(t *testing.T) {
	logger := rules.NewMockLogger()
	ruleStore, err := rules.NewTestRules(context.Background(), t.TempDir(), logger)
	require.NoError(t, err)
	t.Cleanup(ruleStore.Close)

	plugin, err := InitFromStore(
		context.Background(),
		&Config{ComplexityAnalyzerConfig: testEnabledSessionAnalyzerConfig()},
		logger,
		nil,
		ruleStore.Store,
		NewMockGovernance(),
	)
	require.ErrorContains(t, err, "requires a session store")
	require.Nil(t, plugin)
}

// stringTableForTest returns a table of strings that keeps write times, on a new live store.
func stringTableForTest(t *testing.T) *livestore.Table[string, string] {
	t.Helper()
	live, err := livestore.New()
	require.NoError(t, err)
	t.Cleanup(live.Close)
	table, err := live.NewTable[string, string](context.Background(), "test_strings", livestore.WithWriteTimes())
	require.NoError(t, err)
	return table
}

// tableLen counts a table's live entries.
func tableLen(table *livestore.Table[string, string]) int {
	n := 0
	table.Range(func(string, string) bool { n++; return true })
	return n
}

// newSessionTestPlugin returns a routing plugin over a new session table, and the table.
func newSessionTestPlugin(t *testing.T) (*RoutingPlugin, *livestore.Table[string, string]) {
	t.Helper()
	sessions := stringTableForTest(t)
	return &RoutingPlugin{sessions: sessions}, sessions
}

// requireExpiresAfter checks that key's entry in store expires ttl after a moment between before
// and after, the window around the write that set it.
func requireExpiresAfter(t *testing.T, store *livestore.Table[string, string], key string, ttl time.Duration, before, after time.Time) time.Time {
	t.Helper()
	at, ok := store.Expiry(key)
	require.True(t, ok, "the entry has an expiry")
	require.False(t, at.Before(before.Add(ttl)), "expires %v, written no earlier than %v", at, before)
	require.False(t, at.After(after.Add(ttl)), "expires %v, written no later than %v", at, after)
	return at
}

func TestComplexitySessionTierMonotonicLadder(t *testing.T) {
	plugin, _ := newSessionTestPlugin(t)
	key := "complexity-test:ladder"

	resolution, err := plugin.resolveSessionTier(key, complexity.TierSimple)
	require.NoError(t, err)
	require.False(t, resolution.Existed)
	require.Equal(t, complexity.TierSimple, resolution.EffectiveTier)

	resolution, err = plugin.resolveSessionTier(key, complexity.TierMedium)
	require.NoError(t, err)
	require.True(t, resolution.Escalated)
	require.Equal(t, complexity.TierMedium, resolution.EffectiveTier)

	resolution, err = plugin.resolveSessionTier(key, complexity.TierSimple)
	require.NoError(t, err)
	require.False(t, resolution.Escalated)
	require.Equal(t, complexity.TierMedium, resolution.EffectiveTier)

	resolution, err = plugin.resolveSessionTier(key, complexity.TierComplex)
	require.NoError(t, err)
	require.True(t, resolution.Escalated)
	require.Equal(t, complexity.TierComplex, resolution.EffectiveTier)

	resolution, err = plugin.resolveSessionTier(key, complexity.TierSimple)
	require.NoError(t, err)
	require.Equal(t, complexity.TierComplex, resolution.EffectiveTier)
}

func TestComplexitySessionTierEmptyProposalOnlyRefreshesExisting(t *testing.T) {
	plugin, store := newSessionTestPlugin(t)
	key := "complexity-test:continuation"

	resolution, err := plugin.resolveSessionTier(key, "")
	require.NoError(t, err)
	require.Empty(t, resolution.EffectiveTier)
	require.Equal(t, 0, tableLen(store))

	_, err = plugin.resolveSessionTier(key, complexity.TierMedium)
	require.NoError(t, err)
	resolution, err = plugin.resolveSessionTier(key, "")
	require.NoError(t, err)
	require.True(t, resolution.Existed)
	require.Equal(t, complexity.TierMedium, resolution.EffectiveTier)
}

func TestComplexitySessionTierRefreshesTTLWhenTierDoesNotChange(t *testing.T) {
	plugin, store := newSessionTestPlugin(t)
	const sessionTTL = complexitySessionInactivityTTL
	key := "complexity-test:refresh"

	before := time.Now()
	_, err := plugin.resolveSessionTier(key, complexity.TierMedium)
	require.NoError(t, err)
	first := requireExpiresAfter(t, store, key, sessionTTL, before, time.Now())

	time.Sleep(5 * time.Millisecond)
	before = time.Now()
	resolution, err := plugin.resolveSessionTier(key, complexity.TierSimple)
	require.NoError(t, err)
	require.Equal(t, complexity.TierMedium, resolution.EffectiveTier)
	second := requireExpiresAfter(t, store, key, sessionTTL, before, time.Now())
	require.True(t, second.After(first), "a lower proposal still refreshes the TTL")

	time.Sleep(5 * time.Millisecond)
	before = time.Now()
	tier, found, err := plugin.loadSessionTier(key, true)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, complexity.TierMedium, tier)
	third := requireExpiresAfter(t, store, key, sessionTTL, before, time.Now())
	require.True(t, third.After(second), "a refreshing load refreshes the TTL")
}

func TestComplexitySessionTierExpiryStartsNewEpoch(t *testing.T) {
	plugin, store := newSessionTestPlugin(t)
	key := "complexity-test:expiry"

	_, err := plugin.resolveSessionTier(key, complexity.TierComplex)
	require.NoError(t, err)
	store.PutWithTTL(key, complexity.TierComplex, 10*time.Millisecond) // the entry runs out
	time.Sleep(25 * time.Millisecond)

	tier, found, err := plugin.loadSessionTier(key, false)
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, tier)

	resolution, err := plugin.resolveSessionTier(key, complexity.TierSimple)
	require.NoError(t, err)
	require.False(t, resolution.Existed)
	require.Equal(t, complexity.TierSimple, resolution.EffectiveTier)
}

func TestComplexitySessionTierRejectsCorruptTier(t *testing.T) {
	plugin, store := newSessionTestPlugin(t)
	key := "complexity-test:corrupt"
	store.PutWithTTL(key, "UNKNOWN", time.Minute)

	_, _, err := plugin.loadSessionTier(key, false)
	require.ErrorIs(t, err, errInvalidComplexitySessionTier)
	_, err = plugin.resolveSessionTier(key, complexity.TierSimple)
	require.ErrorIs(t, err, errInvalidComplexitySessionTier)
}

func TestComplexitySessionTierReadsAStoredTier(t *testing.T) {
	plugin, store := newSessionTestPlugin(t)
	key := "complexity-test:stored"
	store.PutWithTTL(key, "MEDIUM", time.Minute)

	tier, found, err := plugin.loadSessionTier(key, false)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, complexity.TierMedium, tier)
}

func TestComplexitySessionKeyScopesByIdentityAndHidesIt(t *testing.T) {
	sessionID := "caller-session-secret"
	attributed := func(sessionID, virtualKeyID, userID string) *schemas.BifrostContext {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeySessionID, sessionID)
		var user *schemas.UserRef
		if userID != "" {
			user = &schemas.UserRef{ID: userID}
		}
		var virtualKey *schemas.EntityRef
		if virtualKeyID != "" {
			virtualKey = &schemas.EntityRef{ID: virtualKeyID}
		}
		g := grant.New()
		g.SetIdentity(grant.NewIdentity(schemas.Credential{}, user, virtualKey, nil, nil, nil, nil))
		ctx.SetGrant(g)
		return ctx
	}

	base := complexitySessionKey(attributed(sessionID, "vk-1", "user-1"))
	require.True(t, strings.HasPrefix(base, "session:v2:complexity:"))
	require.NotContains(t, base, sessionID)
	require.NotContains(t, base, "user-1")
	require.NotContains(t, base, "vk-1")
	require.Equal(t, base, complexitySessionKey(attributed(sessionID, "vk-1", "user-1")))

	require.NotEqual(t, base, complexitySessionKey(attributed(sessionID, "vk-2", "user-1")))
	require.NotEqual(t, base, complexitySessionKey(attributed(sessionID, "vk-1", "user-2")))
	require.NotEqual(t, base, complexitySessionKey(attributed("another-session", "vk-1", "user-1")))

	// A request nothing governs keys under the deployment, apart from any caller's scope.
	ungoverned := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ungoverned.SetValue(schemas.BifrostContextKeySessionID, sessionID)
	require.NotEqual(t, base, complexitySessionKey(ungoverned))
}

func TestComplexityStateKeysScopeSubagents(t *testing.T) {
	withHeaders := func(headers map[string]string) *schemas.BifrostContext {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeySessionID, "shared-session")
		ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, headers)
		return ctx
	}

	parent := withHeaders(nil)
	// The main conversation keeps the keys it had before agents were told apart.
	require.Equal(t, bifrost.SessionStateKey(parent, complexitySessionKind), complexitySessionKey(parent))
	require.Equal(t, bifrost.SessionStateKey(parent, complexityTurnTierKind), complexityTurnTierKey(parent))
	// A Codex root thread names itself but no parent, so it is the main conversation.
	require.Equal(t, complexitySessionKey(parent), complexitySessionKey(withHeaders(map[string]string{"thread-id": "root"})))

	agents := []*schemas.BifrostContext{
		withHeaders(map[string]string{"x-claude-code-agent-id": "agent-a"}),
		withHeaders(map[string]string{"x-claude-code-agent-id": "agent-b"}),
		withHeaders(map[string]string{"x-codex-parent-thread-id": "root", "thread-id": "child"}),
	}
	seen := map[string]bool{complexitySessionKey(parent): true, complexityTurnTierKey(parent): true}
	for _, agent := range agents {
		for _, key := range []string{complexitySessionKey(agent), complexityTurnTierKey(agent)} {
			require.False(t, seen[key], "each agent keeps its own complexity state")
			seen[key] = true
		}
	}
	require.Equal(t, complexitySessionKey(agents[0]), complexitySessionKey(withHeaders(map[string]string{"x-claude-code-agent-id": "agent-a"})))
}

func TestPublishLocalSessionFallbackPreservesProposalEvidence(t *testing.T) {
	score := 0.826
	proposal := complexityProposal{
		Result:          &complexity.ComplexityResult{Tier: complexity.TierSimple},
		Mechanism:       complexity.MechanismSemantic,
		Score:           &score,
		MatchedExemplar: "a casual greeting",
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	plugin := &RoutingPlugin{}
	result := plugin.publishLocalSessionFallback(ctx, plugin.complexityRuntime(), complexity.TierMedium, true, proposal)

	require.Equal(t, complexity.TierMedium, result.Tier)
	require.Equal(t, complexity.MechanismSession, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	require.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityScore))
	logs := ctx.GetRoutingEngineLogs()
	require.Len(t, logs, 1)
	require.Equal(
		t,
		`Session complexity reused after state-store failure: effective=MEDIUM proposed=SIMPLE source=semantic proposed_similarity=0.83 proposed_matched="a casual greeting"`,
		logs[0].Message,
	)
}
