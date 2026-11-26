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

// loadRelayRoutes reads the route rows of the named relays, or of every relay when names is nil,
// grouped by relay in stored order.
func loadRelayRoutes(db *gorm.DB, names []string) (map[string][]tables.TableRelayRoute, error) {
	var routes []tables.TableRelayRoute
	query := db.Order("created_at ASC, id ASC")
	if names != nil {
		query = query.Where("relay_name IN ?", names)
	}
	if err := query.Find(&routes).Error; err != nil {
		return nil, err
	}
	out := make(map[string][]tables.TableRelayRoute)
	for _, route := range routes {
		out[route.RelayName] = append(out[route.RelayName], route)
	}
	return out, nil
}

// writeRelayRoutes replaces a relay's route rows with the routes of row. IDs are assigned first
// (kept from prev where a route matches), and a route that already existed keeps its created_at,
// so its tie-break order survives the edit. New routes get increasing timestamps in the order
// sent. It writes the assigned IDs back into cfg, so the caller sees what was stored.
func writeRelayRoutes(tx *gorm.DB, cfg *schemas.RelayConfig, prev []tables.TableRelayRoute) error {
	prevConfigs := make([]schemas.RelayRouteConfig, len(prev))
	createdAt := make(map[string]time.Time, len(prev))
	for i := range prev {
		prevConfigs[i] = prev[i].ToConfig()
		createdAt[prev[i].ID] = prev[i].CreatedAt
	}
	schemas.AssignRelayRouteIDs(prevConfigs, cfg.Routes)
	if err := tx.Where("relay_name = ?", cfg.Name).Delete(&tables.TableRelayRoute{}).Error; err != nil {
		return err
	}
	if len(cfg.Routes) == 0 {
		return nil
	}
	now := time.Now().UTC()
	// New routes sort after every kept one even when this node's clock is behind the one that
	// stored them, so an added route never jumps ahead in a tie.
	base := now
	for _, at := range createdAt {
		if !at.Before(base) {
			base = at.Add(time.Microsecond)
		}
	}
	rows := make([]tables.TableRelayRoute, len(cfg.Routes))
	for i, route := range cfg.Routes {
		rows[i] = tables.NewTableRelayRoute(cfg.Name, route)
		if at, ok := createdAt[route.ID]; ok {
			rows[i].CreatedAt = at
		} else {
			rows[i].CreatedAt = base.Add(time.Duration(i) * time.Microsecond)
		}
		rows[i].UpdatedAt = now
	}
	// Batched so a relay with many routes stays under the drivers' bind-parameter limits.
	return tx.Omit(clause.Associations).CreateInBatches(&rows, 200).Error
}

// ListRelays loads every endpoint, oldest first, so startup reload and API output are
// stable.
func (s *RDBConfigStore) ListRelays(ctx context.Context) ([]*schemas.RelayConfig, error) {
	db := s.DB().WithContext(ctx)
	var rows []tables.TableRelay
	if err := db.Order("created_at ASC, name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	routes, err := loadRelayRoutes(db, nil)
	if err != nil {
		return nil, err
	}
	out := make([]*schemas.RelayConfig, 0, len(rows))
	for i := range rows {
		rows[i].Routes = routes[rows[i].Name]
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
	db := s.DB().WithContext(ctx)
	var row tables.TableRelay
	if err := db.First(&row, "name = ?", name).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	routes, err := loadRelayRoutes(db, []string{name})
	if err != nil {
		return nil, err
	}
	row.Routes = routes[name]
	return row.ToConfig()
}

// CreateRelay persists a new endpoint and its routes, giving each route without an ID a new one
// (written back into cfg). configHash is set by the config.json sync and empty for API writes.
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
		if err := tx.Omit(clause.Associations).Create(row).Error; err != nil {
			return err
		}
		return writeRelayRoutes(tx, cfg, nil)
	})
}

// UpdateRelay replaces an endpoint's settings and routes. A route keeps its ID and created_at
// when it is sent back with its ID or matches a stored route's method, path and headers; the
// assigned IDs are written back into cfg. The stored config hash is written only
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
		if err := tx.Model(row).Select(fields).Updates(row).Error; err != nil {
			return err
		}
		prev, err := loadRelayRoutes(tx, []string{cfg.Name})
		if err != nil {
			return err
		}
		return writeRelayRoutes(tx, cfg, prev[cfg.Name])
	})
}

// DeleteRelay removes an endpoint, its grants and its vault-owned secrets, ErrNotFound
// when it does not exist.
func (s *RDBConfigStore) DeleteRelay(ctx context.Context, name string) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("relay_name = ?", name).Delete(&tables.TableVirtualKeyRelayGrant{}).Error; err != nil {
			return err
		}
		if err := tx.Where("relay_name = ?", name).Delete(&tables.TableRelayRoute{}).Error; err != nil {
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
