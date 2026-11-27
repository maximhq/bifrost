package configstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
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

// RelayGrantTable names a table of relay grants: rows keyed by holderColumn and relay_name, with
// a routes column holding the granted route IDs.
type RelayGrantTable struct {
	Table        string
	HolderColumn string
}

// RegisterRelayGrantTable adds a grant table whose route lists a relay edit prunes, in the same
// transaction as the edit, when it removes routes. Virtual-key grants are always pruned; a build
// with other holders of relay grants registers their tables at startup.
func (s *RDBConfigStore) RegisterRelayGrantTable(table RelayGrantTable) {
	s.relayGrantTablesMu.Lock()
	defer s.relayGrantTablesMu.Unlock()
	if !slices.Contains(s.relayGrantTables, table) {
		s.relayGrantTables = append(s.relayGrantTables, table)
	}
}

// grantTables lists every relay grant table a relay edit prunes: virtual keys, then any registered.
func (s *RDBConfigStore) grantTables() []RelayGrantTable {
	s.relayGrantTablesMu.RLock()
	defer s.relayGrantTablesMu.RUnlock()
	return append([]RelayGrantTable{{Table: tables.TableVirtualKeyRelayGrant{}.TableName(), HolderColumn: "virtual_key_id"}}, s.relayGrantTables...)
}

// writeRelayRoutes replaces a relay's route rows with the routes of cfg. IDs are assigned first
// (kept from prev where a route matches), and a route that already existed keeps its created_at,
// so its tie-break order survives the edit. New routes get increasing timestamps in the order
// sent. Grants in grantTables lose the IDs of removed routes. It writes the assigned IDs back into
// cfg, so the caller sees what was stored.
func writeRelayRoutes(tx *gorm.DB, cfg *schemas.RelayConfig, prev []tables.TableRelayRoute, grantTables []RelayGrantTable) error {
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
	if len(prev) > 0 {
		kept := make([]string, len(cfg.Routes))
		for i, route := range cfg.Routes {
			kept[i] = route.ID
		}
		for _, grants := range grantTables {
			if _, err := PruneRelayGrantRoutes(tx, grants.Table, grants.HolderColumn, cfg.Name, kept); err != nil {
				return fmt.Errorf("prune relay grants in %s: %w", grants.Table, err)
			}
		}
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
		return writeRelayRoutes(tx, cfg, nil, nil)
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
		return writeRelayRoutes(tx, cfg, prev[cfg.Name], s.grantTables())
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

// ReplaceVirtualKeyRelayGrants sets the exact list of relays a virtual key may call, each with
// the routes it may call. It joins the caller's transaction when given one, so a virtual-key create
// or update commits its grants atomically. Grants are checked for empty or duplicate relay names,
// unknown relays, and route IDs that are not routes of their relay before any write; an empty list
// clears the grants. A grant with no route list keeps the routes the key already holds on that
// relay, or gets every route when it is new.
func (s *RDBConfigStore) ReplaceVirtualKeyRelayGrants(ctx context.Context, virtualKeyID string, grants []tables.TableVirtualKeyRelayGrant, tx ...*gorm.DB) error {
	if len(tx) == 0 || tx[0] == nil {
		return s.DB().WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
			return s.ReplaceVirtualKeyRelayGrants(ctx, virtualKeyID, grants, transaction)
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
	sorted := slices.Clone(grants)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].RelayName < sorted[j].RelayName })
	// A grant resent without routes keeps the routes it has, so a client that only knows relay
	// names cannot widen a route-limited grant; a new grant without routes gets every route.
	var stored []tables.TableVirtualKeyRelayGrant
	if err := txDB.Where("virtual_key_id = ?", virtualKeyID).Find(&stored).Error; err != nil {
		return err
	}
	for i := range sorted {
		if sorted[i].Routes != nil {
			continue
		}
		for _, s := range stored {
			if s.RelayName == sorted[i].RelayName {
				sorted[i].Routes = slices.Clone(s.Routes)
			}
		}
	}
	if err := ValidateRelayGrants(txDB, tables.RelayGrantPermits(sorted)); err != nil {
		return err
	}
	if err := txDB.Where("virtual_key_id = ?", virtualKeyID).Delete(&tables.TableVirtualKeyRelayGrant{}).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, g := range sorted {
		row := tables.TableVirtualKeyRelayGrant{VirtualKeyID: virtualKeyID, RelayName: g.RelayName, Routes: slices.Clone(g.Routes), CreatedAt: now}
		if err := txDB.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

// ValidateRelayGrants checks a holder's relay grants against the store: every relay is named once
// and exists, and every route ID is a route of its relay. Errors wrap ErrInvalidRelayGrant, so a
// handler can answer 400. Any holder's grants can be checked, since they share these rules.
func ValidateRelayGrants(db *gorm.DB, permits []schemas.RelayPermit) error {
	names := make([]string, 0, len(permits))
	seen := make(map[string]bool, len(permits))
	for _, p := range permits {
		if p.Relay == "" {
			return fmt.Errorf("%w: relay name cannot be empty", ErrInvalidRelayGrant)
		}
		if seen[p.Relay] {
			return fmt.Errorf("%w: duplicate relay name: %s", ErrInvalidRelayGrant, p.Relay)
		}
		seen[p.Relay] = true
		if err := tables.ValidateRelayGrantRoutes(p.Routes); err != nil {
			return fmt.Errorf("%w: relay %s: %w", ErrInvalidRelayGrant, p.Relay, err)
		}
		names = append(names, p.Relay)
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	// The relay rows are share-locked, as a relay edit locks its row before pruning grants, so a
	// grant cannot be written against routes an edit is removing at the same time.
	var found []string
	if err := db.Model(&tables.TableRelay{}).Clauses(clause.Locking{Strength: "SHARE"}).Where("name IN ?", names).Order("name ASC").Pluck("name", &found).Error; err != nil {
		return err
	}
	if len(found) != len(names) {
		return fmt.Errorf("%w: relays were not found: %s", ErrInvalidRelayGrant, strings.Join(missingFrom(names, found), ", "))
	}
	routeIDs, err := relayRouteIDs(db, names)
	if err != nil {
		return err
	}
	for _, p := range permits {
		if p.Routes.IsUnrestricted() {
			continue
		}
		for _, id := range p.Routes {
			if !slices.Contains(routeIDs[p.Relay], id) {
				return fmt.Errorf("%w: relay %s has no route %s", ErrInvalidRelayGrant, p.Relay, id)
			}
		}
	}
	return nil
}

// relayRouteIDs returns the route IDs of each named relay.
func relayRouteIDs(db *gorm.DB, names []string) (map[string][]string, error) {
	var rows []tables.TableRelayRoute
	if err := db.Select("relay_name, id").Where("relay_name IN ?", names).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string][]string, len(names))
	for _, row := range rows {
		out[row.RelayName] = append(out[row.RelayName], row.ID)
	}
	return out, nil
}

// GetRelayRouteIDs returns the route IDs of each named relay, for checking grants that name routes.
func (s *RDBConfigStore) GetRelayRouteIDs(ctx context.Context, names []string) (map[string][]string, error) {
	if len(names) == 0 {
		return map[string][]string{}, nil
	}
	return relayRouteIDs(s.DB().WithContext(ctx), names)
}

// PruneRelayGrantRoutes removes routes that no longer exist from one relay's grants in a grant
// table: table is its name and holderColumn the column naming the holder, which with relay_name
// keys a row. A grant left naming no route is deleted, since it would grant nothing; all-routes
// grants are untouched. It runs in the relay save's transaction, so stored grants never name a
// deleted route. It returns the holders whose grants changed.
func PruneRelayGrantRoutes(tx *gorm.DB, table, holderColumn, relayName string, kept []string) ([]string, error) {
	type grantRow struct {
		Holder string
		Routes string
	}
	var rows []grantRow
	if err := tx.Table(table).Select(holderColumn+" AS holder, routes").Where("relay_name = ?", relayName).Scan(&rows).Error; err != nil {
		return nil, err
	}
	var changed []string
	for _, row := range rows {
		var routes schemas.WhiteList
		if row.Routes != "" && row.Routes != "null" {
			if err := sonic.UnmarshalString(row.Routes, &routes); err != nil {
				return nil, fmt.Errorf("decode %s routes of %s: %w", table, row.Holder, err)
			}
		}
		if routes == nil || routes.IsUnrestricted() {
			continue
		}
		next := slices.DeleteFunc(slices.Clone(routes), func(id string) bool { return !slices.Contains(kept, id) })
		if len(next) == len(routes) {
			continue
		}
		changed = append(changed, row.Holder)
		match := tx.Table(table).Where(holderColumn+" = ? AND relay_name = ?", row.Holder, relayName)
		if len(next) == 0 {
			if err := match.Delete(nil).Error; err != nil {
				return nil, err
			}
			continue
		}
		encoded, err := sonic.MarshalString(next)
		if err != nil {
			return nil, err
		}
		if err := match.Update("routes", encoded).Error; err != nil {
			return nil, err
		}
	}
	return changed, nil
}
