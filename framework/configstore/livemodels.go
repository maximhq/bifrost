package configstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/migrator"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LiveModelListingStore is the optional persistence capability for key-scoped listings.
// Stores without it retain the in-memory behavior; no existing ConfigStore implementation changes.
type LiveModelListingStore interface {
	GetLiveModelSource(context.Context, schemas.ModelProvider, string) (*LiveModelSource, error)
	GetLiveModelListings(context.Context, schemas.ModelProvider, *string) ([]tables.TableLiveModelListing, error)
	UpsertLiveModelListing(context.Context, LiveModelSource, bool, string) (bool, error)
	PruneLiveModelListings(context.Context, schemas.ModelProvider) error
}

// LiveModelSource binds a fetch to existing rows and configuration, excluding mutable status.
type LiveModelSource struct {
	Provider     schemas.ModelProvider
	ProviderID   uint
	KeyID        string
	TableKeyID   *uint
	Hash         string
	ProviderHash string
}

// LiveModelSourceHash fingerprints credentials and model policy, never persisting their values.
// Provider URL edits retain last-known-good keyed listings; the full provider hash fences new writes.
func LiveModelSourceHash(provider schemas.ModelProvider, config *ProviderConfig, key schemas.Key) (string, error) {
	keyless := config != nil && config.CustomProviderConfig != nil && config.CustomProviderConfig.IsKeyLess
	if keyless {
		key = schemas.Key{Models: schemas.WhiteList{"*"}}
	}
	key.Name, key.ConfigHash, key.Description, key.Status = "", "", "", ""
	key.Weight, key.Enabled, key.UseForBatchAPI = 0, nil, nil
	// SQL stores unset endpoint preferences as false; both representations select the same route.
	if key.UseAnthropicEndpoints != nil && !*key.UseAnthropicEndpoints {
		key.UseAnthropicEndpoints = nil
	}
	if key.UseOpenAIEndpoints != nil && !*key.UseOpenAIEndpoints {
		key.UseOpenAIEndpoints = nil
	}
	if key.OAuthKeyConfig != nil {
		key.OAuthKeyConfig = oauthKeyConfigHashView(key.OAuthKeyConfig)
	}
	var headers map[string]string
	var endpoint string
	if config != nil && config.NetworkConfig != nil {
		if len(config.NetworkConfig.ExtraHeaders) > 0 {
			headers = make(map[string]string, len(config.NetworkConfig.ExtraHeaders))
			for name, value := range config.NetworkConfig.ExtraHeaders {
				headers[name] = value.GetValue()
			}
		}
		if keyless {
			endpoint = config.NetworkConfig.BaseURL
		}
	}
	return hashLiveModelValue(struct {
		Provider   schemas.ModelProvider
		Keyless    bool
		Key        schemas.Key
		Credential string
		Headers    map[string]string
		Endpoint   string
	}{provider, keyless, key, key.Value.GetValue(), headers, endpoint})
}

// LiveModelProviderHash fences request-producing provider changes during a fetch.
func LiveModelProviderHash(config *ProviderConfig) (string, error) {
	if config == nil {
		return "", ErrNotFound
	}
	// Storage omits empty proxy secrets, so hash the same representation on both sides of a reload.
	proxy, err := config.ProxyConfig.MarshalForStorage()
	if err != nil {
		return "", err
	}
	return hashLiveModelValue(struct {
		Network *schemas.NetworkConfig
		Proxy   json.RawMessage
		Custom  *schemas.CustomProviderConfig
		OpenAI  *schemas.OpenAIConfig
	}{config.NetworkConfig, json.RawMessage(proxy), config.CustomProviderConfig, config.OpenAIConfig})
}

// hashLiveModelValue uses canonical JSON so map ordering cannot invalidate a successful listing.
func hashLiveModelValue(value any) (string, error) {
	data, err := providerUtils.MarshalSorted(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// liveModelProvider reads the parent row before its keys, matching configuration writers' lock order.
func liveModelProvider(db *gorm.DB, provider schemas.ModelProvider, lock bool) (*tables.TableProvider, error) {
	var row tables.TableProvider
	if lock {
		db = dbForUpdate(db)
	}
	if err := db.Where("name = ?", string(provider)).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

// liveModelSource derives provenance only for an active key or the configured keyless sentinel.
func liveModelSource(row *tables.TableProvider, key *tables.TableKey) (*LiveModelSource, error) {
	config := &ProviderConfig{NetworkConfig: row.NetworkConfig, ProxyConfig: row.ProxyConfig,
		CustomProviderConfig: row.CustomProviderConfig, OpenAIConfig: row.OpenAIConfig}
	keyless := row.CustomProviderConfig != nil && row.CustomProviderConfig.IsKeyLess
	if row.CustomProviderConfig != nil && !row.CustomProviderConfig.IsOperationAllowed(schemas.ListModelsRequest) {
		return nil, ErrNotFound
	}
	var schemaKey schemas.Key
	var keyRowID *uint
	if !keyless {
		if key == nil || (key.Enabled != nil && !*key.Enabled) {
			return nil, ErrNotFound
		}
		schemaKey = schemaKeyFromTableKey(*key)
		keyRowID = &key.ID
	}
	hash, err := LiveModelSourceHash(schemas.ModelProvider(row.Name), config, schemaKey)
	if err != nil {
		return nil, err
	}
	providerHash, err := LiveModelProviderHash(config)
	if err != nil {
		return nil, err
	}
	return &LiveModelSource{Provider: schemas.ModelProvider(row.Name), ProviderID: row.ID,
		KeyID: schemaKey.ID, TableKeyID: keyRowID, Hash: hash, ProviderHash: providerHash}, nil
}

// readLiveModelSource checks row identity and eligibility inside the caller's transaction.
func readLiveModelSource(db *gorm.DB, provider schemas.ModelProvider, keyID string, lock bool) (*LiveModelSource, error) {
	row, err := liveModelProvider(db, provider, lock)
	if err != nil {
		return nil, err
	}
	if row.CustomProviderConfig != nil && row.CustomProviderConfig.IsKeyLess {
		if keyID != "" {
			return nil, ErrNotFound
		}
		return liveModelSource(row, nil)
	}
	if keyID == "" {
		return nil, ErrNotFound
	}
	var key tables.TableKey
	if lock {
		db = dbForUpdate(db)
	}
	if err := db.Where("provider_id = ? AND key_id = ?", row.ID, keyID).First(&key).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return liveModelSource(row, &key)
}

// GetLiveModelSource captures the current source without retaining credentials in the snapshot.
func (s *RDBConfigStore) GetLiveModelSource(ctx context.Context, provider schemas.ModelProvider, keyID string) (*LiveModelSource, error) {
	return readLiveModelSource(s.DB().WithContext(ctx), provider, keyID, false)
}

// liveModelListingMatches rejects snapshots belonging to retired rows or changed credentials/policy.
func liveModelListingMatches(listing tables.TableLiveModelListing, source *LiveModelSource) bool {
	if source == nil || listing.ProviderID != source.ProviderID || listing.KeyID != source.KeyID || listing.SourceHash != source.Hash {
		return false
	}
	return (listing.TableKeyID == nil && source.TableKeyID == nil) ||
		(listing.TableKeyID != nil && source.TableKeyID != nil && *listing.TableKeyID == *source.TableKeyID)
}

// readLiveModelListings loads both modes together and validates each against authoritative key rows.
func readLiveModelListings(db *gorm.DB, provider schemas.ModelProvider, keyID *string, lock bool) ([]tables.TableLiveModelListing, []tables.TableLiveModelListing, error) {
	row, err := liveModelProvider(db, provider, lock)
	if errors.Is(err, ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	sources := make(map[string]*LiveModelSource)
	if row.CustomProviderConfig != nil && row.CustomProviderConfig.IsKeyLess {
		source, err := liveModelSource(row, nil)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, nil, err
		}
		sources[""] = source
	} else {
		var keys []tables.TableKey
		keyDB := db
		if lock {
			keyDB = dbForUpdate(db)
		}
		keyDB = keyDB.Where("provider_id = ?", row.ID)
		if keyID != nil {
			keyDB = keyDB.Where("key_id = ?", *keyID)
		}
		if err := keyDB.Find(&keys).Error; err != nil {
			return nil, nil, err
		}
		for i := range keys {
			source, err := liveModelSource(row, &keys[i])
			if err != nil && !errors.Is(err, ErrNotFound) {
				return nil, nil, err
			}
			sources[keys[i].KeyID] = source
		}
	}
	var listings, valid, retired []tables.TableLiveModelListing
	listingDB := db.Where("provider_id = ?", row.ID)
	if keyID != nil {
		listingDB = listingDB.Where("key_id = ?", *keyID)
	}
	if err := listingDB.Find(&listings).Error; err != nil {
		return nil, nil, err
	}
	for _, listing := range listings {
		if liveModelListingMatches(listing, sources[listing.KeyID]) {
			valid = append(valid, listing)
		} else {
			retired = append(retired, listing)
		}
	}
	return valid, retired, nil
}

// GetLiveModelListings reads persistent data without mistaking a failed read for an empty catalog.
func (s *RDBConfigStore) GetLiveModelListings(ctx context.Context, provider schemas.ModelProvider, keyID *string) ([]tables.TableLiveModelListing, error) {
	options := &sql.TxOptions{ReadOnly: true}
	if s.DB().Dialector.Name() == "postgres" {
		options.Isolation = sql.LevelRepeatableRead
	}
	var valid []tables.TableLiveModelListing
	err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		valid, _, err = readLiveModelListings(tx, provider, keyID, false)
		return err
	}, options)
	return valid, err
}

// UpsertLiveModelListing commits only if the captured source still exists and is unchanged.
func (s *RDBConfigStore) UpsertLiveModelListing(ctx context.Context, expected LiveModelSource, unfiltered bool, data string) (bool, error) {
	stored := false
	err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := readLiveModelSource(tx, expected.Provider, expected.KeyID, true)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		listing := tables.TableLiveModelListing{ProviderID: expected.ProviderID, KeyID: expected.KeyID,
			TableKeyID: expected.TableKeyID, Unfiltered: unfiltered, SourceHash: expected.Hash, Data: data}
		if !liveModelListingMatches(listing, current) || current.ProviderHash != expected.ProviderHash {
			return nil
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "provider_id"}, {Name: "key_id"}, {Name: "unfiltered"}},
			DoUpdates: clause.AssignmentColumns([]string{"table_key_id", "source_hash", "data"}),
		}).Create(&listing).Error; err != nil {
			return err
		}
		stored = true
		return nil
	})
	return stored && err == nil, err
}

// PruneLiveModelListings removes incompatible entries without deleting a newer, compatible refresh.
func (s *RDBConfigStore) PruneLiveModelListings(ctx context.Context, provider schemas.ModelProvider) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, retired, err := readLiveModelListings(tx, provider, nil, true)
		if err != nil {
			return err
		}
		for _, listing := range retired {
			if err := tx.Delete(&listing).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// migrationAddLiveModelListings adds durable metadata under the existing migration lock.
func migrationAddLiveModelListings(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	return RunSingleMigration(ctx, nil, db, logger, &migrator.Migration{
		ID: "add_live_model_listings",
		Migrate: func(tx *gorm.DB) error {
			if !tx.Migrator().HasTable(&tables.TableLiveModelListing{}) {
				return tx.Migrator().CreateTable(&tables.TableLiveModelListing{})
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&tables.TableLiveModelListing{})
		},
	})
}

var _ LiveModelListingStore = (*RDBConfigStore)(nil)
