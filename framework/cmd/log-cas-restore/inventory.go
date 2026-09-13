package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// verifyInventories validates the durable field boundary before any recovery
// mutation. Legacy bootstrap proves only the boundary observed at bootstrap.
func verifyInventories(db *sql.DB) (bool, error) {
	var tables int
	if e := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('cas_inventories','cas_inventory_state')").Scan(&tables); e != nil {
		return false, e
	}
	if tables == 0 {
		return false, nil
	}
	if tables != 2 {
		return false, errors.New("incomplete CAS inventory schema")
	}
	var version int
	if e := db.QueryRow("SELECT version FROM cas_inventory_state WHERE id=1").Scan(&version); e != nil {
		return false, e
	}
	if version != 1 {
		return false, errors.New("unsupported CAS inventory state")
	}
	inventories := map[string]map[string]string{}
	rows, e := db.Query("SELECT log_id,version,provenance,entries FROM cas_inventories")
	if e != nil {
		return false, e
	}
	for rows.Next() {
		var id, provenance, raw string
		var v int
		if e = rows.Scan(&id, &v, &provenance, &raw); e != nil {
			rows.Close()
			return false, e
		}
		if v != 1 || (provenance != "native" && provenance != "legacy_bootstrap") {
			rows.Close()
			return false, errors.New("unsupported CAS inventory")
		}
		var fields map[string]string
		if e = json.Unmarshal([]byte(raw), &fields); e != nil || fields == nil {
			rows.Close()
			return false, errors.New("invalid CAS inventory entries")
		}
		inventories[id] = fields
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return false, e
	}
	rows, e = db.Query("SELECT log_id,field,blob_hash FROM cas_payloads")
	if e != nil {
		return false, e
	}
	for rows.Next() {
		var id, field, hash string
		if e = rows.Scan(&id, &field, &hash); e != nil {
			rows.Close()
			return false, e
		}
		fields, ok := inventories[id]
		if !ok || fields[field] != hash {
			rows.Close()
			return false, errors.New("CAS inventory pointer mismatch")
		}
		delete(fields, field)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return false, e
	}
	for _, fields := range inventories {
		if len(fields) != 0 {
			return false, errors.New("CAS inventory pointer missing")
		}
	}
	var missing int
	if e = db.QueryRow("SELECT count(*) FROM logs l WHERE NOT EXISTS(SELECT 1 FROM cas_inventories i WHERE i.log_id=l.id)").Scan(&missing); e != nil {
		return false, e
	}
	if missing != 0 {
		return false, fmt.Errorf("CAS inventory missing for %d roots", missing)
	}
	return true, nil
}
