package lib

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

// credentialPersistTimeout bounds the store write of a refreshed credential.
const credentialPersistTimeout = 10 * time.Second

// ErrCredentialFromEnv is returned when a refreshed credential cannot be written back
// because the key reads its value from an environment variable.
var ErrCredentialFromEnv = errors.New("refreshed credential not persisted")

var _ schemas.KeyCredentialUpdater = (*Config)(nil).UpdateProviderKeyCredential

// BaseAccount implements schemas.KeyCredentialStore, so core hands refreshed OAuth
// credentials of subscription providers back to the config store.
var _ schemas.KeyCredentialStore = (*BaseAccount)(nil)

// UpdateKeyCredential persists a refreshed credential through the account's config.
func (baseAccount *BaseAccount) UpdateKeyCredential(ctx context.Context, provider schemas.ModelProvider, keyID string, value string) error {
	if baseAccount.store == nil {
		return errors.New("store not initialized")
	}
	return baseAccount.store.UpdateProviderKeyCredential(ctx, provider, keyID, value)
}

// UpdateProviderKeyCredential persists a credential that a provider refreshed
// on its own (a rotated OAuth refresh token, say) as the key's new value. It
// implements schemas.KeyCredentialUpdater.
//
// Only the value changes: every other key field, the config.json hash
// included, is kept, so a config.json-declared key is not reported as drifted
// on the next boot. The write deliberately skips the provider rebuild and model
// rediscovery a key edit triggers. Providers read keys through the account on
// every request, so the new value is live as soon as the in-memory config is
// swapped, and rebuilding the provider from inside one of its own requests
// would only drain its queue.
//
// Providers call this from request goroutines, often while the caller has
// already given up, so the store write is detached from ctx cancellation: the
// old credential is typically revoked upstream by the time this runs, and
// losing the new one would lock the key out after a restart.
func (c *Config) UpdateProviderKeyCredential(ctx context.Context, provider schemas.ModelProvider, keyID string, value string) error {
	if keyID == "" {
		return errors.New("key id is required")
	}
	if value == "" {
		return errors.New("credential value must not be empty")
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), credentialPersistTimeout)
	defer cancel()

	// Held across the store write, as in UpdateProviderKey: a concurrent key
	// edit must not interleave between the write and the in-memory swap.
	c.Mu.Lock()
	defer c.Mu.Unlock()

	existingConfig, exists := c.Providers[provider]
	if !exists {
		return ErrNotFound
	}
	index := slices.IndexFunc(existingConfig.Keys, func(key schemas.Key) bool {
		return key.ID == keyID
	})
	if index == -1 {
		return ErrNotFound
	}
	current := existingConfig.Keys[index]
	// An env-backed value belongs to whoever sets the variable: overwriting it would
	// silently turn the reference into a stored literal. The provider keeps the refreshed
	// credential in memory; the operator must update the variable before a restart.
	if current.Value.IsFromEnv() {
		return fmt.Errorf("%w: key %s of provider %s reads its credential from environment variable %s; update it with the refreshed credential before restarting", ErrCredentialFromEnv, keyID, provider, current.Value.EnvKey())
	}
	if !current.Value.IsFromSecret() && current.Value.GetValue() == value {
		return nil
	}

	updatedKey := current
	updatedKey.Value = schemas.SecretVar{Val: value, SecretType: schemas.SecretTypePlainText}

	if c.ConfigStore != nil {
		if err := c.ConfigStore.UpdateProviderKey(persistCtx, provider, keyID, updatedKey); err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				return ErrNotFound
			}
			return fmt.Errorf("failed to persist refreshed credential: %w", err)
		}
		// The vault store callback may rewrite the secret into a vault
		// reference on the stored row only; re-read so memory matches.
		storedKey, err := c.ConfigStore.GetProviderKey(persistCtx, provider, keyID)
		if err != nil {
			return fmt.Errorf("failed to re-read provider key after credential refresh: %w", err)
		}
		updatedKey = *storedKey
	}

	updatedConfig := existingConfig
	updatedConfig.Keys = append([]schemas.Key(nil), existingConfig.Keys...)
	updatedConfig.Keys[index] = updatedKey
	c.Providers[provider] = updatedConfig

	logger.Debug("Persisted refreshed credential for key %s of provider %s", keyID, provider)
	return nil
}
