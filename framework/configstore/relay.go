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

// ErrRelayExists is returned when creating an endpoint whose name is taken.
var ErrRelayExists = errors.New("relay already exists")

// ListRelays loads every endpoint, oldest first, so startup reload and API output are
// stable.
func (s *RDBConfigStore) ListRelays(ctx context.Context) ([]*schemas.RelayConfig, error) {
	var rows []tables.TableRelay
	if err := s.DB().WithContext(ctx).Order("created_at ASC, name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*schemas.RelayConfig, 0, len(rows))
	for i := range rows {
		cfg, err := rows[i].ToConfig()
		if err != nil {
			return nil, err
		}
		out = append(out, cfg)
	}
	return out, nil
}

// GetRelay loads one endpoint, ErrNotFound when it does not exist.
func (s *RDBConfigStore) GetRelay(ctx context.Context, name string) (*schemas.RelayConfig, error) {
	var row tables.TableRelay
	if err := s.DB().WithContext(ctx).First(&row, "name = ?", name).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return row.ToConfig()
}

// CreateRelay persists a new endpoint. configHash is set by the config.json sync and
// empty for API writes.
func (s *RDBConfigStore) CreateRelay(ctx context.Context, cfg *schemas.RelayConfig, configHash string) error {
	row, err := tables.NewTableRelay(cfg)
	if err != nil {
		return err
	}
	row.ConfigHash = configHash
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&tables.TableRelay{}).Where("name = ?", cfg.Name).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("%w: %s", ErrRelayExists, cfg.Name)
		}
		return tx.Create(row).Error
	})
}

// UpdateRelay replaces an endpoint's settings. The stored config hash is written only
// when the caller supplies one (the config.json sync); an API edit keeps the stored checkpoint so
// split-mode reconciliation can tell the file has not changed.
func (s *RDBConfigStore) UpdateRelay(ctx context.Context, cfg *schemas.RelayConfig, configHash string) error {
	row, err := tables.NewTableRelay(cfg)
	if err != nil {
		return err
	}
	row.ConfigHash = configHash
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing tables.TableRelay
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&existing, "name = ?", cfg.Name).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		row.CreatedAt = existing.CreatedAt
		fields := []string{"description", "config_json", "credential_value", "aws_access_key", "aws_secret_key", "aws_session_token", "aws_region", "aws_role_arn", "aws_external_id", "aws_session_name", "has_aws_credential", "extra_headers_json", "proxy_type", "proxy_url", "proxy_username", "proxy_password", "proxy_ca_cert_pem", "encryption_status", "updated_at"}
		if configHash != "" {
			fields = append(fields, "config_hash")
		}
		return tx.Model(row).Select(fields).Updates(row).Error
	})
}

// DeleteRelay removes an endpoint, its grants and its vault-owned secrets, ErrNotFound
// when it does not exist.
func (s *RDBConfigStore) DeleteRelay(ctx context.Context, name string) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("relay_name = ?", name).Delete(&tables.TableVirtualKeyRelayGrant{}).Error; err != nil {
			return err
		}
		// The row is loaded first so the vault remove callback sees its secrets and deletes the ones
		// this endpoint owns.
		var row tables.TableRelay
		if err := tx.First(&row, "name = ?", name).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		return tx.Delete(&row).Error
	})
}

// GetRelayConfigHashes returns the stored config.json hash of every endpoint by name.
func (s *RDBConfigStore) GetRelayConfigHashes(ctx context.Context) (map[string]string, error) {
	var rows []tables.TableRelay
	if err := s.DB().WithContext(ctx).Select("name, config_hash").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.Name] = row.ConfigHash
	}
	return out, nil
}

// GetRelayGrantedVirtualKeyIDs lists the virtual keys holding an explicit grant on an
// endpoint, so an endpoint delete can reload exactly those keys.
func (s *RDBConfigStore) GetRelayGrantedVirtualKeyIDs(ctx context.Context, name string) ([]string, error) {
	var ids []string
	err := s.DB().WithContext(ctx).Model(&tables.TableVirtualKeyRelayGrant{}).Where("relay_name = ?", name).Order("virtual_key_id ASC").Pluck("virtual_key_id", &ids).Error
	return ids, err
}

// ReplaceVirtualKeyRelayGrants sets the exact list of endpoints a virtual key may call
// one by one. It joins the caller's transaction when given one, so a virtual-key create or update
// commits its grants atomically. Names are checked for empties, duplicates and unknown endpoints
// before any write; an empty list clears the grants.
func (s *RDBConfigStore) ReplaceVirtualKeyRelayGrants(ctx context.Context, virtualKeyID string, endpointNames []string, tx ...*gorm.DB) error {
	if len(tx) == 0 || tx[0] == nil {
		return s.DB().WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
			return s.ReplaceVirtualKeyRelayGrants(ctx, virtualKeyID, endpointNames, transaction)
		})
	}
	txDB := tx[0].WithContext(ctx)
	var virtualKey tables.TableVirtualKey
	if err := txDB.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&virtualKey, "id = ?", virtualKeyID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	names := slices.Clone(endpointNames)
	sort.Strings(names)
	for i, name := range names {
		if name == "" {
			return fmt.Errorf("%w: endpoint name cannot be empty", ErrInvalidRelayGrant)
		}
		if i > 0 && names[i-1] == name {
			return fmt.Errorf("%w: duplicate endpoint name: %s", ErrInvalidRelayGrant, name)
		}
	}
	if len(names) > 0 {
		var found []string
		if err := txDB.Model(&tables.TableRelay{}).Where("name IN ?", names).Order("name ASC").Pluck("name", &found).Error; err != nil {
			return err
		}
		if len(found) != len(names) {
			return fmt.Errorf("%w: relays were not found: %s", ErrInvalidRelayGrant, strings.Join(missingFrom(names, found), ", "))
		}
	}
	if err := txDB.Where("virtual_key_id = ?", virtualKeyID).Delete(&tables.TableVirtualKeyRelayGrant{}).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, name := range names {
		if err := txDB.Create(&tables.TableVirtualKeyRelayGrant{VirtualKeyID: virtualKeyID, RelayName: name, CreatedAt: now}).Error; err != nil {
			return err
		}
	}
	return nil
}
