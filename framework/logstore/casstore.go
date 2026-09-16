package logstore

// Content-addressed storage (CAS) for log payloads, kept entirely inside the
// relational database (SQLite BLOB / PostgreSQL BYTEA). Large payload TEXT
// fields are split at JSON array element boundaries into byte-exact segments,
// zstd-compressed, deduplicated by SHA-256, and reassembled byte-identically
// on read. See docs/BIFROST-LOG-CAS-DESIGN.md for the fidelity contract:
// no canonicalization, no re-serialization; segments are raw slices of the
// serialized payload.
//
// Tables (same database as logs):
//
//	cas_blobs(id PK, hash UNIQUE, codec, orig_len, data)   immutable compressed segments + manifests
//	cas_refs(owner_id, target_id)               manifest -> segment edges (cas_blobs.id keys)
//	cas_payloads(log_id, field, blob_hash)      per-log field -> manifest pointer
//
// Every write path performs the log-row write and the CAS writes inside ONE
// database transaction, so a row never exists without its payload and a
// payload replacement never loses the old content on failure.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/klauspost/compress/zstd"
	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

const (
	casCodecZstd = "zstd"
	// Hash domains separate data-segment identity from manifest identity so a
	// manifest blob and a data blob with identical bytes never collide.
	casDataDomain     = "casdat1"
	casManifestDomain = "casman1"
	// Defaults; overridable via ContentAddressedConfig.
	casDefaultMinFieldBytes = 1024
	casDefaultMinChunkBytes = 256
	// Version tag persisted in manifests; bump only with a new decoder kept
	// side-by-side (design doc: no silent rewrites).
	casManifestVersion = 1
	// Upper bound for a manifest's declared OrigLen. Persisted metadata is
	// untrusted input: a negative or absurd length must be an integrity error,
	// never an argument to make().
	casMaxPayloadBytes = int64(1) << 32
	// Preallocation cap when rebuilding from an untrusted manifest length;
	// append grows past it as real segments arrive.
	casMaxPreallocBytes = int64(1) << 20
	// Keep generated statements below SQLite's conservative 999-variable
	// ceiling and far below PostgreSQL's 65535-variable protocol limit. Every
	// batched write and hash lookup derives its row count from this budget.
	casSQLParameterLimit = 900
)

// casBlob stores one immutable content-addressed object: either a compressed
// data segment or a compressed manifest. Insert-only with OnConflict DoNothing,
// so concurrent writers of identical content converge on one row. The integer
// id is the database-internal join key for cas_refs; content identity and
// dedup stay on the full SHA-256 hash (unique index). AUTOINCREMENT keeps ids
// from ever being reused, so a stale ref edge can only dangle (detectable),
// never silently reattach to new content.
type casBlob struct {
	ID        int64  `gorm:"primaryKey;column:id;autoIncrement"`
	Hash      string `gorm:"column:hash;uniqueIndex:idx_cas_blobs_hash"`
	Codec     string `gorm:"column:codec"`
	OrigLen   int64  `gorm:"column:orig_len"`
	Data      []byte `gorm:"column:data"`
	CreatedAt int64  `gorm:"column:created_at;autoCreateTime"`
}

func (casBlob) TableName() string { return "cas_blobs" }

// casRef records that the manifest blob with id OwnerID references the data
// blob with id TargetID (ids reference cas_blobs.id). Reachability: a data
// blob lives as long as some ref targets it and that manifest is pointed to
// by a casPayload row.
type casRef struct {
	OwnerID  int64 `gorm:"primaryKey;column:owner_id"`
	TargetID int64 `gorm:"primaryKey;column:target_id"`
}

func (casRef) TableName() string { return "cas_refs" }

// casPayload points a (log, payload field) at its manifest blob.
type casPayload struct {
	LogID    string `gorm:"primaryKey;column:log_id"`
	Field    string `gorm:"primaryKey;column:field"`
	BlobHash string `gorm:"column:blob_hash"`
}

func (casPayload) TableName() string { return "cas_payloads" }

// casPart is one manifest entry: either a blob reference (Hash) or an inline
// byte slice (Inline). Exactly one of the two is set.
type casPart struct {
	Hash   string `json:"h,omitempty"`
	Inline string `json:"i,omitempty"`
}

type casManifest struct {
	Version int       `json:"v"`
	OrigLen int64     `json:"l"`
	Parts   []casPart `json:"p"`
}

var (
	casEncoder *zstd.Encoder
	casDecoder *zstd.Decoder
)

func init() {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		panic(fmt.Sprintf("logstore/cas: init zstd encoder: %v", err))
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		panic(fmt.Sprintf("logstore/cas: init zstd decoder: %v", err))
	}
	casEncoder, casDecoder = enc, dec
}

func casHash(domain string, raw []byte) string {
	h := sha256.New()
	h.Write([]byte(domain))
	h.Write([]byte{0})
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

// CasLogStore wraps an inner LogStore (must expose ScopedDB) and intercepts
// the payload lifecycle: eligible large payload fields go to CAS tables, the
// DB row keeps metadata, pricing fields and small payloads. All other LogStore
// methods are delegated unchanged via embedding.
type CasLogStore struct {
	LogStore
	// db is captured from the CONSTRUCTION context and is used for writes,
	// mirroring the inner store's unscoped s.db.WithContext write handle.
	db *gorm.DB
	// rdb is the inner store narrowed to its concrete RDB type; the unified
	// read snapshot path (casstore_read.go) reads the root row inside a
	// transaction through its tx-scoped helpers (findLogTx/findLogsTx) and
	// reuses its child-aggregate rollup.
	rdb           *RDBLogStore
	logger        schemas.Logger
	excluded      map[string]struct{}
	minFieldBytes int
	minChunkBytes int
	// Observability counters (design doc: fallback and failure metrics).
	fallbacks     atomic.Int64
	hydrateErrors atomic.Int64
	// logSchema caches the parsed Log schema used to normalize Go field-name
	// map keys to DB column names in the Update path (see
	// normalizeUpdateMapKeys in casstore_write.go). A parse failure is cached
	// in logSchemaErr and returned on every call (fail closed): the Update
	// path must never silently fall back to passing raw keys through, which
	// would let Go field-name keys bypass the CAS interception.
	logSchemaOnce sync.Once
	logSchema     *schema.Schema
	logSchemaErr  error
}

// scopedDB returns the database handle with the caller's query scope applied,
// for read paths (hydration) that must respect caller-driven row visibility.
func (c *CasLogStore) scopedDB(ctx context.Context) *gorm.DB {
	if scoped, ok := c.LogStore.(scopedDBLogStore); ok {
		if db := scoped.ScopedDB(ctx); db != nil {
			return db
		}
	}
	return c.db.WithContext(ctx)
}

// newCasLogStore wraps inner with content-addressed payload storage. ctx must
// not carry a QueryScope: the write handle is captured here.
func newCasLogStore(ctx context.Context, inner LogStore, cfg *ContentAddressedConfig, logger schemas.Logger) (*CasLogStore, error) {
	scoped, ok := inner.(scopedDBLogStore)
	if !ok {
		return nil, fmt.Errorf("logstore/cas: inner store %T does not expose a database handle", inner)
	}
	db := scoped.ScopedDB(ctx)
	if db == nil {
		return nil, fmt.Errorf("logstore/cas: inner store returned a nil database handle")
	}
	// The unified read snapshot reads the root row inside a read transaction
	// through the inner store's tx-scoped helpers. CAS only ever wraps the
	// SQL stores (the ClickHouse store exposes no database handle and fails
	// the check above), so require the concrete type.
	rdb, isRdb := inner.(*RDBLogStore)
	if !isRdb {
		return nil, fmt.Errorf("logstore/cas: inner store %T is not an RDB log store", inner)
	}
	excluded := make(map[string]struct{}, len(cfg.ExcludeFields)+2)
	for _, f := range cfg.ExcludeFields {
		if _, isPayload := payloadFieldSet[f]; isPayload {
			excluded[f] = struct{}{}
		}
	}
	// Pricing metadata is always DB-resident: billing reads it without
	// hydration and cost recomputation must never depend on CAS.
	excluded["token_usage"] = struct{}{}
	excluded["cache_debug"] = struct{}{}
	if err := initializeCASInventory(db.WithContext(ctx)); err != nil {
		return nil, fmt.Errorf("logstore/cas: initialize inventory: %w", err)
	}
	// Defense in depth: the write path resolves ref edges through integer
	// cas_blobs ids. Migrations run when the inner store opens and must have
	// reached the integer layout; fail fast with a clear error instead of
	// letting hash/integer mismatches surface as corrupt-looking queries.
	if err := verifyCasIntegerLayout(db.WithContext(ctx)); err != nil {
		return nil, fmt.Errorf("logstore/cas: cas_refs layout check failed (migrations incomplete?): %w", err)
	}
	return &CasLogStore{
		LogStore:      inner,
		db:            db,
		rdb:           rdb,
		logger:        logger,
		excluded:      excluded,
		minFieldBytes: cfg.minFieldBytes(),
		minChunkBytes: cfg.minChunkBytes(),
	}, nil
}

// casEligible reports whether a payload field's content should go to CAS.
func (c *CasLogStore) casEligible(field, content string) bool {
	if _, isPayload := payloadFieldSet[field]; !isPayload {
		return false
	}
	if _, excl := c.excluded[field]; excl {
		return false
	}
	return len(content) >= c.minFieldBytes
}

// splitJSONArray splits a JSON array into an ordered token list of raw byte
// slices: alternating gaps (",", whitespace) and complete top-level elements.
// It never parses or rewrites bytes; concatenating the tokens reproduces the
// input exactly, including any whitespace after the closing bracket. ok is
// false when b is not a top-level JSON array or contains a structural error,
// in which case the caller stores the whole field as one opaque blob
// (design doc fallback).
func splitJSONArray(b []byte) (tokens [][]byte, ok bool) {
	n := len(b)
	skipSpace := func(i int) int {
		for i < n && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
			i++
		}
		return i
	}
	i := skipSpace(0)
	if i >= n || b[i] != '[' {
		return nil, false
	}
	// Leading whitespace + the opening bracket form the first token so that
	// concatenating all tokens reproduces the input byte for byte.
	tokens = append(tokens, b[:i+1])
	i++
	start := i
	depth := 0     // brace/bracket nesting inside the current element
	inStr := false // true while inside a string token (element or nested)
	esc := false
	for i < n {
		ch := b[i]
		if depth == 0 && !inStr {
			switch ch {
			case ']':
				// Trailing whitespace is legal JSON and part of the field's
				// bytes: it must survive the round trip, so it is kept in the
				// closing token.
				end := skipSpace(i + 1)
				if end != n {
					return nil, false // trailing garbage after the array
				}
				tokens = append(tokens, b[start:end])
				return tokens, true
			case ',':
				tokens = append(tokens, b[start:i+1]) // separator token, comma included
				i++
				start = i
				continue
			case '{', '[':
				depth = 1
			case '"':
				inStr = true
			case ' ', '\t', '\n', '\r':
				// leading whitespace stays part of the next token
			default:
				// bare scalar (number/true/false/null): scanned char by char,
				// terminated by ',' or ']' at depth 0 below/above
			}
			i++
			continue
		}
		if inStr {
			if esc {
				esc = false
			} else if ch == '\\' {
				esc = true
			} else if ch == '"' {
				inStr = false
				if depth == 0 {
					tokens = append(tokens, b[start:i+1])
					i++
					start = i
					continue
				}
			}
			i++
			continue
		}
		switch ch {
		case '"':
			inStr = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				tokens = append(tokens, b[start:i+1])
				i++
				start = i
				continue
			}
		}
		i++
	}
	return nil, false // truncated array / unterminated string
}

// buildManifest chunks raw payload bytes. Elements (and gaps) at least
// minChunkBytes long become blobs; smaller ones stay inline in the manifest
// unless they are not valid UTF-8 (the manifest is JSON text; such segments
// are forced into blobs). Returns the manifest, the deduplicated data blobs to
// persist, the serialized manifest bytes, and whether the JSON-array split
// failed and the whole field was stored as one opaque blob (fallback metric).
func buildManifest(raw []byte, minChunk int) (m *casManifest, blobs []casBlob, manifestBytes []byte, fallback bool, err error) {
	var tokens [][]byte
	var ok bool
	if tokens, ok = splitJSONArray(raw); !ok {
		tokens = [][]byte{raw}
		fallback = true
	}
	m = &casManifest{Version: casManifestVersion, OrigLen: int64(len(raw))}
	blobSet := make(map[string]struct{})
	for _, tok := range tokens {
		if len(tok) < minChunk && utf8.Valid(tok) {
			m.Parts = append(m.Parts, casPart{Inline: string(tok)})
			continue
		}
		hash := casHash(casDataDomain, tok)
		m.Parts = append(m.Parts, casPart{Hash: hash})
		if _, dup := blobSet[hash]; !dup {
			blobSet[hash] = struct{}{}
			blobs = append(blobs, casBlob{
				Hash:    hash,
				Codec:   casCodecZstd,
				OrigLen: int64(len(tok)),
				Data:    casEncoder.EncodeAll(tok, nil),
			})
		}
	}
	manifestBytes, err = sonic.Marshal(m)
	if err != nil {
		return nil, nil, nil, false, fmt.Errorf("logstore/cas: marshal manifest: %w", err)
	}
	return m, blobs, manifestBytes, fallback, nil
}

// casReconstruct rebuilds the original payload bytes from a manifest blob.
// The manifest's declared length is untrusted persisted metadata: it is range
// checked before any allocation, and the final length must match exactly.
func casReconstruct(manifestBytes []byte, lookup func(hash string) ([]byte, error)) ([]byte, error) {
	var m casManifest
	if err := sonic.Unmarshal(manifestBytes, &m); err != nil {
		return nil, fmt.Errorf("logstore/cas: unmarshal manifest: %w", err)
	}
	if m.Version != casManifestVersion {
		return nil, fmt.Errorf("logstore/cas: unsupported manifest version %d", m.Version)
	}
	if m.OrigLen < 0 || m.OrigLen > casMaxPayloadBytes {
		return nil, fmt.Errorf("logstore/cas: manifest orig_len %d out of range", m.OrigLen)
	}
	prealloc := m.OrigLen
	if prealloc > casMaxPreallocBytes {
		prealloc = casMaxPreallocBytes
	}
	out := make([]byte, 0, prealloc)
	for _, p := range m.Parts {
		if p.Hash != "" {
			raw, err := lookup(p.Hash)
			if err != nil {
				return nil, fmt.Errorf("logstore/cas: segment %s: %w", p.Hash, err)
			}
			out = append(out, raw...)
			continue
		}
		out = append(out, p.Inline...)
	}
	if int64(len(out)) != m.OrigLen {
		return nil, fmt.Errorf("logstore/cas: reconstructed length %d != manifest %d", len(out), m.OrigLen)
	}
	return out, nil
}

type casPreparedField struct {
	field         string
	manifestHash  string
	manifestBlob  casBlob
	segmentBlobs  []casBlob
	segmentHashes []string
	fallback      bool
}

// casPrepareField performs the CPU-only part of a CAS field write. A log write
// prepares every selected field before issuing CAS SQL so their hashes can be
// resolved together inside the log transaction.
func casPrepareField(field string, content []byte, minChunk int) (*casPreparedField, error) {
	m, blobs, manifestBytes, fallback, err := buildManifest(content, minChunk)
	if err != nil {
		return nil, err
	}
	manifestHash := casHash(casManifestDomain, manifestBytes)
	prepared := &casPreparedField{
		field:        field,
		manifestHash: manifestHash,
		manifestBlob: casBlob{
			Hash:    manifestHash,
			Codec:   casCodecZstd,
			OrigLen: int64(len(manifestBytes)),
			Data:    casEncoder.EncodeAll(manifestBytes, nil),
		},
		segmentBlobs: blobs,
		fallback:     fallback,
	}
	seen := make(map[string]struct{}, len(m.Parts))
	for _, part := range m.Parts {
		if part.Hash == "" {
			continue
		}
		if _, duplicate := seen[part.Hash]; duplicate {
			continue
		}
		seen[part.Hash] = struct{}{}
		prepared.segmentHashes = append(prepared.segmentHashes, part.Hash)
	}
	return prepared, nil
}

// casStorePreparedFields persists one log's prepared fields in two phases: all
// blobs first, then one bounded transaction-local hash-to-id mapping followed
// by refs and payload pointers. This deliberately does not collect across log
// boundaries, even when BatchCreateIfNotExists has a wider transaction.
func casStorePreparedFields(tx *gorm.DB, logID string, fields []*casPreparedField) ([]string, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	lastField := make(map[string]int, len(fields))
	for i, field := range fields {
		lastField[field.field] = i
	}
	uniqueFields := make([]*casPreparedField, 0, len(lastField))
	for i, field := range fields {
		if lastField[field.field] == i {
			uniqueFields = append(uniqueFields, field)
		}
	}
	fields = uniqueFields
	lookup := make([]string, 0, len(fields)*2)
	seenLookup := make(map[string]struct{}, len(fields)*2)
	blobs := make([]casBlob, 0, len(fields)*2)
	seenBlobs := make(map[string]struct{}, len(fields)*2)
	addLookup := func(hash string) {
		if _, duplicate := seenLookup[hash]; duplicate {
			return
		}
		seenLookup[hash] = struct{}{}
		lookup = append(lookup, hash)
	}
	addBlob := func(blob casBlob) {
		if _, duplicate := seenBlobs[blob.Hash]; duplicate {
			return
		}
		seenBlobs[blob.Hash] = struct{}{}
		blobs = append(blobs, blob)
	}
	for _, field := range fields {
		for _, blob := range field.segmentBlobs {
			addBlob(blob)
		}
		addBlob(field.manifestBlob)
		addLookup(field.manifestHash)
		for _, hash := range field.segmentHashes {
			addLookup(hash)
		}
	}
	if err := casInsertBlobs(tx, blobs); err != nil {
		return nil, fmt.Errorf("logstore/cas: insert blobs: %w", err)
	}

	ids, err := casResolveBlobIDs(tx, lookup)
	if err != nil {
		return nil, fmt.Errorf("logstore/cas: resolve blob ids: %w", err)
	}
	// Validate every endpoint before refs or pointers are changed. Every blob was
	// either inserted above or already existed by full SHA-256 identity.
	for _, field := range fields {
		if _, ok := ids[field.manifestHash]; !ok {
			return nil, fmt.Errorf("logstore/cas: manifest blob id missing after insert")
		}
		for _, hash := range field.segmentHashes {
			if _, ok := ids[hash]; !ok {
				return nil, fmt.Errorf("logstore/cas: segment blob id missing after insert: %s", hash)
			}
		}
	}

	refs := make([]casRef, 0, len(lookup))
	seenRefs := make(map[casRef]struct{}, len(lookup))
	for _, field := range fields {
		ownerID := ids[field.manifestHash]
		for _, hash := range field.segmentHashes {
			ref := casRef{OwnerID: ownerID, TargetID: ids[hash]}
			if _, duplicate := seenRefs[ref]; duplicate {
				continue
			}
			seenRefs[ref] = struct{}{}
			refs = append(refs, ref)
		}
	}
	if err := casInsertRefs(tx, refs); err != nil {
		return nil, fmt.Errorf("logstore/cas: insert refs: %w", err)
	}

	payloads := make([]casPayload, 0, len(fields))
	for _, field := range fields {
		payloads = append(payloads, casPayload{LogID: logID, Field: field.field, BlobHash: field.manifestHash})
	}
	return casReplacePayloads(tx, logID, payloads)
}

// casReplacePayloads atomically replaces the selected pointer fields for one
// log. Duplicate fields are collapsed before SQL with the final occurrence
// winning, matching the old sequential per-field replacement semantics.
func casReplacePayloads(tx *gorm.DB, logID string, payloads []casPayload) ([]string, error) {
	if len(payloads) == 0 {
		return nil, nil
	}
	lastIndex := make(map[string]int, len(payloads))
	for i, payload := range payloads {
		if payload.LogID != logID {
			return nil, fmt.Errorf("logstore/cas: payload log id %q does not match transaction log %q", payload.LogID, logID)
		}
		lastIndex[payload.Field] = i
	}
	unique := make([]casPayload, 0, len(lastIndex))
	fields := make([]string, 0, len(lastIndex))
	for i, payload := range payloads {
		if lastIndex[payload.Field] != i {
			continue
		}
		unique = append(unique, payload)
		fields = append(fields, payload.Field)
	}

	var oldManifests []string
	// One log currently has fewer payload fields than this limit, but chunking
	// keeps every SELECT and DELETE valid if the schema grows or focused callers
	// use the helper with a larger field set.
	const fixedPayloadFilterParameters = 1
	fieldsPerFilter := casSQLParameterLimit - fixedPayloadFilterParameters
	for start := 0; start < len(fields); start += fieldsPerFilter {
		end := start + fieldsPerFilter
		if end > len(fields) {
			end = len(fields)
		}
		var chunkOld []string
		if err := tx.Model(&casPayload{}).
			Where("log_id = ? AND field IN ?", logID, fields[start:end]).
			Pluck("blob_hash", &chunkOld).Error; err != nil {
			return nil, fmt.Errorf("logstore/cas: read previous pointers: %w", err)
		}
		oldManifests = append(oldManifests, chunkOld...)
	}
	for start := 0; start < len(fields); start += fieldsPerFilter {
		end := start + fieldsPerFilter
		if end > len(fields) {
			end = len(fields)
		}
		if err := tx.Where("log_id = ? AND field IN ?", logID, fields[start:end]).Delete(&casPayload{}).Error; err != nil {
			return nil, fmt.Errorf("logstore/cas: clear payload pointers: %w", err)
		}
	}
	const parametersPerPayload = 3
	rowsPerStatement := casSQLParameterLimit / parametersPerPayload
	for start := 0; start < len(unique); start += rowsPerStatement {
		end := start + rowsPerStatement
		if end > len(unique) {
			end = len(unique)
		}
		var sql strings.Builder
		sql.WriteString("INSERT INTO cas_payloads (log_id,field,blob_hash) VALUES ")
		args := make([]interface{}, 0, (end-start)*parametersPerPayload)
		for i := start; i < end; i++ {
			if i > start {
				sql.WriteByte(',')
			}
			sql.WriteString("(?,?,?)")
			args = append(args, unique[i].LogID, unique[i].Field, unique[i].BlobHash)
		}
		if err := tx.Exec(sql.String(), args...).Error; err != nil {
			return nil, fmt.Errorf("logstore/cas: insert payload pointers: %w", err)
		}
	}
	return oldManifests, nil
}

// casStoreField retains the single-field helper used by focused tests and
// maintenance probes; production log writes collect all fields through
// casStorePreparedFields.
func casStoreField(tx *gorm.DB, logID, field string, content []byte, minChunk int) (oldManifest string, fallback bool, err error) {
	prepared, err := casPrepareField(field, content, minChunk)
	if err != nil {
		return "", false, err
	}
	old, err := casStorePreparedFields(tx, logID, []*casPreparedField{prepared})
	if err != nil {
		return "", prepared.fallback, err
	}
	if len(old) > 0 {
		oldManifest = old[0]
	}
	return oldManifest, prepared.fallback, nil
}

// casResolveBlobIDs scans each bounded two-column mapping directly, avoiding
// GORM's reflected result slice. Rows are closed before the next chunk or any
// ref write, and the caller verifies that every requested endpoint was found.
func casResolveBlobIDs(tx *gorm.DB, hashes []string) (map[string]int64, error) {
	unique := make([]string, 0, len(hashes))
	seen := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		if _, duplicate := seen[hash]; duplicate {
			continue
		}
		seen[hash] = struct{}{}
		unique = append(unique, hash)
	}
	ids := make(map[string]int64, len(unique))
	for start := 0; start < len(unique); start += casSQLParameterLimit {
		end := start + casSQLParameterLimit
		if end > len(unique) {
			end = len(unique)
		}
		rows, err := tx.Model(&casBlob{}).Select("id", "hash").Where("hash IN ?", unique[start:end]).Rows()
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var hash string
			if err := rows.Scan(&id, &hash); err != nil {
				_ = rows.Close()
				return nil, err
			}
			ids[hash] = id
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// casInsertBlobs inserts unique blobs in bounded multirow statements. It leaves
// ID allocation to the database and resolves IDs by hash afterwards, including
// conflicts. ON CONFLICT DO NOTHING preserves existing immutable blob content
// and every attempted insert still advances SQLite AUTOINCREMENT/PostgreSQL
// sequences according to the database's native conflict semantics.
func casInsertBlobs(tx *gorm.DB, blobs []casBlob) error {
	const parametersPerBlob = 5
	rowsPerStatement := casSQLParameterLimit / parametersPerBlob
	for start := 0; start < len(blobs); start += rowsPerStatement {
		end := start + rowsPerStatement
		if end > len(blobs) {
			end = len(blobs)
		}
		var sql strings.Builder
		sql.WriteString("INSERT INTO cas_blobs (hash,codec,orig_len,data,created_at) VALUES ")
		args := make([]interface{}, 0, (end-start)*parametersPerBlob)
		for i := start; i < end; i++ {
			if i > start {
				sql.WriteByte(',')
			}
			sql.WriteString("(?,?,?,?,?)")
			createdAt := blobs[i].CreatedAt
			if createdAt == 0 {
				createdAt = tx.NowFunc().Unix()
			}
			args = append(args, blobs[i].Hash, blobs[i].Codec, blobs[i].OrigLen, blobs[i].Data, createdAt)
		}
		sql.WriteString(" ON CONFLICT DO NOTHING")
		if err := tx.Exec(sql.String(), args...).Error; err != nil {
			return err
		}
	}
	return nil
}

// casInsertBlob retains the single-row helper for focused callers while using
// the same raw, no-RETURNING semantics as the transaction batch path.
func casInsertBlob(tx *gorm.DB, blob *casBlob) error {
	return casInsertBlobs(tx, []casBlob{*blob})
}

// casInsertRefs inserts unique ref edges in bounded multirow statements. The
// composite primary key and ON CONFLICT DO NOTHING retain idempotency against
// edges that already existed before this transaction.
func casInsertRefs(tx *gorm.DB, refs []casRef) error {
	unique := make([]casRef, 0, len(refs))
	seen := make(map[casRef]struct{}, len(refs))
	for _, ref := range refs {
		if _, duplicate := seen[ref]; duplicate {
			continue
		}
		seen[ref] = struct{}{}
		unique = append(unique, ref)
	}
	const parametersPerRef = 2
	rowsPerStatement := casSQLParameterLimit / parametersPerRef
	for start := 0; start < len(unique); start += rowsPerStatement {
		end := start + rowsPerStatement
		if end > len(unique) {
			end = len(unique)
		}
		var sql strings.Builder
		sql.WriteString("INSERT INTO cas_refs (owner_id,target_id) VALUES ")
		args := make([]interface{}, 0, (end-start)*parametersPerRef)
		for i := start; i < end; i++ {
			if i > start {
				sql.WriteByte(',')
			}
			sql.WriteString("(?,?)")
			args = append(args, unique[i].OwnerID, unique[i].TargetID)
		}
		sql.WriteString(" ON CONFLICT DO NOTHING")
		if err := tx.Exec(sql.String(), args...).Error; err != nil {
			return err
		}
	}
	return nil
}

// casCountPayloads returns the number of CAS pointers a log currently has
// inside tx; used to keep the row's has_object flag in sync with reality.
func casCountPayloads(tx *gorm.DB, logID string) (int64, error) {
	var n int64
	if err := tx.Model(&casPayload{}).Where("log_id = ?", logID).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// casNormalizeProjection strips the "logs." table prefix from projection
// fields and detects a wildcard. A wildcard means "full row": hydration runs
// unfiltered instead of being suppressed by the exact-name check.
func casNormalizeProjection(fields []string) (norm []string, wildcard bool) {
	for _, f := range fields {
		f = strings.TrimPrefix(f, "logs.")
		if f == "*" {
			return nil, true
		}
		norm = append(norm, f)
	}
	return norm, false
}
