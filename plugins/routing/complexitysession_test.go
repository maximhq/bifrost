package routing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
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
	key := complexitySessionKeyPrefix + "ladder"

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
	key := complexitySessionKeyPrefix + "continuation"

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
	key := complexitySessionKeyPrefix + "refresh"

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
	key := complexitySessionKeyPrefix + "expiry"

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
	key := complexitySessionKeyPrefix + "corrupt"
	require.NoError(t, store.SetWithTTL(key, "UNKNOWN", time.Minute))

	_, _, err := sessions.load(key, false)
	require.ErrorIs(t, err, errInvalidComplexitySessionTier)
	_, err = sessions.resolve(key, complexity.TierSimple)
	require.ErrorIs(t, err, errInvalidComplexitySessionTier)
}

func TestComplexitySessionStoreDecodesReplicatedString(t *testing.T) {
	sessions, store := newTestComplexitySessionStore(t, time.Minute)
	key := complexitySessionKeyPrefix + "remote"
	now := time.Now()
	require.NoError(t, store.SetRemote(key, []byte(`"MEDIUM"`), now.UnixNano(), now.Add(time.Minute).UnixNano()))

	tier, found, err := sessions.load(key, false)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, complexity.TierMedium, tier)
}

func TestBuildComplexitySessionKeyScopesAndHidesIdentity(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyUserID, "user-1")
	sessionID := "caller-session-secret"

	base := buildComplexitySessionKey(ctx, &configstoreTables.TableVirtualKey{ID: "vk-1"}, sessionID)
	require.True(t, strings.HasPrefix(base, complexitySessionKeyPrefix))
	require.NotContains(t, base, sessionID)
	require.NotContains(t, base, "user-1")
	require.NotContains(t, base, "vk-1")
	require.Equal(t, base, buildComplexitySessionKey(ctx, &configstoreTables.TableVirtualKey{ID: "vk-1"}, sessionID))

	require.NotEqual(t, base, buildComplexitySessionKey(ctx, &configstoreTables.TableVirtualKey{ID: "vk-2"}, sessionID))
	ctx.SetValue(schemas.BifrostContextKeyUserID, "user-2")
	require.NotEqual(t, base, buildComplexitySessionKey(ctx, &configstoreTables.TableVirtualKey{ID: "vk-1"}, sessionID))
	require.NotEqual(t, base, buildComplexitySessionKey(ctx, &configstoreTables.TableVirtualKey{ID: "vk-1"}, "another-session"))
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
