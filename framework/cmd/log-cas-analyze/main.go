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
	"runtime"
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/maximhq/bifrost/framework/logstore"
)

const reportSchemaVersion = "1"

var validSnapshotMethods = map[string]bool{"online-backup": true, "vacuum-into": true}

type fieldStats struct {
	Null     int64 `json:"null"`
	Empty    int64 `json:"empty"`
	NonEmpty int64 `json:"nonempty"`
	RawBytes int64 `json:"raw_bytes"`
}

type inputReport struct {
	SnapshotMethod string `json:"snapshot_method"`
	SHA256Before   string `json:"sha256_before"`
	SHA256After    string `json:"sha256_after"`
	Unchanged      bool   `json:"unchanged"`
	QuickCheck     string `json:"quick_check"`
	FileBytes      int64  `json:"file_bytes"`
	PageSize       int64  `json:"page_size"`
	PageCount      int64  `json:"page_count"`
	FreelistPages  int64  `json:"freelist_pages"`
	WALSidecar     bool   `json:"wal_sidecar"`
	SHMSidecar     bool   `json:"shm_sidecar"`
}

type schemeReport struct {
	Name                  string `json:"name"`
	EstimatedEncodedBytes int64  `json:"estimated_encoded_bytes"`
	UniqueObjects         int64  `json:"unique_objects,omitempty"`
	ObjectOccurrences     int64  `json:"object_occurrences,omitempty"`
	Pointers              int64  `json:"pointers,omitempty"`
	Refs                  int64  `json:"refs,omitempty"`
	Fallbacks             int64  `json:"fallbacks,omitempty"`
	CheckedFields         int64  `json:"checked_fields,omitempty"`
	CheckedBytes          int64  `json:"checked_bytes,omitempty"`
	RoundtripFailures     int64  `json:"roundtrip_failures,omitempty"`
}

type reportConfig struct {
	MinFieldBytes int      `json:"min_field_bytes"`
	MinChunkBytes int      `json:"min_chunk_bytes"`
	ExcludeFields []string `json:"exclude_fields"`
}

type report struct {
	SchemaVersion       string                 `json:"schema_version"`
	Config              reportConfig           `json:"config"`
	Input               inputReport            `json:"input"`
	Rows                int64                  `json:"rows"`
	ColumnsPresent      []string               `json:"columns_present"`
	ColumnsMissing      []string               `json:"columns_missing"`
	Fields              map[string]*fieldStats `json:"fields"`
	PayloadRawBytes     int64                  `json:"payload_raw_bytes"`
	ContentSummaryBytes int64                  `json:"content_summary_bytes"`
	Schemes             []schemeReport         `json:"schemes"`
	DurationNS          int64                  `json:"duration_ns"`
	HeapHighWaterBytes  uint64                 `json:"heap_high_water_bytes"`
	Limitations         []string               `json:"limitations"`
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type options struct {
	dbPath         string
	snapshotMethod string
	output         string
	minFieldBytes  int
	minChunkBytes  int
	excludeFields  stringList
}

func parseOptions(args []string) (options, error) {
	fs := flag.NewFlagSet("log-cas-analyze", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var o options
	fs.StringVar(&o.dbPath, "db", "", "offline SQLite snapshot path")
	fs.StringVar(&o.snapshotMethod, "snapshot-method", "", "online-backup or vacuum-into")
	fs.StringVar(&o.output, "output", "-", "JSON report path or - for stdout")
	fs.IntVar(&o.minFieldBytes, "min-field-bytes", 1024, "CAS field eligibility threshold")
	fs.IntVar(&o.minChunkBytes, "min-chunk-bytes", 256, "CAS chunk threshold")
	fs.Var(&o.excludeFields, "exclude-field", "payload column to keep row-resident; repeatable")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.dbPath == "" {
		return o, errors.New("--db is required")
	}
	if !validSnapshotMethods[o.snapshotMethod] {
		return o, errors.New("--snapshot-method must be online-backup or vacuum-into")
	}
	if o.minFieldBytes <= 0 || o.minChunkBytes <= 0 {
		return o, errors.New("thresholds must be positive")
	}
	allowed := map[string]bool{}
	for _, col := range logstore.CASPayloadColumnsForAnalysis() {
		allowed[col] = true
	}
	seen := map[string]bool{}
	for _, col := range o.excludeFields {
		if !allowed[col] {
			return o, fmt.Errorf("--exclude-field is not CAS-eligible: %s", col)
		}
		if seen[col] {
			return o, fmt.Errorf("duplicate --exclude-field: %s", col)
		}
		seen[col] = true
	}
	sort.Strings(o.excludeFields)
	return o, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func snapshotSidecars(path string) (wal, shm bool, err error) {
	for _, item := range []struct {
		suffix string
		found  *bool
	}{{"-wal", &wal}, {"-shm", &shm}} {
		if _, statErr := os.Stat(path + item.suffix); statErr == nil {
			*item.found = true
		} else if !os.IsNotExist(statErr) {
			return false, false, fmt.Errorf("check snapshot sidecar: %w", statErr)
		}
	}
	return wal, shm, nil
}

func validateSnapshot(path string) (os.FileInfo, error) {
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, "-wal") || strings.HasSuffix(lower, "-shm") {
		return nil, errors.New("database path must not be a WAL/SHM sidecar")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat snapshot: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("snapshot must be a regular file")
	}
	wal, shm, err := snapshotSidecars(path)
	if err != nil {
		return nil, err
	}
	if wal {
		return nil, errors.New("snapshot sidecar present: -wal")
	}
	if shm {
		return nil, errors.New("snapshot sidecar present: -shm")
	}
	return info, nil
}

func pragmaInt(db *sql.DB, name string) (int64, error) {
	var n int64
	err := db.QueryRow("PRAGMA " + name).Scan(&n)
	return n, err
}

func tableColumns(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query("PRAGMA table_info(logs)")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, errors.New("logs table is missing")
	}
	return cols, nil
}

func analyze(ctx context.Context, o options) (*report, error) {
	start := time.Now()
	info, err := validateSnapshot(o.dbPath)
	if err != nil {
		return nil, err
	}
	before, err := fileSHA256(o.dbPath)
	if err != nil {
		return nil, fmt.Errorf("hash snapshot before analysis: %w", err)
	}
	abs, err := filepath.Abs(o.dbPath)
	if err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: "mode=ro&immutable=1&_query_only=1"}).String()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open snapshot: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var quick string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&quick); err != nil {
		return nil, fmt.Errorf("quick_check: %w", err)
	}
	if quick != "ok" {
		return nil, errors.New("snapshot quick_check failed")
	}
	cols, err := tableColumns(db)
	if err != nil {
		return nil, err
	}
	allPayloadColumns := logstore.PayloadColumnsForAnalysis()
	casEligibleColumns := map[string]bool{}
	for _, col := range logstore.CASPayloadColumnsForAnalysis() {
		casEligibleColumns[col] = true
	}
	for _, col := range o.excludeFields {
		delete(casEligibleColumns, col)
	}
	present, missing := []string{}, []string{}
	for _, col := range allPayloadColumns {
		if cols[col] {
			present = append(present, col)
		} else {
			missing = append(missing, col)
		}
	}
	if len(present) == 0 {
		return nil, errors.New("logs table has no supported CAS payload columns")
	}
	selectCols := append([]string{}, present...)
	if cols["content_summary"] {
		selectCols = append(selectCols, "content_summary")
	}
	quoted := make([]string, len(selectCols))
	for i, col := range selectCols {
		quoted[i] = `"` + col + `"`
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin read transaction: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT "+strings.Join(quoted, ", ")+" FROM logs")
	if err != nil {
		return nil, fmt.Errorf("scan logs: %w", err)
	}
	defer rows.Close()

	r := &report{
		SchemaVersion: reportSchemaVersion,
		Config: reportConfig{
			MinFieldBytes: o.minFieldBytes,
			MinChunkBytes: o.minChunkBytes,
			ExcludeFields: append([]string(nil), o.excludeFields...),
		},
		Fields:         map[string]*fieldStats{},
		ColumnsPresent: present,
		ColumnsMissing: missing,
	}
	for _, col := range present {
		r.Fields[col] = &fieldStats{}
	}
	unique := map[string]int64{}
	uniqueRefs := map[string]struct{}{}
	var compressBytes, rowResidentBytes, pointers, fallback, checkedFields, checkedBytes, failures, occurrences int64
	for rows.Next() {
		r.Rows++
		values := make([]any, len(selectCols))
		slots := make([]sql.RawBytes, len(selectCols))
		for i := range values {
			values[i] = &slots[i]
		}
		if err := rows.Scan(values...); err != nil {
			return nil, fmt.Errorf("scan logs row: %w", err)
		}
		for i, col := range selectCols {
			if col == "content_summary" {
				if slots[i] != nil {
					r.ContentSummaryBytes += int64(len(slots[i]))
				}
				continue
			}
			st := r.Fields[col]
			if slots[i] == nil {
				st.Null++
				continue
			}
			raw := append([]byte(nil), slots[i]...)
			if len(raw) == 0 {
				st.Empty++
				continue
			}
			st.NonEmpty++
			st.RawBytes += int64(len(raw))
			r.PayloadRawBytes += int64(len(raw))
			if !casEligibleColumns[col] || len(raw) < o.minFieldBytes {
				rowResidentBytes += int64(len(raw))
				continue
			}
			compressBytes += logstore.CompressFieldForAnalysis(raw)
			field, err := logstore.AnalyzeCASField(raw, o.minChunkBytes)
			if err != nil {
				return nil, errors.New("CAS field analysis failed")
			}
			rebuilt, err := field.Reconstruct()
			checkedFields++
			checkedBytes += int64(len(raw))
			if err != nil || !bytes.Equal(raw, rebuilt) {
				failures++
				continue
			}
			if field.Fallback {
				fallback++
			}
			pointers++
			for _, ref := range field.References {
				uniqueRefs[ref.OwnerHash+"\x00"+ref.TargetHash] = struct{}{}
			}
			for _, object := range field.Objects {
				occurrences++
				if _, ok := unique[object.Hash]; !ok {
					unique[object.Hash] = object.CompressedBytes
				}
			}
		}
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		if ms.HeapAlloc > r.HeapHighWaterBytes {
			r.HeapHighWaterBytes = ms.HeapAlloc
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan logs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit read transaction: %w", err)
	}
	if failures != 0 {
		return nil, fmt.Errorf("CAS byte-exact verification failed for %d fields", failures)
	}
	var casBytes int64
	for _, n := range unique {
		casBytes += n
	}
	pageSize, err := pragmaInt(db, "page_size")
	if err != nil {
		return nil, err
	}
	pageCount, err := pragmaInt(db, "page_count")
	if err != nil {
		return nil, err
	}
	free, err := pragmaInt(db, "freelist_count")
	if err != nil {
		return nil, err
	}
	if err := db.Close(); err != nil {
		return nil, err
	}
	after, err := fileSHA256(o.dbPath)
	if err != nil {
		return nil, err
	}
	wal, shm, err := snapshotSidecars(o.dbPath)
	if err != nil {
		return nil, err
	}
	if wal || shm {
		return nil, errors.New("snapshot sidecar appeared during analysis")
	}
	r.Input = inputReport{SnapshotMethod: o.snapshotMethod, SHA256Before: before, SHA256After: after, Unchanged: before == after, QuickCheck: quick, FileBytes: info.Size(), PageSize: pageSize, PageCount: pageCount, FreelistPages: free, WALSidecar: wal, SHMSidecar: shm}
	if !r.Input.Unchanged {
		return nil, errors.New("snapshot changed during analysis")
	}
	r.Schemes = []schemeReport{
		{Name: "baseline-payload", EstimatedEncodedBytes: r.PayloadRawBytes},
		{Name: "field-zstd", EstimatedEncodedBytes: rowResidentBytes + compressBytes},
		{Name: "cas-manifest", EstimatedEncodedBytes: rowResidentBytes + casBytes, UniqueObjects: int64(len(unique)), ObjectOccurrences: occurrences, Pointers: pointers, Refs: int64(len(uniqueRefs)), Fallbacks: fallback, CheckedFields: checkedFields, CheckedBytes: checkedBytes, RoundtripFailures: failures},
	}
	r.DurationNS = time.Since(start).Nanoseconds()
	r.Limitations = []string{
		"encoded byte estimates exclude SQLite row, index, manifest-reference and page overhead",
		"input file bytes are reported separately and are not directly comparable to encoded payload estimates",
		"snapshot provenance is caller-declared and must be produced before this offline command runs",
		"single-pass peak heap is a Go heap observation, not process RSS",
	}
	return r, nil
}

func writeReport(w io.Writer, r *report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	o, err := parseOptions(args)
	if err != nil {
		return err
	}
	if o.output != "-" {
		dbAbs, err := filepath.Abs(o.dbPath)
		if err != nil {
			return err
		}
		outAbs, err := filepath.Abs(o.output)
		if err != nil {
			return err
		}
		if filepath.Clean(dbAbs) == filepath.Clean(outAbs) {
			return errors.New("--output must not overwrite the input snapshot")
		}
	}
	r, err := analyze(ctx, o)
	if err != nil {
		return err
	}
	if o.output == "-" {
		return writeReport(stdout, r)
	}
	f, err := os.OpenFile(o.output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("open report: %w", err)
	}
	defer f.Close()
	return writeReport(f, r)
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "log-cas-analyze:", err)
		os.Exit(1)
	}
}
