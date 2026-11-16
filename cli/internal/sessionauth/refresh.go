// Package sessionauth coordinates refreshes of Enterprise browser SSO sessions.
package sessionauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/maximhq/bifrost/cli/internal/browserauth"
	"github.com/maximhq/bifrost/cli/internal/config"
	"github.com/maximhq/bifrost/cli/internal/secrets"
)

// Store is the secret-store surface required to rotate an agent session.
type Store interface {
	Get(profileID string, kind secrets.Kind) (string, error)
	Set(profileID string, kind secrets.Kind, value string) error
}

// Refresher safely rotates one profile's access and refresh token pair.
type Refresher struct {
	Store     Store
	ProfileID string
	StatePath string
	Client    *browserauth.Client
}

// Refresh returns a newer access token, reusing a token another process may
// have already rotated while this process waited for the profile lock.
func (r Refresher) Refresh(ctx context.Context, staleToken string) (string, error) {
	if r.Store == nil || r.Client == nil {
		return "", errors.New("enterprise SSO refresh is unavailable")
	}
	staleToken = strings.TrimSpace(staleToken)
	if !strings.HasPrefix(staleToken, secrets.AgentAccessTokenPrefix) {
		return "", errors.New("stored enterprise SSO session is not refreshable")
	}
	lockPath, err := r.lockPath()
	if err != nil {
		return "", err
	}
	var accessToken string
	err = withFileLock(ctx, lockPath, func() error {
		current, getErr := r.Store.Get(r.ProfileID, secrets.AgentToken)
		if getErr != nil {
			return getErr
		}
		if current = strings.TrimSpace(current); current != "" && current != staleToken {
			accessToken = current
			return nil
		}
		refreshToken, getErr := r.Store.Get(r.ProfileID, secrets.AgentRefreshToken)
		if getErr != nil {
			return getErr
		}
		response, refreshErr := r.Client.Refresh(ctx, refreshToken)
		if refreshErr != nil {
			return refreshErr
		}
		// Store the newly rotated refresh token first so a partial keyring
		// failure never leaves the already-consumed token as the recovery path.
		if setErr := r.Store.Set(r.ProfileID, secrets.AgentRefreshToken, response.RefreshToken); setErr != nil {
			return setErr
		}
		if setErr := r.Store.Set(r.ProfileID, secrets.AgentToken, response.AccessToken); setErr != nil {
			return setErr
		}
		accessToken = strings.TrimSpace(response.AccessToken)
		return nil
	})
	if err != nil {
		return "", err
	}
	if accessToken == "" {
		return "", errors.New("token refresh returned an empty access token")
	}
	return accessToken, nil
}

// lockPath returns an owner-only, profile-specific refresh lock without using
// user-controlled profile text as a path component.
func (r Refresher) lockPath() (string, error) {
	statePath := strings.TrimSpace(r.StatePath)
	if statePath == "" {
		var err error
		statePath, err = config.DefaultStatePath()
		if err != nil {
			return "", err
		}
	}
	digest := sha256.Sum256([]byte(r.ProfileID))
	dir := filepath.Join(filepath.Dir(statePath), "locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create session lock directory: %w", err)
	}
	return filepath.Join(dir, "agent-refresh-"+hex.EncodeToString(digest[:8])+".lock"), nil
}
