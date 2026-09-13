package logstore

import (
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CASInventory is independent evidence of the complete field-to-manifest map.
// Native evidence is created with the original write. Legacy bootstrap records
// only what was present at migration time; it cannot prove earlier completeness.
// Recovery must copy this row alongside its log and CAS data, never regenerate
// native evidence from a potentially damaged pointer set.
type CASInventory struct {
	LogID      string `gorm:"primaryKey;column:log_id"`
	Version    int    `gorm:"column:version;not null"`
	Provenance string `gorm:"column:provenance;not null"`
	Entries    string `gorm:"column:entries;type:text;not null"`
}

func (CASInventory) TableName() string { return "cas_inventories" }

type casInventoryState struct {
	ID      int `gorm:"primaryKey;autoIncrement:false"`
	Version int `gorm:"not null"`
}

func (casInventoryState) TableName() string { return "cas_inventory_state" }

// initializeCASInventory is SQLite-only in this release. The committed state
// row prevents a restart from silently bootstrapping a subsequently lost
// inventory row. DDL, legacy baseline and state commit atomically.
func initializeCASInventory(db *gorm.DB) error {
	if db.Dialector.Name() != "sqlite" {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(&CASInventory{}, &casInventoryState{}); err != nil {
			return err
		}
		// Reserve the SQLite writer before reading the migration state.
		if err := tx.Exec("UPDATE cas_inventory_state SET version = version WHERE id = 1").Error; err != nil {
			return err
		}
		var state casInventoryState
		err := tx.First(&state, 1).Error
		if err == nil {
			if state.Version != 1 {
				return fmt.Errorf("logstore/cas: unsupported inventory migration version")
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var rows []Log
		if err := tx.Select("id", "has_object").FindInBatches(&rows, 500, func(batch *gorm.DB, _ int) error {
			for _, row := range rows {
				var existing int64
				if err := tx.Model(&CASInventory{}).Where("log_id = ?", row.ID).Count(&existing).Error; err != nil {
					return err
				}
				if existing != 0 {
					return fmt.Errorf("logstore/cas: inventory exists without migration state")
				}
				// Do not invent missing historical fields. Even an empty legacy map
				// stays explicitly legacy; has_object inconsistencies fail verification.
				if err := writeCASInventory(tx, row.ID, "legacy_bootstrap"); err != nil {
					return err
				}
			}
			return nil
		}).Error; err != nil {
			return err
		}
		return tx.Create(&casInventoryState{ID: 1, Version: 1}).Error
	})
}

func writeCASInventory(tx *gorm.DB, id, provenance string) error {
	if tx.Dialector.Name() != "sqlite" {
		return nil
	}
	var pointers []casPayload
	if err := tx.Where("log_id = ?", id).Find(&pointers).Error; err != nil {
		return err
	}
	entries := make(map[string]string, len(pointers))
	for _, pointer := range pointers {
		entries[pointer.Field] = pointer.BlobHash
	}
	// encoding/json sorts map keys, keeping the evidence encoding deterministic.
	encoded, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "log_id"}}, DoUpdates: clause.AssignmentColumns([]string{"entries"})}).Create(&CASInventory{LogID: id, Version: 1, Provenance: provenance, Entries: string(encoded)}).Error
}

// VerifyCASInventory verifies the entire field set and manifest identities,
// including fields outside a requested hydration projection. Call it inside
// the SAME read snapshot as the log/payload reads (or recovery source reads).
// It does not verify blob bytes; normal CAS reconstruction verifies those.
// PostgreSQL inventory is not supported by this release.
func VerifyCASInventory(tx *gorm.DB, logID string) error {
	if tx.Dialector.Name() != "sqlite" {
		return fmt.Errorf("logstore/cas: inventory verification supports SQLite only")
	}
	var row Log
	if err := tx.Select("id", "has_object").Where("id = ?", logID).Take(&row).Error; err != nil {
		return err
	}
	var inventory CASInventory
	if err := tx.Where("log_id = ?", logID).Take(&inventory).Error; err != nil {
		return fmt.Errorf("logstore/cas: inventory missing or unreadable for log %s: %w", logID, err)
	}
	if inventory.Version != 1 || (inventory.Provenance != "native" && inventory.Provenance != "legacy_bootstrap") {
		return fmt.Errorf("logstore/cas: invalid inventory evidence for log %s", logID)
	}
	var expected map[string]string
	if err := json.Unmarshal([]byte(inventory.Entries), &expected); err != nil || expected == nil {
		return fmt.Errorf("logstore/cas: invalid inventory encoding for log %s", logID)
	}
	var pointers []casPayload
	if err := tx.Where("log_id = ?", logID).Find(&pointers).Error; err != nil {
		return err
	}
	if row.HasObject != (len(expected) > 0) || len(expected) != len(pointers) {
		return fmt.Errorf("logstore/cas: inventory mismatch (cas payloads are missing or unexpected) for log %s", logID)
	}
	for _, pointer := range pointers {
		if hash, ok := expected[pointer.Field]; !ok || hash == "" || hash != pointer.BlobHash {
			return fmt.Errorf("logstore/cas: inventory pointer mismatch for log %s field %s", logID, pointer.Field)
		}
	}
	return nil
}
