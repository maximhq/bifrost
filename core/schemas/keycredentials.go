package schemas

import "context"

// KeyCredentialUpdater persists a credential that a provider refreshed on its own,
// such as a rotated OAuth refresh token of a subscription provider (Kiro, Antigravity).
// value is the complete new Key.Value for the key identified by keyID. Providers call
// it only when the stored credential would otherwise go stale; a nil updater keeps
// the refreshed credential in memory for the life of the process only.
type KeyCredentialUpdater func(ctx context.Context, provider ModelProvider, keyID string, value string) error

// KeyCredentialStore is an optional interface for an Account whose keys can be written
// back. When BifrostConfig.Account implements it, OAuth subscription providers persist
// refreshed credentials through UpdateKeyCredential (see KeyCredentialUpdater for the
// contract); otherwise refreshed credentials live in memory only.
type KeyCredentialStore interface {
	UpdateKeyCredential(ctx context.Context, provider ModelProvider, keyID string, value string) error
}
