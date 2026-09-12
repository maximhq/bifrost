package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/maximhq/bifrost/framework/logstore"
)

const (
	reportSchemaVersion = "1"
	batchRows           = 100
)

type stringList []string

func (s *stringList) String() string         { return strings.Join(*s, ",") }
func (s *stringList) Set(value string) error { *s = append(*s, value); return nil }

type options struct {
	db, snapshotMethod, workDir, output string
	minFieldBytes, minChunkBytes        int
	excludeFields                       stringList
	verifyExisting                      bool
}

type physicalStats struct {
	FileBytes       int64 `json:"file_bytes"`
	PageSize        int64 `json:"page_size"`
	PageCount       int64 `json:"page_count"`
	FreelistPages   int64 `json:"freelist_pages"`
	DBStatAvailable bool  `json:"dbstat_available"`
	TableBytes      int64 `json:"table_bytes,omitempty"`
	IndexBytes      int64 `json:"index_bytes,omitempty"`
}
type verificationStats struct {
	CheckedRows   int64 `json:"checked_rows"`
	CheckedFields int64 `json:"checked_fields"`
	CheckedBytes  int64 `json:"checked_bytes"`
	Mismatches    int64 `json:"mismatches"`
}
type schemeReport struct {
	Name          string            `json:"name"`
	Physical      physicalStats     `json:"physical"`
	Rows          int64             `json:"rows"`
	Blobs         int64             `json:"blobs,omitempty"`
	Pointers      int64             `json:"pointers,omitempty"`
	Refs          int64             `json:"refs,omitempty"`
	TransformNS   int64             `json:"transform_ns"`
	CompactNS     int64             `json:"compact_ns"`
	VerifyNS      int64             `json:"verify_ns"`
	WALPeakBytes  int64             `json:"wal_peak_bytes"`
	WorkPeakBytes int64             `json:"work_peak_bytes"`
	Verification  verificationStats `json:"verification"`
}
type report struct {
	SchemaVersion  string         `json:"schema_version"`
	SnapshotMethod string         `json:"snapshot_method"`
	InputSHA256    string         `json:"input_sha256"`
	InputUnchanged bool           `json:"input_unchanged"`
	InputFileBytes int64          `json:"input_file_bytes"`
	Rows           int64          `json:"rows"`
	Config         map[string]any `json:"config"`
	Schemes        []schemeReport `json:"schemes"`
	Limitations    []string       `json:"limitations"`
}

func parseOptions(args []string) (options, error) {
	fs := flag.NewFlagSet("log-cas-materialize", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var o options
	fs.StringVar(&o.db, "db", "", "offline SQLite snapshot")
	fs.StringVar(&o.snapshotMethod, "snapshot-method", "", "online-backup or vacuum-into")
	fs.StringVar(&o.workDir, "work-dir", "", "existing empty work directory")
	fs.StringVar(&o.output, "output", "", "new aggregate JSON report")
	fs.IntVar(&o.minFieldBytes, "min-field-bytes", 1024, "field threshold")
	fs.IntVar(&o.minChunkBytes, "min-chunk-bytes", 256, "chunk threshold")
	fs.Var(&o.excludeFields, "exclude-field", "payload column to keep row-resident")
	fs.BoolVar(&o.verifyExisting, "verify-existing", false, "verify existing current.db, field-zstd.db and cas-manifest.db without rewriting them")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.db == "" || o.workDir == "" || o.output == "" {
		return o, errors.New("--db, --work-dir and --output are required")
	}
	if o.snapshotMethod != "online-backup" && o.snapshotMethod != "vacuum-into" {
		return o, errors.New("invalid --snapshot-method")
	}
	if o.minFieldBytes <= 0 || o.minChunkBytes <= 0 {
		return o, errors.New("thresholds must be positive")
	}
	allowed := map[string]bool{}
	for _, column := range logstore.CASPayloadColumnsForAnalysis() {
		allowed[column] = true
	}
	seen := map[string]bool{}
	for _, column := range o.excludeFields {
		if !allowed[column] {
			return o, fmt.Errorf("invalid exclude field: %s", column)
		}
		if seen[column] {
			return o, fmt.Errorf("duplicate exclude field: %s", column)
		}
		seen[column] = true
	}
	sort.Strings(o.excludeFields)
	return o, nil
}

func quote(identifier string) string { return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"` }
func sqliteURI(path string, query string) string {
	absolute, _ := filepath.Abs(path)
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(absolute), RawQuery: query}).String()
}
func readDSN(path string) string  { return sqliteURI(path, "mode=ro&immutable=1&_query_only=1") }
func cloneDSN(path string) string { return sqliteURI(path, "mode=ro&immutable=1") }
func fileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func hasSidecar(path string) (bool, error) {
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	return false, nil
}
func quickCheck(db *sql.DB) error {
	var result string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return errors.New("quick_check failed")
	}
	return nil
}
func tableCount(db *sql.DB, table string) (int64, error) {
	var count int64
	err := db.QueryRow("SELECT COUNT(*) FROM " + quote(table)).Scan(&count)
	return count, err
}
func snapshotColumns(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query("PRAGMA table_info(logs)")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primary int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primary); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, errors.New("logs table missing")
	}
	return columns, nil
}
func payloadSetup(source *sql.DB, o options) ([]string, map[string]bool, error) {
	available, err := snapshotColumns(source)
	if err != nil {
		return nil, nil, err
	}
	columns := []string{}
	for _, column := range logstore.PayloadColumnsForAnalysis() {
		if available[column] {
			columns = append(columns, column)
		}
	}
	if len(columns) == 0 {
		return nil, nil, errors.New("no supported payload columns")
	}
	eligible := map[string]bool{}
	for _, column := range logstore.CASPayloadColumnsForAnalysis() {
		eligible[column] = true
	}
	for _, column := range o.excludeFields {
		delete(eligible, column)
	}
	return columns, eligible, nil
}
func streamLogs(source *sql.DB, columns []string) (*sql.Rows, error) {
	selected := append([]string{"id"}, columns...)
	for i := range selected {
		selected[i] = quote(selected[i])
	}
	return source.Query("SELECT " + strings.Join(selected, ",") + " FROM logs ORDER BY id")
}
func sampleWorkBytes(dir string, current int64) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			if info, infoErr := entry.Info(); infoErr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	if total > current {
		return total
	}
	return current
}
func sampleWAL(path string, current int64) int64 {
	if info, err := os.Stat(path + "-wal"); err == nil && info.Size() > current {
		return info.Size()
	}
	return current
}
func validatePaths(o options) (os.FileInfo, string, error) {
	info, err := os.Stat(o.db)
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() {
		return nil, "", errors.New("snapshot must be regular file")
	}
	if sidecar, err := hasSidecar(o.db); err != nil {
		return nil, "", err
	} else if sidecar {
		return nil, "", errors.New("snapshot sidecar present")
	}
	entries, err := os.ReadDir(o.workDir)
	if err != nil {
		return nil, "", err
	}
	if !o.verifyExisting && len(entries) != 0 {
		return nil, "", errors.New("work-dir must be empty")
	}
	if o.verifyExisting {
		required := map[string]bool{"current.db": false, "field-zstd.db": false, "cas-manifest.db": false}
		for _, entry := range entries {
			if _, ok := required[entry.Name()]; !ok {
				return nil, "", errors.New("verify-existing work-dir contains unexpected files")
			}
			if entry.IsDir() {
				return nil, "", errors.New("verify-existing work-dir contains a directory")
			}
			required[entry.Name()] = true
		}
		for name, found := range required {
			if !found {
				return nil, "", fmt.Errorf("verify-existing missing %s", name)
			}
		}
	}
	dbAbs, _ := filepath.Abs(o.db)
	workAbs, _ := filepath.Abs(o.workDir)
	outputAbs, _ := filepath.Abs(o.output)
	if filepath.Clean(filepath.Dir(dbAbs)) == filepath.Clean(workAbs) {
		return nil, "", errors.New("work-dir must not be snapshot directory")
	}
	if filepath.Clean(dbAbs) == filepath.Clean(outputAbs) {
		return nil, "", errors.New("output must not overwrite snapshot")
	}
	if _, err := os.Lstat(o.output); !os.IsNotExist(err) {
		return nil, "", errors.New("output must not exist")
	}
	hash, err := fileHash(o.db)
	return info, hash, err
}

func cloneCompact(sourcePath, targetPath string) (time.Duration, error) {
	source, err := sql.Open("sqlite3", cloneDSN(sourcePath))
	if err != nil {
		return 0, err
	}
	defer source.Close()
	start := time.Now()
	_, err = source.Exec("VACUUM INTO ?", targetPath)
	if err == nil {
		err = os.Chmod(targetPath, 0o600)
	}
	return time.Since(start), err
}
func openWork(sourcePath, workPath string) (*sql.DB, error) {
	if _, err := cloneCompact(sourcePath, workPath); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", workPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL", "PRAGMA cache_size=10000", "PRAGMA wal_autocheckpoint=1000"} {
		if _, err = db.Exec(pragma); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}
func compactWork(db *sql.DB, workPath, finalPath string, walPeak int64) (time.Duration, int64, error) {
	walPeak = sampleWAL(workPath, walPeak)
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return 0, walPeak, err
	}
	if err := db.Close(); err != nil {
		return 0, walPeak, err
	}
	source, err := sql.Open("sqlite3", cloneDSN(workPath))
	if err != nil {
		return 0, walPeak, err
	}
	start := time.Now()
	_, err = source.Exec("VACUUM INTO ?", finalPath)
	closeErr := source.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(finalPath, 0o600)
	}
	return time.Since(start), walPeak, err
}
func cleanupWork(path string) {
	_ = os.Remove(path)
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
}
func physical(path string) (physicalStats, error) {
	db, err := sql.Open("sqlite3", readDSN(path))
	if err != nil {
		return physicalStats{}, err
	}
	defer db.Close()
	if err = quickCheck(db); err != nil {
		return physicalStats{}, err
	}
	var pageSize, pageCount, free int64
	if err = db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		return physicalStats{}, err
	}
	if err = db.QueryRow("PRAGMA page_count").Scan(&pageCount); err != nil {
		return physicalStats{}, err
	}
	if err = db.QueryRow("PRAGMA freelist_count").Scan(&free); err != nil {
		return physicalStats{}, err
	}
	var tableBytes, indexBytes int64
	rows, statErr := db.Query("SELECT CASE WHEN name IN (SELECT name FROM sqlite_master WHERE type='index') OR name LIKE 'sqlite_autoindex_%' THEN 'index' ELSE 'table' END, SUM(pgsize) FROM dbstat GROUP BY 1")
	dbstatAvailable := statErr == nil
	if dbstatAvailable {
		defer rows.Close()
		for rows.Next() {
			var kind string
			var value int64
			if err = rows.Scan(&kind, &value); err != nil {
				return physicalStats{}, err
			}
			if kind == "index" {
				indexBytes += value
			} else {
				tableBytes += value
			}
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return physicalStats{}, err
	}
	return physicalStats{info.Size(), pageSize, pageCount, free, dbstatAvailable, tableBytes, indexBytes}, nil
}

func updateClearedFields(tx *sql.Tx, id string, fields []string, hasObject bool) error {
	if len(fields) == 0 {
		return nil
	}
	assignments := make([]string, 0, len(fields)+1)
	for _, field := range fields {
		assignments = append(assignments, quote(field)+"=''")
	}
	if hasObject {
		assignments = append(assignments, "has_object=1")
	}
	_, err := tx.Exec("UPDATE logs SET "+strings.Join(assignments, ",")+" WHERE id=?", id)
	return err
}

func transformZstd(ctx context.Context, sourcePath, workPath, finalPath string, o options) (result schemeReport, err error) {
	source, err := sql.Open("sqlite3", readDSN(sourcePath))
	if err != nil {
		return result, err
	}
	defer source.Close()
	columns, eligible, err := payloadSetup(source, o)
	if err != nil {
		return result, err
	}
	db, err := openWork(sourcePath, workPath)
	if err != nil {
		return result, err
	}
	defer func() {
		_ = db.Close()
		if err != nil {
			cleanupWork(workPath)
		}
	}()
	if _, err = db.Exec("CREATE TABLE field_zstd_payloads(log_id TEXT NOT NULL, field TEXT NOT NULL, codec TEXT NOT NULL, orig_len INTEGER NOT NULL, data BLOB NOT NULL, PRIMARY KEY(log_id,field))"); err != nil {
		return result, err
	}
	rows, err := streamLogs(source, columns)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	start := time.Now()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	rowsInBatch := 0
	var blobs, walPeak, workPeak int64
	for rows.Next() {
		values := make([]any, len(columns)+1)
		rawValues := make([]sql.RawBytes, len(values))
		for i := range values {
			values[i] = &rawValues[i]
		}
		if err = rows.Scan(values...); err != nil {
			_ = tx.Rollback()
			return result, err
		}
		id := string(rawValues[0])
		cleared := []string{}
		for i, column := range columns {
			raw := rawValues[i+1]
			if raw == nil || len(raw) == 0 || !eligible[column] || len(raw) < o.minFieldBytes {
				continue
			}
			encoded := logstore.CompressFieldDataForAnalysis(raw)
			if _, err = tx.Exec("INSERT INTO field_zstd_payloads(log_id,field,codec,orig_len,data) VALUES(?,?,?,?,?)", id, column, "zstd", len(raw), encoded); err != nil {
				_ = tx.Rollback()
				return result, err
			}
			cleared = append(cleared, column)
			blobs++
		}
		if err = updateClearedFields(tx, id, cleared, false); err != nil {
			_ = tx.Rollback()
			return result, err
		}
		rowsInBatch++
		if rowsInBatch == batchRows {
			if err = tx.Commit(); err != nil {
				return result, err
			}
			walPeak = sampleWAL(workPath, walPeak)
			workPeak = sampleWorkBytes(filepath.Dir(workPath), workPeak)
			tx, err = db.BeginTx(ctx, nil)
			if err != nil {
				return result, err
			}
			rowsInBatch = 0
		}
	}
	if err = rows.Err(); err != nil {
		_ = tx.Rollback()
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	result.Name = "field-zstd"
	result.Blobs = blobs
	result.TransformNS = time.Since(start).Nanoseconds()
	result.WorkPeakBytes = sampleWorkBytes(filepath.Dir(workPath), workPeak)
	var compactDuration time.Duration
	compactDuration, result.WALPeakBytes, err = compactWork(db, workPath, finalPath, walPeak)
	if err != nil {
		return result, err
	}
	result.CompactNS = compactDuration.Nanoseconds()
	cleanupWork(workPath)
	result.VerifyNS, result.Verification, err = verifyZstd(sourcePath, finalPath, columns)
	if err != nil {
		return result, err
	}
	target, err := sql.Open("sqlite3", readDSN(finalPath))
	if err != nil {
		return result, err
	}
	defer target.Close()
	result.Rows, err = tableCount(target, "logs")
	if err != nil {
		return result, err
	}
	result.Physical, err = physical(finalPath)
	return result, err
}

func transformCAS(ctx context.Context, sourcePath, workPath, finalPath string, o options) (result schemeReport, err error) {
	source, err := sql.Open("sqlite3", readDSN(sourcePath))
	if err != nil {
		return result, err
	}
	defer source.Close()
	columns, eligible, err := payloadSetup(source, o)
	if err != nil {
		return result, err
	}
	db, err := openWork(sourcePath, workPath)
	if err != nil {
		return result, err
	}
	defer func() {
		_ = db.Close()
		if err != nil {
			cleanupWork(workPath)
		}
	}()
	for _, statement := range []string{
		"CREATE TABLE cas_blobs(hash TEXT PRIMARY KEY, codec TEXT NOT NULL, orig_len INTEGER NOT NULL, data BLOB NOT NULL, created_at INTEGER)",
		"CREATE TABLE cas_refs(owner_hash TEXT NOT NULL, target_hash TEXT NOT NULL, PRIMARY KEY(owner_hash,target_hash))",
		"CREATE TABLE cas_payloads(log_id TEXT NOT NULL, field TEXT NOT NULL, blob_hash TEXT NOT NULL, PRIMARY KEY(log_id,field))",
	} {
		if _, err = db.Exec(statement); err != nil {
			return result, err
		}
	}
	rows, err := streamLogs(source, columns)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	start := time.Now()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	rowsInBatch := 0
	var pointers, walPeak, workPeak int64
	for rows.Next() {
		values := make([]any, len(columns)+1)
		rawValues := make([]sql.RawBytes, len(values))
		for i := range values {
			values[i] = &rawValues[i]
		}
		if err = rows.Scan(values...); err != nil {
			_ = tx.Rollback()
			return result, err
		}
		id := string(rawValues[0])
		cleared := []string{}
		for i, column := range columns {
			raw := rawValues[i+1]
			if raw == nil || len(raw) == 0 || !eligible[column] || len(raw) < o.minFieldBytes {
				continue
			}
			analysis, analysisErr := logstore.AnalyzeCASField(raw, o.minChunkBytes)
			if analysisErr != nil {
				_ = tx.Rollback()
				return result, analysisErr
			}
			for _, object := range analysis.Objects {
				if _, err = tx.Exec("INSERT OR IGNORE INTO cas_blobs(hash,codec,orig_len,data,created_at) VALUES(?,?,?,?,?)", object.Hash, object.Codec, object.OrigLen, object.Data, time.Now().Unix()); err != nil {
					_ = tx.Rollback()
					return result, err
				}
			}
			for _, ref := range analysis.References {
				if _, err = tx.Exec("INSERT OR IGNORE INTO cas_refs(owner_hash,target_hash) VALUES(?,?)", ref.OwnerHash, ref.TargetHash); err != nil {
					_ = tx.Rollback()
					return result, err
				}
			}
			if _, err = tx.Exec("INSERT INTO cas_payloads(log_id,field,blob_hash) VALUES(?,?,?)", id, column, analysis.ManifestHash()); err != nil {
				_ = tx.Rollback()
				return result, err
			}
			cleared = append(cleared, column)
			pointers++
		}
		if err = updateClearedFields(tx, id, cleared, len(cleared) > 0); err != nil {
			_ = tx.Rollback()
			return result, err
		}
		rowsInBatch++
		if rowsInBatch == batchRows {
			if err = tx.Commit(); err != nil {
				return result, err
			}
			walPeak = sampleWAL(workPath, walPeak)
			workPeak = sampleWorkBytes(filepath.Dir(workPath), workPeak)
			tx, err = db.BeginTx(ctx, nil)
			if err != nil {
				return result, err
			}
			rowsInBatch = 0
		}
	}
	if err = rows.Err(); err != nil {
		_ = tx.Rollback()
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	result.Name = "cas-manifest"
	result.Pointers = pointers
	result.TransformNS = time.Since(start).Nanoseconds()
	result.WorkPeakBytes = sampleWorkBytes(filepath.Dir(workPath), workPeak)
	var compactDuration time.Duration
	compactDuration, result.WALPeakBytes, err = compactWork(db, workPath, finalPath, walPeak)
	if err != nil {
		return result, err
	}
	result.CompactNS = compactDuration.Nanoseconds()
	cleanupWork(workPath)
	result.VerifyNS, result.Verification, err = verifyCAS(sourcePath, finalPath, columns)
	if err != nil {
		return result, err
	}
	target, err := sql.Open("sqlite3", readDSN(finalPath))
	if err != nil {
		return result, err
	}
	defer target.Close()
	result.Rows, err = tableCount(target, "logs")
	if err != nil {
		return result, err
	}
	result.Blobs, err = tableCount(target, "cas_blobs")
	if err != nil {
		return result, err
	}
	result.Refs, err = tableCount(target, "cas_refs")
	if err != nil {
		return result, err
	}
	result.Physical, err = physical(finalPath)
	return result, err
}

func scanComparableRows(db *sql.DB, columns []string) (*sql.Rows, error) {
	selected := append([]string{"id"}, columns...)
	for i := range selected {
		selected[i] = quote(selected[i])
	}
	return db.Query("SELECT " + strings.Join(selected, ",") + " FROM logs ORDER BY id")
}
func compareFullRows(source, target *sql.DB, columns []string) (verificationStats, error) {
	left, err := scanComparableRows(source, columns)
	if err != nil {
		return verificationStats{}, err
	}
	defer left.Close()
	right, err := scanComparableRows(target, columns)
	if err != nil {
		return verificationStats{}, err
	}
	defer right.Close()
	var result verificationStats
	for left.Next() {
		if !right.Next() {
			return result, errors.New("target has fewer logs")
		}
		lv := make([]any, len(columns)+1)
		rv := make([]any, len(columns)+1)
		lb := make([]sql.RawBytes, len(lv))
		rb := make([]sql.RawBytes, len(rv))
		for i := range lv {
			lv[i] = &lb[i]
			rv[i] = &rb[i]
		}
		if err = left.Scan(lv...); err != nil {
			return result, err
		}
		if err = right.Scan(rv...); err != nil {
			return result, err
		}
		result.CheckedRows++
		for i := 1; i < len(lv); i++ {
			result.CheckedFields++
			if lb[i] != nil {
				result.CheckedBytes += int64(len(lb[i]))
			}
			if (lb[i] == nil) != (rb[i] == nil) || !bytes.Equal(lb[i], rb[i]) {
				result.Mismatches++
			}
		}
	}
	if right.Next() {
		return result, errors.New("target has extra logs")
	}
	if err = left.Err(); err != nil {
		return result, err
	}
	if err = right.Err(); err != nil {
		return result, err
	}
	return result, nil
}
func verifyZstd(sourcePath, targetPath string, columns []string) (int64, verificationStats, error) {
	start := time.Now()
	source, _ := sql.Open("sqlite3", readDSN(sourcePath))
	target, _ := sql.Open("sqlite3", readDSN(targetPath))
	defer source.Close()
	defer target.Close()
	rows, err := target.Query("SELECT log_id,field,orig_len,data FROM field_zstd_payloads ORDER BY log_id,field")
	if err != nil {
		return 0, verificationStats{}, err
	}
	defer rows.Close()
	var transformed verificationStats
	for rows.Next() {
		var id, column string
		var n int64
		var data []byte
		if err = rows.Scan(&id, &column, &n, &data); err != nil {
			return 0, transformed, err
		}
		var original []byte
		if err = source.QueryRow("SELECT "+quote(column)+" FROM logs WHERE id=?", id).Scan(&original); err != nil {
			return 0, transformed, err
		}
		decoded, decodeErr := logstore.DecompressFieldDataForAnalysis(data, n)
		transformed.CheckedFields++
		transformed.CheckedBytes += n
		if decodeErr != nil || !bytes.Equal(original, decoded) {
			transformed.Mismatches++
		}
	}
	rowStats, err := compareResidentWithTransforms(source, target, columns, "field_zstd_payloads")
	if err != nil {
		return 0, transformed, err
	}
	transformed.CheckedRows = rowStats.CheckedRows
	transformed.CheckedFields += rowStats.CheckedFields
	transformed.CheckedBytes += rowStats.CheckedBytes
	transformed.Mismatches += rowStats.Mismatches
	if transformed.Mismatches > 0 {
		return 0, transformed, errors.New("field-zstd verification failed")
	}
	return time.Since(start).Nanoseconds(), transformed, nil
}
func compareResidentWithTransforms(source, target *sql.DB, columns []string, pointerTable string) (verificationStats, error) {
	left, err := streamLogs(source, columns)
	if err != nil {
		return verificationStats{}, err
	}
	defer left.Close()
	right, err := streamLogs(target, columns)
	if err != nil {
		return verificationStats{}, err
	}
	defer right.Close()
	var result verificationStats
	for left.Next() {
		if !right.Next() {
			return result, errors.New("target has fewer logs")
		}
		leftValues := make([]any, len(columns)+1)
		rightValues := make([]any, len(columns)+1)
		leftBytes := make([]sql.RawBytes, len(leftValues))
		rightBytes := make([]sql.RawBytes, len(rightValues))
		for i := range leftValues {
			leftValues[i] = &leftBytes[i]
			rightValues[i] = &rightBytes[i]
		}
		if err = left.Scan(leftValues...); err != nil {
			return result, err
		}
		if err = right.Scan(rightValues...); err != nil {
			return result, err
		}
		result.CheckedRows++
		id := string(leftBytes[0])
		for i, column := range columns {
			var transformed int
			if err = target.QueryRow("SELECT COUNT(*) FROM "+pointerTable+" WHERE log_id=? AND field=?", id, column).Scan(&transformed); err != nil {
				return result, err
			}
			if transformed == 0 {
				result.CheckedFields++
				if leftBytes[i+1] != nil {
					result.CheckedBytes += int64(len(leftBytes[i+1]))
				}
				if (leftBytes[i+1] == nil) != (rightBytes[i+1] == nil) || !bytes.Equal(leftBytes[i+1], rightBytes[i+1]) {
					result.Mismatches++
				}
			} else if rightBytes[i+1] != nil && len(rightBytes[i+1]) != 0 {
				result.Mismatches++
			}
		}
	}
	if right.Next() {
		return result, errors.New("target has extra logs")
	}
	return result, nil
}
func verifyCAS(sourcePath, targetPath string, columns []string) (int64, verificationStats, error) {
	start := time.Now()
	source, _ := sql.Open("sqlite3", readDSN(sourcePath))
	target, _ := sql.Open("sqlite3", readDSN(targetPath))
	defer source.Close()
	defer target.Close()
	objects := map[string]logstore.CASAnalysisObject{}
	rows, err := target.Query("SELECT hash,codec,orig_len,data FROM cas_blobs")
	if err != nil {
		return 0, verificationStats{}, err
	}
	for rows.Next() {
		var o logstore.CASAnalysisObject
		if err = rows.Scan(&o.Hash, &o.Codec, &o.OrigLen, &o.Data); err != nil {
			rows.Close()
			return 0, verificationStats{}, err
		}
		o.CompressedBytes = int64(len(o.Data))
		objects[o.Hash] = o
	}
	rows.Close()
	objectStore := logstore.NewCASObjectStoreForAnalysis(objects)
	pointers, err := target.Query("SELECT log_id,field,blob_hash FROM cas_payloads ORDER BY log_id,field")
	if err != nil {
		return 0, verificationStats{}, err
	}
	defer pointers.Close()
	var transformed verificationStats
	for pointers.Next() {
		var id, column, mh string
		if err = pointers.Scan(&id, &column, &mh); err != nil {
			return 0, transformed, err
		}
		var original []byte
		if err = source.QueryRow("SELECT "+quote(column)+" FROM logs WHERE id=?", id).Scan(&original); err != nil {
			return 0, transformed, err
		}
		decoded, decodeErr := objectStore.Reconstruct(mh)
		transformed.CheckedFields++
		transformed.CheckedBytes += int64(len(original))
		if decodeErr != nil || !bytes.Equal(original, decoded) {
			transformed.Mismatches++
		}
	}
	rowStats, err := compareResidentWithTransforms(source, target, columns, "cas_payloads")
	if err != nil {
		return 0, transformed, err
	}
	transformed.CheckedRows = rowStats.CheckedRows
	transformed.CheckedFields += rowStats.CheckedFields
	transformed.CheckedBytes += rowStats.CheckedBytes
	transformed.Mismatches += rowStats.Mismatches
	if transformed.Mismatches > 0 {
		return 0, transformed, errors.New("CAS verification failed")
	}
	return time.Since(start).Nanoseconds(), transformed, nil
}

func verifyExistingSchemes(sourcePath, workDir string, columns []string) (schemeReport, schemeReport, schemeReport, error) {
	currentPath := filepath.Join(workDir, "current.db")
	zstdPath := filepath.Join(workDir, "field-zstd.db")
	casPath := filepath.Join(workDir, "cas-manifest.db")
	current := schemeReport{Name: "current"}
	zstd := schemeReport{Name: "field-zstd"}
	cas := schemeReport{Name: "cas-manifest"}
	var err error
	current.Physical, err = physical(currentPath)
	if err != nil {
		return current, zstd, cas, err
	}
	zstd.Physical, err = physical(zstdPath)
	if err != nil {
		return current, zstd, cas, err
	}
	cas.Physical, err = physical(casPath)
	if err != nil {
		return current, zstd, cas, err
	}
	source, err := sql.Open("sqlite3", readDSN(sourcePath))
	if err != nil {
		return current, zstd, cas, err
	}
	defer source.Close()
	currentDB, err := sql.Open("sqlite3", readDSN(currentPath))
	if err != nil {
		return current, zstd, cas, err
	}
	currentStart := time.Now()
	current.Verification, err = compareFullRows(source, currentDB, columns)
	current.VerifyNS = time.Since(currentStart).Nanoseconds()
	currentDB.Close()
	if err != nil || current.Verification.Mismatches > 0 {
		return current, zstd, cas, errors.New("current verification failed")
	}
	current.Rows, err = tableCount(source, "logs")
	if err != nil {
		return current, zstd, cas, err
	}
	zstd.VerifyNS, zstd.Verification, err = verifyZstd(sourcePath, zstdPath, columns)
	if err != nil {
		return current, zstd, cas, err
	}
	zstdDB, err := sql.Open("sqlite3", readDSN(zstdPath))
	if err != nil {
		return current, zstd, cas, err
	}
	zstd.Rows, err = tableCount(zstdDB, "logs")
	if err == nil {
		zstd.Blobs, err = tableCount(zstdDB, "field_zstd_payloads")
	}
	zstdDB.Close()
	if err != nil {
		return current, zstd, cas, err
	}
	cas.VerifyNS, cas.Verification, err = verifyCAS(sourcePath, casPath, columns)
	if err != nil {
		return current, zstd, cas, err
	}
	casDB, err := sql.Open("sqlite3", readDSN(casPath))
	if err != nil {
		return current, zstd, cas, err
	}
	cas.Rows, err = tableCount(casDB, "logs")
	if err == nil {
		cas.Blobs, err = tableCount(casDB, "cas_blobs")
	}
	if err == nil {
		cas.Pointers, err = tableCount(casDB, "cas_payloads")
	}
	if err == nil {
		cas.Refs, err = tableCount(casDB, "cas_refs")
	}
	casDB.Close()
	return current, zstd, cas, err
}

func run(ctx context.Context, args []string) (err error) {
	o, err := parseOptions(args)
	if err != nil {
		return err
	}
	info, before, err := validatePaths(o)
	if err != nil {
		return err
	}
	source, err := sql.Open("sqlite3", readDSN(o.db))
	if err != nil {
		return err
	}
	if err = quickCheck(source); err != nil {
		source.Close()
		return err
	}
	rows, err := tableCount(source, "logs")
	if err != nil {
		source.Close()
		return err
	}
	allColumns, _, err := payloadSetup(source, o)
	source.Close()
	if err != nil {
		return err
	}
	var current, zstd, cas schemeReport
	if o.verifyExisting {
		current, zstd, cas, err = verifyExistingSchemes(o.db, o.workDir, allColumns)
		if err != nil {
			return err
		}
	} else {
		currentPath := filepath.Join(o.workDir, "current.db")
		start := time.Now()
		_, err = cloneCompact(o.db, currentPath)
		if err != nil {
			return err
		}
		current = schemeReport{Name: "current", Rows: rows, CompactNS: time.Since(start).Nanoseconds()}
		current.Physical, err = physical(currentPath)
		if err != nil {
			return err
		}
		source, _ = sql.Open("sqlite3", readDSN(o.db))
		currentDB, _ := sql.Open("sqlite3", readDSN(currentPath))
		current.Verification, err = compareFullRows(source, currentDB, allColumns)
		source.Close()
		currentDB.Close()
		if err != nil || current.Verification.Mismatches > 0 {
			return errors.New("current verification failed")
		}
		zstd, err = transformZstd(ctx, o.db, filepath.Join(o.workDir, ".field-work.db"), filepath.Join(o.workDir, "field-zstd.db"), o)
		if err != nil {
			return err
		}
		cas, err = transformCAS(ctx, o.db, filepath.Join(o.workDir, ".cas-work.db"), filepath.Join(o.workDir, "cas-manifest.db"), o)
		if err != nil {
			return err
		}
	}
	after, err := fileHash(o.db)
	if err != nil {
		return err
	}
	if before != after {
		return errors.New("input snapshot changed")
	}
	if sidecar, _ := hasSidecar(o.db); sidecar {
		return errors.New("input sidecar appeared")
	}
	reportValue := report{SchemaVersion: reportSchemaVersion, SnapshotMethod: o.snapshotMethod, InputSHA256: before, InputUnchanged: true, InputFileBytes: info.Size(), Rows: rows, Config: map[string]any{"min_field_bytes": o.minFieldBytes, "min_chunk_bytes": o.minChunkBytes, "exclude_fields": []string(o.excludeFields)}, Schemes: []schemeReport{current, zstd, cas}, Limitations: []string{"field-zstd is a benchmark schema, not a production reader format", "peak disk is sampled at batch boundaries", "CAS verification loads unique encoded objects into memory", "PostgreSQL and online-write latency are outside this SQLite benchmark"}}
	file, err := os.OpenFile(o.output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(reportValue)
}
func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "log-cas-materialize:", err)
		os.Exit(1)
	}
}
