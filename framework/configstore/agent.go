package configstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// registrationToTable flattens the domain credential structs into the columns the
// registration row actually stores. The mode column is derived from credential
// presence rather than supplied by callers, so "none" and "gateway_managed" can
// never disagree with whether a secret was persisted.
func cloneUpstreamAuth(auth *schemas.UpstreamAuth) *schemas.UpstreamAuth {
	if auth == nil {
		return nil
	}
	clone := *auth
	clone.Headers = make(map[string]schemas.SecretVar, len(auth.Headers))
	for name, value := range auth.Headers {
		clone.Headers[name] = *value.Clone()
	}
	clone.SecuritySchemes = slices.Clone(auth.SecuritySchemes)
	if auth.OAuth != nil {
		oauth := *auth.OAuth
		oauth.ClientID = auth.OAuth.ClientID.Clone()
		oauth.ClientSecret = auth.OAuth.ClientSecret.Clone()
		oauth.Scopes = slices.Clone(auth.OAuth.Scopes)
		clone.OAuth = &oauth
	}
	if auth.TLS != nil {
		tlsConfig := *auth.TLS
		tlsConfig.CACertPEM = auth.TLS.CACertPEM.Clone()
		tlsConfig.ClientCertPEM = auth.TLS.ClientCertPEM.Clone()
		tlsConfig.ClientKeyPEM = auth.TLS.ClientKeyPEM.Clone()
		clone.TLS = &tlsConfig
	}
	return &clone
}

func registrationToTable(r *schemas.AgentRegistration) tables.TableAgentRegistration {
	return tables.TableAgentRegistration{Name: r.Name, AgentCardURL: r.AgentCardURL, Tenant: r.Tenant, Enabled: r.Enabled, AllowByDefault: r.AllowByDefault, ForwardAcceptedCredential: r.ForwardAcceptedCredential, ForwardAcceptedCredentialOverridesAuth: r.ForwardAcceptedCredentialOverridesAuth, DiscoveryAuth: cloneUpstreamAuth(r.DiscoveryAuth), ExtensionURIs: slices.Clone(r.ExtensionURIs), RuntimeAuth: cloneUpstreamAuth(r.RuntimeAuth), ConfigHash: r.ConfigHash, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// tableToRegistration rebuilds the domain registration from its row and its grant
// rows. Grants are sorted here so the runtime can binary-search authorization
// without re-sorting, and a credential is reconstructed only when a usable secret
// exists.
func tableToRegistration(r tables.TableAgentRegistration, grants []string) schemas.AgentRegistration {
	sort.Strings(grants)
	return schemas.AgentRegistration{Name: r.Name, AgentCardURL: r.AgentCardURL, Tenant: r.Tenant, Enabled: r.Enabled, AllowByDefault: r.AllowByDefault, ForwardAcceptedCredential: r.ForwardAcceptedCredential, ForwardAcceptedCredentialOverridesAuth: r.ForwardAcceptedCredentialOverridesAuth, DiscoveryAuth: r.DiscoveryAuth, RuntimeAuth: r.RuntimeAuth, ExtensionURIs: slices.Clone(r.ExtensionURIs), VirtualKeyIDs: grants, ConfigHash: r.ConfigHash, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// missingFrom returns the requested items that are absent from found, preserving
// the order of requested.
func missingFrom(requested, found []string) []string {
	foundSet := make(map[string]struct{}, len(found))
	for _, item := range found {
		foundSet[item] = struct{}{}
	}
	missing := make([]string, 0, len(requested)-len(found))
	for _, item := range requested {
		if _, ok := foundSet[item]; !ok {
			missing = append(missing, item)
		}
	}
	return missing
}

// validateAgentVirtualKeyIDs rejects grants that reference nonexistent virtual keys
// before any row is written, so a registration cannot be created or updated with
// unreachable grants. It runs inside the caller's transaction and names every
// missing ID so the API can return an actionable error.
func validateAgentVirtualKeyIDs(ctx context.Context, tx *gorm.DB, virtualKeyIDs []string) error {
	if len(virtualKeyIDs) == 0 {
		return nil
	}
	ids := slices.Clone(virtualKeyIDs)
	sort.Strings(ids)
	for i, id := range ids {
		if i > 0 && ids[i-1] == id {
			return errors.New("duplicate virtual key ID: " + id)
		}
	}
	var found []string
	if err := tx.WithContext(ctx).Model(&tables.TableVirtualKey{}).Where("id IN ?", ids).Order("id ASC").Pluck("id", &found).Error; err != nil {
		return err
	}
	if len(found) == len(ids) {
		return nil
	}
	return errors.New("virtual key IDs were not found: " + strings.Join(missingFrom(ids, found), ", "))
}

// CreateAgentRegistration persists a registration and its direct virtual-key grants
// in one transaction, so a caller never observes an agent without its intended
// grants or grants without their agent. Referenced virtual keys are validated first.
func (s *RDBConfigStore) CreateAgentRegistration(ctx context.Context, registration *schemas.AgentRegistration) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateAgentVirtualKeyIDs(ctx, tx, registration.VirtualKeyIDs); err != nil {
			return err
		}
		// Check for an existing registration explicitly so callers get a clear,
		// backend-agnostic conflict error instead of a masked driver duplicate-key error.
		var count int64
		if err := tx.WithContext(ctx).Model(&tables.TableAgentRegistration{}).Where("name = ?", registration.Name).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errors.New("agent with name '" + registration.Name + "' already exists")
		}
		row := registrationToTable(registration)
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		for _, vkID := range registration.VirtualKeyIDs {
			if err := tx.Create(&tables.TableVirtualKeyAgentGrant{VirtualKeyID: vkID, AgentName: registration.Name, CreatedAt: registration.CreatedAt}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// UpdateAgentRegistration replaces a registration and its grants transactionally.
// The existing row is locked for update so concurrent writers cannot interleave,
// the immutable creation time is carried forward, and grants owned by this agent are
// deleted and rewritten so the request's list becomes the exact effective set.
func (s *RDBConfigStore) UpdateAgentRegistration(ctx context.Context, registration *schemas.AgentRegistration) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing tables.TableAgentRegistration
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&existing, "name = ?", registration.Name).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		row := registrationToTable(registration)
		row.CreatedAt = existing.CreatedAt
		row.EncryptionStatus = existing.EncryptionStatus
		if err := validateAgentVirtualKeyIDs(ctx, tx, registration.VirtualKeyIDs); err != nil {
			return err
		}
		// ConfigHash is written only when the caller supplies one (the config.json
		// sync). API updates carry an empty hash and must not clear the stored
		// checkpoint: leaving it stale is what lets split-mode reconciliation keep
		// UI/API edits when the file itself has not changed.
		fields := []string{"agent_card_url", "tenant", "enabled", "allow_by_default", "forward_accepted_credential", "forward_accepted_credential_overrides_auth", "discovery_auth", "extension_uris", "runtime_auth", "encryption_status", "updated_at"}
		if registration.ConfigHash != "" {
			fields = append(fields, "config_hash")
		}
		if err := tx.Model(&row).Select(fields).Updates(&row).Error; err != nil {
			return err
		}
		if err := tx.Where("agent_name = ?", registration.Name).Delete(&tables.TableVirtualKeyAgentGrant{}).Error; err != nil {
			return err
		}
		for _, vkID := range registration.VirtualKeyIDs {
			if err := tx.Create(&tables.TableVirtualKeyAgentGrant{VirtualKeyID: vkID, AgentName: registration.Name, CreatedAt: registration.UpdatedAt}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ListAgentRegistrations loads every registration plus all grants in two ordered
// queries and joins them in memory, avoiding a per-agent grant query during startup
// reload. Deterministic ordering keeps reload and API output stable.
func (s *RDBConfigStore) ListAgentRegistrations(ctx context.Context) ([]schemas.AgentRegistration, error) {
	var rows []tables.TableAgentRegistration
	if err := s.DB().WithContext(ctx).Order("created_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	var grants []tables.TableVirtualKeyAgentGrant
	if err := s.DB().WithContext(ctx).Order("agent_name ASC, virtual_key_id ASC").Find(&grants).Error; err != nil {
		return nil, err
	}
	byAgent := make(map[string][]string)
	for _, grant := range grants {
		byAgent[grant.AgentName] = append(byAgent[grant.AgentName], grant.VirtualKeyID)
	}
	result := make([]schemas.AgentRegistration, 0, len(rows))
	for _, row := range rows {
		result = append(result, tableToRegistration(row, byAgent[row.Name]))
	}
	return result, nil
}

// GetAgentRegistration loads one registration with its grants, translating a missing
// row into ErrNotFound so handlers can map it to a 404 without inspecting GORM errors.
func (s *RDBConfigStore) GetAgentRegistration(ctx context.Context, name string) (*schemas.AgentRegistration, error) {
	var row tables.TableAgentRegistration
	if err := s.DB().WithContext(ctx).First(&row, "name = ?", name).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var grants []string
	if err := s.DB().WithContext(ctx).Model(&tables.TableVirtualKeyAgentGrant{}).Where("agent_name = ?", name).Order("virtual_key_id ASC").Pluck("virtual_key_id", &grants).Error; err != nil {
		return nil, err
	}
	result := tableToRegistration(row, grants)
	return &result, nil
}

// ReplaceVirtualKeyAgentGrants writes the same join table from the virtual-key side,
// so grants can be managed from either the agent APIs or the virtual-key APIs with
// identical results. It accepts an optional caller transaction so a virtual-key
// create/update can commit its grants atomically with the rest of that request, and
// otherwise opens its own. The virtual key row is locked for update, names are
// sorted and checked for empties, duplicates, and unknown agents before any write,
// and only the grants owned by this virtual key are replaced: an empty list clears
// them all, while omitting the field is the caller's decision not to call this at all.
func (s *RDBConfigStore) ReplaceVirtualKeyAgentGrants(ctx context.Context, virtualKeyID string, agentNames []string, tx ...*gorm.DB) error {
	txDB := s.DB()
	if len(tx) > 0 && tx[0] != nil {
		txDB = tx[0]
	} else {
		return txDB.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
			return s.ReplaceVirtualKeyAgentGrants(ctx, virtualKeyID, agentNames, transaction)
		})
	}

	var virtualKey tables.TableVirtualKey
	if err := txDB.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&virtualKey, "id = ?", virtualKeyID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}

	names := slices.Clone(agentNames)
	sort.Strings(names)
	for i, name := range names {
		if name == "" {
			return fmt.Errorf("%w: agent name cannot be empty", ErrInvalidAgentGrant)
		}
		if i > 0 && names[i-1] == name {
			return fmt.Errorf("%w: duplicate agent name: %s", ErrInvalidAgentGrant, name)
		}
	}
	if len(names) > 0 {
		var found []string
		if err := txDB.WithContext(ctx).Model(&tables.TableAgentRegistration{}).Where("name IN ?", names).Order("name ASC").Pluck("name", &found).Error; err != nil {
			return err
		}
		if len(found) != len(names) {
			return fmt.Errorf("%w: agent names were not found: %s", ErrInvalidAgentGrant, strings.Join(missingFrom(names, found), ", "))
		}
	}
	if err := txDB.WithContext(ctx).Where("virtual_key_id = ?", virtualKeyID).Delete(&tables.TableVirtualKeyAgentGrant{}).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, name := range names {
		if err := txDB.WithContext(ctx).Create(&tables.TableVirtualKeyAgentGrant{VirtualKeyID: virtualKeyID, AgentName: name, CreatedAt: now}).Error; err != nil {
			return err
		}
	}
	return nil
}

// DeleteAgentRegistration removes the agent's grants and then the registration in one
// transaction, so no grant row can outlive the agent it references. A missing
// registration is reported as ErrNotFound rather than silently succeeding.
func (s *RDBConfigStore) DeleteAgentRegistration(ctx context.Context, name string) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("agent_name = ?", name).Delete(&tables.TableVirtualKeyAgentGrant{}).Error; err != nil {
			return err
		}
		result := tx.Delete(&tables.TableAgentRegistration{}, "name = ?", name)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
}
