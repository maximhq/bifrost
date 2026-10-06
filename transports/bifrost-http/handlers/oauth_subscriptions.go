// This file contains the login flows that mint credentials for the OAuth
// subscription providers (Antigravity and Kiro). The flows only produce the
// credential string; the dashboard then saves it as a key value through the
// regular POST /api/providers/{provider}/keys endpoint.
package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/providers/antigravity"
	"github.com/maximhq/bifrost/core/providers/kiro"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

const (
	// oauthSubscriptionSessionTTL bounds how long a started login stays usable.
	oauthSubscriptionSessionTTL = 15 * time.Minute
	// oauthSubscriptionMaxSessions caps pending logins per flow; the oldest is
	// evicted when a new login would exceed it.
	oauthSubscriptionMaxSessions = 64
	// antigravityExchangeTimeout covers the token exchange plus Cloud Code
	// project discovery/onboarding, which polls upstream until provisioned.
	antigravityExchangeTimeout = 2 * time.Minute
	// kiroUpstreamTimeout bounds a single device-authorization or poll call.
	kiroUpstreamTimeout = 30 * time.Second
	// kiroDefaultPollInterval applies when upstream does not send an interval.
	kiroDefaultPollInterval = 5 * time.Second
	// kiroMinPollInterval keeps a misbehaving upstream from forcing a hot loop.
	kiroMinPollInterval = time.Second
	// kiroSlowDownIncrement is the RFC 8628 back-off on a slow_down answer.
	kiroSlowDownIncrement = 5 * time.Second
)

// Kiro device login methods accepted by POST /api/oauth-subscriptions/kiro/start.
const (
	kiroLoginMethodBuilderID = "builder-id"
	kiroLoginMethodGoogle    = "google"
	kiroLoginMethodGithub    = "github"
)

// Kiro poll statuses: the provider package's values plus "error", which the
// handler reports for a failed poll.
const (
	kiroPollStatusPending  = "pending"
	kiroPollStatusSlowDown = "slow_down"
	kiroPollStatusExpired  = "expired"
	kiroPollStatusComplete = "complete"
	kiroPollStatusError    = "error"
)

// Upstream calls go through package variables so tests can substitute fakes.
var (
	antigravityNewPKCE      = antigravity.NewPKCE
	antigravityBuildAuthURL = antigravity.BuildAuthURL
	antigravityExchangeCode = antigravity.ExchangeCode
	kiroStartDeviceLogin    = kiro.StartDeviceLogin
	kiroPollDeviceLogin     = kiro.PollDeviceLogin
)

var (
	errOAuthSessionNotFound = errors.New("unknown login session; start a new login")
	errOAuthSessionExpired  = errors.New("login session expired; start a new login")
)

// oauthSessionEntry is one pending login.
type oauthSessionEntry[T any] struct {
	createdAt time.Time
	expiresAt time.Time
	value     T
}

// oauthSessionStore keeps pending logins in memory. Sessions never leave the
// process: a restart simply requires starting the login again.
type oauthSessionStore[T any] struct {
	mu       sync.Mutex
	sessions map[string]*oauthSessionEntry[T]
	ttl      time.Duration
	max      int
	now      func() time.Time
}

func newOAuthSessionStore[T any](ttl time.Duration, maxSessions int) *oauthSessionStore[T] {
	return &oauthSessionStore[T]{
		sessions: make(map[string]*oauthSessionEntry[T]),
		ttl:      ttl,
		max:      maxSessions,
		now:      time.Now,
	}
}

// put stores value under a fresh random id. The session expires at the
// earlier of the store TTL and notAfter (zero notAfter means TTL only).
func (s *oauthSessionStore[T]) put(value T, notAfter time.Time) (string, time.Time, error) {
	id, err := randomHex(16)
	if err != nil {
		return "", time.Time{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	expiresAt := now.Add(s.ttl)
	if !notAfter.IsZero() && notAfter.Before(expiresAt) {
		expiresAt = notAfter
	}
	s.pruneLocked(now)
	for len(s.sessions) >= s.max {
		s.evictOldestLocked()
	}
	s.sessions[id] = &oauthSessionEntry[T]{createdAt: now, expiresAt: expiresAt, value: value}
	return id, expiresAt, nil
}

// update runs fn on the session under the store lock. fn returns true to
// remove the session afterwards. An expired session is removed and reported
// as errOAuthSessionExpired without calling fn.
func (s *oauthSessionStore[T]) update(id string, fn func(entry *oauthSessionEntry[T], now time.Time) (remove bool)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.sessions[id]
	if !ok {
		return errOAuthSessionNotFound
	}
	now := s.now()
	if !now.Before(entry.expiresAt) {
		delete(s.sessions, id)
		return errOAuthSessionExpired
	}
	if fn(entry, now) {
		delete(s.sessions, id)
	}
	return nil
}

// len reports the number of stored sessions, expired ones included.
func (s *oauthSessionStore[T]) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func (s *oauthSessionStore[T]) pruneLocked(now time.Time) {
	for id, entry := range s.sessions {
		if !now.Before(entry.expiresAt) {
			delete(s.sessions, id)
		}
	}
}

func (s *oauthSessionStore[T]) evictOldestLocked() {
	var oldestID string
	var oldest time.Time
	for id, entry := range s.sessions {
		if oldestID == "" || entry.createdAt.Before(oldest) {
			oldestID, oldest = id, entry.createdAt
		}
	}
	delete(s.sessions, oldestID)
}

// antigravityLoginSession is a pending Antigravity PKCE login.
type antigravityLoginSession struct {
	state        string
	codeVerifier string
	redirectURI  string
	// exchanging is set while a code exchange runs so a double submit cannot
	// spend the verifier twice.
	exchanging bool
}

// kiroLoginSession is a pending Kiro device-code login.
type kiroLoginSession struct {
	auth       *kiro.DeviceAuthorization
	nextPollAt time.Time
	// polling is set while an upstream poll runs; concurrent polls answer
	// pending without contacting upstream.
	polling bool
}

// OAuthSubscriptionHandler serves the login flows of the OAuth subscription
// providers.
type OAuthSubscriptionHandler struct {
	config              *lib.Config
	antigravitySessions *oauthSessionStore[antigravityLoginSession]
	kiroSessions        *oauthSessionStore[kiroLoginSession]
}

// NewOAuthSubscriptionHandler creates the handler with empty session stores.
func NewOAuthSubscriptionHandler(config *lib.Config) *OAuthSubscriptionHandler {
	return &OAuthSubscriptionHandler{
		config:              config,
		antigravitySessions: newOAuthSessionStore[antigravityLoginSession](oauthSubscriptionSessionTTL, oauthSubscriptionMaxSessions),
		kiroSessions:        newOAuthSessionStore[kiroLoginSession](oauthSubscriptionSessionTTL, oauthSubscriptionMaxSessions),
	}
}

// RegisterRoutes registers the OAuth subscription login routes. They sit
// behind the same middleware chain as the provider key routes, since their
// only output is a credential destined for a provider key.
func (h *OAuthSubscriptionHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.POST("/api/oauth-subscriptions/antigravity/start", lib.ChainMiddlewares(h.startAntigravityLogin, middlewares...))
	r.POST("/api/oauth-subscriptions/antigravity/complete", lib.ChainMiddlewares(h.completeAntigravityLogin, middlewares...))
	r.POST("/api/oauth-subscriptions/kiro/start", lib.ChainMiddlewares(h.startKiroLogin, middlewares...))
	r.POST("/api/oauth-subscriptions/kiro/poll", lib.ChainMiddlewares(h.pollKiroLogin, middlewares...))
}

type antigravityStartResponse struct {
	SessionID   string `json:"session_id"`
	AuthURL     string `json:"auth_url"`
	RedirectURI string `json:"redirect_uri"`
	ExpiresAt   string `json:"expires_at"`
}

type antigravityCompleteRequest struct {
	SessionID   string `json:"session_id"`
	CallbackURL string `json:"callback_url"`
}

type antigravityCompleteResponse struct {
	Credential    string `json:"credential"`
	Email         string `json:"email"`
	ProjectID     string `json:"project_id"`
	SuggestedName string `json:"suggested_name"`
}

type kiroStartRequest struct {
	Method string `json:"method"`
}

type kiroStartResponse struct {
	SessionID               string `json:"session_id"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresAt               string `json:"expires_at"`
	IntervalSeconds         int    `json:"interval_seconds"`
}

type kiroPollRequest struct {
	SessionID string `json:"session_id"`
}

type kiroPollResponse struct {
	Status          string `json:"status"`
	Credential      string `json:"credential,omitempty"`
	SuggestedName   string `json:"suggested_name,omitempty"`
	Error           string `json:"error,omitempty"`
	IntervalSeconds int    `json:"interval_seconds"`
}

// startAntigravityLogin handles POST /api/oauth-subscriptions/antigravity/start.
func (h *OAuthSubscriptionHandler) startAntigravityLogin(ctx *fasthttp.RequestCtx) {
	if body := strings.TrimSpace(string(ctx.PostBody())); body != "" {
		var payload map[string]any
		if err := sonic.Unmarshal([]byte(body), &payload); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
			return
		}
	}
	verifier, challenge, err := antigravityNewPKCE()
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to generate PKCE challenge")
		return
	}
	state, err := randomBase64URL(32)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to generate OAuth state")
		return
	}
	redirectURI := antigravity.DefaultRedirectURI
	sessionID, expiresAt, err := h.antigravitySessions.put(antigravityLoginSession{
		state:        state,
		codeVerifier: verifier,
		redirectURI:  redirectURI,
	}, time.Time{})
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to create login session")
		return
	}
	SendJSON(ctx, antigravityStartResponse{
		SessionID:   sessionID,
		AuthURL:     antigravityBuildAuthURL(redirectURI, state, challenge),
		RedirectURI: redirectURI,
		ExpiresAt:   expiresAt.UTC().Format(time.RFC3339),
	})
}

// completeAntigravityLogin handles POST /api/oauth-subscriptions/antigravity/complete.
func (h *OAuthSubscriptionHandler) completeAntigravityLogin(ctx *fasthttp.RequestCtx) {
	var payload antigravityCompleteRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	payload.SessionID = strings.TrimSpace(payload.SessionID)
	if payload.SessionID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "session_id is required")
		return
	}
	code, state, err := parseAntigravityCallback(payload.CallbackURL)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	var session antigravityLoginSession
	var rejection string
	err = h.antigravitySessions.update(payload.SessionID, func(entry *oauthSessionEntry[antigravityLoginSession], _ time.Time) bool {
		switch {
		case entry.value.exchanging:
			rejection = "a code exchange for this login is already in progress"
		case state != "" && state != entry.value.state:
			rejection = "OAuth state mismatch: the callback URL belongs to a different login attempt"
		default:
			entry.value.exchanging = true
			session = entry.value
		}
		return false
	})
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if rejection != "" {
		SendError(ctx, fasthttp.StatusBadRequest, rejection)
		return
	}

	exchangeCtx, cancel := context.WithTimeout(ctx, antigravityExchangeTimeout)
	creds, exchangeErr := antigravityExchangeCode(exchangeCtx, code, session.redirectURI, session.codeVerifier)
	cancel()
	if exchangeErr == nil && creds == nil {
		exchangeErr = errors.New("no credentials returned")
	}
	var credential string
	if exchangeErr == nil {
		credential, exchangeErr = creds.Encode()
	}
	// Success consumes the session; failure releases it so a corrected code
	// can be submitted within the same login.
	_ = h.antigravitySessions.update(payload.SessionID, func(entry *oauthSessionEntry[antigravityLoginSession], _ time.Time) bool {
		entry.value.exchanging = false
		return exchangeErr == nil
	})
	if exchangeErr != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Antigravity token exchange failed: %v", exchangeErr))
		return
	}

	suggestedName := strings.TrimSpace(creds.Email)
	if suggestedName == "" {
		suggestedName = h.nextAntigravityKeyName()
	}
	SendJSON(ctx, antigravityCompleteResponse{
		Credential:    credential,
		Email:         creds.Email,
		ProjectID:     creds.ProjectID,
		SuggestedName: suggestedName,
	})
}

// startKiroLogin handles POST /api/oauth-subscriptions/kiro/start.
func (h *OAuthSubscriptionHandler) startKiroLogin(ctx *fasthttp.RequestCtx) {
	var payload kiroStartRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	method := strings.TrimSpace(payload.Method)
	switch method {
	case kiroLoginMethodBuilderID, kiroLoginMethodGoogle, kiroLoginMethodGithub:
	default:
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("method must be one of %q, %q or %q", kiroLoginMethodBuilderID, kiroLoginMethodGoogle, kiroLoginMethodGithub))
		return
	}

	startCtx, cancel := context.WithTimeout(ctx, kiroUpstreamTimeout)
	auth, err := kiroStartDeviceLogin(startCtx, method)
	cancel()
	if err == nil && auth == nil {
		err = errors.New("no device authorization returned")
	}
	if err != nil {
		SendError(ctx, fasthttp.StatusBadGateway, fmt.Sprintf("Failed to start Kiro device login: %v", err))
		return
	}
	if auth.Method == "" {
		auth.Method = method
	}
	auth.Interval = normalizeKiroPollInterval(auth.Interval)

	sessionID, expiresAt, err := h.kiroSessions.put(kiroLoginSession{
		auth:       auth,
		nextPollAt: h.kiroSessions.now().Add(auth.Interval),
	}, auth.ExpiresAt)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to create login session")
		return
	}
	SendJSON(ctx, kiroStartResponse{
		SessionID:               sessionID,
		UserCode:                auth.UserCode,
		VerificationURI:         auth.VerificationURI,
		VerificationURIComplete: auth.VerificationURIComplete,
		ExpiresAt:               expiresAt.UTC().Format(time.RFC3339),
		IntervalSeconds:         intervalSeconds(auth.Interval),
	})
}

// pollKiroLogin handles POST /api/oauth-subscriptions/kiro/poll. Polls that
// arrive before the session interval has elapsed answer pending without
// contacting upstream.
func (h *OAuthSubscriptionHandler) pollKiroLogin(ctx *fasthttp.RequestCtx) {
	var payload kiroPollRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	sessionID := strings.TrimSpace(payload.SessionID)
	if sessionID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "session_id is required")
		return
	}

	var auth *kiro.DeviceAuthorization
	var interval time.Duration
	throttled := false
	err := h.kiroSessions.update(sessionID, func(entry *oauthSessionEntry[kiroLoginSession], now time.Time) bool {
		interval = entry.value.auth.Interval
		if entry.value.polling || now.Before(entry.value.nextPollAt) {
			throttled = true
			return false
		}
		entry.value.polling = true
		entry.value.nextPollAt = now.Add(interval)
		auth = entry.value.auth
		return false
	})
	if errors.Is(err, errOAuthSessionExpired) {
		SendJSON(ctx, kiroPollResponse{Status: kiroPollStatusExpired, Error: err.Error()})
		return
	}
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if throttled {
		SendJSON(ctx, kiroPollResponse{Status: kiroPollStatusPending, IntervalSeconds: intervalSeconds(interval)})
		return
	}

	pollCtx, cancel := context.WithTimeout(ctx, kiroUpstreamTimeout)
	creds, status, pollErr := kiroPollDeviceLogin(pollCtx, auth)
	cancel()

	response := kiroPollResponse{}
	switch {
	case pollErr != nil:
		response.Status = kiroPollStatusError
		response.Error = fmt.Sprintf("Kiro device login failed: %v", pollErr)
	case status == kiroPollStatusComplete:
		if creds == nil {
			response.Status = kiroPollStatusError
			response.Error = "Kiro device login completed without credentials"
			break
		}
		credential, encodeErr := creds.Encode()
		if encodeErr != nil {
			response.Status = kiroPollStatusError
			response.Error = fmt.Sprintf("Failed to encode Kiro credentials: %v", encodeErr)
			break
		}
		response.Status = kiroPollStatusComplete
		response.Credential = credential
		response.SuggestedName = kiroSuggestedName(auth.Method, sessionID)
	case status == kiroPollStatusPending, status == kiroPollStatusSlowDown:
		response.Status = kiroPollStatusPending
	case status == kiroPollStatusExpired:
		response.Status = kiroPollStatusExpired
		response.Error = "the device code expired before the login was approved"
	default:
		response.Status = kiroPollStatusError
		response.Error = fmt.Sprintf("unexpected Kiro device login status %q", status)
	}

	_ = h.kiroSessions.update(sessionID, func(entry *oauthSessionEntry[kiroLoginSession], now time.Time) bool {
		entry.value.polling = false
		if response.Status != kiroPollStatusPending {
			return true
		}
		if status == kiroPollStatusSlowDown {
			entry.value.auth.Interval += kiroSlowDownIncrement
			entry.value.nextPollAt = now.Add(entry.value.auth.Interval)
		}
		interval = entry.value.auth.Interval
		return false
	})
	response.IntervalSeconds = intervalSeconds(interval)
	SendJSON(ctx, response)
}

// parseAntigravityCallback extracts the authorization code (and state, when
// present) from what the user pasted: the full redirected URL, its query
// string, or the bare code. A pasted URL must carry the state so it can be
// matched against the session; a bare code cannot, and is bound to the
// session through the PKCE verifier alone.
func parseAntigravityCallback(input string) (code string, state string, err error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", errors.New("callback_url is required")
	}
	isURL := strings.Contains(input, "://")
	if !isURL && !strings.HasPrefix(input, "?") && !strings.Contains(input, "code=") {
		if strings.ContainsAny(input, " \t\r\n&") {
			return "", "", errors.New("callback_url is neither a URL nor an authorization code")
		}
		if strings.Contains(input, "%") {
			unescaped, unescapeErr := url.QueryUnescape(input)
			if unescapeErr != nil {
				return "", "", errors.New("authorization code is not valid URL encoding")
			}
			input = unescaped
		}
		return input, "", nil
	}

	rawQuery := input
	if isURL {
		parsed, parseErr := url.Parse(input)
		if parseErr != nil {
			return "", "", errors.New("callback_url is not a valid URL")
		}
		rawQuery = parsed.RawQuery
		if rawQuery == "" {
			rawQuery = parsed.Fragment
		}
	} else if idx := strings.IndexByte(input, '?'); idx >= 0 {
		rawQuery = input[idx+1:]
	}
	values, parseErr := url.ParseQuery(rawQuery)
	if parseErr != nil {
		return "", "", errors.New("callback_url has a malformed query string")
	}
	if oauthErr := values.Get("error"); oauthErr != "" {
		if description := values.Get("error_description"); description != "" {
			return "", "", fmt.Errorf("authorization failed: %s (%s)", oauthErr, description)
		}
		return "", "", fmt.Errorf("authorization failed: %s", oauthErr)
	}
	code = strings.TrimSpace(values.Get("code"))
	if code == "" {
		return "", "", errors.New("callback_url has no code parameter")
	}
	state = values.Get("state")
	if state == "" {
		return "", "", errors.New("callback_url has no state parameter")
	}
	return code, state, nil
}

// nextAntigravityKeyName returns the first antigravity-account-<n> name, n
// counting from one past the existing Antigravity keys, that no key uses yet
// (key names are unique across providers).
func (h *OAuthSubscriptionHandler) nextAntigravityKeyName() string {
	taken := make(map[string]struct{})
	existing := 0
	if h.config != nil {
		h.config.Mu.RLock()
		for provider, providerConfig := range h.config.Providers {
			if provider == schemas.Antigravity {
				existing = len(providerConfig.Keys)
			}
			for _, key := range providerConfig.Keys {
				taken[key.Name] = struct{}{}
			}
		}
		h.config.Mu.RUnlock()
	}
	for n := existing + 1; ; n++ {
		name := "antigravity-account-" + strconv.Itoa(n)
		if _, ok := taken[name]; !ok {
			return name
		}
	}
}

// kiroSuggestedName returns kiro-<method>-<short>, short being a prefix of the
// random session id so successive logins get distinct names.
func kiroSuggestedName(method, sessionID string) string {
	if method == "" {
		method = "login"
	}
	short := sessionID
	if len(short) > 6 {
		short = short[:6]
	}
	return "kiro-" + method + "-" + short
}

func normalizeKiroPollInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return kiroDefaultPollInterval
	}
	if interval < kiroMinPollInterval {
		return kiroMinPollInterval
	}
	return interval
}

// intervalSeconds rounds up so clients never poll sooner than allowed.
func intervalSeconds(interval time.Duration) int {
	return int((interval + time.Second - 1) / time.Second)
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func randomBase64URL(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
