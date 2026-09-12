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
//	cas_blobs(hash PK, codec, orig_len, data)   immutable compressed segments + manifests
//	cas_refs(owner_hash, target_hash)           manifest -> segment edges
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
	"sync/atomic"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/klauspost/compress/zstd"
	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
)

// casBlob stores one immutable content-addressed object: either a compressed
// data segment or a compressed manifest. Insert-only with OnConflict DoNothing,
// so concurrent writers of identical content converge on one row.
type casBlob struct {
	Hash      string `gorm:"primaryKey;column:hash"`
	Codec     string `gorm:"column:codec"`
	OrigLen   int64  `gorm:"column:orig_len"`
	Data      []byte `gorm:"column:data"`
	CreatedAt int64  `gorm:"column:created_at;autoCreateTime"`
}

func (casBlob) TableName() string { return "cas_blobs" }

// casRef records that manifest OwnerHash references data blob TargetHash.
// Reachability: a data blob lives as long as some ref targets it and that
// manifest is pointed to by a casPayload row.
type casRef struct {
	OwnerHash  string `gorm:"primaryKey;column:owner_hash"`
	TargetHash string `gorm:"primaryKey;column:target_hash"`
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
	logger        schemas.Logger
	excluded      map[string]struct{}
	minFieldBytes int
	minChunkBytes int
	// Observability counters (design doc: fallback and failure metrics).
	fallbacks     atomic.Int64
	hydrateErrors atomic.Int64
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
	return &CasLogStore{
		LogStore:      inner,
		db:            db,
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

// casStoreField persists one payload field's content into CAS inside tx and
// points (logID, field) at the manifest blob. It returns the manifest hash the
// pointer previously referenced ("" when there was none); the caller reclaims
// it via gcForManifests after the new pointer is in place, so a replaced
// field's old content does not leak. Idempotent per (logID, field).
func casStoreField(tx *gorm.DB, logID, field string, content []byte, minChunk int) (oldManifest string, fallback bool, err error) {
	_, blobs, manifestBytes, fallback, err := buildManifest(content, minChunk)
	if err != nil {
		return "", fallback, err
	}
	for i := range blobs {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&blobs[i]).Error; err != nil {
			return "", fallback, fmt.Errorf("logstore/cas: insert segment blob: %w", err)
		}
	}
	manifestHash := casHash(casManifestDomain, manifestBytes)
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&casBlob{
		Hash:    manifestHash,
		Codec:   casCodecZstd,
		OrigLen: int64(len(manifestBytes)),
		Data:    casEncoder.EncodeAll(manifestBytes, nil),
	}).Error; err != nil {
		return "", fallback, fmt.Errorf("logstore/cas: insert manifest blob: %w", err)
	}
	var m casManifest
	if err := sonic.Unmarshal(manifestBytes, &m); err != nil {
		return "", fallback, err
	}
	for _, p := range m.Parts {
		if p.Hash == "" {
			continue
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&casRef{OwnerHash: manifestHash, TargetHash: p.Hash}).Error; err != nil {
			return "", fallback, fmt.Errorf("logstore/cas: insert ref: %w", err)
		}
	}
	var prev []string
	if err := tx.Model(&casPayload{}).
		Where("log_id = ? AND field = ?", logID, field).
		Pluck("blob_hash", &prev).Error; err != nil {
		return "", fallback, fmt.Errorf("logstore/cas: read previous pointer: %w", err)
	}
	if len(prev) > 0 {
		oldManifest = prev[0]
	}
	if err := tx.Where("log_id = ? AND field = ?", logID, field).Delete(&casPayload{}).Error; err != nil {
		return "", fallback, fmt.Errorf("logstore/cas: clear payload pointer: %w", err)
	}
	return oldManifest, fallback, tx.Create(&casPayload{LogID: logID, Field: field, BlobHash: manifestHash}).Error
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
