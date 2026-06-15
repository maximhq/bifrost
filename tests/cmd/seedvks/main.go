// Command seedvks bulk-generates virtual keys and stores them encrypted in the
// governance_virtual_keys table.
//
// It reuses the production encrypt package and the TableVirtualKey GORM model so
// the value is encrypted (AES-256-GCM) and the value_hash (SHA-256 of plaintext)
// is computed by the exact same BeforeSave logic the running server uses. The
// encryption key and postgres DSN are read from a Bifrost config.json.
//
// After inserting the keys it assigns a budget and a rate limit to every
// prefix-matching key that does not already have one, so the step is idempotent
// and safe to re-run. Use -skip-seed to only (re)assign governance to existing
// keys without inserting new ones.
//
// -governance-mode selects how governance is attached:
//   - "direct" (default): a vk-owned budget (governance_budgets.virtual_key_id)
//     plus a rate limit via governance_virtual_keys.rate_limit_id. This is the OSS
//     representation; the enterprise UI/API does not surface it.
//   - "model-config": a vk-scoped all-models model config that owns the budget and
//     references the rate limit. This is what the enterprise UI/API surfaces and
//     the scoped-model checks enforce.
//
// Example:
//
//	go run ./cmd/seedvks -config /path/to/config.json -count 100000
//	go run ./cmd/seedvks -config /path/to/config.json -skip-seed -prefix loadtest-vk
//	go run ./cmd/seedvks -config /path/to/config.json -skip-seed -prefix loadtest-vk -governance-mode model-config
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"

	bifrost "github.com/maximhq/bifrost/core"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// virtualKeyPrefix mirrors governance.VirtualKeyPrefix; the value format is
// "sk-bf-<uuid>". It is duplicated here to avoid importing the governance plugin
// (which lives in a separate module) into this seeding utility.
const virtualKeyPrefix = "sk-bf-"

// Fixed budget assigned to each seeded virtual key. A budget belongs to the VK
// via governance_budgets.virtual_key_id (a VK may own several budgets).
const (
	defaultBudgetMaxLimit      = 100.0
	defaultBudgetResetDuration = "1M"
)

// Governance attachment modes selectable via -governance-mode.
const (
	// governanceModeDirect attaches a VK-owned budget (governance_budgets.virtual_key_id)
	// and a rate limit via governance_virtual_keys.rate_limit_id. This is the OSS
	// representation; the enterprise UI/API does not surface it.
	governanceModeDirect = "direct"
	// governanceModeModelConfig attaches governance the enterprise-canonical way: a
	// vk-scoped, all-models model config (governance_model_configs scope=virtual_key,
	// model_name="*", provider NULL) that owns the budget (governance_budgets.model_config_id)
	// and references the rate limit (governance_model_configs.rate_limit_id). This is what the
	// enterprise UI/API reverse-maps for display, and what the scoped-model checks enforce.
	governanceModeModelConfig = "model-config"
)

// rateLimitParams holds the per-key rate limit values assigned during seeding.
type rateLimitParams struct {
	tokenMaxLimit        int64
	tokenResetDuration   string
	requestMaxLimit      int64
	requestResetDuration string
}

// storeConfig is the subset of a config.json store block needed to build a DSN.
type storeConfig struct {
	Enabled bool   `json:"enabled"`
	Type    string `json:"type"`
	Config  struct {
		Host     string `json:"host"`
		Port     string `json:"port"`
		User     string `json:"user"`
		Password string `json:"password"`
		DBName   string `json:"db_name"`
		SSLMode  string `json:"ssl_mode"`
	} `json:"config"`
}

// fileConfig is the subset of a Bifrost config.json this tool reads.
type fileConfig struct {
	EncryptionKey string      `json:"encryption_key"`
	ConfigStore   storeConfig `json:"config_store"`
}

// loadConfig reads encryption key and config-store connection settings from a
// Bifrost config.json at the given path.
func loadConfig(path string) (fileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return fileConfig{}, fmt.Errorf("read config %q: %w", path, err)
	}
	var cfg fileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fileConfig{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	return cfg, nil
}

// dsn builds a postgres DSN from the store config.
func (s storeConfig) dsn() (string, error) {
	if s.Type != "postgres" {
		return "", fmt.Errorf("config_store type %q is not postgres", s.Type)
	}
	c := s.Config
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=%s",
		c.User, c.Password, c.Host, c.Port, c.DBName, c.SSLMode,
	), nil
}

// generateBatch builds a slice of unencrypted TableVirtualKey records. The GORM
// BeforeSave hook encrypts the value and computes the value_hash on insert, so
// these are constructed with plaintext values.
func generateBatch(prefix string, start, n int, now time.Time) []configstoreTables.TableVirtualKey {
	rows := make([]configstoreTables.TableVirtualKey, 0, n)
	for i := 0; i < n; i++ {
		idx := start + i
		rows = append(rows, configstoreTables.TableVirtualKey{
			ID:        uuid.NewString(),
			Name:      fmt.Sprintf("%s-%d", prefix, idx),
			Value:     virtualKeyPrefix + uuid.NewString(),
			CreatedAt: now,
			UpdatedAt: now,
		})
	}
	return rows
}

// run executes the seeding: connects, initializes encryption, inserts count
// virtual keys in batches (unless skipSeed is set), then assigns a budget and a
// rate limit to every prefix-matching key that does not already have one. It
// returns the number of virtual keys inserted.
func run(cfg fileConfig, count, batch int, prefix string, skipSeed bool, rl rateLimitParams, governanceMode string) (int, error) {
	if cfg.EncryptionKey == "" {
		return 0, fmt.Errorf("config has no encryption_key; refusing to store plaintext virtual keys")
	}

	encrypt.Init(cfg.EncryptionKey, bifrost.NewDefaultLogger(schemas.LogLevelWarn))
	if !encrypt.IsEnabled() {
		return 0, fmt.Errorf("encryption did not initialize")
	}

	dsn, err := cfg.ConfigStore.dsn()
	if err != nil {
		return 0, err
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return 0, fmt.Errorf("open postgres: %w", err)
	}

	now := time.Now().UTC()
	inserted := 0
	if skipSeed {
		log.Printf("skip-seed: not inserting virtual keys; assigning governance to existing keys with prefix %q", prefix)
	} else {
		for start := 1; start <= count; start += batch {
			size := batch
			if start+size-1 > count {
				size = count - start + 1
			}
			rows := generateBatch(prefix, start, size, now)
			// CreateInBatches runs BeforeSave per row, which encrypts Value,
			// sets ValueHash, and stamps EncryptionStatus=encrypted.
			if err := db.CreateInBatches(rows, size).Error; err != nil {
				return inserted, fmt.Errorf("insert batch at %d: %w", start, err)
			}
			inserted += size
			log.Printf("inserted %d/%d virtual keys", inserted, count)
		}
	}

	if err := assignGovernance(db, prefix, rl, batch, now, governanceMode); err != nil {
		return inserted, err
	}
	return inserted, nil
}

// assignGovernance ensures every virtual key whose name matches "<prefix>-%" has
// a budget and a rate limit, creating only the ones that are missing. It first
// reports how many keys already have each so a re-run is a cheap no-op when the
// expected count is already present.
func assignGovernance(db *gorm.DB, prefix string, rl rateLimitParams, batch int, now time.Time, governanceMode string) error {
	namePattern := prefix + "-%"

	var vkCount int64
	if err := db.Model(&configstoreTables.TableVirtualKey{}).
		Where("name LIKE ?", namePattern).Count(&vkCount).Error; err != nil {
		return fmt.Errorf("count virtual keys for prefix %q: %w", prefix, err)
	}
	if vkCount == 0 {
		log.Printf("no virtual keys match prefix %q; nothing to assign", prefix)
		return nil
	}

	if governanceMode == governanceModeModelConfig {
		return assignModelConfigGovernance(db, namePattern, rl, batch, now)
	}

	budgetOwners := db.Table("governance_budgets").
		Select("virtual_key_id").Where("virtual_key_id IS NOT NULL")

	var withBudget, withRateLimit int64
	if err := db.Model(&configstoreTables.TableVirtualKey{}).
		Where("name LIKE ? AND id IN (?)", namePattern, budgetOwners).
		Count(&withBudget).Error; err != nil {
		return fmt.Errorf("count virtual keys with budgets: %w", err)
	}
	if err := db.Model(&configstoreTables.TableVirtualKey{}).
		Where("name LIKE ? AND rate_limit_id IS NOT NULL", namePattern).
		Count(&withRateLimit).Error; err != nil {
		return fmt.Errorf("count virtual keys with rate limits: %w", err)
	}

	log.Printf("prefix %q matches %d virtual keys: %d already have budgets, %d already have rate limits",
		prefix, vkCount, withBudget, withRateLimit)

	if withBudget >= vkCount && withRateLimit >= vkCount {
		log.Printf("all %d virtual keys already have budgets and rate limits; nothing to assign", vkCount)
		return nil
	}

	if err := assignBudgets(db, namePattern, batch, now); err != nil {
		return err
	}
	return assignRateLimits(db, namePattern, rl, batch, now)
}

// assignBudgets creates a budget for every prefix-matching virtual key that does
// not already own one. Budgets are plain inserts pointing back via virtual_key_id.
func assignBudgets(db *gorm.DB, namePattern string, batch int, now time.Time) error {
	budgetOwners := db.Table("governance_budgets").
		Select("virtual_key_id").Where("virtual_key_id IS NOT NULL")

	var vkIDs []string
	if err := db.Model(&configstoreTables.TableVirtualKey{}).
		Where("name LIKE ? AND id NOT IN (?)", namePattern, budgetOwners).
		Pluck("id", &vkIDs).Error; err != nil {
		return fmt.Errorf("list virtual keys missing budgets: %w", err)
	}
	if len(vkIDs) == 0 {
		log.Printf("no virtual keys missing budgets")
		return nil
	}

	log.Printf("creating budgets for %d virtual keys", len(vkIDs))
	created := 0
	for start := 0; start < len(vkIDs); start += batch {
		end := start + batch
		if end > len(vkIDs) {
			end = len(vkIDs)
		}
		rows := make([]configstoreTables.TableBudget, 0, end-start)
		for _, vkID := range vkIDs[start:end] {
			owner := vkID
			rows = append(rows, configstoreTables.TableBudget{
				ID:            uuid.NewString(),
				MaxLimit:      defaultBudgetMaxLimit,
				ResetDuration: defaultBudgetResetDuration,
				LastReset:     now,
				VirtualKeyID:  &owner,
				CreatedAt:     now,
				UpdatedAt:     now,
			})
		}
		if err := db.CreateInBatches(rows, len(rows)).Error; err != nil {
			return fmt.Errorf("insert budget batch: %w", err)
		}
		created += len(rows)
		log.Printf("assigned budgets to %d/%d virtual keys", created, len(vkIDs))
	}
	return nil
}

// assignRateLimits creates a rate limit for every prefix-matching virtual key
// whose rate_limit_id is null, then links it onto the key. The link uses a raw
// column UPDATE so the virtual key's BeforeSave hook (which hashes/encrypts the
// value) never runs against an empty in-memory value.
func assignRateLimits(db *gorm.DB, namePattern string, rl rateLimitParams, batch int, now time.Time) error {
	var vkIDs []string
	if err := db.Model(&configstoreTables.TableVirtualKey{}).
		Where("name LIKE ? AND rate_limit_id IS NULL", namePattern).
		Pluck("id", &vkIDs).Error; err != nil {
		return fmt.Errorf("list virtual keys missing rate limits: %w", err)
	}
	if len(vkIDs) == 0 {
		log.Printf("no virtual keys missing rate limits")
		return nil
	}

	log.Printf("creating rate limits for %d virtual keys", len(vkIDs))
	created := 0
	for start := 0; start < len(vkIDs); start += batch {
		end := start + batch
		if end > len(vkIDs) {
			end = len(vkIDs)
		}
		chunk := vkIDs[start:end]
		if err := db.Transaction(func(tx *gorm.DB) error {
			rlRows := make([]configstoreTables.TableRateLimit, 0, len(chunk))
			rlIDByVK := make(map[string]string, len(chunk))
			for _, vkID := range chunk {
				rlID := uuid.NewString()
				rlIDByVK[vkID] = rlID
				rlRows = append(rlRows, newRateLimit(rlID, rl, now))
			}
			if err := tx.CreateInBatches(rlRows, len(rlRows)).Error; err != nil {
				return fmt.Errorf("insert rate limit batch: %w", err)
			}
			for vkID, rlID := range rlIDByVK {
				if err := tx.Exec(
					"UPDATE governance_virtual_keys SET rate_limit_id = ?, updated_at = ? WHERE id = ?",
					rlID, now, vkID).Error; err != nil {
					return fmt.Errorf("link rate limit to virtual key %s: %w", vkID, err)
				}
			}
			return nil
		}); err != nil {
			return err
		}
		created += len(chunk)
		log.Printf("assigned rate limits to %d/%d virtual keys", created, len(vkIDs))
	}
	return nil
}

// newRateLimit builds a TableRateLimit from the configured per-key limits.
func newRateLimit(id string, rl rateLimitParams, now time.Time) configstoreTables.TableRateLimit {
	tokenMax := rl.tokenMaxLimit
	tokenReset := rl.tokenResetDuration
	requestMax := rl.requestMaxLimit
	requestReset := rl.requestResetDuration
	return configstoreTables.TableRateLimit{
		ID:                   id,
		TokenMaxLimit:        &tokenMax,
		TokenResetDuration:   &tokenReset,
		TokenLastReset:       now,
		RequestMaxLimit:      &requestMax,
		RequestResetDuration: &requestReset,
		RequestLastReset:     now,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
}

// assignModelConfigGovernance attaches governance the enterprise-canonical way for
// every prefix-matching virtual key that does not already own a vk-scoped,
// all-models model config. For each such key it creates, in one transaction per
// chunk: a rate limit, a model config (scope=virtual_key, model_name="*",
// provider NULL) that references the rate limit, and a budget owned by that model
// config (governance_budgets.model_config_id). This is the representation the
// enterprise UI/API reverse-maps for display and the scoped-model checks enforce.
func assignModelConfigGovernance(db *gorm.DB, namePattern string, rl rateLimitParams, batch int, now time.Time) error {
	// Keys that already own a vk-scoped all-models model config are skipped so
	// re-runs are idempotent.
	configured := db.Table("governance_model_configs").
		Select("scope_id").
		Where("scope = ? AND model_name = ? AND provider IS NULL AND scope_id IS NOT NULL",
			configstoreTables.ModelConfigScopeVirtualKey, configstoreTables.ModelConfigAllModels)

	var vkIDs []string
	if err := db.Model(&configstoreTables.TableVirtualKey{}).
		Where("name LIKE ? AND id NOT IN (?)", namePattern, configured).
		Pluck("id", &vkIDs).Error; err != nil {
		return fmt.Errorf("list virtual keys missing model-config governance: %w", err)
	}
	if len(vkIDs) == 0 {
		log.Printf("no virtual keys missing model-config governance")
		return nil
	}

	log.Printf("creating model-config governance for %d virtual keys", len(vkIDs))
	created := 0
	for start := 0; start < len(vkIDs); start += batch {
		end := start + batch
		if end > len(vkIDs) {
			end = len(vkIDs)
		}
		chunk := vkIDs[start:end]
		// Insert order matters for foreign keys: rate limits first (referenced by
		// the model config), then model configs (referenced by budgets), then budgets.
		if err := db.Transaction(func(tx *gorm.DB) error {
			rlRows := make([]configstoreTables.TableRateLimit, 0, len(chunk))
			mcRows := make([]configstoreTables.TableModelConfig, 0, len(chunk))
			budgetRows := make([]configstoreTables.TableBudget, 0, len(chunk))
			for _, vkID := range chunk {
				scopeID := vkID
				rlID := uuid.NewString()
				mcID := uuid.NewString()
				rateLimitID := rlID
				modelConfigID := mcID
				rlRows = append(rlRows, newRateLimit(rlID, rl, now))
				mcRows = append(mcRows, configstoreTables.TableModelConfig{
					ID:          mcID,
					ModelName:   configstoreTables.ModelConfigAllModels,
					Provider:    nil,
					Scope:       configstoreTables.ModelConfigScopeVirtualKey,
					ScopeID:     &scopeID,
					RateLimitID: &rateLimitID,
					CreatedAt:   now,
					UpdatedAt:   now,
				})
				budgetRows = append(budgetRows, configstoreTables.TableBudget{
					ID:            uuid.NewString(),
					MaxLimit:      defaultBudgetMaxLimit,
					ResetDuration: defaultBudgetResetDuration,
					LastReset:     now,
					ModelConfigID: &modelConfigID,
					CreatedAt:     now,
					UpdatedAt:     now,
				})
			}
			if err := tx.CreateInBatches(rlRows, len(rlRows)).Error; err != nil {
				return fmt.Errorf("insert rate limit batch: %w", err)
			}
			if err := tx.CreateInBatches(mcRows, len(mcRows)).Error; err != nil {
				return fmt.Errorf("insert model config batch: %w", err)
			}
			if err := tx.CreateInBatches(budgetRows, len(budgetRows)).Error; err != nil {
				return fmt.Errorf("insert budget batch: %w", err)
			}
			return nil
		}); err != nil {
			return err
		}
		created += len(chunk)
		log.Printf("assigned model-config governance to %d/%d virtual keys", created, len(vkIDs))
	}
	return nil
}

// main parses flags and runs the virtual-key seeder.
func main() {
	configPath := flag.String("config", "", "path to Bifrost config.json (provides encryption_key and postgres DSN)")
	count := flag.Int("count", 100000, "number of virtual keys to generate")
	batch := flag.Int("batch", 1000, "insert batch size")
	prefix := flag.String("prefix", "loadtest-vk", "virtual key name prefix (names are <prefix>-<n>)")
	skipSeed := flag.Bool("skip-seed", false, "skip virtual key insertion; only assign budgets/rate limits to existing keys matching -prefix")
	tokenLimit := flag.Int64("token-limit", 1000000, "per-key token rate limit max")
	tokenReset := flag.String("token-reset", "1m", "per-key token rate limit reset duration (e.g. 30s, 5m, 1h, 1d)")
	requestLimit := flag.Int64("request-limit", 10000, "per-key request rate limit max")
	requestReset := flag.String("request-reset", "1m", "per-key request rate limit reset duration (e.g. 30s, 5m, 1h, 1d)")
	governanceMode := flag.String("governance-mode", "direct", "how to attach governance: 'direct' (vk-owned budget + vk.rate_limit_id) or 'model-config' (a vk-scoped all-models model config owning the budget + rate limit; required for the enterprise UI/API to surface VK governance)")
	flag.Parse()

	if *configPath == "" {
		log.Fatal("missing required -config flag")
	}
	if !*skipSeed && *count <= 0 {
		log.Fatal("-count must be positive")
	}
	if *batch <= 0 {
		log.Fatal("-batch must be positive")
	}
	if *governanceMode != governanceModeDirect && *governanceMode != governanceModeModelConfig {
		log.Fatalf("-governance-mode must be %q or %q", governanceModeDirect, governanceModeModelConfig)
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}

	rl := rateLimitParams{
		tokenMaxLimit:        *tokenLimit,
		tokenResetDuration:   *tokenReset,
		requestMaxLimit:      *requestLimit,
		requestResetDuration: *requestReset,
	}

	start := time.Now()
	inserted, err := run(cfg, *count, *batch, *prefix, *skipSeed, rl, *governanceMode)
	if err != nil {
		log.Fatalf("seed failed after %d rows: %v", inserted, err)
	}
	log.Printf("done: inserted %d encrypted virtual keys in %s", inserted, time.Since(start))
}
