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

	// PendingTier is the lower tier currently being considered for a sustained
	// downgrade. Empty means that no downgrade is pending.
	PendingTier string `json:"pending_tier,omitempty"`
	// PendingTurns is the number of consecutive qualifying proposals for
	// PendingTier. No-tier and weak proposals must not advance it.
	PendingTurns int `json:"pending_turns,omitempty"`
	// PendingMinScore is the lowest semantic similarity observed in the current
	// pending sequence.
	PendingMinScore float64 `json:"pending_min_score,omitempty"`

	// ConsecutiveFailures counts upstream failures associated with the current
	// pin and is reset after a successful pinned attempt.
	ConsecutiveFailures int `json:"consecutive_failures,omitempty"`
	// RouteObservations is keyed by a stable, opaque effective-route identity.
	// It is a map so primary, fallback, and destination warmth remain separate.
	// Writers are responsible for bounding retained history.
	RouteObservations map[string]SessionRouteObservation `json:"route_observations,omitempty"`
}

// SessionStoreStatus describes the guarantees of the active session-state
// backend. Multi-replica session routing is safe only when both
// SharedAcrossReplicas and AtomicUpdates are true.
type SessionStoreStatus struct {
	// Backend is a diagnostic name for the active implementation. Callers must
	// use the capability fields, not this label, when deciding readiness.
	Backend string `json:"backend"`
	// SharedAcrossReplicas reports whether all replicas observe the same logical
	// session records.
	SharedAcrossReplicas bool `json:"shared_across_replicas"`
	// AtomicUpdates reports whether concurrent updates to one session are
	// serialized across every replica that shares the backend.
	AtomicUpdates bool `json:"atomic_updates"`
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
	// Get atomically reads a record and refreshes its sliding expiration to ttl.
	// The returned record is caller-owned. found is false, with a nil error, when
	// the key is absent or expired. ttl must be positive.
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

	value, found, err := s.store.UpdateWithTTL(key, ttl, func(current any) (any, error) {
		return complexitySessionRecordFromValue(current)
	})
	if err != nil {
		return nil, found, fmt.Errorf("refresh complexity session %q: %w", key, err)
	}
	if !found {
		return nil, false, nil
	}

	record, err := complexitySessionRecordFromValue(value)
	if err != nil {
		return nil, true, fmt.Errorf("read complexity session %q: %w", key, err)
	}
	return record, true, nil
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

	created, err := s.store.SetNXWithTTL(key, cloneSessionComplexityRecord(record), ttl)
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

	value, found, err := s.store.UpdateWithTTL(key, ttl, func(current any) (any, error) {
		record, err := complexitySessionRecordFromValue(current)
		if err != nil {
			return nil, err
		}
		if err := update(record); err != nil {
			return nil, err
		}
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

	sharedAcrossReplicas := s.store.HasSyncDelegate()
	return SessionStoreStatus{
		Backend:              complexitySessionStoreBackendKV,
		SharedAcrossReplicas: sharedAcrossReplicas,
		AtomicUpdates:        !sharedAcrossReplicas,
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
