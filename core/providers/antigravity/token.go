package antigravity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	// tokenRefreshMargin refreshes an access token this long before Google expires it.
	tokenRefreshMargin = 5 * time.Minute
	// refreshTimeout bounds one refresh (token exchange plus project discovery). The
	// refresh runs detached from the request context so a caller hanging up does not
	// abort a refresh other requests are waiting on.
	refreshTimeout = 60 * time.Second
	// refreshFailureBackoff is how long a failed refresh or discovery is replayed from
	// cache instead of hitting Google again.
	refreshFailureBackoff = 30 * time.Second
	// credentialUpdateTimeout bounds one call of the credential updater.
	credentialUpdateTimeout = 30 * time.Second
)

// httpDoer is the minimal HTTP surface the OAuth and discovery calls need, so the same
// code runs on net/http (interactive login) and on the provider's configured fasthttp
// client (refresh during inference, which must honour proxy and dial policy).
type httpDoer interface {
	do(ctx context.Context, method, url string, headers map[string]string, body []byte) (int, []byte, error)
}

// netHTTPDoer runs requests on a net/http client.
type netHTTPDoer struct {
	client *http.Client
}

func (d netHTTPDoer) do(ctx context.Context, method, url string, headers map[string]string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthBodyBytes))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, respBody, nil
}

// fastHTTPDoer runs requests on a configured fasthttp client.
type fastHTTPDoer struct {
	client *fasthttp.Client
}

func (d fastHTTPDoer) do(ctx context.Context, method, url string, headers map[string]string, body []byte) (int, []byte, error) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(url)
	req.Header.SetMethod(method)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.SetBody(body)
	}
	_, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, d.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		if bifrostErr.Error != nil {
			if bifrostErr.Error.Error != nil {
				return 0, nil, fmt.Errorf("%s: %w", bifrostErr.Error.Message, bifrostErr.Error.Error)
			}
			return 0, nil, errors.New(bifrostErr.Error.Message)
		}
		return 0, nil, errors.New("antigravity request failed")
	}
	respBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode(), append([]byte(nil), respBody...), nil
}

// tokenState is one minted access token and what was learned with it.
type tokenState struct {
	accessToken  string
	expiresAt    time.Time
	refreshToken string
	projectID    string
	email        string
	// sources are the hashes of the key values this state belongs to: the value it was
	// built from plus every value this provider wrote back for the key. A key value
	// outside this set means the operator replaced the credential.
	sources []string
}

// fresh reports whether the access token outlives the refresh margin.
func (s *tokenState) fresh(now time.Time) bool {
	return s != nil && s.accessToken != "" && now.Add(tokenRefreshMargin).Before(s.expiresAt)
}

// accepts reports whether the state was built from, or written back as, valueHash.
func (s *tokenState) accepts(valueHash string) bool {
	return s != nil && slices.Contains(s.sources, valueHash)
}

// withSource returns a copy of s that also accepts valueHash.
func (s *tokenState) withSource(valueHash string) *tokenState {
	next := *s
	if !s.accepts(valueHash) {
		next.sources = append(slices.Clip(s.sources), valueHash)
	}
	return &next
}

// refreshFailure replays a recent refresh or discovery failure of one key value.
type refreshFailure struct {
	until     time.Time
	valueHash string
	err       *schemas.BifrostError
}

// tokenEntry caches the access token of one key. Entries live in a package-level map
// because Bifrost rebuilds provider instances on every configuration change, and a
// rebuild must not throw away live tokens.
type tokenEntry struct {
	mu      sync.Mutex
	state   atomic.Pointer[tokenState]
	failure atomic.Pointer[refreshFailure]
}

// tokenPool holds one entry per key: keyed by key ID, so a value this provider writes
// back (a discovered project, a rotated refresh token) keeps using the same entry and
// the same single-flight lock instead of leaving the old one behind. Keys without an ID
// fall back to the value hash.
var tokenPool sync.Map // cache key -> *tokenEntry

// seedPool holds tokens minted by ExchangeCode for a value no key carries yet. The first
// key presenting that value adopts the seed. Seeds expire with their access token.
var seedPool sync.Map // value hash -> *tokenState

// valueHash identifies a credential value without keeping it in memory.
func valueHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func tokenCacheKey(key schemas.Key, hash string) string {
	if key.ID != "" {
		return "id:" + key.ID
	}
	return "value:" + hash
}

func loadTokenEntry(cacheKey string) *tokenEntry {
	if entry, ok := tokenPool.Load(cacheKey); ok {
		return entry.(*tokenEntry)
	}
	entry, _ := tokenPool.LoadOrStore(cacheKey, &tokenEntry{})
	return entry.(*tokenEntry)
}

// seedTokenCache records a freshly minted token for a credential value about to be
// stored on a key, so the key's first request does not redeem the refresh token again.
func seedTokenCache(value string, state *tokenState) {
	now := time.Now()
	seedPool.Range(func(k, v any) bool {
		if !v.(*tokenState).fresh(now) {
			seedPool.Delete(k)
		}
		return true
	})
	hash := valueHash(value)
	seedPool.Store(hash, state.withSource(hash))
}

// takeSeed returns and removes the seed for valueHash, if one is still fresh.
func takeSeed(hash string) *tokenState {
	v, ok := seedPool.LoadAndDelete(hash)
	if !ok {
		return nil
	}
	st := v.(*tokenState)
	if !st.fresh(time.Now()) {
		return nil
	}
	return st
}

// authSession is what a request needs to call Cloud Code Assist.
type authSession struct {
	accessToken string
	projectID   string
}

// resolveAuth returns a usable access token and project for key, refreshing when the
// cached token is near expiry. staleToken, when set, is an access token upstream just
// rejected: it forces a refresh unless a concurrent request has already replaced it.
func (p *AntigravityProvider) resolveAuth(ctx *schemas.BifrostContext, key schemas.Key, staleToken string) (*authSession, *schemas.BifrostError) {
	value := key.Value.GetValue()
	creds, err := ParseCredentials(value)
	if err != nil {
		return nil, credentialError(err.Error())
	}
	hash := valueHash(value)
	entry := loadTokenEntry(tokenCacheKey(key, hash))

	usable := func(st *tokenState, now time.Time) bool {
		return st.accepts(hash) && st.fresh(now) && st.projectID != "" && (staleToken == "" || st.accessToken != staleToken)
	}
	replayFailure := func(now time.Time) *schemas.BifrostError {
		if f := entry.failure.Load(); f != nil && f.valueHash == hash && now.Before(f.until) {
			return cloneError(f.err)
		}
		return nil
	}

	if st := entry.state.Load(); usable(st, time.Now()) {
		return &authSession{accessToken: st.accessToken, projectID: st.projectID}, nil
	}
	if bifrostErr := replayFailure(time.Now()); bifrostErr != nil {
		return nil, bifrostErr
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()

	now := time.Now()
	st := entry.state.Load()
	if st != nil && !st.accepts(hash) {
		// The operator replaced the key's credential: nothing cached belongs to it.
		st = nil
		entry.state.Store(nil)
	}
	if st == nil {
		if seed := takeSeed(hash); seed != nil {
			st = seed
			entry.state.Store(st)
		}
	}
	if usable(st, now) {
		return &authSession{accessToken: st.accessToken, projectID: st.projectID}, nil
	}
	if bifrostErr := replayFailure(now); bifrostErr != nil {
		return nil, bifrostErr
	}

	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	defer cancel()
	doer := fastHTTPDoer{client: p.exchangeClient}

	next := &tokenState{refreshToken: creds.RefreshToken, projectID: creds.ProjectID, email: creds.Email, sources: []string{hash}}
	if st != nil {
		next = st.withSource(hash)
		if next.projectID == "" {
			next.projectID = creds.ProjectID
		}
	}

	if !st.fresh(now) || (staleToken != "" && st.accessToken == staleToken) {
		tok, err := refreshAccessToken(refreshCtx, doer, next.refreshToken)
		if err != nil {
			bifrostErr := refreshError(err)
			entry.failure.Store(&refreshFailure{until: time.Now().Add(refreshFailureBackoff), valueHash: hash, err: bifrostErr})
			p.logger.Warn("antigravity: token refresh failed for key %s: %s", key.ID, bifrostErr.Error.Message)
			return nil, cloneError(bifrostErr)
		}
		refreshed := *next
		refreshed.accessToken = tok.AccessToken
		refreshed.expiresAt = tok.expiresAt(time.Now())
		if tok.RefreshToken != "" {
			refreshed.refreshToken = tok.RefreshToken
		}
		next = &refreshed
		entry.state.Store(next)
	}

	if next.projectID == "" {
		project, err := discoverProject(refreshCtx, doer, next.accessToken)
		if err != nil {
			bifrostErr := &schemas.BifrostError{
				IsBifrostError: false,
				StatusCode:     schemas.Ptr(http.StatusForbidden),
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr("PERMISSION_DENIED"),
					Message: err.Error(),
				},
			}
			entry.failure.Store(&refreshFailure{until: time.Now().Add(refreshFailureBackoff), valueHash: hash, err: bifrostErr})
			return nil, cloneError(bifrostErr)
		}
		updated := *next
		updated.projectID = project
		next = &updated
		entry.state.Store(next)
	}
	entry.failure.Store(nil)

	// Write back a credential carrying what the stored value lacks: a discovered
	// project or a rotated refresh token. The written value joins the state's sources
	// first, so the provider rebuild it triggers keeps this entry warm, and a value
	// already written is never written again.
	if next.projectID != creds.ProjectID || next.refreshToken != creds.RefreshToken {
		encoded, encErr := (&Credentials{
			RefreshToken: next.refreshToken,
			ProjectID:    next.projectID,
			Email:        firstNonEmpty(creds.Email, next.email),
		}).Encode()
		if encodedHash := valueHash(encoded); encErr == nil && !next.accepts(encodedHash) {
			next = next.withSource(encodedHash)
			entry.state.Store(next)
			p.persistCredential(ctx, key, encoded)
		}
	}

	return &authSession{accessToken: next.accessToken, projectID: next.projectID}, nil
}

// invalidateToken drops an access token upstream rejected even after a forced refresh,
// so the next request mints a new one instead of replaying it. The project is kept.
func invalidateToken(key schemas.Key, accessToken string) {
	entry := loadTokenEntry(tokenCacheKey(key, valueHash(key.Value.GetValue())))
	current := entry.state.Load()
	if current == nil || current.accessToken != accessToken {
		return
	}
	cleared := *current
	cleared.accessToken = ""
	cleared.expiresAt = time.Time{}
	entry.state.CompareAndSwap(current, &cleared)
}

// persistCredential hands a re-encoded credential to the credential updater in the
// background: the updater writes configuration and may rebuild this provider, which must
// not happen while a request holds the entry lock.
func (p *AntigravityProvider) persistCredential(ctx context.Context, key schemas.Key, encoded string) {
	if p.credentialUpdater == nil || key.ID == "" {
		return
	}
	updater := p.credentialUpdater
	logger := p.logger
	keyID := key.ID
	detached := context.WithoutCancel(ctx)
	go func() {
		updateCtx, cancel := context.WithTimeout(detached, credentialUpdateTimeout)
		defer cancel()
		if err := updater(updateCtx, schemas.Antigravity, keyID, encoded); err != nil {
			logger.Warn("antigravity: failed to persist refreshed credential for key %s: %v", keyID, err)
		}
	}()
}

// refreshError maps a failed refresh onto the status the retry loop acts on: a refused
// refresh token is a dead credential (401), anything else is an upstream failure.
func refreshError(err error) *schemas.BifrostError {
	var tokErr *tokenError
	if errors.As(err, &tokErr) {
		switch {
		case tokErr.terminal():
			return credentialError("antigravity refresh token was rejected (" + firstNonEmpty(tokErr.Code, fmt.Sprintf("HTTP %d", tokErr.Status)) + "); log in to this Antigravity account again")
		case tokErr.Status == http.StatusTooManyRequests:
			return &schemas.BifrostError{
				StatusCode: schemas.Ptr(http.StatusTooManyRequests),
				Error:      &schemas.ErrorField{Message: tokErr.Error(), Error: err},
			}
		case tokErr.Status >= 500:
			return &schemas.BifrostError{
				StatusCode: schemas.Ptr(http.StatusBadGateway),
				Error:      &schemas.ErrorField{Message: tokErr.Error(), Error: err},
			}
		default:
			return credentialError(tokErr.Error())
		}
	}
	return &schemas.BifrostError{
		StatusCode: schemas.Ptr(http.StatusBadGateway),
		Error:      &schemas.ErrorField{Message: err.Error(), Error: err},
	}
}

// credentialError is a per-key authentication failure, which rotates to another key.
func credentialError(message string) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     schemas.Ptr(http.StatusUnauthorized),
		Error: &schemas.ErrorField{
			Type:    schemas.Ptr("authentication_error"),
			Message: message,
		},
	}
}

// cloneError copies a cached error so callers that enrich it do not race each other.
func cloneError(err *schemas.BifrostError) *schemas.BifrostError {
	if err == nil {
		return nil
	}
	clone := *err
	if err.Error != nil {
		field := *err.Error
		clone.Error = &field
	}
	if err.StatusCode != nil {
		clone.StatusCode = schemas.Ptr(*err.StatusCode)
	}
	return &clone
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
