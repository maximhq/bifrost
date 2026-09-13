package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"github.com/maximhq/bifrost/framework/logstore"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func uri(path, query string) string {
	return (&url.URL{Scheme: "file", Path: path, RawQuery: query}).String()
}
func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func hashFile(path string) ([32]byte, error) {
	var out [32]byte
	f, e := os.Open(path)
	if e != nil {
		return out, e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return out, e
	}
	copy(out[:], h.Sum(nil))
	return out, nil
}
func snapshot(path string) error {
	st, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if !st.Mode().IsRegular() {
		return errors.New("snapshot must be regular, not a symlink")
	}
	for _, s := range []string{"-wal", "-shm", "-journal"} {
		if _, e := os.Lstat(path + s); !os.IsNotExist(e) {
			return errors.New("snapshot sidecar present or inaccessible")
		}
	}
	return nil
}
func integrity(db *sql.DB) error {
	r, e := db.Query("PRAGMA integrity_check")
	if e != nil {
		return e
	}
	defer r.Close()
	n := 0
	for r.Next() {
		var s string
		if e = r.Scan(&s); e != nil {
			return e
		}
		if s != "ok" {
			return errors.New("SQLite integrity check failed")
		}
		n++
	}
	if e = r.Err(); e != nil {
		return e
	}
	if n != 1 {
		return errors.New("invalid integrity result")
	}
	return nil
}
func restore(ctx context.Context, source, output string) (err error) {
	source, err = filepath.Abs(source)
	if err != nil {
		return err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return err
	}
	if err = snapshot(source); err != nil {
		return err
	}
	if _, err = os.Lstat(output); !os.IsNotExist(err) {
		return errors.New("output exists or is inaccessible")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, e := os.Lstat(output + suffix); !os.IsNotExist(e) {
			return errors.New("output sidecar present or inaccessible")
		}
	}
	parent := filepath.Dir(output)
	st, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode().Perm() != 0700 {
		return errors.New("output parent must be an existing 0700 directory")
	}
	before, err := hashFile(source)
	if err != nil {
		return err
	}
	src, err := sql.Open("sqlite3", uri(source, "mode=ro&immutable=1&_query_only=1"))
	if err != nil {
		return err
	}
	defer src.Close()
	if err = integrity(src); err != nil {
		return err
	}
	hasInventory, err := verifyInventories(src)
	if err != nil {
		return err
	}
	// Preserve the entire schema and metadata without ORM serialization.
	dir, err := os.MkdirTemp(parent, ".cas-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	stage := filepath.Join(dir, "logs.db")
	f, err := os.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		f.Close()
		return err
	}
	_, err = io.Copy(f, input)
	input.Close()
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite3", uri(stage, "mode=rw&_journal_mode=DELETE"))
	if err != nil {
		return err
	}
	defer db.Close()
	var bad int
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='trigger'").Scan(&bad); err != nil {
		return err
	}
	if bad != 0 {
		return errors.New("snapshot triggers unsupported")
	}
	for _, q := range []string{"SELECT count(*) FROM logs l WHERE has_object AND NOT EXISTS(SELECT 1 FROM cas_payloads p WHERE p.log_id=l.id)", "SELECT count(*) FROM cas_payloads p LEFT JOIN logs l ON l.id=p.log_id WHERE l.id IS NULL OR NOT l.has_object"} {
		if err = db.QueryRow(q).Scan(&bad); err != nil {
			return err
		}
		if bad != 0 {
			return errors.New("CAS root/pointer invariant failed")
		}
	}
	objects := map[string]logstore.CASAnalysisObject{}
	rows, err := src.QueryContext(ctx, "SELECT hash,codec,orig_len,data FROM cas_blobs")
	if err != nil {
		return err
	}
	for rows.Next() {
		var o logstore.CASAnalysisObject
		if err = rows.Scan(&o.Hash, &o.Codec, &o.OrigLen, &o.Data); err != nil {
			rows.Close()
			return err
		}
		objects[o.Hash] = o
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Hidden snapshots contain metadata copies: validate but never replay them.
	columns := map[string]bool{}
	cr, e := src.Query("PRAGMA table_info(logs)")
	if e != nil {
		return e
	}
	for cr.Next() {
		var cid, nn, pk int
		var name, typ string
		var def any
		if e = cr.Scan(&cid, &name, &typ, &nn, &def, &pk); e != nil {
			cr.Close()
			return e
		}
		columns[name] = true
	}
	err = cr.Err()
	cr.Close()
	if err != nil {
		return err
	}
	codec := logstore.NewCASObjectStoreForAnalysis(objects)
	allowed := map[string]bool{}
	for _, s := range logstore.PayloadColumnsForAnalysis() {
		allowed[s] = true
	}
	rows, err = src.QueryContext(ctx, "SELECT p.log_id,p.field,p.blob_hash,l.content_hidden FROM cas_payloads p JOIN logs l ON l.id=p.log_id ORDER BY p.log_id,p.field")
	if err != nil {
		return err
	}
	defer rows.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for rows.Next() {
		var id, field, hash string
		var hidden bool
		if err = rows.Scan(&id, &field, &hash, &hidden); err != nil {
			return err
		}
		if !allowed[field] && (!hidden || !columns[field]) {
			return errors.New("unknown CAS payload field")
		}
		raw, e := codec.Reconstruct(hash)
		if e != nil {
			return errors.New("CAS payload integrity/reconstruction failed")
		}
		if hidden {
			continue
		}
		if _, err = tx.ExecContext(ctx, "UPDATE logs SET "+quote(field)+"=? WHERE id=?", string(raw), id); err != nil {
			return err
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	rows.Close()
	// Hidden content remains in same-database CAS, never exposed to legacy readers.
	// All hidden pointers are validated above, including content not being served.
	if _, err = tx.Exec("DELETE FROM cas_payloads WHERE log_id IN (SELECT id FROM logs WHERE NOT content_hidden)"); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE logs SET has_object=0 WHERE NOT content_hidden"); err != nil {
		return err
	}
	if hasInventory {
		if _, err = tx.Exec("UPDATE cas_inventories SET entries='{}' WHERE log_id IN (SELECT id FROM logs WHERE NOT content_hidden)"); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if err = integrity(db); err != nil {
		return err
	}
	if err = db.Close(); err != nil {
		return err
	}
	if err = snapshot(source); err != nil {
		return err
	}
	after, err := hashFile(source)
	if err != nil {
		return err
	}
	if before != after {
		return errors.New("source changed during recovery")
	}
	f, err = os.OpenFile(stage, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	f.Close()
	if err != nil {
		return err
	}
	// Atomic no-replace publication; failed runs leave no output database.
	if err = os.Link(stage, output); err != nil {
		return err
	}
	parentFile, e := os.Open(parent)
	if e != nil {
		return e
	}
	defer parentFile.Close()
	return parentFile.Sync()
}
func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("log-cas-restore", flag.ContinueOnError)
	source := fs.String("db", "", "offline SQLite CAS snapshot, no WAL/SHM/journal")
	output := fs.String("output", "", "new SQLite DB in existing 0700 directory (file 0600)")
	method := fs.String("snapshot-method", "", "online-backup or vacuum-into; operator attestation, never raw-copy a live DB")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "SQLite-only CAS recovery. PostgreSQL unsupported and not validated.\nVisible selected payload bytes restored exactly. Hidden content retained in same-database CAS for new readers, withheld from legacy readers. No object storage.\nLegacy export is NOT a full service rollback: legacy billing cannot hydrate CAS-only hidden pricing inputs. Use a retained CAS-compatible binary for full service rollback.\nLegacy snapshots without field inventory cannot detect loss of only some pointers; legacy_bootstrap inventory cannot prove pre-bootstrap completeness. Encoded unique CAS objects are loaded into memory; unreachable blobs are retained.\nUsage: log-cas-restore --db /private/snapshot.db --snapshot-method online-backup --output /private/recovered/logs.db")
		fs.PrintDefaults()
	}
	if e := fs.Parse(args); e != nil {
		return e
	}
	if *source == "" || *output == "" || fs.NArg() != 0 {
		return errors.New("--db and --output required; no positional arguments")
	}
	if *method != "online-backup" && *method != "vacuum-into" {
		return errors.New("invalid --snapshot-method")
	}
	return restore(ctx, *source, *output)
}
func main() {
	if e := run(context.Background(), os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, "restore failed:", e)
		os.Exit(1)
	}
	fmt.Println("SQLite recovery complete; source unchanged; hidden payloads retained in CAS")
}
