package governance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/governance/complexity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingSessionStore reports an error from every read so tests can prove a
// broken backend degrades to classification rather than failing the request.
type failingSessionStore struct {
	SessionStore
	getErr    error
	createErr error
	creates   int
}

func (f *failingSessionStore) Get(context.Context, string, time.Duration) (*SessionComplexityRecord, bool, error) {
	return nil, false, f.getErr
}

func (f *failingSessionStore) Create(context.Context, string, *SessionComplexityRecord, time.Duration) (bool, error) {
	f.creates++
	return false, f.createErr
}

type failingUpdateSessionStore struct {
	SessionStore
	err     error
	updates int
}

func (f *failingUpdateSessionStore) Update(
	context.Context,
	string,
	time.Duration,
	SessionRecordUpdater,
) (*SessionComplexityRecord, bool, error) {
	f.updates++
	return nil, true, f.err
}

func sessionRoutePlugin(t *testing.T, store SessionStore, sessionConfig *configstore.ComplexitySessionConfig) *GovernancePlugin {
	t.Helper()
	p := &GovernancePlugin{logger: NewMockLogger()}
	if store != nil {
		p.complexitySessionStore.Store(&store)
	}
	if sessionConfig != nil {
		p.complexitySessionConfig.Store(sessionConfig)
	}
	return p
}

func enabledSessionConfig() *configstore.ComplexitySessionConfig {
	return &configstore.ComplexitySessionConfig{
		Mode:            configstore.ComplexitySessionModePinned,
		TTL:             time.Hour,
		IdentitySources: configstore.DefaultComplexitySessionIdentitySources(),
	}
}

func cacheAwareSessionConfig() *configstore.ComplexitySessionConfig {
	config := enabledSessionConfig()
	config.Mode = configstore.ComplexitySessionModeCacheAware
	return config
}

func semanticSessionProposal(ctx *schemas.BifrostContext, tier string, score float64) *complexity.ComplexityResult {
	ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityTier, tier)
	ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityScore, score)
	ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityMechanism, complexity.MechanismSemantic)
	return &complexity.ComplexityResult{Tier: tier, Score: score}
}

func assertComplexitySessionLogContext(
	t *testing.T,
	ctx *schemas.BifrostContext,
	state *complexitySessionState,
	tierSource string,
	switchCount *int,
) {
	t.Helper()
	require.Equal(t, state.ID, ctx.Value(schemas.BifrostContextKeyGovernanceComplexitySessionID))
	assert.NotContains(t, state.Key, state.ID, "raw session identity reached the KV key")
	require.Equal(t, state.Config.Mode, ctx.Value(schemas.BifrostContextKeyGovernanceComplexitySessionMode))
	if tierSource == "" {
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceComplexitySessionTierSource))
	} else {
		assert.Equal(t, tierSource, ctx.Value(schemas.BifrostContextKeyGovernanceComplexitySessionTierSource))
	}
	if switchCount == nil {
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceComplexitySessionSwitchCount))
	} else {
		assert.Equal(t, *switchCount, ctx.Value(schemas.BifrostContextKeyGovernanceComplexitySessionSwitchCount))
	}
}

// The second argument is a deadline, not a timestamp. It has to be in the
// future: an expired context makes every store call fail validation, which the
// session path deliberately degrades to plain classification — so the tests
// would pass their assertions about fallback and silently prove nothing about
// the reuse path.
func sessionTestContext(t *testing.T, sessionID string) *schemas.BifrostContext {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	if sessionID != "" {
		ctx.SetValue(schemas.BifrostContextKeySessionID, sessionID)
	}
	return ctx
}

// The first turn classifies and remembers; every later turn reuses the tier
// without running the classifier again. Skipping that call is the entire point:
// it is what keeps the provider's prompt cache warm and avoids a per-turn
// embedding.
func TestWithComplexitySessionReusesTierAfterFirstTurn(t *testing.T) {
	sessionStore, _ := newTestKVSessionStore(t)
	p := sessionRoutePlugin(t, sessionStore, enabledSessionConfig())
	ctx := sessionTestContext(t, "session-abc")

	state := p.resolveComplexitySessionState(ctx, nil)
	require.NotNil(t, state)

	classifyCalls := 0
	classify := func() *complexity.ComplexityResult {
		classifyCalls++
		return &complexity.ComplexityResult{Tier: "COMPLEX"}
	}

	first := p.withComplexitySession(ctx, state, classify)()
	require.NotNil(t, first)
	assert.Equal(t, "COMPLEX", first.Tier)
	assert.Equal(t, 1, classifyCalls)
	assertComplexitySessionLogContext(t, ctx, state, complexitySessionTierSourceClassified, schemas.Ptr(0))

	for i := 0; i < 5; i++ {
		held := p.withComplexitySession(ctx, state, classify)()
		require.NotNil(t, held)
		assert.Equal(t, "COMPLEX", held.Tier)
	}
	assert.Equal(t, 1, classifyCalls, "classifier ran again for an established session")
	assertComplexitySessionLogContext(t, ctx, state, complexitySessionTierSourceMemoised, schemas.Ptr(0))
}

// Resolving identity is eager for sticky target selection, but observability
// must stay coupled to the lazy complexity closure. Otherwise every request in
// a session would look complexity-routed even when no rule inspected its tier.
func TestWithComplexitySessionDoesNotPublishLogContextBeforeEvaluation(t *testing.T) {
	sessionStore, _ := newTestKVSessionStore(t)
	p := sessionRoutePlugin(t, sessionStore, enabledSessionConfig())
	ctx := sessionTestContext(t, "session-abc")
	state := p.resolveComplexitySessionState(ctx, nil)
	require.NotNil(t, state)

	_ = p.withComplexitySession(ctx, state, func() *complexity.ComplexityResult {
		return &complexity.ComplexityResult{Tier: complexity.TierSimple}
	})

	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceComplexitySessionID))
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceComplexitySessionMode))
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceComplexitySessionTierSource))
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceComplexitySessionSwitchCount))
}

// A held turn must report that it was held. Recording "semantic" would make a
// log of reused turns indistinguishable from a log of embedded ones.
func TestWithComplexitySessionPublishesSessionMechanism(t *testing.T) {
	sessionStore, _ := newTestKVSessionStore(t)
	p := sessionRoutePlugin(t, sessionStore, enabledSessionConfig())
	ctx := sessionTestContext(t, "session-abc")
	state := p.resolveComplexitySessionState(ctx, nil)
	require.NotNil(t, state)

	classify := func() *complexity.ComplexityResult { return &complexity.ComplexityResult{Tier: "MEDIUM"} }
	p.withComplexitySession(ctx, state, classify)()

	held := sessionTestContext(t, "session-abc")
	result := p.withComplexitySession(held, state, classify)()
	require.NotNil(t, result)

	tier, _ := held.Value(schemas.BifrostContextKeyGovernanceComplexityTier).(string)
	mechanism, _ := held.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism).(string)
	assert.Equal(t, "MEDIUM", tier)
	assert.Equal(t, complexity.MechanismSession, mechanism)
}

// Cache-aware mode pays for a proposal on every relevant turn, but an unchanged
// decision must not turn that read path into one replicated write per request.
func TestWithComplexitySessionCacheAwareClassifiesEveryTurnWithoutWritingUnchangedTier(t *testing.T) {
	sessionStore, store := newTestKVSessionStore(t)
	_, targetKV := newTestKVSessionStore(t)
	p := sessionRoutePlugin(t, sessionStore, cacheAwareSessionConfig())
	ctx := sessionTestContext(t, "session-abc")
	state := p.resolveComplexitySessionState(ctx, nil)
	require.NotNil(t, state)

	classifyCalls := 0
	classify := func() *complexity.ComplexityResult {
		classifyCalls++
		return semanticSessionProposal(ctx, complexity.TierComplex, 0.95)
	}
	first := p.withComplexitySession(ctx, state, classify)()
	require.NotNil(t, first)
	require.Equal(t, complexity.TierComplex, first.Tier)

	// Count only established-session decisions. The initial Create is expected
	// to write; equal-tier decisions after it are not.
	delegate := &forwardingKVDelegate{target: targetKV}
	store.SetDelegate(delegate)
	for range 5 {
		result := p.withComplexitySession(ctx, state, classify)()
		require.NotNil(t, result)
		assert.Equal(t, complexity.TierComplex, result.Tier)
	}

	assert.Equal(t, 6, classifyCalls)
	assert.Zero(t, delegate.Sets(), "an unchanged cache-aware tier was replicated on every turn")
	assertComplexitySessionLogContext(t, ctx, state, complexitySessionTierSourceClassified, schemas.Ptr(0))
}

func TestWithComplexitySessionCacheAwareEscalatesAndKeepsSemanticMechanism(t *testing.T) {
	config := cacheAwareSessionConfig()
	config.SwitchMinSimilarity = 0.8
	sessionStore, _ := newTestKVSessionStore(t)
	p := sessionRoutePlugin(t, sessionStore, config)
	ctx := sessionTestContext(t, "session-abc")
	state := p.resolveComplexitySessionState(ctx, nil)
	require.NotNil(t, state)
	created, err := sessionStore.Create(context.Background(), state.Key, &SessionComplexityRecord{
		Tier:      complexity.TierSimple,
		DecidedAt: time.Now().Add(-time.Minute),
	}, state.Config.TTL)
	require.NoError(t, err)
	require.True(t, created)

	result := p.withComplexitySession(ctx, state, func() *complexity.ComplexityResult {
		return semanticSessionProposal(ctx, complexity.TierComplex, 0.93)
	})()
	require.NotNil(t, result)
	assert.Equal(t, complexity.TierComplex, result.Tier)
	assert.Equal(t, complexity.MechanismSemantic, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	assert.Equal(t, 0.93, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityScore))
	assertComplexitySessionLogContext(t, ctx, state, complexitySessionTierSourceClassified, schemas.Ptr(1))

	record, found, err := sessionStore.Get(context.Background(), state.Key, state.Config.TTL)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, complexity.TierComplex, record.Tier)
	assert.Equal(t, 1, record.SwitchCount)
	assert.False(t, record.DecidedAt.IsZero())
}

func TestWithComplexitySessionCacheAwareRequiresSustainedDowngrade(t *testing.T) {
	config := cacheAwareSessionConfig()
	config.SwitchMinSimilarity = 0.8
	config.DowngradeAfterNTurns = 2
	config.MinCachedTokensToHold = 1024
	sessionStore, _ := newTestKVSessionStore(t)
	p := sessionRoutePlugin(t, sessionStore, config)
	ctx := sessionTestContext(t, "session-abc")
	state := p.resolveComplexitySessionState(ctx, nil)
	require.NotNil(t, state)
	created, err := sessionStore.Create(context.Background(), state.Key, &SessionComplexityRecord{
		Tier:      complexity.TierComplex,
		DecidedAt: time.Now().Add(-time.Minute),
		RouteObservations: map[string]SessionRouteObservation{"route": {
			CachedReadTokens: 128,
			CacheObserved:    true,
			LastSeenAt:       time.Now(),
		}},
	}, state.Config.TTL)
	require.NoError(t, err)
	require.True(t, created)

	first := p.withComplexitySession(ctx, state, func() *complexity.ComplexityResult {
		return semanticSessionProposal(ctx, complexity.TierMedium, 0.93)
	})()
	require.NotNil(t, first)
	assert.Equal(t, complexity.TierComplex, first.Tier)
	assert.Equal(t, complexity.TierComplex, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	assert.Equal(t, complexity.MechanismSemantic, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	_, hasScore := ctx.Value(schemas.BifrostContextKeyGovernanceComplexityScore).(float64)
	assert.False(t, hasScore, "a held tier kept the score calculated for a different proposal")
	assertComplexitySessionLogContext(t, ctx, state, complexitySessionTierSourceHeld, schemas.Ptr(0))

	record, found, err := sessionStore.Get(context.Background(), state.Key, state.Config.TTL)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, complexity.TierComplex, record.Tier)
	assert.Equal(t, complexity.TierMedium, record.PendingTier)
	assert.Equal(t, 1, record.PendingTurns)

	second := p.withComplexitySession(ctx, state, func() *complexity.ComplexityResult {
		return semanticSessionProposal(ctx, complexity.TierMedium, 0.91)
	})()
	require.NotNil(t, second)
	assert.Equal(t, complexity.TierMedium, second.Tier)
	assert.Equal(t, 0.91, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityScore))
	assertComplexitySessionLogContext(t, ctx, state, complexitySessionTierSourceClassified, schemas.Ptr(1))

	record, found, err = sessionStore.Get(context.Background(), state.Key, state.Config.TTL)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, complexity.TierMedium, record.Tier)
	assert.Equal(t, 1, record.SwitchCount)
	assert.Empty(t, record.PendingTier)
	assert.Zero(t, record.PendingTurns)
}

func TestWithComplexitySessionCacheAwareHoldsWhenClassificationHasNoTier(t *testing.T) {
	sessionStore, _ := newTestKVSessionStore(t)
	p := sessionRoutePlugin(t, sessionStore, cacheAwareSessionConfig())
	ctx := sessionTestContext(t, "session-abc")
	state := p.resolveComplexitySessionState(ctx, nil)
	require.NotNil(t, state)
	created, err := sessionStore.Create(context.Background(), state.Key, &SessionComplexityRecord{
		Tier:            complexity.TierComplex,
		PendingTier:     complexity.TierMedium,
		PendingTurns:    1,
		PendingMinScore: 0.9,
	}, state.Config.TTL)
	require.NoError(t, err)
	require.True(t, created)

	result := p.withComplexitySession(ctx, state, func() *complexity.ComplexityResult {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityMechanism, complexity.MechanismSkipped)
		return nil
	})()
	require.NotNil(t, result)
	assert.Equal(t, complexity.TierComplex, result.Tier)
	assert.Equal(t, complexity.TierComplex, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	assert.Equal(t, complexity.MechanismSkipped, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	assertComplexitySessionLogContext(t, ctx, state, complexitySessionTierSourceHeld, schemas.Ptr(0))

	record, found, err := sessionStore.Get(context.Background(), state.Key, state.Config.TTL)
	require.NoError(t, err)
	require.True(t, found)
	assert.Empty(t, record.PendingTier)
	assert.Zero(t, record.PendingTurns)
}

func TestWithComplexitySessionCacheAwareFallsBackWhenUpdateFails(t *testing.T) {
	sessionStore, _ := newTestKVSessionStore(t)
	broken := &failingUpdateSessionStore{SessionStore: sessionStore, err: errors.New("backend down")}
	p := sessionRoutePlugin(t, broken, cacheAwareSessionConfig())
	ctx := sessionTestContext(t, "session-abc")
	state := p.resolveComplexitySessionState(ctx, nil)
	require.NotNil(t, state)
	created, err := sessionStore.Create(context.Background(), state.Key, &SessionComplexityRecord{
		Tier: complexity.TierSimple,
	}, state.Config.TTL)
	require.NoError(t, err)
	require.True(t, created)

	result := p.withComplexitySession(ctx, state, func() *complexity.ComplexityResult {
		return semanticSessionProposal(ctx, complexity.TierComplex, 0.95)
	})()
	require.NotNil(t, result)
	assert.Equal(t, complexity.TierComplex, result.Tier)
	assert.Equal(t, 1, broken.updates)
	assertComplexitySessionLogContext(t, ctx, state, complexitySessionTierSourceClassified, nil)

	record, found, err := sessionStore.Get(context.Background(), state.Key, state.Config.TTL)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, complexity.TierSimple, record.Tier, "a failed update mutated the stored tier")
}

// A failed or rejected classification must not be stored. Pinning "no tier"
// would turn one bad turn into a session-long outage of complexity routing.
func TestWithComplexitySessionDoesNotStoreAbsentTier(t *testing.T) {
	sessionStore, _ := newTestKVSessionStore(t)
	p := sessionRoutePlugin(t, sessionStore, enabledSessionConfig())
	ctx := sessionTestContext(t, "session-abc")
	state := p.resolveComplexitySessionState(ctx, nil)
	require.NotNil(t, state)

	classifyCalls := 0
	failing := func() *complexity.ComplexityResult {
		classifyCalls++
		return nil
	}

	require.Nil(t, p.withComplexitySession(ctx, state, failing)())
	require.Nil(t, p.withComplexitySession(ctx, state, failing)())
	assert.Equal(t, 2, classifyCalls, "a failed classification was pinned instead of retried")
	assertComplexitySessionLogContext(t, ctx, state, "", nil)

	// A later success still establishes the session.
	result := p.withComplexitySession(ctx, state, func() *complexity.ComplexityResult {
		return &complexity.ComplexityResult{Tier: "SIMPLE"}
	})()
	require.NotNil(t, result)
	assertComplexitySessionLogContext(t, ctx, state, complexitySessionTierSourceClassified, schemas.Ptr(0))
	record, found, err := sessionStore.Get(context.Background(), state.Key, time.Hour)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "SIMPLE", record.Tier)
}

// An unreadable store is not a reason to fail a request: it degrades to the
// behaviour that predates session state.
func TestWithComplexitySessionFallsBackWhenStoreFails(t *testing.T) {
	broken := &failingSessionStore{getErr: errors.New("backend down"), createErr: errors.New("backend down")}
	p := sessionRoutePlugin(t, broken, enabledSessionConfig())
	ctx := sessionTestContext(t, "session-abc")
	state := p.resolveComplexitySessionState(ctx, nil)
	require.NotNil(t, state)

	classifyCalls := 0
	result := p.withComplexitySession(ctx, state, func() *complexity.ComplexityResult {
		classifyCalls++
		return &complexity.ComplexityResult{Tier: "COMPLEX"}
	})()

	require.NotNil(t, result)
	assert.Equal(t, "COMPLEX", result.Tier)
	assert.Equal(t, 1, classifyCalls)
	assert.Equal(t, 1, broken.creates, "a failed write should not stop the request")
	assertComplexitySessionLogContext(t, ctx, state, complexitySessionTierSourceClassified, nil)
}

// The tier belongs to the conversation, not to whichever rule consulted it
// first. Binding them would let an unrelated rule edit drop every live session.
func TestWithComplexitySessionDoesNotBindTierToARule(t *testing.T) {
	sessionStore, _ := newTestKVSessionStore(t)
	p := sessionRoutePlugin(t, sessionStore, enabledSessionConfig())
	ctx := sessionTestContext(t, "session-abc")
	state := p.resolveComplexitySessionState(ctx, nil)
	require.NotNil(t, state)

	p.withComplexitySession(ctx, state, func() *complexity.ComplexityResult {
		return &complexity.ComplexityResult{Tier: "COMPLEX"}
	})()

	record, found, err := sessionStore.Get(context.Background(), state.Key, time.Hour)
	require.NoError(t, err)
	require.True(t, found)
	assert.Empty(t, record.RuleID)
	assert.False(t, record.DecidedAt.IsZero())
}

// Sessions are namespaced per virtual key, so the same id presented by two
// tenants must never share a pinned tier.
func TestResolveComplexitySessionStateNamespacesByVirtualKey(t *testing.T) {
	sessionStore, _ := newTestKVSessionStore(t)
	p := sessionRoutePlugin(t, sessionStore, enabledSessionConfig())
	ctx := sessionTestContext(t, "shared-session-id")

	first := p.resolveComplexitySessionState(ctx, &configstoreTables.TableVirtualKey{ID: "vk-1"})
	second := p.resolveComplexitySessionState(ctx, &configstoreTables.TableVirtualKey{ID: "vk-2"})
	global := p.resolveComplexitySessionState(ctx, nil)

	require.NotNil(t, first)
	require.NotNil(t, second)
	require.NotNil(t, global)
	assert.NotEqual(t, first.Key, second.Key)
	assert.NotEqual(t, first.Key, global.Key)
}

// Every "session behaviour does not apply" path must yield nil, which the caller
// reads as "classify this turn normally".
func TestResolveComplexitySessionStateDisabledPaths(t *testing.T) {
	sessionStore, _ := newTestKVSessionStore(t)

	t.Run("mode off", func(t *testing.T) {
		p := sessionRoutePlugin(t, sessionStore, nil)
		assert.Nil(t, p.resolveComplexitySessionState(sessionTestContext(t, "session-abc"), nil))
	})

	t.Run("no store attached", func(t *testing.T) {
		p := sessionRoutePlugin(t, nil, enabledSessionConfig())
		assert.Nil(t, p.resolveComplexitySessionState(sessionTestContext(t, "session-abc"), nil))
	})

	t.Run("nothing identifies the conversation", func(t *testing.T) {
		p := sessionRoutePlugin(t, sessionStore, enabledSessionConfig())
		assert.Nil(t, p.resolveComplexitySessionState(sessionTestContext(t, ""), nil))
	})

	t.Run("non-positive ttl", func(t *testing.T) {
		config := enabledSessionConfig()
		config.TTL = 0
		p := sessionRoutePlugin(t, sessionStore, config)
		assert.Nil(t, p.resolveComplexitySessionState(sessionTestContext(t, "session-abc"), nil))
	})
}

// A harness-derived identity has to reach core's key stickiness, or the API key
// rotates between turns and discards the provider cache the pinned tier exists
// to protect.
func TestPublishSessionKeyAffinityBridgesHarnessIdentity(t *testing.T) {
	sessionStore, _ := newTestKVSessionStore(t)
	config := enabledSessionConfig()
	config.TTL = 30 * time.Minute
	p := sessionRoutePlugin(t, sessionStore, config)

	// No x-bf-session-id: the identity comes from the harness rung, so nothing
	// has published a session id for key selection yet.
	ctx := sessionTestContext(t, "")
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{
		"user-agent":              "claude-cli/1.0.0",
		claudeCodeSessionIDHeader: "harness-session-1",
	})

	state := p.resolveComplexitySessionState(ctx, nil)
	require.NotNil(t, state, "harness identity should resolve")
	p.publishSessionKeyAffinity(ctx, state)

	published, _ := ctx.Value(schemas.BifrostContextKeySessionID).(string)
	assert.Equal(t, state.ID, published, "core key stickiness cannot see the derived identity")

	// Both lifetimes must agree, or the tier releases while the key stays pinned.
	ttl, _ := ctx.Value(schemas.BifrostContextKeySessionTTL).(time.Duration)
	assert.Equal(t, 30*time.Minute, ttl)
}

// Session behaviour off must leave key selection exactly as it was: core
// activates stickiness on any non-empty id, so publishing one here would pin API
// keys for callers who never enabled the feature.
func TestPublishSessionKeyAffinityIsInertWhenDisabled(t *testing.T) {
	p := sessionRoutePlugin(t, nil, nil)
	ctx := sessionTestContext(t, "")
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{
		"user-agent":              "claude-cli/1.0.0",
		claudeCodeSessionIDHeader: "harness-session-1",
	})

	state := p.resolveComplexitySessionState(ctx, nil)
	require.Nil(t, state)
	p.publishSessionKeyAffinity(ctx, state)

	published, _ := ctx.Value(schemas.BifrostContextKeySessionID).(string)
	assert.Empty(t, published, "a derived session id leaked into key selection with the feature off")
	ttl, _ := ctx.Value(schemas.BifrostContextKeySessionTTL).(time.Duration)
	assert.Zero(t, ttl)
}

// An explicit per-request ttl was asked for by name and must outrank the
// configured session window.
func TestPublishSessionKeyAffinityKeepsExplicitTTL(t *testing.T) {
	sessionStore, _ := newTestKVSessionStore(t)
	p := sessionRoutePlugin(t, sessionStore, enabledSessionConfig())
	ctx := sessionTestContext(t, "session-abc")
	ctx.SetValue(schemas.BifrostContextKeySessionTTL, 5*time.Minute)

	state := p.resolveComplexitySessionState(ctx, nil)
	require.NotNil(t, state)
	p.publishSessionKeyAffinity(ctx, state)

	ttl, _ := ctx.Value(schemas.BifrostContextKeySessionTTL).(time.Duration)
	assert.Equal(t, 5*time.Minute, ttl)
}

// Switching the mode off needs no record cleanup: the stored pins become
// unreachable on the next request and expire on their own ttl. This pins that
// claim, because the alternative — records that keep being honoured after the
// feature is switched off — would be a silent routing bug.
func TestModeOffStopsHonouringExistingPins(t *testing.T) {
	sessionStore, _ := newTestKVSessionStore(t)
	p := sessionRoutePlugin(t, sessionStore, enabledSessionConfig())
	ctx := sessionTestContext(t, "session-abc")

	state := p.resolveComplexitySessionState(ctx, nil)
	require.NotNil(t, state)
	classifyCalls := 0
	classify := func() *complexity.ComplexityResult {
		classifyCalls++
		return &complexity.ComplexityResult{Tier: "COMPLEX"}
	}
	p.withComplexitySession(ctx, state, classify)()
	require.Equal(t, 1, classifyCalls)

	// The record still exists...
	record, found, err := sessionStore.Get(context.Background(), state.Key, time.Hour)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "COMPLEX", record.Tier)

	// ...but with the mode off nothing resolves a session, so it is never read
	// and every turn classifies again.
	p.complexitySessionConfig.Store(nil)
	offState := p.resolveComplexitySessionState(ctx, nil)
	require.Nil(t, offState)
	p.withComplexitySession(ctx, offState, classify)()
	assert.Equal(t, 2, classifyCalls, "a pin was still honoured after session behaviour was switched off")
}

// With no session state the closure must be returned untouched, so the
// pre-session path keeps classifying exactly as before.
func TestWithComplexitySessionPassesThroughWhenDisabled(t *testing.T) {
	p := sessionRoutePlugin(t, nil, nil)
	ctx := sessionTestContext(t, "session-abc")

	classifyCalls := 0
	classify := func() *complexity.ComplexityResult {
		classifyCalls++
		return &complexity.ComplexityResult{Tier: "SIMPLE"}
	}

	wrapped := p.withComplexitySession(ctx, nil, classify)
	for i := 0; i < 3; i++ {
		require.NotNil(t, wrapped())
	}
	assert.Equal(t, 3, classifyCalls, "classification was suppressed with session behaviour off")
}

func chatResponseWithCacheDetails(cached int, includeDetails bool) *schemas.BifrostResponse {
	usage := &schemas.BifrostLLMUsage{TotalTokens: 100}
	if includeDetails {
		usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{CachedReadTokens: cached}
	}
	return &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{Usage: usage}}
}

func sessionWithObservations(t *testing.T) (*GovernancePlugin, *kvSessionStore, *complexitySessionState, *schemas.BifrostContext) {
	t.Helper()
	sessionStore, _ := newTestKVSessionStore(t)
	p := sessionRoutePlugin(t, sessionStore, cacheAwareSessionConfig())
	ctx := sessionTestContext(t, "session-abc")
	state := p.resolveComplexitySessionState(ctx, nil)
	require.NotNil(t, state)
	_, err := sessionStore.Create(context.Background(), state.Key, &SessionComplexityRecord{Tier: "COMPLEX"}, state.Config.TTL)
	require.NoError(t, err)
	return p, sessionStore, state, ctx
}

// Pinned mode never consumes route observations, so recording them would add a
// response-side store operation and occasional cluster broadcast for no benefit.
func TestRecordSessionRouteObservationSkipsPinnedMode(t *testing.T) {
	sessionStore, store := newTestKVSessionStore(t)
	_, targetKV := newTestKVSessionStore(t)
	p := sessionRoutePlugin(t, sessionStore, enabledSessionConfig())
	ctx := sessionTestContext(t, "session-abc")
	state := p.resolveComplexitySessionState(ctx, nil)
	require.NotNil(t, state)
	_, err := sessionStore.Create(context.Background(), state.Key, &SessionComplexityRecord{Tier: "COMPLEX"}, state.Config.TTL)
	require.NoError(t, err)

	delegate := &forwardingKVDelegate{target: targetKV}
	store.SetDelegate(delegate)
	p.recordSessionRouteObservation(ctx, chatResponseWithCacheDetails(4096, true), schemas.OpenAI, "gpt-4o", true)

	record, found, err := sessionStore.Get(context.Background(), state.Key, state.Config.TTL)
	require.NoError(t, err)
	require.True(t, found)
	assert.Empty(t, record.RouteObservations)
	assert.Zero(t, delegate.Sets(), "pinned mode wrote an unused route observation")
}

// The observation is the input cache-aware switching reasons about, so the
// provider's reported cache size has to reach the record.
func TestRecordSessionRouteObservationStoresCacheSize(t *testing.T) {
	p, sessionStore, state, ctx := sessionWithObservations(t)

	p.recordSessionRouteObservation(ctx, chatResponseWithCacheDetails(4096, true), schemas.OpenAI, "gpt-4o", true)

	record, found, err := sessionStore.Get(context.Background(), state.Key, state.Config.TTL)
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, record.RouteObservations, 1)
	for _, observation := range record.RouteObservations {
		assert.Equal(t, 4096, observation.CachedReadTokens)
		assert.True(t, observation.CacheObserved)
		assert.False(t, observation.LastSeenAt.IsZero())
	}
}

// A modality breakdown can create prompt-token details without saying anything
// about cache reuse. Treating that shape as a cold cache would make a downgrade
// discard cache state that is actually unknown.
func TestRecordSessionRouteObservationTreatsAmbiguousZeroAsUnknown(t *testing.T) {
	p, sessionStore, state, ctx := sessionWithObservations(t)
	response := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{Usage: &schemas.BifrostLLMUsage{
		TotalTokens:         100,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{TextTokens: 100},
	}}}

	p.recordSessionRouteObservation(ctx, response, schemas.Gemini, "gemini-2.5-pro", true)

	record, found, err := sessionStore.Get(context.Background(), state.Key, state.Config.TTL)
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, record.RouteObservations, 1)
	for _, observation := range record.RouteObservations {
		assert.Zero(t, observation.CachedReadTokens)
		assert.False(t, observation.CacheObserved, "modality details were recorded as proof of a cold cache")
	}
}

// Usage arrives with the final chunk. Recording earlier persists zeros and reads
// as "no cache worth keeping" on every streamed request.
func TestRecordSessionRouteObservationIgnoresNonFinalChunks(t *testing.T) {
	p, sessionStore, state, ctx := sessionWithObservations(t)

	p.recordSessionRouteObservation(ctx, chatResponseWithCacheDetails(4096, true), schemas.OpenAI, "gpt-4o", false)

	record, found, err := sessionStore.Get(context.Background(), state.Key, state.Config.TTL)
	require.NoError(t, err)
	require.True(t, found)
	assert.Empty(t, record.RouteObservations)
}

// Recording an unchanged observation every turn would put a cluster broadcast
// back on every request, undoing the read-path work.
func TestRecordSessionRouteObservationDoesNotWriteWhenUnchanged(t *testing.T) {
	sessionStore, store := newTestKVSessionStore(t)
	_, targetKV := newTestKVSessionStore(t)
	p := sessionRoutePlugin(t, sessionStore, cacheAwareSessionConfig())
	ctx := sessionTestContext(t, "session-abc")
	state := p.resolveComplexitySessionState(ctx, nil)
	require.NotNil(t, state)
	_, err := sessionStore.Create(context.Background(), state.Key, &SessionComplexityRecord{Tier: "COMPLEX"}, state.Config.TTL)
	require.NoError(t, err)

	response := chatResponseWithCacheDetails(4096, true)
	p.recordSessionRouteObservation(ctx, response, schemas.OpenAI, "gpt-4o", true)

	// Attached after the first observation so only the repeats are counted.
	delegate := &forwardingKVDelegate{target: targetKV}
	store.SetDelegate(delegate)
	for i := 0; i < 25; i++ {
		p.recordSessionRouteObservation(ctx, response, schemas.OpenAI, "gpt-4o", true)
	}
	assert.Zero(t, delegate.Sets(), "an unchanged observation was written on every turn")

	// A materially different cache size is still worth recording.
	p.recordSessionRouteObservation(ctx, chatResponseWithCacheDetails(8192, true), schemas.OpenAI, "gpt-4o", true)
	assert.NotZero(t, delegate.Sets(), "a materially changed cache size was never recorded")
}

// Fallbacks and key rotation change which cache is warm, so each effective route
// is tracked separately rather than overwriting one another.
func TestRecordSessionRouteObservationSeparatesRoutes(t *testing.T) {
	p, sessionStore, state, ctx := sessionWithObservations(t)

	p.recordSessionRouteObservation(ctx, chatResponseWithCacheDetails(4096, true), schemas.OpenAI, "gpt-4o", true)
	p.recordSessionRouteObservation(ctx, chatResponseWithCacheDetails(128, true), schemas.Anthropic, "claude-3-5-sonnet", true)

	record, found, err := sessionStore.Get(context.Background(), state.Key, state.Config.TTL)
	require.NoError(t, err)
	require.True(t, found)
	assert.Len(t, record.RouteObservations, 2)
}

// The updater executes against the latest store value. A response that finished
// earlier must not replace a newer observation merely because its cache count is
// materially different.
func TestApplySessionRouteObservationRejectsStaleObservation(t *testing.T) {
	now := time.Now()
	record := &SessionComplexityRecord{RouteObservations: map[string]SessionRouteObservation{
		"route": {
			CachedReadTokens: 4096,
			CacheObserved:    true,
			LastSeenAt:       now,
		},
	}}

	changed := applySessionRouteObservation(record, "route", SessionRouteObservation{
		CachedReadTokens: 128,
		CacheObserved:    true,
		LastSeenAt:       now.Add(-time.Second),
	}, time.Hour)
	assert.False(t, changed)
	assert.Equal(t, 4096, record.RouteObservations["route"].CachedReadTokens)
	assert.Equal(t, now, record.RouteObservations["route"].LastSeenAt)

	changed = applySessionRouteObservation(record, "route", SessionRouteObservation{
		CachedReadTokens: 8192,
		CacheObserved:    true,
		LastSeenAt:       now.Add(time.Second),
	}, time.Hour)
	assert.True(t, changed)
	assert.Equal(t, 8192, record.RouteObservations["route"].CachedReadTokens)
	assert.Equal(t, now.Add(time.Second), record.RouteObservations["route"].LastSeenAt)
}

func TestSessionCachedReadTokensRequiresPositiveEvidence(t *testing.T) {
	tests := []struct {
		name         string
		response     *schemas.BifrostResponse
		wantTokens   int
		wantObserved bool
	}{
		{
			name:     "chat details absent",
			response: chatResponseWithCacheDetails(0, false),
		},
		{
			name:     "chat details contain ambiguous zero",
			response: chatResponseWithCacheDetails(0, true),
		},
		{
			name: "chat details contain only modality usage",
			response: &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{Usage: &schemas.BifrostLLMUsage{
				PromptTokensDetails: &schemas.ChatPromptTokensDetails{TextTokens: 100},
			}}},
		},
		{
			name:         "chat reports positive cache reuse",
			response:     chatResponseWithCacheDetails(128, true),
			wantTokens:   128,
			wantObserved: true,
		},
		{
			name: "responses details contain ambiguous zero",
			response: &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{Usage: &schemas.ResponsesResponseUsage{
				InputTokensDetails: &schemas.ResponsesResponseInputTokens{},
			}}},
		},
		{
			name: "responses report positive cache reuse",
			response: &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{Usage: &schemas.ResponsesResponseUsage{
				InputTokensDetails: &schemas.ResponsesResponseInputTokens{CachedReadTokens: 256},
			}}},
			wantTokens:   256,
			wantObserved: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens, observed := sessionCachedReadTokens(tt.response)
			assert.Equal(t, tt.wantTokens, tokens)
			assert.Equal(t, tt.wantObserved, observed)
		})
	}
}

// A long session cycling through fallbacks must not grow the map without limit:
// the whole record is rewritten, and replicated, on every write.
func TestBoundSessionRouteObservationsEvictsLeastRecent(t *testing.T) {
	now := time.Now()
	observations := map[string]SessionRouteObservation{}
	for i := 0; i < maxSessionRouteObservations+4; i++ {
		observations[string(rune('a'+i))] = SessionRouteObservation{
			CachedReadTokens: i,
			CacheObserved:    true,
			LastSeenAt:       now.Add(time.Duration(i) * time.Minute),
		}
	}

	boundSessionRouteObservations(observations, maxSessionRouteObservations)

	require.Len(t, observations, maxSessionRouteObservations)
	// The four oldest went; the most recent survived.
	assert.NotContains(t, observations, "a")
	assert.Contains(t, observations, string(rune('a'+maxSessionRouteObservations+3)))
}

func TestSessionCachedTokensMateriallyChanged(t *testing.T) {
	// Crossing zero flips whether there is a cache to protect at all.
	assert.True(t, sessionCachedTokensMateriallyChanged(0, 10))
	assert.True(t, sessionCachedTokensMateriallyChanged(4096, 0))
	// Ordinary drift is not worth a cluster message.
	assert.False(t, sessionCachedTokensMateriallyChanged(4096, 4096))
	assert.False(t, sessionCachedTokensMateriallyChanged(4096, 4200))
	// A large move is.
	assert.True(t, sessionCachedTokensMateriallyChanged(4096, 8192))
}
