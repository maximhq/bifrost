package configstore

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// ─── SSO Provider CRUD ────────────────────────────────────────────────────────

// CreateSSOProvider inserts a new SSO provider.
func (s *RDBConfigStore) CreateSSOProvider(ctx context.Context, provider *tables.TableSSOProvider) error {
	if provider.ID == "" {
		provider.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	provider.CreatedAt = now
	provider.UpdatedAt = now
	if err := s.db.WithContext(ctx).Create(provider).Error; err != nil {
		return fmt.Errorf("create SSO provider: %w", err)
	}
	return nil
}

// GetSSOProvider returns a provider by ID.
func (s *RDBConfigStore) GetSSOProvider(ctx context.Context, id string) (*tables.TableSSOProvider, error) {
	var p tables.TableSSOProvider
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&p).Error; err != nil {
		return nil, fmt.Errorf("get SSO provider %s: %w", id, err)
	}
	return &p, nil
}

// ListSSOProviders returns all SSO providers.
func (s *RDBConfigStore) ListSSOProviders(ctx context.Context) ([]tables.TableSSOProvider, error) {
	var providers []tables.TableSSOProvider
	if err := s.db.WithContext(ctx).Find(&providers).Error; err != nil {
		return nil, fmt.Errorf("list SSO providers: %w", err)
	}
	return providers, nil
}

// UpdateSSOProvider applies a partial update to a provider.
func (s *RDBConfigStore) UpdateSSOProvider(ctx context.Context, id string, updates map[string]any) error {
	updates["updated_at"] = time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&tables.TableSSOProvider{}).
		Where("id = ?", id).Updates(updates).Error; err != nil {
		return fmt.Errorf("update SSO provider %s: %w", id, err)
	}
	return nil
}

// DeleteSSOProvider deletes a provider by ID.
func (s *RDBConfigStore) DeleteSSOProvider(ctx context.Context, id string) error {
	if err := s.db.WithContext(ctx).Where("id = ?", id).Delete(&tables.TableSSOProvider{}).Error; err != nil {
		return fmt.Errorf("delete SSO provider %s: %w", id, err)
	}
	return nil
}

// ─── External User CRUD ───────────────────────────────────────────────────────

// UpsertExternalUser creates or updates a user record keyed on ExternalID.
// Called on every SSO login and SCIM provisioning event.
func (s *RDBConfigStore) UpsertExternalUser(ctx context.Context, user *tables.TableExternalUser) (*tables.TableExternalUser, error) {
	if user.ID == "" {
		user.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	user.UpdatedAt = now

	var existing tables.TableExternalUser
	err := s.db.WithContext(ctx).
		Where("external_id = ?", user.ExternalID).
		First(&existing).Error
	if err == nil {
		// Update existing record
		user.ID = existing.ID
		user.CreatedAt = existing.CreatedAt
		user.SCIMVersion = existing.SCIMVersion + 1
		if err2 := s.db.WithContext(ctx).Save(user).Error; err2 != nil {
			return nil, fmt.Errorf("update external user: %w", err2)
		}
		return user, nil
	}
	// Create new
	user.CreatedAt = now
	user.SCIMVersion = 1
	if err2 := s.db.WithContext(ctx).Create(user).Error; err2 != nil {
		return nil, fmt.Errorf("create external user: %w", err2)
	}
	return user, nil
}

// GetExternalUser returns a user by internal ID.
func (s *RDBConfigStore) GetExternalUser(ctx context.Context, id string) (*tables.TableExternalUser, error) {
	var u tables.TableExternalUser
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&u).Error; err != nil {
		return nil, fmt.Errorf("get external user %s: %w", id, err)
	}
	return &u, nil
}

// FindExternalUserByEmail returns a user by email address.
func (s *RDBConfigStore) FindExternalUserByEmail(ctx context.Context, email string) (*tables.TableExternalUser, error) {
	var u tables.TableExternalUser
	if err := s.db.WithContext(ctx).Where("email = ?", email).First(&u).Error; err != nil {
		return nil, fmt.Errorf("find external user by email %s: %w", email, err)
	}
	return &u, nil
}

// ListExternalUsers returns all external users for a provider.
func (s *RDBConfigStore) ListExternalUsers(ctx context.Context, providerID string) ([]tables.TableExternalUser, error) {
	q := s.db.WithContext(ctx)
	if providerID != "" {
		q = q.Where("provider_id = ?", providerID)
	}
	var users []tables.TableExternalUser
	if err := q.Find(&users).Error; err != nil {
		return nil, fmt.Errorf("list external users: %w", err)
	}
	return users, nil
}

// DeactivateExternalUser sets active=false for a user.
func (s *RDBConfigStore) DeactivateExternalUser(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Model(&tables.TableExternalUser{}).
		Where("id = ?", id).
		Updates(map[string]any{"active": false, "updated_at": time.Now().UTC()}).Error
}

// ─── SSO Session CRUD ─────────────────────────────────────────────────────────

// CreateSSOSession stores a new SSO session.
func (s *RDBConfigStore) CreateSSOSession(ctx context.Context, sess *tables.TableSSOSession) error {
	if sess.ID == "" {
		sess.ID = uuid.New().String()
	}
	sess.CreatedAt = time.Now().UTC()
	if err := s.db.WithContext(ctx).Create(sess).Error; err != nil {
		return fmt.Errorf("create SSO session: %w", err)
	}
	return nil
}

// DeleteSSOSession invalidates a session.
func (s *RDBConfigStore) DeleteSSOSession(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Where("id = ?", id).Delete(&tables.TableSSOSession{}).Error
}

// CleanExpiredSSOSessions removes sessions that have passed their ExpiresAt.
func (s *RDBConfigStore) CleanExpiredSSOSessions(ctx context.Context) (int64, error) {
	result := s.db.WithContext(ctx).
		Where("expires_at < ?", time.Now().UTC()).
		Delete(&tables.TableSSOSession{})
	return result.RowsAffected, result.Error
}
