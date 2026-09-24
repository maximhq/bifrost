package sessionauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/cli/internal/browserauth"
	"github.com/maximhq/bifrost/cli/internal/secrets"
)

const agentDeviceProfileID = "installation"

// SessionStore extends the refresh-only store with deletion for login, logout,
// and origin invalidation.
type SessionStore interface {
	Store
	Delete(profileID string, kind secrets.Kind) error
}

// Authenticator owns the profile-scoped lifecycle for Enterprise SSO tokens.
// BrowserAuth performs PKCE and gateway calls; Authenticator persists only the
// opaque Bifrost credentials and the non-secret identity summary.
type Authenticator struct {
	Store     SessionStore
	ProfileID string
	Client    *browserauth.Client
}

// RevocationError means local session removal succeeded but the gateway could
// not be notified. Callers may safely change origins while surfacing a warning.
type RevocationError struct {
	Err error
}

func (e *RevocationError) Error() string {
	return "local session removed; gateway logout failed: " + e.Err.Error()
}

func (e *RevocationError) Unwrap() error {
	return e.Err
}

// SignIn completes browser PKCE and stores the resulting session atomically
// enough to preserve the rotated refresh token across a partial keyring error.
func (a Authenticator) SignIn(ctx context.Context, noBrowser bool) (browserauth.TokenResponse, error) {
	var response browserauth.TokenResponse
	if a.Store == nil || a.Client == nil {
		return response, errors.New("enterprise SSO sign-in is unavailable")
	}
	deviceID, err := EnsureDeviceID(a.Store)
	if err != nil {
		return response, err
	}
	a.Client.HardwareID = deviceID
	response, err = a.Client.SignIn(ctx, noBrowser)
	if err != nil {
		return response, err
	}
	if err := a.storeResponse(response); err != nil {
		return response, err
	}
	return response, nil
}

func (a Authenticator) storeResponse(response browserauth.TokenResponse) error {
	// Store the rotated refresh token first. If a later write fails, the next
	// invocation can still recover instead of retaining a consumed token.
	if err := a.Store.Set(a.ProfileID, secrets.AgentRefreshToken, response.RefreshToken); err != nil {
		return err
	}
	if err := a.Store.Set(a.ProfileID, secrets.AgentToken, response.AccessToken); err != nil {
		return err
	}
	userJSON, err := json.Marshal(response.User)
	if err != nil {
		return err
	}
	if err := a.Store.Set(a.ProfileID, secrets.AgentUser, string(userJSON)); err != nil {
		return err
	}
	if err := a.Store.Delete(a.ProfileID, secrets.AgentVirtualKeyID); err != nil {
		return err
	}
	return nil
}

// Logout attempts gateway revocation and always removes the local agent
// session so a failed network request cannot leave stale credentials active.
func (a Authenticator) Logout(ctx context.Context) error {
	if a.Store == nil {
		return errors.New("enterprise SSO logout is unavailable")
	}
	accessToken, getErr := a.Store.Get(a.ProfileID, secrets.AgentToken)
	var revokeErr error
	if getErr == nil && strings.TrimSpace(accessToken) != "" && a.Client != nil {
		revokeErr = a.Client.Logout(ctx, accessToken)
	}
	clearErr := a.Clear()
	if clearErr != nil {
		return clearErr
	}
	if getErr != nil {
		return &RevocationError{Err: getErr}
	}
	if revokeErr != nil {
		return &RevocationError{Err: revokeErr}
	}
	return nil
}

// Clear removes all profile-scoped Enterprise SSO state without making a
// network request. Virtual and management keys are intentionally preserved.
func (a Authenticator) Clear() error {
	if a.Store == nil {
		return errors.New("enterprise SSO session store is unavailable")
	}
	for _, kind := range []secrets.Kind{
		secrets.AgentToken,
		secrets.AgentRefreshToken,
		secrets.AgentUser,
		secrets.AgentVirtualKeyID,
	} {
		if err := a.Store.Delete(a.ProfileID, kind); err != nil {
			return err
		}
	}
	return nil
}

// EnsureDeviceID returns a stable opaque installation ID without reading a
// hardware or operating-system machine identifier.
func EnsureDeviceID(store Store) (string, error) {
	if store == nil {
		return "", errors.New("enterprise SSO session store is unavailable")
	}
	value, err := store.Get(agentDeviceProfileID, secrets.AgentDeviceID)
	if err != nil {
		return "", err
	}
	if value = strings.TrimSpace(value); value != "" {
		return value, nil
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("create opaque CLI device ID: %w", err)
	}
	value = "cli-" + base64.RawURLEncoding.EncodeToString(random)
	if err := store.Set(agentDeviceProfileID, secrets.AgentDeviceID, value); err != nil {
		return "", err
	}
	return value, nil
}

// UserLabel returns the most useful non-secret display label for an SSO user.
func UserLabel(user browserauth.User) string {
	if email := strings.TrimSpace(user.Email); email != "" {
		return email
	}
	if name := strings.TrimSpace(user.Name); name != "" {
		return name
	}
	return strings.TrimSpace(user.ID)
}

// StoredUserLabel reads the non-secret identity summary saved at sign-in.
func StoredUserLabel(store Store, profileID string) (string, error) {
	if store == nil {
		return "", errors.New("enterprise SSO session store is unavailable")
	}
	value, err := store.Get(profileID, secrets.AgentUser)
	if err != nil || strings.TrimSpace(value) == "" {
		return "", err
	}
	var user browserauth.User
	if err := json.Unmarshal([]byte(value), &user); err != nil {
		return "", fmt.Errorf("decode stored Enterprise SSO identity: %w", err)
	}
	return UserLabel(user), nil
}
