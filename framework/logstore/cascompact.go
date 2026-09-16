package logstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"gorm.io/gorm"
)

// CompactCASReport describes verified logical contents, independent of inline layout.
type CompactCASReport struct {
	Logs          int64  `json:"logs"`
	Fields        int64  `json:"fields"`
	ClearedBytes  int64  `json:"cleared_bytes"`
	PayloadSHA256 string `json:"payload_sha256"`
}

// casRefsHexLayout reports whether cas_refs still uses the pre-integer
// (owner_hash, target_hash) layout, so maintenance paths can validate both.
func casRefsHexLayout(tx *gorm.DB) (bool, error) {
	var cols []struct{ Name string }
	if err := tx.Raw("PRAGMA table_info(cas_refs)").Scan(&cols).Error; err != nil {
		return false, err
	}
	for _, c := range cols {
		if c.Name == "owner_hash" {
			return true, nil
		}
	}
	return false, nil
}

// MigrateCASRefs changes only SQLite's physical layout. The transaction preserves
// all edges and explicit indexes. PostgreSQL is deliberately untouched. Stores
// already on the integer-id layout are left as-is.
func MigrateCASRefs(db *gorm.DB) error {
	if db.Dialector.Name() != "sqlite" {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var cols []struct{ Name string }
		if err := tx.Raw("PRAGMA table_info(cas_refs)").Scan(&cols).Error; err != nil {
			return err
		}
		if len(cols) == 2 && cols[0].Name == "owner_id" && cols[1].Name == "target_id" {
			// Integer-id layout from cas_integer_ref_ids_v1; the WITHOUT ROWID
			// guarantee is that migration's responsibility.
			return nil
		}
		if err := tx.Exec("UPDATE cas_refs SET owner_hash=owner_hash WHERE 0").Error; err != nil {
			return err
		}
		var ddl string
		if err := tx.Raw("SELECT sql FROM sqlite_master WHERE type='table' AND name='cas_refs'").Scan(&ddl).Error; err != nil {
			return err
		}
		if strings.Contains(strings.ToUpper(ddl), "WITHOUT ROWID") {
			return nil
		}
		if len(cols) != 2 || cols[0].Name != "owner_hash" || cols[1].Name != "target_hash" {
			return fmt.Errorf("unexpected cas_refs columns")
		}
		var triggers int64
		if err := tx.Raw("SELECT count(*) FROM sqlite_master WHERE type='trigger' AND tbl_name='cas_refs'").Scan(&triggers).Error; err != nil {
			return err
		}
		if triggers != 0 {
			return fmt.Errorf("cas_refs has custom triggers; migration refused")
		}
		var indexes []string
		if err := tx.Raw("SELECT sql FROM sqlite_master WHERE type='index' AND tbl_name='cas_refs' AND sql IS NOT NULL ORDER BY name").Scan(&indexes).Error; err != nil {
			return err
		}
		for _, q := range []string{
			"CREATE TABLE cas_refs_compact (owner_hash TEXT NOT NULL, target_hash TEXT NOT NULL, PRIMARY KEY(owner_hash,target_hash)) WITHOUT ROWID",
			"INSERT INTO cas_refs_compact SELECT owner_hash,target_hash FROM cas_refs",
		} {
			if err := tx.Exec(q).Error; err != nil {
				return err
			}
		}
		var diff int64
		if err := tx.Raw("SELECT count(*) FROM (SELECT owner_hash,target_hash FROM cas_refs EXCEPT SELECT owner_hash,target_hash FROM cas_refs_compact)").Scan(&diff).Error; err != nil {
			return err
		}
		if diff != 0 {
			return fmt.Errorf("cas_refs copy mismatch")
		}
		for _, q := range append([]string{"DROP TABLE cas_refs", "ALTER TABLE cas_refs_compact RENAME TO cas_refs"}, indexes...) {
			if err := tx.Exec(q).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func verifiedCASValues(tx *gorm.DB, id string) (map[string]string, error) {
	if err := VerifyCASInventory(tx, id); err != nil {
		return nil, err
	}
	var pointers []casPayload
	if err := tx.Where("log_id = ?", id).Find(&pointers).Error; err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, p := range pointers {
		// Hidden rows also snapshot DB-resident attribution metadata. Verify its
		// CAS bytes, but never clear those metadata columns.
		_, known := payloadFieldSet[p.Field]
		if !known {
			for _, field := range strings.Fields("metadata provider model status timestamp selected_key_id selected_key_name virtual_key_id virtual_key_name user_id user_name team_id team_name team_ids team_names customer_id customer_name customer_ids customer_names business_unit_id business_unit_name business_unit_ids business_unit_names project_id project_name cost latency") {
				if p.Field == field {
					known = true
					break
				}
			}
		}
		if !known {
			return nil, fmt.Errorf("unknown CAS snapshot field %s", p.Field)
		}
		var blob casBlob
		if err := tx.Where("hash = ?", p.BlobHash).Take(&blob).Error; err != nil {
			return nil, err
		}
		raw, err := casDecodeBlob(blob, casManifestDomain)
		if err != nil {
			return nil, err
		}
		var manifest casManifest
		if err = json.Unmarshal(raw, &manifest); err != nil {
			return nil, err
		}
		expected := map[string]bool{}
		for _, part := range manifest.Parts {
			if part.Hash != "" {
				expected[part.Hash] = true
			}
		}
		var targetHashes []string
		hexLayout, err := casRefsHexLayout(tx)
		if err != nil {
			return nil, err
		}
		if hexLayout {
			if err = tx.Raw("SELECT target_hash FROM cas_refs WHERE owner_hash = ?", p.BlobHash).Scan(&targetHashes).Error; err != nil {
				return nil, err
			}
		} else {
			if err = tx.Raw("SELECT b.hash FROM cas_refs r JOIN cas_blobs b ON b.id = r.target_id WHERE r.owner_id = (SELECT id FROM cas_blobs WHERE hash = ?)", p.BlobHash).Scan(&targetHashes).Error; err != nil {
				return nil, err
			}
		}
		if len(targetHashes) != len(expected) {
			return nil, fmt.Errorf("CAS reference set mismatch for %s", id)
		}
		for _, th := range targetHashes {
			if !expected[th] {
				return nil, fmt.Errorf("unexpected CAS reference for %s", id)
			}
		}
		content, err := casReconstruct(raw, func(hash string) ([]byte, error) {
			var b casBlob
			if err := tx.Where("hash = ?", hash).Take(&b).Error; err != nil {
				return nil, err
			}
			return casDecodeBlob(b, casDataDomain)
		})
		if err != nil {
			return nil, err
		}
		values[p.Field] = string(content)
	}
	return values, nil
}

// CompactCAS verifies every row and its complete CAS inventory and bytes before
// clearing duplicates. Use on a stopped database copy. One transaction covers
// validation, cleanup and optional refs migration; errors leave history intact.
// Legacy bootstrap inventories cannot prove original completeness and fail closed.
func CompactCAS(ctx context.Context, db *gorm.DB, clearInline, compactRefs bool) (CompactCASReport, error) {
	var report CompactCASReport
	if db.Dialector.Name() != "sqlite" {
		return report, fmt.Errorf("CAS compaction supports SQLite only")
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if clearInline || compactRefs {
			if err := tx.Exec("UPDATE logs SET has_object=has_object WHERE 0").Error; err != nil {
				return err
			}
		}
		var check string
		if err := tx.Raw("PRAGMA integrity_check").Scan(&check).Error; err != nil {
			return err
		}
		if check != "ok" {
			return fmt.Errorf("SQLite integrity check failed")
		}
		if clearInline {
			var triggers int64
			if err := tx.Raw("SELECT count(*) FROM sqlite_master WHERE type='trigger' AND tbl_name='logs'").Scan(&triggers).Error; err != nil {
				return err
			}
			if triggers != 0 {
				return fmt.Errorf("logs has custom triggers; cleanup refused")
			}
		}
		h := sha256.New()
		var last string
		for {
			var rows []Log
			q := tx.Order("id").Limit(100)
			if last != "" {
				q = q.Where("id > ?", last)
			}
			if err := q.Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) == 0 {
				break
			}
			for _, row := range rows {
				var inv CASInventory
				if err := tx.Where("log_id = ?", row.ID).Take(&inv).Error; err != nil {
					return err
				}
				if inv.Provenance != "native" {
					return fmt.Errorf("log %s lacks native inventory evidence", row.ID)
				}
				values, err := verifiedCASValues(tx, row.ID)
				if err != nil {
					return fmt.Errorf("log %s: %w", row.ID, err)
				}
				payload := ExtractPayload(&row)
				updates := map[string]interface{}{}
				for field, value := range values {
					report.Fields++
					_, isPayload := payloadFieldSet[field]
					clearable := isPayload && field != "token_usage" && field != "cache_debug"
					if clearable {
						report.ClearedBytes += int64(len(payload[field]))
					}
					payload[field] = value
					if clearInline && clearable {
						updates[field] = ""
					}
				}
				encoded, err := json.Marshal(struct {
					ID      string
					Payload map[string]string
				}{row.ID, payload})
				if err != nil {
					return err
				}
				h.Write(encoded)
				h.Write([]byte{0})
				if len(updates) > 0 {
					if err := tx.Model(&Log{}).Where("id = ?", row.ID).UpdateColumns(updates).Error; err != nil {
						return err
					}
					var persisted Log
					if err := tx.Where("id = ?", row.ID).Take(&persisted).Error; err != nil {
						return err
					}
					expectedRow := row
					for field := range updates {
						clearPayloadField(&expectedRow, field)
					}
					if !reflect.DeepEqual(expectedRow, persisted) {
						return fmt.Errorf("row changed unexpectedly during cleanup")
					}
					after, err := verifiedCASValues(tx, row.ID)
					if err != nil {
						return err
					}
					a, _ := json.Marshal(values)
					b, _ := json.Marshal(after)
					if string(a) != string(b) {
						return fmt.Errorf("payload changed during cleanup")
					}
				}
				report.Logs++
			}
			last = rows[len(rows)-1].ID
		}
		if compactRefs {
			if err := MigrateCASRefs(tx); err != nil {
				return err
			}
		}
		report.PayloadSHA256 = hex.EncodeToString(h.Sum(nil))
		return nil
	})
	if err != nil {
		return CompactCASReport{}, err
	}
	return report, nil
}
