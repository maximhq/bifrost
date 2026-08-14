package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

const (
	complexitySessionStoreBackendKV = "kvstore"
)

var (
	errNilComplexitySessionStore   = errors.New("complexity session kvstore cannot be nil")
	errNilComplexitySessionContext = errors.New("complexity session context cannot be nil")
	errNilComplexitySessionRecord  = errors.New("complexity session record cannot be nil")
	errNilComplexitySessionUpdater = errors.New("complexity session updater cannot be nil")
	errInvalidComplexitySessionTTL = errors.New("complexity session ttl must be positive")
	errInvalidComplexitySession    = errors.New("invalid complexity session record")
)

// SessionComplexityRecord is the persisted classifier decision for one
// scope-namespaced session. It stores runtime facts, never session policy, so
// config changes take effect without record migration.
type SessionComplexityRecord struct {
	// Tier is the currently pinned or held complexity tier.
	Tier string `json:"tier"`
	// DecidedAt is when Tier was first selected or most recently switched.
	DecidedAt time.Time `json:"decided_at"`
	// RuleID identifies the routing rule that established the current tier. A
	// missing rule invalidates the pin.
	RuleID string `json:"rule_id"`
	// SwitchCount is the number of tier changes made during this session.
	SwitchCount int `json:"switch_count,omitempty"`
	// RefreshedAt is when the sliding expiration was last extended. It exists so
	// reads can tell whether the window actually needs sliding: refreshing on
	// every read makes each read a write, and each write a cluster broadcast.
	RefreshedAt time.Time `json:"refreshed_at"`

	// PendingTurns is the number of consecutive downgrade-direction proposals
	// made since the last switch or non-pending hold. Any tier lower than the
	// one currently held counts identically toward this streak — it does not
	// track which specific lower tier was proposed, because a downgrade that
	// completes the streak lands on whatever tier the completing turn itself
	// proposed, not one rank below the held tier.
	PendingTurns int `json:"pending_turns,omitempty"`
}

// sessionTierDecisionReason is a stable, machine-readable explanation of a
// cache-aware tier decision. The integration layer may turn it into a richer
// routing log, but policy remains independent of logging and request state.
type sessionTierDecisionReason string

const (
	sessionTierReasonInvalidState           sessionTierDecisionReason = "invalid_state"
	sessionTierReasonInvalidProposal        sessionTierDecisionReason = "invalid_proposal"
	sessionTierReasonNoProposal             sessionTierDecisionReason = "no_proposal"
	sessionTierReasonSameTier               sessionTierDecisionReason = "same_tier"
	sessionTierReasonBelowSwitchSimilarity  sessionTierDecisionReason = "below_switch_similarity"
	sessionTierReasonSwitchLimitReached     sessionTierDecisionReason = "switch_limit_reached"
	sessionTierReasonEscalated              sessionTierDecisionReason = "escalated"
	sessionTierReasonDowngradePending       sessionTierDecisionReason = "downgrade_pending"
	sessionTierReasonDowngraded             sessionTierDecisionReason = "downgraded"
	sessionTierReasonDowngradedByConfidence sessionTierDecisionReason = "downgraded_by_confidence"
)

// sessionTierDecision is the result of applying cache-aware session policy.
// Tier is the value to publish for this turn. Switched distinguishes a tier
// move from a hold, while RecordChanged tells the caller whether persistence is
// necessary even when the tier stayed put, such as while advancing a pending
// downgrade.
type sessionTierDecision struct {
	Tier          string
	Switched      bool
	RecordChanged bool
	Reason        sessionTierDecisionReason
}

// SessionStoreStatus describes the guarantees of the active session-state
// backend. It reports what the storage layer can prove about itself, never
// whether a given deployment is safe: that also depends on how many replicas
// are running, which this layer cannot observe.
type SessionStoreStatus struct {
	// Backend is a diagnostic name for the active implementation. Callers must
	// use the capability fields, not this label, when deciding readiness.
	Backend string `json:"backend"`
	// ReplicationConfigured reports whether local mutations are forwarded to
	// other nodes. It is deliberately named for the configuration rather than
	// the outcome: forwarding is fire-and-forget, so a delegate being installed
	// says nothing about whether peers are connected, reachable, or converged.
	ReplicationConfigured bool `json:"replication_configured"`
	// AtomicAcrossReplicas reports whether concurrent updates to one session are
	// serialized across everything sharing this backend. A single node holding
	// the only copy satisfies this through its own lock; gossip replication does
	// not, because it resolves conflicts by last-write-wins after the fact.
	AtomicAcrossReplicas bool `json:"atomic_across_replicas"`
}

// SessionRecordUpdater mutates a caller-owned copy of the current session
// record. A store backed by compare-and-swap may invoke it more than once, so an
// updater must be deterministic, free of side effects, and complete promptly.
// It must not call back into the same store. Returning an error aborts the write.
type SessionRecordUpdater func(*SessionComplexityRecord) error

// SessionStore persists typed complexity-session state behind a backend-neutral
// contract. Implementations must be safe for concurrent use and must not expose
// shared record pointers to callers.
type SessionStore interface {
	// Get reads a record and keeps its sliding expiration at least ttl away. The
	// returned record is caller-owned. found is false, with a nil error, when the
	// key is absent or expired. ttl must be positive.
	//
	// The refresh is coarse rather than per-read: an implementation may leave the
	// expiry untouched while the window is still comfortably in the future. A
	// record therefore survives at least ttl of idleness, and may survive somewhat
	// longer. This is deliberate — sliding on literally every read turns a read
	// path into a write path, which on a replicated backend means a cluster
	// message per request.
	Get(ctx context.Context, key string, ttl time.Duration) (record *SessionComplexityRecord, found bool, err error)
	// Create atomically stores a caller-owned snapshot only when key is absent or
	// expired. created is false, with a nil error, when another caller already
	// created the record. ttl must be positive.
	Create(ctx context.Context, key string, record *SessionComplexityRecord, ttl time.Duration) (created bool, err error)
	// Update atomically applies update to an existing record and refreshes its
	// sliding expiration to ttl. It returns a caller-owned copy of the committed
	// record. When found is false, update was not called. Implementations may
	// retry update to resolve concurrent writes; ttl must be positive.
	Update(ctx context.Context, key string, ttl time.Duration, update SessionRecordUpdater) (record *SessionComplexityRecord, found bool, err error)
	// Delete removes a record. deleted is false, with a nil error, when the key
	// does not exist.
	Delete(ctx context.Context, key string) (deleted bool, err error)
	// Status reports the backend's current replication and atomicity guarantees.
	// An error means readiness could not be determined.
	Status(ctx context.Context) (SessionStoreStatus, error)
}

// kvSessionStore persists complexity-session records in framework/kvstore. Its
// atomicity is process-local: a configured gossip delegate shares committed
// values across replicas but does not turn updates into distributed transactions.
type kvSessionStore struct {
	store *kvstore.Store
}

var _ SessionStore = (*kvSessionStore)(nil)

// newKVSessionStore creates a typed complexity-session view over store. The
// registered decoder ensures remotely replicated records retain their concrete
// type instead of falling back to raw JSON bytes.
func newKVSessionStore(store *kvstore.Store) (*kvSessionStore, error) {
	if store == nil {
		return nil, errNilComplexitySessionStore
	}
	store.RegisterDecoder(complexity.SessionKeyPrefix, func(data []byte) (any, error) {
		var record SessionComplexityRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, fmt.Errorf("decode complexity session record: %w", err)
		}
		return &record, nil
	})
	return &kvSessionStore{store: store}, nil
}

// Get reads a caller-owned record and refreshes its sliding TTL atomically. A
// successful read is also a write, so a configured sync delegate receives one
// replication event for each refresh.
func (s *kvSessionStore) Get(ctx context.Context, key string, ttl time.Duration) (*SessionComplexityRecord, bool, error) {
	if err := validateComplexitySessionStoreRequest(ctx, ttl); err != nil {
		return nil, false, err
	}

	// The read-only path first. kvstore.Get takes a read lock and notifies no
	// delegate, so a held turn costs nothing beyond a map lookup.
	value, err := s.store.Get(key)
	if errors.Is(err, kvstore.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read complexity session %q: %w", key, err)
	}

	record, err := complexitySessionRecordFromValue(value)
	if err != nil {
		return nil, true, fmt.Errorf("read complexity session %q: %w", key, err)
	}

	// Still comfortably inside the window, so leave the expiry alone.
	if time.Since(record.RefreshedAt) < complexitySessionRefreshInterval(ttl) {
		return record, true, nil
	}

	refreshedAt := time.Now()
	value, found, err := s.store.UpdateWithTTL(key, complexitySessionStoredTTL(ttl), func(current any) (any, error) {
		refreshed, err := complexitySessionRecordFromValue(current)
		if err != nil {
			return nil, err
		}
		refreshed.RefreshedAt = refreshedAt
		return refreshed, nil
	})
	if err != nil {
		return nil, true, fmt.Errorf("refresh complexity session %q: %w", key, err)
	}
	if !found {
		// Expired between the read and the refresh. The record we hold is already
		// gone, so report it as absent rather than serving a tier the store no
		// longer has.
		return nil, false, nil
	}

	refreshed, err := complexitySessionRecordFromValue(value)
	if err != nil {
		return nil, true, fmt.Errorf("read refreshed complexity session %q: %w", key, err)
	}
	return refreshed, true, nil
}

// complexitySessionRefreshInterval is how much of the idle window may elapse
// before a read slides the expiry. A quarter keeps writes to roughly four per
// window however chatty the conversation is, while staying frequent enough that
// the stored expiry never drifts far from the truth.
func complexitySessionRefreshInterval(ttl time.Duration) time.Duration {
	interval := ttl / 4
	if interval <= 0 {
		// A ttl too short to quarter refreshes on every read, which is correct:
		// there is no amplification to avoid at that scale.
		return 0
	}
	return interval
}

// complexitySessionStoredTTL is the expiry actually written to the backend. It
// carries one refresh interval of headroom so a coarse refresh can never expire
// a session earlier than the configured idle window: the worst case is a read
// that declines to slide just before the caller goes idle, and the headroom
// covers exactly that gap. The cost is that an abandoned session lingers up to
// one interval longer than configured.
func complexitySessionStoredTTL(ttl time.Duration) time.Duration {
	return ttl + complexitySessionRefreshInterval(ttl)
}

// Create stores a detached record snapshot when key is currently absent or
// expired.
func (s *kvSessionStore) Create(ctx context.Context, key string, record *SessionComplexityRecord, ttl time.Duration) (bool, error) {
	if err := validateComplexitySessionStoreRequest(ctx, ttl); err != nil {
		return false, err
	}
	if record == nil {
		return false, errNilComplexitySessionRecord
	}

	// The store owns the refresh clock, not the caller: a record created with a
	// zero or stale RefreshedAt would slide its expiry on the very next read,
	// which is the write-per-read this exists to avoid.
	stored := cloneSessionComplexityRecord(record)
	stored.RefreshedAt = time.Now()

	created, err := s.store.SetNXWithTTL(key, stored, complexitySessionStoredTTL(ttl))
	if err != nil {
		return false, fmt.Errorf("create complexity session %q: %w", key, err)
	}
	return created, nil
}

// Update applies update to a detached copy and atomically commits another copy,
// preventing the caller-owned record from aliasing stored state.
func (s *kvSessionStore) Update(ctx context.Context, key string, ttl time.Duration, update SessionRecordUpdater) (*SessionComplexityRecord, bool, error) {
	if err := validateComplexitySessionStoreRequest(ctx, ttl); err != nil {
		return nil, false, err
	}
	if update == nil {
		return nil, false, errNilComplexitySessionUpdater
	}

	// An update is already a write, so it slides the window too — leaving it on
	// the old expiry would make a just-modified session expire sooner than a
	// merely-read one.
	refreshedAt := time.Now()
	value, found, err := s.store.UpdateWithTTL(key, complexitySessionStoredTTL(ttl), func(current any) (any, error) {
		record, err := complexitySessionRecordFromValue(current)
		if err != nil {
			return nil, err
		}
		if err := update(record); err != nil {
			return nil, err
		}
		// Stamped after the updater so a caller cannot rewind the refresh clock.
		record.RefreshedAt = refreshedAt
		return cloneSessionComplexityRecord(record), nil
	})
	if err != nil {
		return nil, found, fmt.Errorf("update complexity session %q: %w", key, err)
	}
	if !found {
		return nil, false, nil
	}

	record, err := complexitySessionRecordFromValue(value)
	if err != nil {
		return nil, true, fmt.Errorf("read updated complexity session %q: %w", key, err)
	}
	return record, true, nil
}

// Delete removes a live session record. Expired records are treated as absent.
func (s *kvSessionStore) Delete(ctx context.Context, key string) (bool, error) {
	if err := validateComplexitySessionStoreContext(ctx); err != nil {
		return false, err
	}

	_, err := s.store.GetAndDelete(key)
	if errors.Is(err, kvstore.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("delete complexity session %q: %w", key, err)
	}
	return true, nil
}

// Status reports the guarantees relevant to session routing. Gossip makes
// records visible across replicas, but updates remain atomic only within one
// process.
func (s *kvSessionStore) Status(ctx context.Context) (SessionStoreStatus, error) {
	if err := validateComplexitySessionStoreContext(ctx); err != nil {
		return SessionStoreStatus{}, err
	}

	replicationConfigured := s.store.HasSyncDelegate()
	return SessionStoreStatus{
		Backend:               complexitySessionStoreBackendKV,
		ReplicationConfigured: replicationConfigured,
		// Inverted deliberately. With no delegate this process holds the only
		// copy and its own lock serializes every update, so updates are atomic
		// across the one replica that exists. Once replication is configured the
		// lock is local to each node while the records are shared, which is the
		// combination that lets two nodes decide different tiers for one session.
		AtomicAcrossReplicas: !replicationConfigured,
	}, nil
}

func validateComplexitySessionStoreRequest(ctx context.Context, ttl time.Duration) error {
	if err := validateComplexitySessionStoreContext(ctx); err != nil {
		return err
	}
	if ttl <= 0 {
		return errInvalidComplexitySessionTTL
	}
	return nil
}

func validateComplexitySessionStoreContext(ctx context.Context) error {
	if ctx == nil {
		return errNilComplexitySessionContext
	}
	return ctx.Err()
}

func complexitySessionRecordFromValue(value any) (*SessionComplexityRecord, error) {
	switch record := value.(type) {
	case *SessionComplexityRecord:
		if record == nil {
			return nil, fmt.Errorf("%w: nil pointer", errInvalidComplexitySession)
		}
		return cloneSessionComplexityRecord(record), nil
	case SessionComplexityRecord:
		return cloneSessionComplexityRecord(&record), nil
	default:
		return nil, fmt.Errorf("%w: got %T", errInvalidComplexitySession, value)
	}
}

func cloneSessionComplexityRecord(record *SessionComplexityRecord) *SessionComplexityRecord {
	if record == nil {
		return nil
	}
	cloned := *record
	return &cloned
}

// updateSessionTierRecord applies cache-aware switching policy to record and
// returns the tier to publish for this turn. It mutates only the supplied
// caller-owned record and performs no I/O, logging, or store access.
//
// Callers must invoke it from inside SessionStore.Update so the decision and
// its PendingTurns/SwitchCount mutations use the latest record atomically. A
// false RecordChanged result lets the caller abort the write, avoiding an
// unnecessary replication message for an ordinary held turn.
func updateSessionTierRecord(
	record *SessionComplexityRecord,
	proposed *complexity.ComplexityResult,
	config *configstore.ComplexitySessionConfig,
	now time.Time,
) sessionTierDecision {
	if record == nil {
		return sessionTierDecision{Reason: sessionTierReasonInvalidState}
	}
	if config == nil || config.Mode != configstore.ComplexitySessionModeCacheAware {
		return sessionTierDecision{Tier: record.Tier, Reason: sessionTierReasonInvalidState}
	}

	currentRank, currentValid := sessionTierRank(record.Tier)
	if !currentValid {
		return sessionTierDecision{Tier: record.Tier, Reason: sessionTierReasonInvalidState}
	}
	if proposed == nil || proposed.Tier == "" {
		return heldSessionTierDecision(record, sessionTierReasonNoProposal)
	}

	proposedRank, proposedValid := sessionTierRank(proposed.Tier)
	if !proposedValid {
		return heldSessionTierDecision(record, sessionTierReasonInvalidProposal)
	}
	if proposedRank == currentRank {
		return heldSessionTierDecision(record, sessionTierReasonSameTier)
	}

	escalating := proposedRank > currentRank
	// The similarity floor gates escalations only. For a downgrade,
	// SwitchMinSimilarity is Path A's fast-path trigger, not an entry bar (see
	// below): applying it here too would reject every sub-threshold downgrade
	// turn before it could accumulate, leaving DowngradeAfterNTurns unreachable
	// whenever SwitchMinSimilarity is positive.
	if escalating && !meetsSessionSwitchSimilarity(proposed.Score, config.SwitchMinSimilarity) &&
		!config.AlwaysAllowEscalation {
		return heldSessionTierDecision(record, sessionTierReasonBelowSwitchSimilarity)
	}
	if config.MaxSwitchesPerSession > 0 && record.SwitchCount >= config.MaxSwitchesPerSession {
		return heldSessionTierDecision(record, sessionTierReasonSwitchLimitReached)
	}
	if escalating {
		return switchedSessionTierDecision(record, proposed.Tier, now, sessionTierReasonEscalated)
	}

	// Downgrade: an OR of two independent paths, not a single AND'd gate. A
	// turn confident enough on its own switches immediately (Path A); otherwise
	// several turns that simply keep pointing simpler than the held tier — none
	// of them individually confident, and not necessarily the same specific
	// tier — accumulate toward the same result (Path B).
	if sessionMeetsDowngradeConfidence(proposed.Score, config.SwitchMinSimilarity) {
		return switchedSessionTierDecision(record, proposed.Tier, now, sessionTierReasonDowngradedByConfidence)
	}

	requiredTurns := config.DowngradeAfterNTurns
	if requiredTurns < 1 {
		requiredTurns = 1
	}
	advancePendingSessionTurns(record)
	if record.PendingTurns < requiredTurns {
		return unswitchedSessionTierDecision(record, sessionTierReasonDowngradePending, true)
	}

	return switchedSessionTierDecision(record, proposed.Tier, now, sessionTierReasonDowngraded)
}

func sessionTierRank(tier string) (int, bool) {
	switch tier {
	case complexity.TierSimple:
		return 0, true
	case complexity.TierMedium:
		return 1, true
	case complexity.TierComplex:
		return 2, true
	default:
		return 0, false
	}
}

func meetsSessionSwitchSimilarity(score, threshold float64) bool {
	return threshold <= 0 || score >= threshold
}

// sessionMeetsDowngradeConfidence reports whether this turn's own proposal is
// confident enough, alone, to switch without waiting on downgrade_after_n_turns.
// threshold<=0 must mean "fast path disabled," not "always fast": unlike
// meetsSessionSwitchSimilarity, where threshold<=0 means "no floor at all,"
// treating an unset threshold as always-satisfied here would make Path A fire
// on literally every downgrade that clears the (separate) similarity gate
// above, permanently starving Path B under the shipped default.
func sessionMeetsDowngradeConfidence(score, threshold float64) bool {
	return threshold > 0 && score >= threshold
}

func heldSessionTierDecision(record *SessionComplexityRecord, reason sessionTierDecisionReason) sessionTierDecision {
	return unswitchedSessionTierDecision(record, reason, clearPendingSessionTurns(record))
}

func unswitchedSessionTierDecision(
	record *SessionComplexityRecord,
	reason sessionTierDecisionReason,
	recordChanged bool,
) sessionTierDecision {
	return sessionTierDecision{
		Tier:          record.Tier,
		RecordChanged: recordChanged,
		Reason:        reason,
	}
}

func clearPendingSessionTurns(record *SessionComplexityRecord) bool {
	if record.PendingTurns == 0 {
		return false
	}
	record.PendingTurns = 0
	return true
}

func advancePendingSessionTurns(record *SessionComplexityRecord) {
	record.PendingTurns++
}

func switchedSessionTierDecision(
	record *SessionComplexityRecord,
	tier string,
	now time.Time,
	reason sessionTierDecisionReason,
) sessionTierDecision {
	record.Tier = tier
	record.DecidedAt = now
	record.SwitchCount++
	clearPendingSessionTurns(record)
	return sessionTierDecision{
		Tier:          record.Tier,
		Switched:      true,
		RecordChanged: true,
		Reason:        reason,
	}
}

// ComplexitySessionKVStoreSetter is implemented by routing plugins that accept
// Bifrost's configured key-value store as the backend for session state. Wired
// by the HTTP server like EmbeddingExecutorSetter; wrappers that embed
// *RoutingPlugin satisfy this via method promotion.
type ComplexitySessionKVStoreSetter interface {
	SetComplexitySessionKVStore(*kvstore.Store) error
}

// SetComplexitySessionKVStore installs the backing store for session state.
//
// It is a setter rather than an Init parameter so the store can be attached
// after the plugin exists, matching how the embedding executor is wired.
// Attachment time does not affect what Status reports: the backend's
// guarantees are read at call time, so a replication delegate installed later
// is picked up without re-registering anything here.
func (p *RoutingPlugin) SetComplexitySessionKVStore(store *kvstore.Store) error {
	sessionStore, err := newKVSessionStore(store)
	if err != nil {
		return err
	}
	var asInterface SessionStore = sessionStore
	p.complexitySessionStore.Store(&asInterface)
	return nil
}

// ComplexitySessionStoreStatus reports what the session-state backend can
// guarantee, or nil when no store is attached. Callers must not read a nil
// result as "unsafe": it means session state has no backing store at all, which
// is the normal case while session routing is switched off.
func (p *RoutingPlugin) ComplexitySessionStoreStatus(ctx context.Context) (*SessionStoreStatus, error) {
	stored := p.complexitySessionStore.Load()
	if stored == nil {
		return nil, nil
	}
	status, err := (*stored).Status(ctx)
	if err != nil {
		return nil, err
	}
	return &status, nil
}
