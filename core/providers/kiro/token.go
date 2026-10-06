package kiro

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	// tokenRefreshMargin is how long before expiry an access token is considered stale.
	tokenRefreshMargin = 60 * time.Second

	// Backoff for a cached refresh failure. A dead refresh token will not recover by retrying
	// quickly; a transient failure deserves only a short pause.
	permanentBackoffBase = 5 * time.Second
	permanentBackoffCap  = 5 * time.Minute
	transientBackoffBase = 500 * time.Millisecond
	transientBackoffCap  = 10 * time.Second

	// credentialUpdateTimeout bounds the persistence callback for a rotated credential.
	credentialUpdateTimeout = 10 * time.Second
)

// kiroSession is a usable access token plus the credential it belongs to. The credential may be
// newer than the key value (a rotated refresh token or a learned profile ARN), and request
// routing (profile ARN, regions) must always come from the same credential as the token.
type kiroSession struct {
	accessToken string
	expiresAt   time.Time
	creds       *Credentials
}

// refreshFailure is the negative cache entry.
type refreshFailure struct {
	err        *schemas.BifrostError
	retryAfter time.Time
	attempts   int
}

// cachedError returns a copy of the cached error: callers enrich and annotate the error they
// receive, and concurrent requests must not share that mutation.
func (f *refreshFailure) cachedError() *schemas.BifrostError {
	copied := *f.err
	if f.err.Error != nil {
		field := *f.err.Error
		copied.Error = &field
	}
	return &copied
}

// tokenEntry is the cache slot of one credential: the refresh lock, the current session, the
// failure backoff, and the lineage of key values that belong to the credential.
type tokenEntry struct {
	mu      sync.Mutex
	session atomic.Pointer[kiroSession]
	failure atomic.Pointer[refreshFailure]
	lineage atomic.Pointer[credentialLineage]
}

// credentialLineage is the set of key values (by hash) that are the same credential: the value
// the entry was first seen with and the values this process persisted after rotations. A key value
// outside it means the operator replaced the credential. Immutable once stored.
type credentialLineage struct {
	generation uint64
	hashes     []string
}

const (
	// maxLineageValues bounds a lineage. Older rotations fall off; a value that stale has been
	// replaced in configuration long before it could arrive again.
	maxLineageValues = 8
	// maxAnonymousEntries bounds the entries of keys without an ID (keyed by value hash).
	maxAnonymousEntries = 1024
)

// tokenPool maps key ID to *tokenEntry. It is package-level because Bifrost rebuilds provider
// instances on every configuration change, and an access token must survive that. Keying by ID
// keeps one entry, and so one refresh lock, across refresh-token rotations: the pre- and
// post-rotation values of a key can never refresh concurrently and spend the same refresh token
// twice. It grows only with the number of configured key IDs.
var tokenPool sync.Map

// anonymousEntries holds entries for keys without an ID (direct keys), keyed by value hash. It is
// bounded because those values arrive per request and are not limited by configuration.
var anonymousEntries = boundedEntries{entries: map[string]*tokenEntry{}}

type boundedEntries struct {
	mu      sync.Mutex
	entries map[string]*tokenEntry
}

func (b *boundedEntries) get(hash string) *tokenEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	if entry, ok := b.entries[hash]; ok {
		return entry
	}
	if len(b.entries) >= maxAnonymousEntries {
		for evict := range b.entries {
			delete(b.entries, evict)
			break
		}
	}
	entry := &tokenEntry{}
	b.entries[hash] = entry
	return entry
}

func credentialHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// entryFor returns the cache entry of a key, adopting the key's current value into the entry's
// lineage.
func entryFor(keyID, valueHash string) *tokenEntry {
	var entry *tokenEntry
	if keyID == "" {
		entry = anonymousEntries.get(valueHash)
	} else if existing, ok := tokenPool.Load(keyID); ok {
		entry = existing.(*tokenEntry)
	} else {
		actual, _ := tokenPool.LoadOrStore(keyID, &tokenEntry{})
		entry = actual.(*tokenEntry)
	}
	entry.adopt(valueHash)
	return entry
}

func (e *tokenEntry) knows(hash string) bool {
	lineage := e.lineage.Load()
	return lineage != nil && slices.Contains(lineage.hashes, hash)
}

// adopt starts the entry's lineage with the first value seen. A value outside the lineage means
// the operator replaced the credential: the cached session and failure belong to the old one and
// are dropped, and the generation moves on so an in-flight refresh of the old credential does not
// persist over the new one.
func (e *tokenEntry) adopt(hash string) {
	if e.knows(hash) {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.adoptLocked(hash)
}

// adoptLocked is adopt with e.mu held.
func (e *tokenEntry) adoptLocked(hash string) {
	if e.knows(hash) {
		return
	}
	next := &credentialLineage{hashes: []string{hash}}
	if previous := e.lineage.Load(); previous != nil {
		next.generation = previous.generation + 1
		e.session.Store(nil)
		e.failure.Store(nil)
	}
	e.lineage.Store(next)
}

// tokenSource resolves access tokens for Kiro keys.
type tokenSource struct {
	client  *fasthttp.Client
	updater schemas.KeyCredentialUpdater
	logger  schemas.Logger
}

// session returns a valid access token for the key. stale is the access token the upstream just
// rejected ("" when none): a cached token equal to it is never returned, which forces exactly one
// refresh per rejected generation even when many requests observe the rejection at once.
func (s *tokenSource) session(ctx context.Context, key schemas.Key, stale string) (*kiroSession, *schemas.BifrostError) {
	value := key.Value.GetValue()
	creds, err := ParseCredentials(value)
	if err != nil {
		return nil, newKiroError(401, "authentication_error", "invalid_api_key", "kiro: "+err.Error())
	}
	valueHash := credentialHash(value)
	entry := entryFor(key.ID, valueHash)

	if sess := usableSession(entry, creds, stale, time.Now()); sess != nil {
		return sess, nil
	}
	if f := entry.failure.Load(); f != nil && time.Now().Before(f.retryAfter) {
		return nil, f.cachedError()
	}

	sess, generation, rotated, bErr := s.refreshLocked(ctx, entry, creds, valueHash, stale)
	if bErr != nil {
		return nil, bErr
	}
	// Persisted outside the entry lock: the updater writes configuration and must never be able
	// to re-enter a refresh of this same credential while the lock is held.
	if rotated {
		s.persist(ctx, key.ID, entry, generation, sess)
	}
	return sess, nil
}

// refreshLocked performs the single-flight refresh under the entry lock. rotated reports a new
// refresh token or a newly learned profile ARN, i.e. a credential that has to be persisted.
func (s *tokenSource) refreshLocked(ctx context.Context, entry *tokenEntry, creds *Credentials, valueHash, stale string) (*kiroSession, uint64, bool, *schemas.BifrostError) {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	// Another request may have replaced the lineage while this one queued; refreshing this
	// value's credential under that lineage would persist it over the newer credential.
	entry.adoptLocked(valueHash)
	generation := entry.lineage.Load().generation

	// Double-checked: requests queued behind the refresher leave here with its token.
	now := time.Now()
	if sess := usableSession(entry, creds, stale, now); sess != nil {
		return sess, generation, false, nil
	}
	if f := entry.failure.Load(); f != nil && now.Before(f.retryAfter) {
		return nil, generation, false, f.cachedError()
	}

	// The newest credential known for this entry wins: after a rotation the key value may still
	// carry the consumed refresh token until the persisted update reaches this process.
	base := creds
	if cached := entry.session.Load(); cached != nil {
		base = cached.creds
	}

	// WithoutCancel: other requests are queued on the entry lock waiting for this refresh, so
	// one caller hanging up must not fail all of them.
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	defer cancel()
	grant, bErr := refreshAccessToken(refreshCtx, s.client, base)
	if bErr != nil {
		entry.recordFailure(bErr, isTerminalRefreshError(bErr))
		return nil, generation, false, bErr
	}

	updated := *base
	updated.AccessToken = grant.accessToken
	updated.RefreshToken = grant.refreshToken
	updated.ExpiresAt = grant.expiresAt.UTC().Format(time.RFC3339Nano)
	learnedProfile := false
	if updated.ProfileArn == "" && grant.profileArn != "" {
		updated.ProfileArn = grant.profileArn
		learnedProfile = true
	}
	sess := &kiroSession{accessToken: grant.accessToken, expiresAt: grant.expiresAt, creds: &updated}
	entry.session.Store(sess)
	entry.failure.Store(nil)
	return sess, generation, grant.refreshToken != base.RefreshToken || learnedProfile, nil
}

// usableSession returns a cached or credential-embedded access token that is neither expiring nor
// the rejected one.
func usableSession(entry *tokenEntry, creds *Credentials, stale string, now time.Time) *kiroSession {
	if cached := entry.session.Load(); cached != nil {
		if cached.accessToken != stale && now.Add(tokenRefreshMargin).Before(cached.expiresAt) {
			return cached
		}
		return nil
	}
	if creds.AccessToken == "" || creds.AccessToken == stale {
		return nil
	}
	expiresAt := creds.accessTokenExpiry()
	if !now.Add(tokenRefreshMargin).Before(expiresAt) {
		return nil
	}
	sess := &kiroSession{accessToken: creds.AccessToken, expiresAt: expiresAt, creds: creds}
	// Seed the entry so a later rejection of this token (stale == token) is visible to every
	// request sharing the credential.
	entry.session.CompareAndSwap(nil, sess)
	return sess
}

// persist writes a rotated credential back through the credential updater. The new value joins
// the entry's lineage first, so when it reaches this process as the key value it is recognised
// as the same credential and reuses the cached access token instead of refreshing again. Nothing
// is written when the operator replaced the credential while the refresh ran.
func (s *tokenSource) persist(ctx context.Context, keyID string, entry *tokenEntry, generation uint64, sess *kiroSession) {
	if s.updater == nil || keyID == "" {
		s.warn("kiro: the credential for key %q rotated but no credential updater is configured; the new refresh token lives only in memory", keyID)
		return
	}
	encoded, err := sess.creds.Encode()
	if err != nil {
		s.warn("kiro: could not encode the refreshed credential: %v", err)
		return
	}
	entry.mu.Lock()
	lineage := entry.lineage.Load()
	if lineage == nil || lineage.generation != generation {
		entry.mu.Unlock()
		return
	}
	hashes := append(slices.Clone(lineage.hashes), credentialHash(encoded))
	if len(hashes) > maxLineageValues {
		hashes = hashes[len(hashes)-maxLineageValues:]
	}
	entry.lineage.Store(&credentialLineage{generation: generation, hashes: hashes})
	entry.mu.Unlock()

	updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), credentialUpdateTimeout)
	defer cancel()
	if err := s.updater(updateCtx, schemas.Kiro, keyID, encoded); err != nil {
		s.warn("kiro: could not persist the rotated credential for key %q: %v", keyID, err)
	}
}

func (s *tokenSource) warn(format string, args ...any) {
	if s.logger != nil {
		s.logger.Warn(format, args...)
	}
}

// recordFailure caches a private copy of a refresh failure with exponential backoff.
func (e *tokenEntry) recordFailure(bErr *schemas.BifrostError, permanent bool) {
	attempts := 1
	if prev := e.failure.Load(); prev != nil {
		attempts = prev.attempts + 1
	}
	failure := &refreshFailure{
		err:        bErr,
		retryAfter: time.Now().Add(backoffFor(attempts, permanent)),
		attempts:   attempts,
	}
	failure.err = failure.cachedError()
	e.failure.Store(failure)
}

func backoffFor(attempts int, permanent bool) time.Duration {
	base, ceiling := transientBackoffBase, transientBackoffCap
	if permanent {
		base, ceiling = permanentBackoffBase, permanentBackoffCap
	}
	delay := base
	for i := 1; i < attempts && delay < ceiling; i++ {
		delay *= 2
	}
	return min(delay, ceiling)
}

// newExchangeClient clones the configured inference client for auth calls, so refreshes follow
// the same proxy, dial and TLS policy as inference.
func newExchangeClient(base *fasthttp.Client) *fasthttp.Client {
	client := providerUtils.CloneFastHTTPClientConfig(base)
	client.MaxResponseBodySize = maxAuthBodyBytes
	client.StreamResponseBody = false
	return client
}
