// Package registry holds the records livestore keeps for the config store's tables and the
// database reads that build them, one file per domain. It does not import livestore:
// livestore.New registers each kind on the engine with these reads as its loader.
package registry

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// Provider is one configured provider's settings. Its keys are records of their own
// (ProviderKey), so the record holds no keys to read: a key write re-reads only its key, and a
// settings write never touches a key.
type Provider struct {
	Name     schemas.ModelProvider
	Settings configstore.ProviderSettings
	// BudgetID and RateLimitID name the limits governance holds every request to this provider
	// to; "" for none.
	BudgetID, RateLimitID string
}

// ProviderName is a provider record's primary key.
func ProviderName(p *Provider) string { return string(p.Name) }

// ProvidersPage reads up to limit providers named after the cursor, in name order, and returns
// the next cursor, "" after the last page.
func ProvidersPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*Provider, string, error) {
	rows, err := readProviders(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("name > ?", after).Order("name").Limit(limit)
	})
	if err != nil || len(rows) < limit {
		return rows, "", err
	}
	return rows, string(rows[len(rows)-1].Name), nil
}

// ProvidersByName reads the named providers; a name with no row is left out. The names bind as one
// argument, so a refresh of any number of them stays within the statement parameter limit.
func ProvidersByName(ctx context.Context, db *gorm.DB, names []string) ([]*Provider, error) {
	return readProviders(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "name", names)) })
}

// readProviders loads the provider rows where selects, without their keys.
func readProviders(ctx context.Context, db *gorm.DB, where func(*gorm.DB) *gorm.DB) ([]*Provider, error) {
	var rows []tables.TableProvider
	if err := where(db.WithContext(ctx)).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*Provider, 0, len(rows))
	for i := range rows {
		config := configstore.ProviderConfigFromTable(&rows[i])
		out = append(out, &Provider{
			Name: schemas.ModelProvider(rows[i].Name), Settings: config.Settings(),
			BudgetID: deref(rows[i].BudgetID), RateLimitID: deref(rows[i].RateLimitID),
		})
	}
	return out, nil
}

// ProviderKey is one key of a provider, a record of its own under its provider.
type ProviderKey struct {
	Provider schemas.ModelProvider // the parent: config_keys.provider
	RowID    uint                  // config_keys.id; 0 for a key loaded from config.json
	Order    int64                 // creation order: RowID with a database, the config.json position without one
	Key      schemas.Key           // Key.ID is key_id, the primary key
}

// ProviderKeyID is a provider key record's primary key, the key's key_id.
func ProviderKeyID(k *ProviderKey) string { return k.Key.ID }

// ProviderKeyProvider names the provider a key record belongs to, its ChildOf parent.
func ProviderKeyProvider(k *ProviderKey) string { return string(k.Provider) }

// ProviderKeysPage reads up to limit keys whose key_id sorts after the cursor, in key_id order,
// and returns the next cursor, "" after the last page.
func ProviderKeysPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*ProviderKey, string, error) {
	keys, err := readProviderKeys(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("key_id > ?", after).Order("key_id").Limit(limit)
	})
	if err != nil || len(keys) < limit {
		return keys, "", err
	}
	return keys, keys[len(keys)-1].Key.ID, nil
}

// ProviderKeysByID reads the keys with these key_ids; one with no row is left out. The IDs bind as
// one argument, so a refresh of any number of them stays within the statement parameter limit.
func ProviderKeysByID(ctx context.Context, db *gorm.DB, ids []string) ([]*ProviderKey, error) {
	return readProviderKeys(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "key_id", ids)) })
}

// ProviderKeysByProviders reads every key of the named providers in creation order, which is how a
// provider's refresh finds the keys under it.
func ProviderKeysByProviders(ctx context.Context, db *gorm.DB, providers []string) ([]*ProviderKey, error) {
	return readProviderKeys(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where(queryscope.InStrings(q, "provider", providers)).Order("id")
	})
}

// readProviderKeys loads the key rows where selects. Find runs the rows' AfterFind hook, so
// encrypted values come back decrypted, as the provider preload returned them.
func readProviderKeys(ctx context.Context, db *gorm.DB, where func(*gorm.DB) *gorm.DB) ([]*ProviderKey, error) {
	var rows []tables.TableKey
	if err := where(db.WithContext(ctx)).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*ProviderKey, 0, len(rows))
	for i := range rows {
		out = append(out, &ProviderKey{
			Provider: schemas.ModelProvider(rows[i].Provider),
			RowID:    rows[i].ID,
			Order:    int64(rows[i].ID),
			Key:      configstore.SchemaKeyFromTableKey(rows[i]),
		})
	}
	return out, nil
}
