package routing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/kvstore"
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

func TestRoutingPluginRequiresKVStoreForSessionMode(t *testing.T) {
	logger := rules.NewMockLogger()
	ruleStore, err := rules.NewLocalStore(context.Background(), logger, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(
		context.Background(),
		&Config{ComplexityAnalyzerConfig: testEnabledSessionAnalyzerConfig()},
		logger,
		nil,
		ruleStore,
		NewMockGovernance(),
	)
	require.ErrorContains(t, err, "requires a KV store")
	require.Nil(t, plugin)
}

func newTestComplexitySessionStore(t *testing.T, ttl time.Duration) (*complexitySessionStore, *kvstore.Store) {
	t.Helper()
	store, err := kvstore.New(kvstore.Config{CleanupInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return newComplexitySessionStore(store, ttl), store
}

type recordingSessionKVStore struct {
	schemas.KVStore
	setTTLs []time.Duration
}

func (s *recordingSessionKVStore) SetWithTTL(key string, value any, ttl time.Duration) error {
	s.setTTLs = append(s.setTTLs, ttl)
	return s.KVStore.SetWithTTL(key, value, ttl)
}

func TestComplexitySessionStoreMonotonicLadder(t *testing.T) {
	sessions, _ := newTestComplexitySessionStore(t, time.Minute)
	key := "complexity-test:ladder"

	resolution, err := sessions.resolve(key, complexity.TierSimple)
	require.NoError(t, err)
	require.False(t, resolution.Existed)
	require.Equal(t, complexity.TierSimple, resolution.EffectiveTier)

	resolution, err = sessions.resolve(key, complexity.TierMedium)
	require.NoError(t, err)
	require.True(t, resolution.Escalated)
	require.Equal(t, complexity.TierMedium, resolution.EffectiveTier)

	resolution, err = sessions.resolve(key, complexity.TierSimple)
	require.NoError(t, err)
	require.False(t, resolution.Escalated)
	require.Equal(t, complexity.TierMedium, resolution.EffectiveTier)

	resolution, err = sessions.resolve(key, complexity.TierComplex)
	require.NoError(t, err)
	require.True(t, resolution.Escalated)
	require.Equal(t, complexity.TierComplex, resolution.EffectiveTier)

	resolution, err = sessions.resolve(key, complexity.TierSimple)
	require.NoError(t, err)
	require.Equal(t, complexity.TierComplex, resolution.EffectiveTier)
}

func TestComplexitySessionStoreEmptyProposalOnlyRefreshesExisting(t *testing.T) {
	sessions, store := newTestComplexitySessionStore(t, time.Minute)
	key := "complexity-test:continuation"

	resolution, err := sessions.resolve(key, "")
	require.NoError(t, err)
	require.Empty(t, resolution.EffectiveTier)
	require.Equal(t, 0, store.Len())

	_, err = sessions.resolve(key, complexity.TierMedium)
	require.NoError(t, err)
	resolution, err = sessions.resolve(key, "")
	require.NoError(t, err)
	require.True(t, resolution.Existed)
	require.Equal(t, complexity.TierMedium, resolution.EffectiveTier)
}

func TestComplexitySessionStoreRefreshesTTLWhenTierDoesNotChange(t *testing.T) {
	store, err := kvstore.New(kvstore.Config{CleanupInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	const sessionTTL = 45 * time.Minute
	recordingStore := &recordingSessionKVStore{KVStore: store}
	sessions := newComplexitySessionStore(recordingStore, sessionTTL)
	key := "complexity-test:refresh"

	_, err = sessions.resolve(key, complexity.TierMedium)
	require.NoError(t, err)
	resolution, err := sessions.resolve(key, complexity.TierSimple)
	require.NoError(t, err)
	require.Equal(t, complexity.TierMedium, resolution.EffectiveTier)
	tier, found, err := sessions.load(key, true)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, complexity.TierMedium, tier)

	require.Equal(t, []time.Duration{sessionTTL, sessionTTL, sessionTTL}, recordingStore.setTTLs)
}

func TestComplexitySessionStoreExpiryStartsNewEpoch(t *testing.T) {
	sessions, _ := newTestComplexitySessionStore(t, 10*time.Millisecond)
	key := "complexity-test:expiry"

	_, err := sessions.resolve(key, complexity.TierComplex)
	require.NoError(t, err)
	time.Sleep(25 * time.Millisecond)

	tier, found, err := sessions.load(key, false)
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, tier)

	resolution, err := sessions.resolve(key, complexity.TierSimple)
	require.NoError(t, err)
	require.False(t, resolution.Existed)
	require.Equal(t, complexity.TierSimple, resolution.EffectiveTier)
}

func TestComplexitySessionStoreRejectsCorruptTier(t *testing.T) {
	sessions, store := newTestComplexitySessionStore(t, time.Minute)
	key := "complexity-test:corrupt"
	require.NoError(t, store.SetWithTTL(key, "UNKNOWN", time.Minute))

	_, _, err := sessions.load(key, false)
	require.ErrorIs(t, err, errInvalidComplexitySessionTier)
	_, err = sessions.resolve(key, complexity.TierSimple)
	require.ErrorIs(t, err, errInvalidComplexitySessionTier)
}

func TestComplexitySessionStoreDecodesReplicatedString(t *testing.T) {
	sessions, store := newTestComplexitySessionStore(t, time.Minute)
	key := "complexity-test:remote"
	now := time.Now()
	require.NoError(t, store.SetRemote(key, []byte(`"MEDIUM"`), now.UnixNano(), now.Add(time.Minute).UnixNano()))

	tier, found, err := sessions.load(key, false)
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

func TestPublishLocalSessionFallbackPreservesProposalEvidence(t *testing.T) {
	score := 0.826
	proposal := complexityProposal{
		Result:          &complexity.ComplexityResult{Tier: complexity.TierSimple},
		Mechanism:       complexity.MechanismSemantic,
		Score:           &score,
		MatchedExemplar: "a casual greeting",
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	result := (&RoutingPlugin{}).publishLocalSessionFallback(ctx, complexity.TierMedium, true, proposal)

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

// TestRoutingPluginAppliesSessionIdleTimeoutOnReload pins that a saved timeout
// reaches the store without a restart, and that clearing it restores the default.
func TestRoutingPluginAppliesSessionIdleTimeoutOnReload(t *testing.T) {
	sessions, _ := newTestComplexitySessionStore(t, complexitySessionInactivityTTL)
	p := &RoutingPlugin{sessionStore: sessions}

	config := testEnabledSessionAnalyzerConfig()
	config.Session.IdleTimeout = 10 * time.Minute
	require.NoError(t, p.ReloadComplexityAnalyzerConfig(config))
	require.Equal(t, 10*time.Minute, sessions.inactivityTTL())

	require.NoError(t, p.ReloadComplexityAnalyzerConfig(testEnabledSessionAnalyzerConfig()))
	require.Equal(t, complexitySessionInactivityTTL, sessions.inactivityTTL())
}

// TestComplexitySessionStoreUsesReloadedTTL pins that the next refresh uses the
// new window, so a live session moves onto it at its next turn.
func TestComplexitySessionStoreUsesReloadedTTL(t *testing.T) {
	store, err := kvstore.New(kvstore.Config{CleanupInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	recordingStore := &recordingSessionKVStore{KVStore: store}
	sessions := newComplexitySessionStore(recordingStore, time.Hour)

	_, err = sessions.resolve("complexity-test:reload", complexity.TierMedium)
	require.NoError(t, err)
	sessions.setTTL(10 * time.Minute)
	_, _, err = sessions.load("complexity-test:reload", true)
	require.NoError(t, err)
	require.Equal(t, []time.Duration{time.Hour, 10 * time.Minute}, recordingStore.setTTLs)
}

// TestPersistComplexityTurnTierHonorsIdleTimeout pins the continuation store's
// lifetime. It outlives the session otherwise, and a tool continuation would
// read COMPLEX from it and recreate the session the timeout just released.
func TestPersistComplexityTurnTierHonorsIdleTimeout(t *testing.T) {
	tests := []struct {
		name        string
		sessionOn   bool
		idleTimeout time.Duration
		requestTTL  time.Duration
		want        time.Duration
	}{
		{name: "unset keeps the sticky default", sessionOn: true, want: schemas.DefaultSessionStickyTTL},
		{name: "shorter timeout caps it", sessionOn: true, idleTimeout: 10 * time.Minute, want: 10 * time.Minute},
		{name: "longer timeout leaves it", sessionOn: true, idleTimeout: 2 * time.Hour, want: schemas.DefaultSessionStickyTTL},
		{name: "shorter request ttl wins", sessionOn: true, idleTimeout: 10 * time.Minute, requestTTL: 5 * time.Minute, want: 5 * time.Minute},
		{name: "longer request ttl is capped", sessionOn: true, idleTimeout: 10 * time.Minute, requestTTL: 2 * time.Hour, want: 10 * time.Minute},
		{name: "session routing off ignores it", idleTimeout: 10 * time.Minute, want: schemas.DefaultSessionStickyTTL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := kvstore.New(kvstore.Config{CleanupInterval: time.Hour})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			recordingStore := &recordingSessionKVStore{KVStore: store}
			p := &RoutingPlugin{turnTierStore: &complexityTurnTierStore{store: recordingStore}}
			config := testEnabledSessionAnalyzerConfig()
			config.Session = &configstore.ComplexitySessionConfig{Enabled: tt.sessionOn, IdleTimeout: tt.idleTimeout}
			p.complexityConfig.Store(config)
			p.sessionEnabled.Store(tt.sessionOn)

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			t.Cleanup(ctx.Cancel)
			ctx.SetValue(schemas.BifrostContextKeySessionID, "idle-session")
			ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityTier, complexity.TierComplex)
			if tt.requestTTL > 0 {
				ctx.SetValue(schemas.BifrostContextKeySessionTTL, tt.requestTTL)
			}
			p.persistComplexityTurnTier(ctx)
			require.Equal(t, []time.Duration{tt.want}, recordingStore.setTTLs)
		})
	}
}

// BenchmarkComplexitySessionStoreResolve measures one session-tier refresh, the
// per-request cost of session routing, against the in-memory KV store.
func BenchmarkComplexitySessionStoreResolve(b *testing.B) {
	store, err := kvstore.New(kvstore.Config{CleanupInterval: time.Hour})
	require.NoError(b, err)
	b.Cleanup(func() { require.NoError(b, store.Close()) })
	sessions := newComplexitySessionStore(store, time.Hour)
	key := "complexity-bench:resolve"
	_, err = sessions.resolve(key, complexity.TierMedium)
	require.NoError(b, err)
	b.ReportAllocs()
	for b.Loop() {
		_, _ = sessions.resolve(key, complexity.TierSimple)
	}
}
