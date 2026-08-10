package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/kvstore"
)

const (
	claudeCodeSessionIDHeader       = "x-claude-code-session-id"
	complexitySessionKeyPrefix      = "complexity-session:v1:"
	complexitySessionStoreBackendKV = "kvstore"
	maxComplexitySessionIDBytes     = 255
)

var (
	errNilComplexitySessionStore   = errors.New("complexity session kvstore cannot be nil")
	errNilComplexitySessionContext = errors.New("complexity session context cannot be nil")
	errNilComplexitySessionRecord  = errors.New("complexity session record cannot be nil")
	errNilComplexitySessionUpdater = errors.New("complexity session updater cannot be nil")
	errInvalidComplexitySessionTTL = errors.New("complexity session ttl must be positive")
	errInvalidComplexitySession    = errors.New("invalid complexity session record")
)

// SessionRouteObservation records provider facts for one effective route in a
// complexity-routed session. It deliberately carries no switching thresholds
// or other policy; callers apply the current session config to these facts.
type SessionRouteObservation struct {
	// CachedReadTokens is the cached input-token count most recently reported by
	// the provider for this route.
	CachedReadTokens int `json:"cached_read_tokens"`
	// CacheObserved distinguishes a provider-reported zero from a response that
	// did not include cache telemetry at all.
	CacheObserved bool `json:"cache_observed"`
	// LastSeenAt is when this route most recently produced the observation.
	LastSeenAt time.Time `json:"last_seen_at"`
}

// SessionComplexityRecord is the persisted classifier decision and observed
// routing history for one scope-namespaced session. It stores runtime facts,
// never session policy, so config changes take effect without record migration.
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

	// PendingTier is the lower tier currently being considered for a sustained
	// downgrade. Empty means that no downgrade is pending.
	PendingTier string `json:"pending_tier,omitempty"`
	// PendingTurns is the number of consecutive qualifying proposals for
	// PendingTier. No-tier and weak proposals must not advance it.
	PendingTurns int `json:"pending_turns,omitempty"`
	// PendingMinScore is the lowest semantic similarity observed in the current
	// pending sequence.
	PendingMinScore float64 `json:"pending_min_score,omitempty"`

	// RouteObservations is keyed by a stable, opaque effective-route identity.
	// It is a map so primary, fallback, and destination warmth remain separate.
	// Writers are responsible for bounding retained history.
	RouteObservations map[string]SessionRouteObservation `json:"route_observations,omitempty"`
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
// shared record pointers or RouteObservations maps to callers.
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
	store.RegisterDecoder(complexitySessionKeyPrefix, func(data []byte) (any, error) {
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
// preventing caller-owned RouteObservations maps from aliasing stored state.
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
	cloned.RouteObservations = maps.Clone(record.RouteObservations)
	return &cloned
}

// resolveSessionID walks the enabled identity sources in their fixed precedence
// order. identitySources must contain normalized ComplexitySessionIdentity*
// values; configuration normalization owns defaults and validation.
func resolveSessionID(ctx *schemas.BifrostContext, identitySources []string) (id, source string, ok bool) {
	if sessionIdentitySourceEnabled(identitySources, configstore.ComplexitySessionIdentityHeader) {
		if id := normalizeComplexitySessionID(sessionIDFromContext(ctx)); id != "" {
			return id, configstore.ComplexitySessionIdentityHeader, true
		}
	}
	if sessionIdentitySourceEnabled(identitySources, configstore.ComplexitySessionIdentityHarness) {
		if id := normalizeComplexitySessionID(harnessSessionID(ctx)); id != "" {
			return id, configstore.ComplexitySessionIdentityHarness, true
		}
	}
	return "", "", false
}

func sessionIdentitySourceEnabled(identitySources []string, wanted string) bool {
	for _, source := range identitySources {
		if source == wanted {
			return true
		}
	}
	return false
}

// normalizeComplexitySessionID validates the opaque identity shared by the
// session store and request logs. IDs are never truncated because doing so can
// merge otherwise distinct conversations.
func normalizeComplexitySessionID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxComplexitySessionIDBytes || !utf8.ValidString(id) || strings.IndexByte(id, 0) >= 0 {
		return ""
	}
	return id
}

func sessionIDFromContext(ctx *schemas.BifrostContext) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(schemas.BifrostContextKeySessionID).(string)
	return strings.TrimSpace(id)
}

func harnessSessionID(ctx *schemas.BifrostContext) string {
	if ctx == nil {
		return ""
	}
	headers, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	switch detectComplexityHarness(ctx) {
	case complexityHarnessClaudeCode:
		return strings.TrimSpace(headers[claudeCodeSessionIDHeader])
	case complexityHarnessCodex:
		metadata, ok := parseCodexTurnMetadata(ctx)
		if !ok {
			return ""
		}
		return strings.TrimSpace(metadata.SessionID)
	default:
		return ""
	}
}

// buildComplexitySessionKey isolates session state by scope while keeping raw
// scope and session identifiers out of storage keys.
func buildComplexitySessionKey(scopeID, sessionID string) (string, bool) {
	scopeID = strings.TrimSpace(scopeID)
	sessionID = strings.TrimSpace(sessionID)
	if scopeID == "" || sessionID == "" {
		return "", false
	}
	return complexitySessionKeyPrefix + complexitySessionHash(scopeID+"\x00"+sessionID), true
}

func complexitySessionHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
