package routing

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

func TestKVSessionStoreRoundTripAndOwnership(t *testing.T) {
	sessionStore, _ := newTestKVSessionStore(t)
	key := testComplexitySessionKey(t, "tenant", "round-trip")
	decidedAt := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	record := &SessionComplexityRecord{
		Tier:         "complex",
		DecidedAt:    decidedAt,
		RuleID:       "rule-1",
		SwitchCount:  2,
		PendingTurns: 1,
	}
	want := cloneSessionComplexityRecord(record)

	created, err := sessionStore.Create(context.Background(), key, record, time.Hour)
	require.NoError(t, err)
	require.True(t, created)

	// Mutating the input after Create must not mutate stored state.
	record.Tier = "simple"

	got, found, err := sessionStore.Get(context.Background(), key, time.Hour)
	require.NoError(t, err)
	require.True(t, found)
	// The store stamps RefreshedAt itself, so it is asserted separately and
	// cleared before the structural comparison.
	assert.False(t, got.RefreshedAt.IsZero(), "Create should stamp the refresh clock")
	got.RefreshedAt = time.Time{}
	want.RefreshedAt = time.Time{}
	assert.Equal(t, want, got)

	// Mutating a returned record must not mutate stored state either.
	got.PendingTurns = 99

	gotAgain, found, err := sessionStore.Get(context.Background(), key, time.Hour)
	require.NoError(t, err)
	require.True(t, found)
	gotAgain.RefreshedAt = time.Time{}
	assert.Equal(t, want, gotAgain)
}

func TestKVSessionStoreCreateIsAtomic(t *testing.T) {
	sessionStore, _ := newTestKVSessionStore(t)
	key := testComplexitySessionKey(t, "tenant", "create-race")

	const contenders = 50
	var createdCount atomic.Int32
	errCh := make(chan error, contenders)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			created, err := sessionStore.Create(context.Background(), key, &SessionComplexityRecord{
				Tier:   "complex",
				RuleID: "rule-1",
			}, time.Hour)
			if err != nil {
				errCh <- err
				return
			}
			if created {
				createdCount.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("create failed: %v", err)
	}
	assert.EqualValues(t, 1, createdCount.Load())
}

func TestKVSessionStoreUpdateIsAtomic(t *testing.T) {
	sessionStore, _ := newTestKVSessionStore(t)
	key := testComplexitySessionKey(t, "tenant", "update-race")
	created, err := sessionStore.Create(context.Background(), key, &SessionComplexityRecord{
		Tier:   "complex",
		RuleID: "rule-1",
	}, time.Hour)
	require.NoError(t, err)
	require.True(t, created)

	const updates = 100
	errCh := make(chan error, updates)
	var wg sync.WaitGroup
	for range updates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, found, err := sessionStore.Update(context.Background(), key, time.Hour, func(record *SessionComplexityRecord) error {
				record.PendingTurns++
				return nil
			})
			if err != nil {
				errCh <- err
				return
			}
			if !found {
				errCh <- kvstore.ErrNotFound
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("update failed: %v", err)
	}

	record, found, err := sessionStore.Get(context.Background(), key, time.Hour)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, updates, record.PendingTurns)
}

func TestKVSessionStoreUpdateFailureDoesNotMutate(t *testing.T) {
	sessionStore, _ := newTestKVSessionStore(t)
	key := testComplexitySessionKey(t, "tenant", "failed-update")
	created, err := sessionStore.Create(context.Background(), key, &SessionComplexityRecord{
		Tier:         "complex",
		RuleID:       "rule-1",
		PendingTurns: 1,
	}, time.Hour)
	require.NoError(t, err)
	require.True(t, created)

	wantErr := errors.New("policy rejected update")
	_, found, err := sessionStore.Update(context.Background(), key, time.Hour, func(record *SessionComplexityRecord) error {
		record.PendingTurns = 99
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)
	require.True(t, found)

	record, found, err := sessionStore.Get(context.Background(), key, time.Hour)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, 1, record.PendingTurns)
}

func TestKVSessionStoreSlidingTTL(t *testing.T) {
	sessionStore, _ := newTestKVSessionStore(t)
	key := testComplexitySessionKey(t, "tenant", "sliding-ttl")
	// Wide enough that ordinary scheduler jitter cannot reorder the checks
	// against the deadlines they straddle; each sleep stays a fixed fraction of
	// the window so the sequence still proves the slide.
	const ttl = time.Second

	created, err := sessionStore.Create(context.Background(), key, &SessionComplexityRecord{
		Tier:   "complex",
		RuleID: "rule-1",
	}, ttl)
	require.NoError(t, err)
	require.True(t, created)

	time.Sleep(600 * time.Millisecond)
	_, found, err := sessionStore.Get(context.Background(), key, ttl)
	require.NoError(t, err)
	require.True(t, found, "record should exist before its original deadline")

	time.Sleep(600 * time.Millisecond)
	_, found, err = sessionStore.Get(context.Background(), key, ttl)
	require.NoError(t, err)
	require.True(t, found, "successful Get should extend the deadline")

	// Expiry is ttl plus one refresh interval of headroom, so an abandoned
	// session lingers a little past the configured window rather than expiring
	// early — the direction that matters, since expiring early would drop a live
	// conversation's tier mid-way.
	time.Sleep(ttl + complexitySessionRefreshInterval(ttl) + 100*time.Millisecond)
	_, found, err = sessionStore.Get(context.Background(), key, ttl)
	require.NoError(t, err)
	assert.False(t, found, "record should expire once its TTL is no longer refreshed")
}

// The refresh is coarse on purpose: sliding the expiry on every read makes each
// read a write, and on a replicated backend each write is a cluster broadcast.
// A chatty conversation must not cost one broadcast per turn.
func TestKVSessionStoreGetDoesNotWriteOnEveryRead(t *testing.T) {
	sessionStore, store := newTestKVSessionStore(t)
	_, targetKV := newTestKVSessionStore(t)
	delegate := &forwardingKVDelegate{target: targetKV}

	key := testComplexitySessionKey(t, "tenant", "read-amplification")
	_, err := sessionStore.Create(context.Background(), key, &SessionComplexityRecord{Tier: "complex"}, time.Hour)
	require.NoError(t, err)

	// Attached after Create so only the reads below are counted.
	store.SetDelegate(delegate)
	for i := 0; i < 50; i++ {
		_, found, err := sessionStore.Get(context.Background(), key, time.Hour)
		require.NoError(t, err)
		require.True(t, found)
	}

	assert.Zero(t, delegate.Sets(), "reads inside the refresh interval must not replicate")
}

// The window must still slide for a long-lived conversation, or a session that
// stays busy past its ttl would expire underneath itself.
func TestKVSessionStoreGetRefreshesOnceTheIntervalElapses(t *testing.T) {
	sessionStore, _ := newTestKVSessionStore(t)
	key := testComplexitySessionKey(t, "tenant", "interval-elapsed")
	const ttl = 400 * time.Millisecond

	_, err := sessionStore.Create(context.Background(), key, &SessionComplexityRecord{Tier: "complex"}, ttl)
	require.NoError(t, err)

	before, found, err := sessionStore.Get(context.Background(), key, ttl)
	require.NoError(t, err)
	require.True(t, found)

	// Past one refresh interval (ttl/4 = 100ms), so the next read slides it.
	time.Sleep(complexitySessionRefreshInterval(ttl) + 50*time.Millisecond)
	after, found, err := sessionStore.Get(context.Background(), key, ttl)
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, after.RefreshedAt.After(before.RefreshedAt), "the window never slid")
}

func TestKVSessionStoreRegistersReplicationDecoder(t *testing.T) {
	source, sourceKV := newTestKVSessionStore(t)
	target, targetKV := newTestKVSessionStore(t)
	delegate := &forwardingKVDelegate{target: targetKV}
	sourceKV.SetDelegate(delegate)

	key := testComplexitySessionKey(t, "tenant", "replicated")
	want := &SessionComplexityRecord{
		Tier:   "complex",
		RuleID: "rule-1",
	}
	created, err := source.Create(context.Background(), key, want, time.Hour)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, delegate.Err())

	replicatedValue, err := targetKV.Get(key)
	require.NoError(t, err)
	assert.IsType(t, &SessionComplexityRecord{}, replicatedValue)

	got, found, err := target.Get(context.Background(), key, time.Hour)
	require.NoError(t, err)
	require.True(t, found)
	assert.False(t, got.RefreshedAt.IsZero(), "the replicated record should carry the refresh clock")
	got.RefreshedAt = time.Time{}
	assert.Equal(t, want, got)
}

func TestKVSessionStoreRejectsInvalidReplicatedRecord(t *testing.T) {
	sessionStore, store := newTestKVSessionStore(t)
	key := testComplexitySessionKey(t, "tenant", "invalid-replica")
	require.NoError(t, store.SetRemote(key, []byte("{"), time.Now().UnixNano(), 0))

	_, found, err := sessionStore.Get(context.Background(), key, time.Hour)
	require.ErrorIs(t, err, errInvalidComplexitySession)
	assert.True(t, found)
}

func TestKVSessionStoreDeleteAndValidation(t *testing.T) {
	sessionStore, _ := newTestKVSessionStore(t)
	key := testComplexitySessionKey(t, "tenant", "delete")
	record := &SessionComplexityRecord{Tier: "complex", RuleID: "rule-1"}

	created, err := sessionStore.Create(context.Background(), key, record, time.Hour)
	require.NoError(t, err)
	require.True(t, created)
	deleted, err := sessionStore.Delete(context.Background(), key)
	require.NoError(t, err)
	assert.True(t, deleted)
	deleted, err = sessionStore.Delete(context.Background(), key)
	require.NoError(t, err)
	assert.False(t, deleted)

	_, err = sessionStore.Create(context.Background(), key, record, 0)
	require.ErrorIs(t, err, errInvalidComplexitySessionTTL)
	_, err = sessionStore.Create(context.Background(), key, nil, time.Hour)
	require.ErrorIs(t, err, errNilComplexitySessionRecord)
	_, _, err = sessionStore.Update(context.Background(), key, time.Hour, nil)
	require.ErrorIs(t, err, errNilComplexitySessionUpdater)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = sessionStore.Create(canceled, key, record, time.Hour)
	require.ErrorIs(t, err, context.Canceled)
	_, err = sessionStore.Status(nil)
	require.ErrorIs(t, err, errNilComplexitySessionContext)
}

func TestKVSessionStoreStatus(t *testing.T) {
	sessionStore, store := newTestKVSessionStore(t)

	status, err := sessionStore.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, SessionStoreStatus{
		Backend:               complexitySessionStoreBackendKV,
		ReplicationConfigured: false,
		AtomicAcrossReplicas:  true,
	}, status)

	_, targetKV := newTestKVSessionStore(t)
	store.SetDelegate(&forwardingKVDelegate{target: targetKV})
	status, err = sessionStore.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, SessionStoreStatus{
		Backend:               complexitySessionStoreBackendKV,
		ReplicationConfigured: true,
		AtomicAcrossReplicas:  false,
	}, status)
}

func TestSessionTierRank(t *testing.T) {
	tests := []struct {
		name     string
		tier     string
		wantRank int
		wantOK   bool
	}{
		{name: "simple", tier: complexity.TierSimple, wantRank: 0, wantOK: true},
		{name: "medium", tier: complexity.TierMedium, wantRank: 1, wantOK: true},
		{name: "complex", tier: complexity.TierComplex, wantRank: 2, wantOK: true},
		{name: "unknown", tier: "UNKNOWN", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rank, ok := sessionTierRank(tt.tier)
			assert.Equal(t, tt.wantRank, rank)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}

func TestUpdateSessionTierRecordClearsInterruptedDowngrade(t *testing.T) {
	now := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		proposed   *complexity.ComplexityResult
		configure  func(*configstore.ComplexitySessionConfig)
		wantReason sessionTierDecisionReason
	}{
		{name: "no proposal", wantReason: sessionTierReasonNoProposal},
		{
			name:       "empty proposed tier",
			proposed:   sessionTierProposal("", 0.95),
			wantReason: sessionTierReasonNoProposal,
		},
		{
			name:       "invalid proposed tier",
			proposed:   sessionTierProposal("UNKNOWN", 0.95),
			wantReason: sessionTierReasonInvalidProposal,
		},
		{
			name:       "same tier",
			proposed:   sessionTierProposal(complexity.TierComplex, 0.95),
			wantReason: sessionTierReasonSameTier,
		},
		{
			name:       "switch limit",
			proposed:   sessionTierProposal(complexity.TierMedium, 0.95),
			configure:  func(config *configstore.ComplexitySessionConfig) { config.MaxSwitchesPerSession = 1 },
			wantReason: sessionTierReasonSwitchLimitReached,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := sessionTierTestConfig()
			if tt.configure != nil {
				tt.configure(config)
			}
			record := sessionTierTestRecord(now)
			record.SwitchCount = 1
			record.PendingTurns = 1

			decision := updateSessionTierRecord(record, tt.proposed, config, now)

			assert.Equal(t, complexity.TierComplex, decision.Tier)
			assert.Equal(t, tt.wantReason, decision.Reason)
			assert.True(t, decision.RecordChanged)
			assert.False(t, decision.Switched)
			assert.Zero(t, record.PendingTurns)
			assert.Equal(t, 1, record.SwitchCount)
		})
	}

	record := sessionTierTestRecord(now)
	decision := updateSessionTierRecord(record, nil, sessionTierTestConfig(), now)
	assert.False(t, decision.RecordChanged, "a repeated hold must not create a store write")
}

func TestUpdateSessionTierRecordEscalation(t *testing.T) {
	now := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		score      float64
		configure  func(*configstore.ComplexitySessionConfig)
		wantReason sessionTierDecisionReason
		wantSwitch bool
	}{
		{
			name:       "strong proposal switches immediately",
			score:      0.91,
			wantReason: sessionTierReasonEscalated,
			wantSwitch: true,
		},
		{
			name:       "weak proposal is held",
			score:      0.7,
			wantReason: sessionTierReasonBelowSwitchSimilarity,
		},
		{
			name:       "zero threshold disables similarity gate",
			score:      0,
			configure:  func(config *configstore.ComplexitySessionConfig) { config.SwitchMinSimilarity = 0 },
			wantReason: sessionTierReasonEscalated,
			wantSwitch: true,
		},
		{
			name:       "always allow bypasses similarity",
			score:      0.7,
			configure:  func(config *configstore.ComplexitySessionConfig) { config.AlwaysAllowEscalation = true },
			wantReason: sessionTierReasonEscalated,
			wantSwitch: true,
		},
		{
			name:  "switch cap remains absolute",
			score: 0.91,
			configure: func(config *configstore.ComplexitySessionConfig) {
				config.AlwaysAllowEscalation = true
				config.MaxSwitchesPerSession = 1
			},
			wantReason: sessionTierReasonSwitchLimitReached,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := sessionTierTestConfig()
			if tt.configure != nil {
				tt.configure(config)
			}
			record := sessionTierTestRecord(now)
			record.Tier = complexity.TierMedium
			if config.MaxSwitchesPerSession > 0 {
				record.SwitchCount = config.MaxSwitchesPerSession
			}

			decision := updateSessionTierRecord(record, sessionTierProposal(complexity.TierComplex, tt.score), config, now)

			assert.Equal(t, tt.wantReason, decision.Reason)
			assert.Equal(t, tt.wantSwitch, decision.Switched)
			assert.Equal(t, tt.wantSwitch, decision.RecordChanged)
			if tt.wantSwitch {
				assert.Equal(t, complexity.TierComplex, record.Tier)
				assert.Equal(t, now, record.DecidedAt)
				assert.Equal(t, 1, record.SwitchCount)
			} else {
				assert.Equal(t, complexity.TierMedium, record.Tier)
			}
		})
	}
}

// Exercises Path B (persistence) in isolation: SwitchMinSimilarity is forced
// to 0 so no turn can take the confidence fast path, and the two downgrade
// turns propose different specific tiers (MEDIUM then SIMPLE) to prove the
// streak accumulates on direction alone rather than resetting on a tier
// mismatch, landing on the completing turn's own proposal.
func TestUpdateSessionTierRecordRequiresSustainedDowngrade(t *testing.T) {
	now := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	config := sessionTierTestConfig()
	config.SwitchMinSimilarity = 0
	record := sessionTierTestRecord(now)

	first := updateSessionTierRecord(record, sessionTierProposal(complexity.TierMedium, 0.93), config, now)
	assert.Equal(t, sessionTierReasonDowngradePending, first.Reason)
	assert.False(t, first.Switched)
	assert.True(t, first.RecordChanged)
	assert.Equal(t, 1, record.PendingTurns)

	second := updateSessionTierRecord(record, sessionTierProposal(complexity.TierSimple, 0.4), config, now.Add(time.Minute))
	assert.Equal(t, sessionTierReasonDowngraded, second.Reason)
	assert.True(t, second.Switched)
	assert.True(t, second.RecordChanged)
	assert.Equal(t, complexity.TierSimple, record.Tier, "a different lower tier still accumulates toward the streak and lands on its own proposal")
	assert.Equal(t, now.Add(time.Minute), record.DecidedAt)
	assert.Equal(t, 1, record.SwitchCount)
	assert.Zero(t, record.PendingTurns)
}

// Regression: DowngradeAfterNTurns must stay reachable when SwitchMinSimilarity
// is positive. The similarity floor gates escalations only, so sub-threshold
// downgrade turns (score < SwitchMinSimilarity) accumulate through Path B
// instead of being rejected at the entry gate — which would have made the
// turn-persistence path dead for any positive threshold.
func TestUpdateSessionTierRecordSustainedDowngradeWithPositiveSwitchThreshold(t *testing.T) {
	now := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	config := sessionTierTestConfig() // SwitchMinSimilarity: 0.8, DowngradeAfterNTurns: 2
	record := sessionTierTestRecord(now)

	// Both turns are below the 0.8 switch threshold, so Path A never fires and
	// neither turn is confident enough to switch on its own.
	first := updateSessionTierRecord(record, sessionTierProposal(complexity.TierMedium, 0.5), config, now)
	assert.Equal(t, sessionTierReasonDowngradePending, first.Reason, "a sub-threshold downgrade accumulates, it is not rejected at the gate")
	assert.False(t, first.Switched)
	assert.Equal(t, complexity.TierComplex, record.Tier)
	assert.Equal(t, 1, record.PendingTurns)

	second := updateSessionTierRecord(record, sessionTierProposal(complexity.TierSimple, 0.4), config, now.Add(time.Minute))
	assert.Equal(t, sessionTierReasonDowngraded, second.Reason)
	assert.True(t, second.Switched)
	assert.Equal(t, complexity.TierSimple, record.Tier)
	assert.Equal(t, 1, record.SwitchCount)
	assert.Zero(t, record.PendingTurns)
}

// Exercises Path A (confidence): a single turn that clears SwitchMinSimilarity
// switches immediately, without waiting on DowngradeAfterNTurns, and can land
// directly on a tier more than one rank below the held tier.
func TestUpdateSessionTierRecordSwitchesImmediatelyOnConfidence(t *testing.T) {
	now := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	config := sessionTierTestConfig() // SwitchMinSimilarity: 0.8
	record := sessionTierTestRecord(now)

	decision := updateSessionTierRecord(record, sessionTierProposal(complexity.TierSimple, 0.85), config, now)

	assert.Equal(t, sessionTierReasonDowngradedByConfidence, decision.Reason)
	assert.True(t, decision.Switched)
	assert.True(t, decision.RecordChanged)
	assert.Equal(t, complexity.TierSimple, record.Tier, "the confidence path can skip straight past an intermediate tier")
	assert.Equal(t, now, record.DecidedAt)
	assert.Equal(t, 1, record.SwitchCount)
	assert.Zero(t, record.PendingTurns, "Path A never touches the persistence streak")
}

func TestUpdateSessionTierRecordRejectsInvalidState(t *testing.T) {
	now := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	proposal := sessionTierProposal(complexity.TierComplex, 0.95)

	decision := updateSessionTierRecord(nil, proposal, sessionTierTestConfig(), now)
	assert.Equal(t, sessionTierReasonInvalidState, decision.Reason)
	assert.False(t, decision.RecordChanged)

	record := sessionTierTestRecord(now)
	original := cloneSessionComplexityRecord(record)
	decision = updateSessionTierRecord(record, proposal, nil, now)
	assert.Equal(t, sessionTierReasonInvalidState, decision.Reason)
	assert.Equal(t, original, record)

	config := sessionTierTestConfig()
	config.Mode = configstore.ComplexitySessionModePinned
	decision = updateSessionTierRecord(record, proposal, config, now)
	assert.Equal(t, sessionTierReasonInvalidState, decision.Reason)
	assert.Equal(t, original, record)

	record.Tier = "UNKNOWN"
	original = cloneSessionComplexityRecord(record)
	decision = updateSessionTierRecord(record, proposal, sessionTierTestConfig(), now)
	assert.Equal(t, sessionTierReasonInvalidState, decision.Reason)
	assert.Equal(t, "UNKNOWN", decision.Tier)
	assert.Equal(t, original, record)
}

func sessionTierTestConfig() *configstore.ComplexitySessionConfig {
	return &configstore.ComplexitySessionConfig{
		Mode:                 configstore.ComplexitySessionModeCacheAware,
		TTL:                  time.Hour,
		SwitchMinSimilarity:  0.8,
		DowngradeAfterNTurns: 2,
	}
}

func sessionTierProposal(tier string, score float64) *complexity.ComplexityResult {
	return &complexity.ComplexityResult{Tier: tier, Score: score}
}

func sessionTierTestRecord(now time.Time) *SessionComplexityRecord {
	return &SessionComplexityRecord{
		Tier:      complexity.TierComplex,
		DecidedAt: now.Add(-time.Hour),
		RuleID:    "rule-1",
	}
}

func newTestKVSessionStore(t *testing.T) (*kvSessionStore, *kvstore.Store) {
	t.Helper()
	store, err := kvstore.New(kvstore.Config{CleanupInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})
	sessionStore, err := newKVSessionStore(store)
	require.NoError(t, err)
	return sessionStore, store
}

type forwardingKVDelegate struct {
	target *kvstore.Store
	mu     sync.Mutex
	err    error
	sets   int
}

func (d *forwardingKVDelegate) OnSet(key string, valueJSON []byte, writtenAt int64, expiresAt int64) {
	d.mu.Lock()
	d.sets++
	d.mu.Unlock()
	d.recordError(d.target.SetRemote(key, valueJSON, writtenAt, expiresAt))
}

// Sets reports how many replication messages this delegate has been asked to
// send, which is the cost a read path must not incur per request.
func (d *forwardingKVDelegate) Sets() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sets
}

func (d *forwardingKVDelegate) OnDelete(key string, deletedAt int64) {
	d.recordError(d.target.DeleteRemote(key, deletedAt))
}

func (d *forwardingKVDelegate) recordError(err error) {
	if err == nil {
		return
	}
	d.mu.Lock()
	if d.err == nil {
		d.err = err
	}
	d.mu.Unlock()
}

func (d *forwardingKVDelegate) Err() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

func testComplexitySessionKey(t *testing.T, scopeID, sessionID string) string {
	t.Helper()
	key, ok := complexity.BuildSessionKey(scopeID, sessionID)
	require.True(t, ok)
	return key
}
