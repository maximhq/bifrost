// Package copilotsignin implements "Sign in with GitHub" for GitHub Copilot provider keys.
//
// An operator signs in once through GitHub's device flow with their own GitHub App's client
// ID. The resulting user token (ghu_, 8 hours) and refresh token (ghr_, 6 months) are stored
// per key, refreshed in the background, and handed to the Copilot provider as the key value
// at request time. Device-flow tokens refresh without a client secret, so none is stored.
//
// GitHub rotates the refresh token on every use and the old pair stops working at once. The
// database row is therefore authoritative across nodes: one node at a time holds a refresh
// lease, every write is conditional on the row version, and each node re-reads the rows on
// every tick so a refresh done elsewhere reaches it within a minute.
package copilotsignin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

const (
	tickInterval  = time.Minute
	refreshAhead  = 15 * time.Minute // refresh once the access token has less than this left
	leaseDuration = 2 * time.Minute
	orphanGrace   = time.Hour // keeps a sign-in for a key that has not been saved yet
	httpTimeout   = 30 * time.Second
	// statusSignedOut marks a row the operator signed out of. The row is kept rather than
	// deleted so its version keeps moving forward for the next sign-in.
	statusSignedOut = "signed_out"
)

// clientIDPattern accepts GitHub App client IDs (Iv1.0123abcd, Iv23liAbC...).
var clientIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// Store is the slice of configstore.ConfigStore the manager uses.
type Store interface {
	GetGithubCopilotSignIns(ctx context.Context) ([]tables.TableGithubCopilotSignIn, error)
	GetGithubCopilotSignIn(ctx context.Context, keyID string) (*tables.TableGithubCopilotSignIn, error)
	ReplaceGithubCopilotSignIn(ctx context.Context, row *tables.TableGithubCopilotSignIn) error
	UpdateGithubCopilotSignInIfVersion(ctx context.Context, row *tables.TableGithubCopilotSignIn, expectedVersion int64) (bool, error)
	ClaimGithubCopilotSignInRefresh(ctx context.Context, keyID string, version int64, leaseUntil time.Time) (bool, error)
	DeleteGithubCopilotSignIn(ctx context.Context, keyID string) error
}

// PollResult is the outcome of one device-flow poll.
type PollResult struct {
	Status   string `json:"status"`             // pending, complete, expired or denied
	Interval int    `json:"interval,omitempty"` // seconds to wait before the next poll
	Login    string `json:"login,omitempty"`
}

// Status describes a key's sign-in for the UI.
type Status struct {
	SignedIn         bool       `json:"signed_in"`
	Status           string     `json:"status,omitempty"`
	StatusReason     string     `json:"status_reason,omitempty"`
	Login            string     `json:"login,omitempty"`
	AccessExpiresAt  *time.Time `json:"access_expires_at,omitempty"`
	RefreshExpiresAt *time.Time `json:"refresh_expires_at,omitempty"`
}

// cachedToken is the per-key state the request path reads.
type cachedToken struct {
	accessToken string
	expiresAt   time.Time // zero when the token does not expire
	version     int64
	active      bool
}

// Manager owns the sign-in tokens for every GitHub Copilot key.
type Manager struct {
	store     Store
	logger    schemas.Logger
	keyExists func(keyID string) bool
	client    *http.Client
	endpoints endpoints
	now       func() time.Time

	mu     sync.RWMutex
	tokens map[string]cachedToken
}

// New builds a manager. keyExists reports whether a GitHub Copilot key with that ID is saved;
// sign-ins for keys that stay unsaved past a grace period are deleted.
func New(store Store, logger schemas.Logger, keyExists func(keyID string) bool) *Manager {
	return &Manager{
		store:     store,
		logger:    logger,
		keyExists: keyExists,
		client:    &http.Client{Timeout: httpTimeout},
		endpoints: githubEndpoints,
		now:       time.Now,
		tokens:    make(map[string]cachedToken),
	}
}

// Start loads the stored tokens, so requests can use them immediately, then refreshes them
// in the background until ctx is cancelled.
func (m *Manager) Start(ctx context.Context) {
	m.load(ctx)
	go func() {
		m.tick(ctx)
		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.tick(ctx)
			}
		}
	}()
}

// AccessToken returns the current user token for a key. It is on the request path: a read
// lock and a map lookup, no allocation and no I/O.
func (m *Manager) AccessToken(keyID string) (string, bool) {
	m.mu.RLock()
	entry, ok := m.tokens[keyID]
	m.mu.RUnlock()
	if !ok || !entry.active {
		return "", false
	}
	if !entry.expiresAt.IsZero() && !m.now().Before(entry.expiresAt) {
		return "", false
	}
	return entry.accessToken, true
}

// StartSignIn asks GitHub for a device code. The operator enters the user code at the
// verification URI, and the caller polls with the device code.
func (m *Manager) StartSignIn(ctx context.Context, clientID string) (*DeviceCode, error) {
	if !clientIDPattern.MatchString(clientID) {
		return nil, fmt.Errorf("client ID is not a valid GitHub App client ID")
	}
	var code DeviceCode
	err := postForm(ctx, m.client, m.endpoints.deviceCodeURL, url.Values{"client_id": {clientID}}, &code)
	if err != nil {
		return nil, describeGithubError(err)
	}
	if code.DeviceCode == "" || code.UserCode == "" {
		return nil, fmt.Errorf("GitHub did not return a device code")
	}
	return &code, nil
}

// PollSignIn checks once whether the operator has finished signing in. On success the token
// is stored for keyID, replacing any earlier sign-in.
func (m *Manager) PollSignIn(ctx context.Context, keyID, clientID, deviceCode string) (*PollResult, error) {
	if !clientIDPattern.MatchString(clientID) {
		return nil, fmt.Errorf("client ID is not a valid GitHub App client ID")
	}
	if deviceCode == "" {
		return nil, fmt.Errorf("device_code is required")
	}
	var resp githubTokenResponse
	err := postForm(ctx, m.client, m.endpoints.tokenURL, url.Values{
		"client_id":   {clientID},
		"device_code": {deviceCode},
		"grant_type":  {deviceCodeGrantType},
	}, &resp)
	var oauthErr *githubOAuthError
	if errors.As(err, &oauthErr) {
		switch oauthErr.Code {
		case "authorization_pending":
			return &PollResult{Status: "pending"}, nil
		case "slow_down":
			return &PollResult{Status: "pending", Interval: oauthErr.Interval}, nil
		case "expired_token":
			return &PollResult{Status: "expired"}, nil
		case "access_denied":
			return &PollResult{Status: "denied"}, nil
		}
	}
	if err != nil {
		return nil, describeGithubError(err)
	}
	if resp.AccessToken == "" {
		return nil, fmt.Errorf("GitHub did not return an access token")
	}

	now := m.now().UTC()
	row := &tables.TableGithubCopilotSignIn{
		KeyID:       keyID,
		ClientID:    clientID,
		GithubLogin: lookupLogin(ctx, m.client, m.endpoints.userURL, resp.AccessToken),
		Status:      tables.GithubCopilotSignInActive,
	}
	applyTokens(row, &resp, now)
	if err := m.store.ReplaceGithubCopilotSignIn(ctx, row); err != nil {
		return nil, err
	}
	m.cache(row)
	return &PollResult{Status: "complete", Login: row.GithubLogin}, nil
}

// Status reports a key's sign-in. It reads the store, so it is for the UI, not requests.
func (m *Manager) Status(ctx context.Context, keyID string) (*Status, error) {
	row, err := m.store.GetGithubCopilotSignIn(ctx, keyID)
	if err != nil {
		return nil, err
	}
	if row == nil || row.Status == statusSignedOut {
		return &Status{}, nil
	}
	return &Status{
		SignedIn:         row.Status == tables.GithubCopilotSignInActive,
		Status:           row.Status,
		StatusReason:     row.StatusReason,
		Login:            row.GithubLogin,
		AccessExpiresAt:  row.AccessExpiresAt,
		RefreshExpiresAt: row.RefreshExpiresAt,
	}, nil
}

// SignOut forgets a key's tokens. GitHub only lets the App owner revoke them (it needs the
// client secret), so the token stays valid on GitHub until it expires or the user revokes it.
func (m *Manager) SignOut(ctx context.Context, keyID string) error {
	row, err := m.store.GetGithubCopilotSignIn(ctx, keyID)
	if err != nil || row == nil {
		return err
	}
	cleared := &tables.TableGithubCopilotSignIn{KeyID: keyID, ClientID: row.ClientID, Status: statusSignedOut}
	if err := m.store.ReplaceGithubCopilotSignIn(ctx, cleared); err != nil {
		return err
	}
	m.cache(cleared)
	return nil
}

// load fills the cache from the store without refreshing anything.
func (m *Manager) load(ctx context.Context) {
	rows, err := m.store.GetGithubCopilotSignIns(ctx)
	if err != nil {
		m.logger.Warn("[copilot-sign-in] failed to load sign-ins: %v", err)
		return
	}
	for i := range rows {
		m.cache(&rows[i])
	}
}

// tick re-reads every row, refreshes tokens close to expiry, and drops sign-ins whose key
// was deleted or never saved.
func (m *Manager) tick(ctx context.Context) {
	rows, err := m.store.GetGithubCopilotSignIns(ctx)
	if err != nil {
		m.logger.Warn("[copilot-sign-in] failed to load sign-ins: %v", err)
		return
	}
	now := m.now().UTC()
	present := make(map[string]struct{}, len(rows))
	for i := range rows {
		row := &rows[i]
		if !m.keyExists(row.KeyID) {
			if now.Sub(row.UpdatedAt) > orphanGrace {
				if err := m.store.DeleteGithubCopilotSignIn(ctx, row.KeyID); err != nil {
					m.logger.Warn("[copilot-sign-in] failed to delete sign-in for removed key %s: %v", row.KeyID, err)
				}
				continue
			}
		} else if row.Status == tables.GithubCopilotSignInActive && dueForRefresh(row, now) {
			row = m.refresh(ctx, row, now)
		}
		present[row.KeyID] = struct{}{}
		m.cache(row)
	}
	m.mu.Lock()
	for keyID := range m.tokens {
		if _, ok := present[keyID]; !ok {
			delete(m.tokens, keyID)
		}
	}
	m.mu.Unlock()
}

// dueForRefresh reports whether a row's access token is close enough to expiry to act on.
func dueForRefresh(row *tables.TableGithubCopilotSignIn, now time.Time) bool {
	return row.AccessExpiresAt != nil && row.AccessExpiresAt.Sub(now) < refreshAhead
}

// refresh redeems a row's refresh token under the refresh lease and returns the row to cache.
// When another node holds the lease, or the write loses to a newer one, it returns the row
// unchanged and the next tick picks up whatever was stored.
func (m *Manager) refresh(ctx context.Context, row *tables.TableGithubCopilotSignIn, now time.Time) *tables.TableGithubCopilotSignIn {
	next := *row
	switch {
	case row.RefreshToken == "":
		if row.AccessExpiresAt.After(now) {
			return row
		}
		next.Status = tables.GithubCopilotSignInNeedsReauth
		next.StatusReason = "the GitHub token expired and GitHub issued no refresh token"
	case row.RefreshExpiresAt != nil && !row.RefreshExpiresAt.After(now):
		next.Status = tables.GithubCopilotSignInNeedsReauth
		next.StatusReason = "the GitHub refresh token expired"
	default:
		claimed, err := m.store.ClaimGithubCopilotSignInRefresh(ctx, row.KeyID, row.Version, now.Add(leaseDuration))
		if err != nil {
			m.logger.Warn("[copilot-sign-in] failed to claim refresh for key %s: %v", row.KeyID, err)
			return row
		}
		if !claimed {
			return row
		}
		var resp githubTokenResponse
		err = postForm(ctx, m.client, m.endpoints.tokenURL, url.Values{
			"client_id":     {row.ClientID},
			"grant_type":    {"refresh_token"},
			"refresh_token": {row.RefreshToken},
		}, &resp)
		var oauthErr *githubOAuthError
		switch {
		case errors.As(err, &oauthErr):
			next.Status = tables.GithubCopilotSignInNeedsReauth
			next.StatusReason = "GitHub rejected the refresh token: " + oauthErr.Error()
		case err != nil || resp.AccessToken == "":
			// A network failure leaves the old pair valid. The lease lapses and a later
			// tick retries while the access token still has time left.
			m.logger.Warn("[copilot-sign-in] refresh for key %s failed, will retry: %v", row.KeyID, err)
			return row
		default:
			applyTokens(&next, &resp, now)
			next.Status = tables.GithubCopilotSignInActive
			next.StatusReason = ""
		}
	}
	if next.Status != tables.GithubCopilotSignInActive {
		m.logger.Warn("[copilot-sign-in] key %s needs a new sign-in: %s", row.KeyID, next.StatusReason)
	}
	return m.persist(ctx, &next, row.Version)
}

// persist writes a refresh result. GitHub has already retired the old pair, so a failed
// write would strand the key; it is retried briefly before giving up.
func (m *Manager) persist(ctx context.Context, next *tables.TableGithubCopilotSignIn, expectedVersion int64) *tables.TableGithubCopilotSignIn {
	var err error
	for attempt, delay := range []time.Duration{0, 200 * time.Millisecond, time.Second} {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return next
			case <-time.After(delay):
			}
		}
		var updated bool
		updated, err = m.store.UpdateGithubCopilotSignInIfVersion(ctx, next, expectedVersion)
		if err == nil {
			if !updated {
				// A new sign-in or another node's write landed first; theirs is the live one.
				fresh, getErr := m.store.GetGithubCopilotSignIn(ctx, next.KeyID)
				if getErr == nil && fresh != nil {
					return fresh
				}
			}
			return next
		}
	}
	m.logger.Error("[copilot-sign-in] could not store the refreshed token for key %s; it will need a new sign-in after a restart: %v", next.KeyID, err)
	return next
}

// cache stores a row's token for the request path. An older version never replaces a newer
// one, so a tick that read the store before a sign-in completed cannot undo it.
func (m *Manager) cache(row *tables.TableGithubCopilotSignIn) {
	entry := cachedToken{
		accessToken: row.AccessToken,
		version:     row.Version,
		active:      row.Status == tables.GithubCopilotSignInActive && row.AccessToken != "",
	}
	if row.AccessExpiresAt != nil {
		entry.expiresAt = *row.AccessExpiresAt
	}
	m.mu.Lock()
	if existing, ok := m.tokens[row.KeyID]; !ok || existing.version <= entry.version {
		m.tokens[row.KeyID] = entry
	}
	m.mu.Unlock()
}

// applyTokens copies a token response onto a row. GitHub omits the expiry fields when the
// App does not expire user tokens.
func applyTokens(row *tables.TableGithubCopilotSignIn, resp *githubTokenResponse, now time.Time) {
	row.AccessToken = resp.AccessToken
	row.RefreshToken = resp.RefreshToken
	row.AccessExpiresAt = nil
	row.RefreshExpiresAt = nil
	if resp.ExpiresIn > 0 {
		t := now.Add(time.Duration(resp.ExpiresIn) * time.Second)
		row.AccessExpiresAt = &t
	}
	if resp.RefreshTokenExpiresIn > 0 {
		t := now.Add(time.Duration(resp.RefreshTokenExpiresIn) * time.Second)
		row.RefreshExpiresAt = &t
	}
}

// describeGithubError turns the GitHub errors an operator can fix into instructions.
func describeGithubError(err error) error {
	var oauthErr *githubOAuthError
	if !errors.As(err, &oauthErr) {
		return err
	}
	switch oauthErr.Code {
	case "device_flow_disabled":
		return fmt.Errorf("device flow is not enabled for this GitHub App; turn on \"Enable Device Flow\" in the App's settings")
	case "incorrect_client_credentials":
		return fmt.Errorf("GitHub does not recognize this client ID; copy the Client ID from the GitHub App's settings page")
	case "incorrect_device_code":
		return fmt.Errorf("GitHub does not recognize this sign-in attempt; start the sign-in again")
	default:
		return fmt.Errorf("GitHub rejected the sign-in: %s", oauthErr.Error())
	}
}
