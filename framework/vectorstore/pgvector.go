package vectorstore

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// pgvectorTableComment marks a table as a namespace created by this store, so
	// DeleteNamespace and ListNamespaces never touch tables Bifrost did not create.
	pgvectorTableComment = "bifrost_vectorstore"

	pgvectorDefaultSchema       = "public"
	pgvectorDefaultMaxConns     = 10
	pgvectorDefaultQueryTimeout = 30 * time.Second
	pgvectorConnectTimeout      = 10 * time.Second

	// pgvector can build an HNSW index on a vector column of at most 2000 dimensions.
	pgvectorMaxIndexedDimension = 2000
	// A halfvec column can be indexed up to 4000 dimensions (verified on pgvector 0.8.7).
	pgvectorMaxIndexedDimensionHalf = 4000
	// pgvector's hard limit on the dimensions of a vector column.
	pgvectorMaxDimension = 16000
	// Index names are table name + suffix and PostgreSQL truncates identifiers at 63
	// bytes, so longer namespaces are mapped to a hashed table name of this length.
	pgvectorMaxTableNameLen = 48
	// Upper bound on namespace length; longer names are almost certainly a mistake.
	pgvectorMaxNamespaceLen = 128
)

const (
	// pgvectorTypeVector stores 4 bytes per dimension; pgvectorTypeHalfvec stores 2 (pgvector 0.7.0+).
	pgvectorTypeVector  = "vector"
	pgvectorTypeHalfvec = "halfvec"

	// Name of the row-level security policy and the session settings it reads.
	pgvectorPolicyName     = "bifrost_tenant_scope"
	pgvectorSettingScope   = "app.tenant_scope"
	pgvectorSettingAdmin   = "app.tenant_admin"
	pgvectorExpiresAtField = "expires_at"
)

// pgvectorExpiresAtExpr reads expires_at as a number, and yields NULL for a missing or non-numeric
// value, so such entries never expire. The expiry index is built on exactly this expression.
const pgvectorExpiresAtExpr = `(CASE WHEN jsonb_typeof(metadata -> 'expires_at') = 'number' THEN (metadata ->> 'expires_at')::float8 END)`

var pgvectorIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// PgVectorConfig represents the configuration for the PostgreSQL + pgvector vector store.
type PgVectorConfig struct {
	Host     schemas.SecretVar `json:"host"`                // PostgreSQL host - REQUIRED
	Port     schemas.SecretVar `json:"port,omitempty"`      // PostgreSQL port (default 5432)
	User     schemas.SecretVar `json:"user"`                // Database user - REQUIRED
	Password schemas.SecretVar `json:"password,omitempty"`  // Database password - Optional
	Database schemas.SecretVar `json:"database"`            // Database name - REQUIRED
	SSLMode  schemas.SecretVar `json:"ssl_mode,omitempty"`  // disable, allow, prefer, require, verify-ca, verify-full (default prefer)
	Schema   schemas.SecretVar `json:"schema,omitempty"`    // Schema that holds the namespace tables (default public)
	MaxConns schemas.SecretVar `json:"max_conns,omitempty"` // Maximum pooled connections (default 10)

	// VectorType is the column type for embeddings: "vector" (default, 4 bytes per dimension) or
	// "halfvec" (2 bytes per dimension, needs pgvector 0.7.0 or later). It is fixed when a
	// namespace's table is created.
	VectorType string `json:"vector_type,omitempty"`
	// RowLevelSecurity turns on PostgreSQL row-level security on every namespace table, as a second
	// wall between tenants: a row is visible only to a call whose tenant scope (WithTenantScope)
	// matches the row's metadata tenant_scope. Off by default.
	RowLevelSecurity bool `json:"row_level_security,omitempty"`
}

// PgVectorStore is a vector store backed by PostgreSQL with the pgvector extension.
//
// Each namespace is one table with the columns (id TEXT PRIMARY KEY, embedding
// vector(dimension) NULL, metadata JSONB). The embedding is nullable, so the store
// reports RequiresVectors() == false and accepts metadata-only entries such as the
// semantic cache's direct-hash entries. Filters are evaluated against the JSONB
// metadata. Similarity is cosine similarity, computed as 1 - cosine distance.
type PgVectorStore struct {
	pool    *pgxpool.Pool
	schema  string
	logger  schemas.Logger
	timeout time.Duration
	// iterativeScan is true when the installed pgvector (0.8.0 or later) can keep
	// scanning an HNSW index until enough rows pass the metadata filter. Without it
	// a selective filter applied after an approximate search can return too few rows.
	iterativeScan bool
	// binaryVectors is true when every pooled connection knows the vector type, so
	// vectors travel in pgvector's binary format instead of as text.
	binaryVectors bool
	// vectorType is "vector" or "halfvec"; it picks the column type, the index operator class and
	// the cast applied to bound vectors.
	vectorType string
	// rowLevelSecurity is true when every namespace table is protected by a tenant policy and
	// every data call sets the tenant scope for its own transaction.
	rowLevelSecurity bool
	// extVersion is the installed pgvector version.
	extVersion string
	// expiryIndexed remembers the tables whose expiry index has been ensured.
	expiryIndexed sync.Map
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

// newPgVectorStore creates a new PostgreSQL + pgvector vector store.
func newPgVectorStore(ctx context.Context, config *PgVectorConfig, logger schemas.Logger) (*PgVectorStore, error) {
	host := strings.TrimSpace(config.Host.GetValue())
	if host == "" {
		return nil, fmt.Errorf("pgvector host is required")
	}
	user := strings.TrimSpace(config.User.GetValue())
	if user == "" {
		return nil, fmt.Errorf("pgvector user is required")
	}
	database := strings.TrimSpace(config.Database.GetValue())
	if database == "" {
		return nil, fmt.Errorf("pgvector database is required")
	}
	schema := strings.TrimSpace(config.Schema.GetValue())
	if schema == "" {
		schema = pgvectorDefaultSchema
	}
	if !pgvectorIdentifierPattern.MatchString(schema) {
		return nil, fmt.Errorf("invalid pgvector schema %q: use letters, digits and underscores, starting with a letter or underscore", schema)
	}
	port := config.Port.CoerceInt(5432)
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid pgvector port %d", port)
	}
	maxConns := config.MaxConns.CoerceInt(pgvectorDefaultMaxConns)
	if maxConns <= 0 || maxConns > math.MaxInt32 {
		return nil, fmt.Errorf("invalid pgvector max_conns %d", maxConns)
	}
	vectorType := strings.ToLower(strings.TrimSpace(config.VectorType))
	switch vectorType {
	case "":
		vectorType = pgvectorTypeVector
	case pgvectorTypeVector, pgvectorTypeHalfvec:
	default:
		return nil, fmt.Errorf("invalid pgvector vector_type %q: use %q or %q", config.VectorType, pgvectorTypeVector, pgvectorTypeHalfvec)
	}
	sslMode := strings.TrimSpace(config.SSLMode.GetValue())
	if sslMode == "" {
		sslMode = "prefer"
	}

	// The connection string is built as a URL so that special characters in the
	// user or password are escaped. It is never logged.
	connURL := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, config.Password.GetValue()),
		Host:     net.JoinHostPort(host, strconv.Itoa(port)),
		Path:     "/" + database,
		RawQuery: url.Values{"sslmode": {sslMode}}.Encode(),
	}
	poolConfig, err := pgxpool.ParseConfig(connURL.String())
	if err != nil {
		// pgx redacts the password in its parse errors.
		return nil, fmt.Errorf("invalid pgvector connection settings: %w", err)
	}
	poolConfig.MaxConns = int32(maxConns)
	poolConfig.ConnConfig.ConnectTimeout = pgvectorConnectTimeout
	poolConfig.AfterConnect = registerPgVectorType

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create pgvector connection pool: %w", err)
	}

	store := &PgVectorStore{
		pool:    pool,
		schema:  schema,
		logger:  logger,
		timeout: pgvectorDefaultQueryTimeout,

		vectorType:       vectorType,
		rowLevelSecurity: config.RowLevelSecurity,
	}

	if err := store.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to connect to pgvector database: %w", err)
	}
	if err := store.ensureExtension(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	// Connections opened before the extension existed could not learn the vector type, so the
	// pool starts afresh and every connection from here on registers it.
	pool.Reset()
	store.binaryVectors = store.vectorTypeRegistered(ctx)
	if !store.binaryVectors {
		logger.Warn("pgvector: the vector type could not be registered on the connection, vectors will be sent as text")
	}
	if vectorType == pgvectorTypeHalfvec && !pgvectorVersionAtLeast(store.extVersion, 0, 7) {
		pool.Close()
		return nil, fmt.Errorf("vector_type %q needs pgvector 0.7.0 or later, but version %q is installed", pgvectorTypeHalfvec, store.extVersion)
	}
	if store.rowLevelSecurity {
		if err := store.checkRoleHonoursRLS(ctx); err != nil {
			pool.Close()
			return nil, err
		}
	}
	return store, nil
}

// registerPgVectorType teaches a new connection pgvector's binary format. When the extension is
// not installed yet the connection is left as it is.
func registerPgVectorType(ctx context.Context, conn *pgx.Conn) error {
	var oid uint32
	if err := conn.QueryRow(ctx, `SELECT COALESCE(to_regtype('vector')::oid, 0::oid)`).Scan(&oid); err != nil {
		return fmt.Errorf("failed to look up the pgvector type: %w", err)
	}
	if oid == 0 {
		return nil
	}
	conn.TypeMap().RegisterType(&pgtype.Type{Name: "vector", OID: oid, Codec: pgvectorCodec{}})

	// Metadata filters run after the approximate index search. Iterative scans keep reading the
	// index until enough rows pass the filter, so a selective filter does not silently turn a real
	// match into a miss. Set once per connection, so a search stays a single round trip.
	var version string
	if err := conn.QueryRow(ctx, `SELECT extversion FROM pg_extension WHERE extname = 'vector'`).Scan(&version); err != nil {
		return fmt.Errorf("failed to read the pgvector version: %w", err)
	}
	if pgvectorSupportsIterativeScan(version) {
		if _, err := conn.Exec(ctx, `SET hnsw.iterative_scan = strict_order`); err != nil {
			return fmt.Errorf("failed to enable iterative index scans: %w", err)
		}
	}
	return nil
}

// vectorTypeRegistered reports whether pooled connections know the vector type.
func (s *PgVectorStore) vectorTypeRegistered(ctx context.Context) bool {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return false
	}
	defer conn.Release()
	_, ok := conn.Conn().TypeMap().TypeForName("vector")
	return ok
}

// ensureExtension makes sure the pgvector extension is installed in the database and
// records whether iterative index scans are available.
func (s *PgVectorStore) ensureExtension(ctx context.Context) error {
	ctx, cancel := withTimeout(ctx, s.timeout)
	defer cancel()

	var version string
	err := s.pool.QueryRow(ctx, `SELECT extversion FROM pg_extension WHERE extname = 'vector'`).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, execErr := s.pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); execErr != nil {
			return fmt.Errorf("the pgvector extension is not installed and could not be created (%v): install pgvector on the server and run \"CREATE EXTENSION vector\" as a user with the required privilege", execErr)
		}
		err = s.pool.QueryRow(ctx, `SELECT extversion FROM pg_extension WHERE extname = 'vector'`).Scan(&version)
	}
	if err != nil {
		return fmt.Errorf("failed to check the pgvector extension: %w", err)
	}
	s.iterativeScan = pgvectorSupportsIterativeScan(version)
	s.extVersion = version
	return nil
}

// checkRoleHonoursRLS refuses to start with row_level_security on when the connected role would
// silently ignore it: superusers and roles with BYPASSRLS skip every policy, even on a table
// with FORCE ROW LEVEL SECURITY.
func (s *PgVectorStore) checkRoleHonoursRLS(ctx context.Context) error {
	ctx, cancel := withTimeout(ctx, s.timeout)
	defer cancel()
	var user string
	var super, bypass bool
	if err := s.pool.QueryRow(ctx, `SELECT current_user::text, rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&user, &super, &bypass); err != nil {
		return fmt.Errorf("failed to check the database role for row_level_security: %w", err)
	}
	if super || bypass {
		return fmt.Errorf("row_level_security is on, but the database role %q is a superuser or has BYPASSRLS, so PostgreSQL would skip the policy: connect as an ordinary role (see the pgvector docs)", user)
	}
	return nil
}

// pgvectorVersionAtLeast reports whether an extension version such as "0.8.7" is at least
// major.minor.
func pgvectorVersionAtLeast(version string, major, minor int) bool {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return false
	}
	gotMajor, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	gotMinor, err := strconv.Atoi(strings.TrimFunc(parts[1], func(r rune) bool { return r < '0' || r > '9' }))
	if err != nil {
		return false
	}
	return gotMajor > major || (gotMajor == major && gotMinor >= minor)
}

// pgvectorSupportsIterativeScan reports whether an extension version (for example
// "0.8.0") supports hnsw.iterative_scan, which arrived in 0.8.0.
func pgvectorSupportsIterativeScan(version string) bool {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(strings.TrimFunc(parts[1], func(r rune) bool { return r < '0' || r > '9' }))
	if err != nil {
		return false
	}
	return major > 0 || minor >= 8
}

// ---------------------------------------------------------------------------
// Namespace management
// ---------------------------------------------------------------------------

// Ping checks that the database is reachable.
func (s *PgVectorStore) Ping(ctx context.Context) error {
	ctx, cancel := withTimeout(ctx, s.timeout)
	defer cancel()
	return s.pool.Ping(ctx)
}

// PoolStats reports the pgxpool's current connection usage, implementing
// PoolStatsProvider so an operator (via /api/health/vectorstore) can see whether
// max_conns is large enough for the current load without reaching for pg_stat_activity.
func (s *PgVectorStore) PoolStats() PoolStats {
	stat := s.pool.Stat()
	return PoolStats{
		MaxConns:      stat.MaxConns(),
		TotalConns:    stat.TotalConns(),
		IdleConns:     stat.IdleConns(),
		AcquiredConns: stat.AcquiredConns(),
	}
}

// ErrInvalidNamespace is wrapped when a namespace cannot name a pgvector table. It is the
// caller's configuration that is wrong, so callers can surface it as a client error.
var ErrInvalidNamespace = errors.New("vectorstore: invalid namespace")

// validatePgVectorNamespace checks that a namespace can safely become a table name.
func validatePgVectorNamespace(namespace string) error {
	if namespace == "" {
		return fmt.Errorf("%w: a namespace is required", ErrInvalidNamespace)
	}
	if len(namespace) > pgvectorMaxNamespaceLen {
		return fmt.Errorf("%w: %q is too long, at most %d characters", ErrInvalidNamespace, namespace, pgvectorMaxNamespaceLen)
	}
	if !pgvectorIdentifierPattern.MatchString(namespace) {
		return fmt.Errorf("%w: %q must use letters, digits and underscores, starting with a letter or underscore", ErrInvalidNamespace, namespace)
	}
	return nil
}

// pgvectorTableName maps a namespace to its table name. Names that fit are used as they are;
// longer ones (some callers derive namespaces from a hash) keep a readable prefix plus a hash of
// the full name, so the table name and its index names stay under PostgreSQL's 63-byte limit.
func pgvectorTableName(namespace string) string {
	if len(namespace) <= pgvectorMaxTableNameLen {
		return namespace
	}
	sum := sha256.Sum256([]byte(namespace))
	return namespace[:32] + "_" + hex.EncodeToString(sum[:])[:15]
}

// pgvectorComment is the table comment that marks a table as this namespace's. Tables whose name is
// the namespace carry the bare marker; hashed names also record the namespace they belong to.
func pgvectorComment(namespace string) string {
	if pgvectorTableName(namespace) == namespace {
		return pgvectorTableComment
	}
	return pgvectorTableComment + ":" + namespace
}

// pgvectorCommentNamespace returns the namespace a table comment belongs to, given the table's
// name, and whether the comment marks a table created by this store at all.
func pgvectorCommentNamespace(comment *string, tableName string) (string, bool) {
	if comment == nil {
		return "", false
	}
	if *comment == pgvectorTableComment {
		return tableName, true
	}
	if namespace, ok := strings.CutPrefix(*comment, pgvectorTableComment+":"); ok && namespace != "" {
		return namespace, true
	}
	return "", false
}

// table returns the quoted, schema-qualified table name for a namespace.
func (s *PgVectorStore) table(namespace string) (string, error) {
	if err := validatePgVectorNamespace(namespace); err != nil {
		return "", err
	}
	return pgx.Identifier{s.schema, pgvectorTableName(namespace)}.Sanitize(), nil
}

// CreateNamespace creates the table and indexes for a namespace. It is idempotent and safe
// when several Bifrost instances start at the same time.
func (s *PgVectorStore) CreateNamespace(ctx context.Context, namespace string, dimension int, properties map[string]VectorStoreProperties) error {
	table, err := s.table(namespace)
	if err != nil {
		return err
	}
	if dimension <= 0 || dimension > pgvectorMaxDimension {
		return fmt.Errorf("invalid dimension %d: pgvector supports 1 to %d", dimension, pgvectorMaxDimension)
	}

	ctx, cancel := withTimeout(ctx, s.timeout)
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// A transaction-scoped advisory lock serialises concurrent initialisation of the
	// same namespace across replicas.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "bifrost_pgvector:"+s.schema+"."+namespace); err != nil {
		return fmt.Errorf("failed to lock namespace %q: %w", namespace, err)
	}

	var exists bool
	var comment *string
	if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL, obj_description(to_regclass($1), 'pg_class')`, table).Scan(&exists, &comment); err != nil {
		return fmt.Errorf("failed to check namespace %q: %w", namespace, err)
	}

	if exists {
		owner, owned := pgvectorCommentNamespace(comment, pgvectorTableName(namespace))
		if !owned {
			return fmt.Errorf("table %s already exists and was not created by Bifrost: choose another vector_store_namespace or drop the table manually", table)
		}
		if owner != namespace {
			return fmt.Errorf("table %s already holds namespace %q: choose another vector_store_namespace", table, owner)
		}
		var typmod int32
		var columnType string
		err := tx.QueryRow(ctx, `SELECT a.atttypmod, t.typname::text FROM pg_attribute a JOIN pg_type t ON t.oid = a.atttypid WHERE a.attrelid = to_regclass($1) AND a.attname = 'embedding' AND NOT a.attisdropped`, table).Scan(&typmod, &columnType)
		if err != nil {
			return fmt.Errorf("failed to read the dimension of namespace %q: %w", namespace, err)
		}
		if int(typmod) != dimension {
			return dimensionMismatchError(namespace, int(typmod), dimension, "table")
		}
		if columnType != s.vectorType {
			return fmt.Errorf("namespace %q already exists with %s embeddings but vector_type is %q: choose another vector_store_namespace or drop the table", namespace, columnType, s.vectorType)
		}
		if err := s.ensureTenantPolicy(ctx, tx, namespace, table); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	statements := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (id TEXT PRIMARY KEY, embedding %s(%d), metadata JSONB NOT NULL DEFAULT '{}'::jsonb)`, table, s.vectorType, dimension),
		// The namespace is validated to letters, digits and underscores, so it is safe in a literal.
		fmt.Sprintf(`COMMENT ON TABLE %s IS '%s'`, table, pgvectorComment(namespace)),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s USING gin (metadata jsonb_path_ops)`, pgx.Identifier{pgvectorTableName(namespace) + "_meta_gin"}.Sanitize(), table),
	}
	maxIndexed := pgvectorMaxIndexedDimension
	if s.vectorType == pgvectorTypeHalfvec {
		maxIndexed = pgvectorMaxIndexedDimensionHalf
	}
	if dimension <= maxIndexed {
		statements = append(statements, fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s USING hnsw (embedding %s_cosine_ops)`, pgx.Identifier{pgvectorTableName(namespace) + "_emb_hnsw"}.Sanitize(), table, s.vectorType))
	} else {
		s.logger.Warn("pgvector namespace %s has %d dimensions, above the %d that an HNSW index supports: similarity search will scan the whole table", namespace, dimension, maxIndexed)
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return fmt.Errorf("failed to create namespace %q: %w", namespace, err)
		}
	}
	if err := s.ensureTenantPolicy(ctx, tx, namespace, table); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ensureTenantPolicy makes sure a namespace table has row-level security turned on and forced,
// with the tenant policy. With row_level_security off it only warns about a table that an earlier
// run protected, because such a table shows nothing to a store that sets no tenant scope.
//
// A table that is already protected needs no DDL, so a runtime role that does not own the table
// can start against tables prepared by an owner.
func (s *PgVectorStore) ensureTenantPolicy(ctx context.Context, tx pgx.Tx, namespace, table string) error {
	var enabled, forced, hasPolicy bool
	err := tx.QueryRow(ctx, `SELECT c.relrowsecurity, c.relforcerowsecurity,
		EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid = c.oid AND p.polname = $2)
		FROM pg_class c WHERE c.oid = to_regclass($1)`, table, pgvectorPolicyName).Scan(&enabled, &forced, &hasPolicy)
	if err != nil {
		return fmt.Errorf("failed to read the row-level security state of namespace %q: %w", namespace, err)
	}
	if !s.rowLevelSecurity {
		if enabled {
			s.logger.Warn("pgvector namespace %s has row-level security enabled but row_level_security is off: calls without a tenant scope will see no rows", namespace)
		}
		return nil
	}
	if enabled && forced && hasPolicy {
		return nil
	}
	statements := []string{
		fmt.Sprintf(`ALTER TABLE %s ENABLE ROW LEVEL SECURITY`, table),
		fmt.Sprintf(`ALTER TABLE %s FORCE ROW LEVEL SECURITY`, table),
		fmt.Sprintf(`DROP POLICY IF EXISTS %s ON %s`, pgvectorPolicyName, table),
		// A row is visible when its tenant_scope equals the scope set for this transaction. A missing
		// or empty setting becomes NULL, and NULL never equals anything, so no scope means no rows.
		// The admin marker is the one deliberate way to cross tenants (clear by key, expiry sweep).
		fmt.Sprintf(`CREATE POLICY %s ON %s USING (metadata ->> 'tenant_scope' = NULLIF(current_setting('%s', true), '') OR current_setting('%s', true) = 'on')`,
			pgvectorPolicyName, table, pgvectorSettingScope, pgvectorSettingAdmin),
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return fmt.Errorf("failed to enable row-level security on namespace %q (the table owner must do this once): %w", namespace, err)
		}
	}
	return nil
}

// EnforcesTenantScope reports whether PostgreSQL itself keeps tenants apart (row_level_security).
func (s *PgVectorStore) EnforcesTenantScope() bool { return s.rowLevelSecurity }

// scopedRows closes the batch that carried the tenant setting together with the rows.
type scopedRows struct {
	pgx.Rows
	results pgx.BatchResults
}

func (r *scopedRows) Close() {
	r.Rows.Close()
	_ = r.results.Close()
}

// tenantSettings returns the values for app.tenant_scope and app.tenant_admin for this call.
func tenantSettings(ctx context.Context) (scope, admin string) {
	scope, _ = TenantScopeFromContext(ctx)
	if tenantAdminRequested(ctx) {
		admin = "on"
	}
	return scope, admin
}

// batchWithTenant queues the statement that sets the tenant for the transaction, then the caller's
// statement. pgx sends a batch with one Sync, so both run in one implicit transaction and in one
// round trip, and set_config(..., true) lasts exactly as long as that transaction.
func (s *PgVectorStore) batchWithTenant(ctx context.Context, sql string, args []any) (pgx.BatchResults, error) {
	scope, admin := tenantSettings(ctx)
	batch := &pgx.Batch{}
	batch.Queue(`SELECT set_config('`+pgvectorSettingScope+`', $1::text, true), set_config('`+pgvectorSettingAdmin+`', $2::text, true)`, scope, admin)
	batch.Queue(sql, args...)
	results := s.pool.SendBatch(ctx, batch)
	if _, err := results.Exec(); err != nil {
		_ = results.Close()
		return nil, fmt.Errorf("failed to set the tenant scope: %w", err)
	}
	return results, nil
}

// query runs a statement that returns rows. With row-level security the tenant setting travels in
// the same round trip.
func (s *PgVectorStore) query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if !s.rowLevelSecurity {
		return s.pool.Query(ctx, sql, args...)
	}
	results, err := s.batchWithTenant(ctx, sql, args)
	if err != nil {
		return nil, err
	}
	rows, err := results.Query()
	if err != nil {
		_ = results.Close()
		return nil, err
	}
	return &scopedRows{Rows: rows, results: results}, nil
}

// exec runs a statement that returns no rows, with the same tenant handling as query. It reports
// the number of rows affected.
func (s *PgVectorStore) exec(ctx context.Context, sql string, args ...any) (int64, error) {
	if !s.rowLevelSecurity {
		tag, err := s.pool.Exec(ctx, sql, args...)
		return tag.RowsAffected(), err
	}
	results, err := s.batchWithTenant(ctx, sql, args)
	if err != nil {
		return 0, err
	}
	tag, err := results.Exec()
	closeErr := results.Close()
	if err == nil {
		err = closeErr
	}
	return tag.RowsAffected(), err
}

// ownedTable verifies that a namespace's table was created by this store for that namespace.
func (s *PgVectorStore) ownedTable(ctx context.Context, namespace, table string) (exists bool, owned bool, err error) {
	var comment *string
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL, obj_description(to_regclass($1), 'pg_class')`, table).Scan(&exists, &comment); err != nil {
		return false, false, err
	}
	owner, ok := pgvectorCommentNamespace(comment, pgvectorTableName(namespace))
	return exists, ok && owner == namespace, nil
}

// DeleteNamespace drops the table of a namespace. Tables that Bifrost did not create are never dropped.
func (s *PgVectorStore) DeleteNamespace(ctx context.Context, namespace string) error {
	table, err := s.table(namespace)
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(ctx, s.timeout)
	defer cancel()

	exists, owned, err := s.ownedTable(ctx, namespace, table)
	if err != nil {
		return fmt.Errorf("failed to check namespace %q: %w", namespace, err)
	}
	if !exists {
		return nil
	}
	if !owned {
		return fmt.Errorf("refusing to drop table %s: it was not created by Bifrost", table)
	}
	if _, err := s.pool.Exec(ctx, `DROP TABLE IF EXISTS `+table); err != nil {
		return fmt.Errorf("failed to drop namespace %q: %w", namespace, err)
	}
	return nil
}

// ListNamespaces returns the namespaces created by this store that begin with prefix, sorted.
func (s *PgVectorStore) ListNamespaces(ctx context.Context, prefix string) ([]string, error) {
	ctx, cancel := withTimeout(ctx, s.timeout)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		SELECT c.relname, obj_description(c.oid, 'pg_class')
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind = 'r'
		  AND (obj_description(c.oid, 'pg_class') = $2 OR starts_with(obj_description(c.oid, 'pg_class'), $2 || ':'))`,
		s.schema, pgvectorTableComment)
	if err != nil {
		return nil, fmt.Errorf("failed to list namespaces: %w", err)
	}
	defer rows.Close()

	namespaces := []string{}
	for rows.Next() {
		var tableName string
		var comment *string
		if err := rows.Scan(&tableName, &comment); err != nil {
			return nil, fmt.Errorf("failed to read namespace: %w", err)
		}
		namespace, ok := pgvectorCommentNamespace(comment, tableName)
		if ok && strings.HasPrefix(namespace, prefix) {
			namespaces = append(namespaces, namespace)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list namespaces: %w", err)
	}
	sort.Strings(namespaces)
	return namespaces, nil
}

// ---------------------------------------------------------------------------
// Data operations
// ---------------------------------------------------------------------------

// Add stores or replaces a vector with its metadata. An empty embedding stores a
// metadata-only entry.
func (s *PgVectorStore) Add(ctx context.Context, namespace string, id string, embedding []float32, metadata map[string]interface{}) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}
	table, err := s.table(namespace)
	if err != nil {
		return err
	}
	var vectorValue any
	if len(embedding) > 0 {
		value, err := s.vectorArgument(embedding)
		if err != nil {
			return err
		}
		vectorValue = value
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("failed to encode metadata: %w", err)
	}

	ctx, cancel := withTimeout(ctx, s.timeout)
	defer cancel()
	_, err = s.exec(ctx, fmt.Sprintf(
		`INSERT INTO %s (id, embedding, metadata) VALUES ($1, $2%s, $3::text::jsonb)
		 ON CONFLICT (id) DO UPDATE SET embedding = EXCLUDED.embedding, metadata = EXCLUDED.metadata`, table, s.vectorCast()),
		id, vectorValue, string(metadataJSON))
	if err != nil {
		return fmt.Errorf("failed to upsert vector: %w", err)
	}
	return nil
}

// GetChunk retrieves a single entry by id.
func (s *PgVectorStore) GetChunk(ctx context.Context, namespace string, id string) (SearchResult, error) {
	if strings.TrimSpace(id) == "" {
		return SearchResult{}, fmt.Errorf("id is required")
	}
	table, err := s.table(namespace)
	if err != nil {
		return SearchResult{}, err
	}
	ctx, cancel := withTimeout(ctx, s.timeout)
	defer cancel()

	var raw []byte
	rows, err := s.query(ctx, fmt.Sprintf(`SELECT metadata FROM %s WHERE id = $1`, table), id)
	if err != nil {
		return SearchResult{}, fmt.Errorf("failed to get vector: %w", err)
	}
	found := false
	if rows.Next() {
		found = true
		err = rows.Scan(&raw)
	}
	rows.Close()
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		return SearchResult{}, fmt.Errorf("failed to get vector: %w", err)
	}
	if !found {
		return SearchResult{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	properties, err := decodePgVectorMetadata(raw)
	if err != nil {
		return SearchResult{}, err
	}
	return SearchResult{ID: id, Properties: properties}, nil
}

// GetChunks retrieves multiple entries by id. Ids that do not exist are skipped.
func (s *PgVectorStore) GetChunks(ctx context.Context, namespace string, ids []string) ([]SearchResult, error) {
	if len(ids) == 0 {
		return []SearchResult{}, nil
	}
	table, err := s.table(namespace)
	if err != nil {
		return nil, err
	}
	ctx, cancel := withTimeout(ctx, s.timeout)
	defer cancel()

	rows, err := s.query(ctx, fmt.Sprintf(`SELECT id, metadata FROM %s WHERE id = ANY($1::text[])`, table), ids)
	if err != nil {
		return nil, fmt.Errorf("failed to get vectors: %w", err)
	}
	defer rows.Close()

	results := make([]SearchResult, 0, len(ids))
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, fmt.Errorf("failed to read vector: %w", err)
		}
		properties, err := decodePgVectorMetadata(raw)
		if err != nil {
			return nil, err
		}
		results = append(results, SearchResult{ID: id, Properties: properties})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to get vectors: %w", err)
	}
	return results, nil
}

// GetAll retrieves entries matching the queries, one page at a time. Pages are ordered by id and
// the cursor is the id of the last entry returned.
func (s *PgVectorStore) GetAll(ctx context.Context, namespace string, queries []Query, selectFields []string, cursor *string, limit int64) ([]SearchResult, *string, error) {
	table, err := s.table(namespace)
	if err != nil {
		return nil, nil, err
	}
	pageSize := int64(boundedPageLimit(limit, 100))
	includeVectors := IncludeVectorsRequested(ctx)

	builder := &pgFilterBuilder{}
	conditions, err := builder.conditions(queries)
	if err != nil {
		return nil, nil, err
	}
	if cursor != nil && *cursor != "" {
		conditions = append(conditions, "id > "+builder.arg(*cursor)+"::text")
	}
	columns := "id, metadata"
	if includeVectors {
		columns += ", " + s.embeddingColumn()
	}
	// One extra row tells whether another page follows without a final empty page.
	sql := fmt.Sprintf(`SELECT %s FROM %s%s ORDER BY id LIMIT %s`, columns, table, whereClause(conditions), builder.arg(pageSize+1))

	ctx, cancel := withTimeout(ctx, s.timeout)
	defer cancel()
	rows, err := s.query(ctx, sql, builder.args...)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read vectors: %w", err)
	}
	defer rows.Close()

	results := make([]SearchResult, 0, pageSize)
	hasMore := false
	for rows.Next() {
		var id string
		var raw []byte
		var vector []float32
		var vectorText *string
		dest := []any{&id, &raw}
		if includeVectors {
			if s.binaryVectors {
				dest = append(dest, &vector)
			} else {
				dest = append(dest, &vectorText)
			}
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, nil, fmt.Errorf("failed to read vector: %w", err)
		}
		if int64(len(results)) == pageSize {
			hasMore = true
			break
		}
		properties, err := decodePgVectorMetadata(raw)
		if err != nil {
			return nil, nil, err
		}
		result := SearchResult{ID: id, Properties: filterProperties(properties, selectFields)}
		if includeVectors {
			if s.binaryVectors {
				result.Vector = vector
			} else if vectorText != nil {
				result.Vector = parsePgVectorLiteral(*vectorText)
			}
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("failed to read vectors: %w", err)
	}
	if hasMore && len(results) > 0 {
		next := results[len(results)-1].ID
		return results, &next, nil
	}
	return results, nil, nil
}

// GetNearest retrieves the entries nearest to a vector. threshold and each Score are cosine
// similarity: an entry matches when similarity >= threshold.
func (s *PgVectorStore) GetNearest(ctx context.Context, namespace string, vector []float32, queries []Query, selectFields []string, threshold float64, limit int64) ([]SearchResult, error) {
	table, err := s.table(namespace)
	if err != nil {
		return nil, err
	}
	if len(vector) == 0 {
		return nil, fmt.Errorf("a query vector is required")
	}
	vectorValue, err := s.vectorArgument(vector)
	if err != nil {
		return nil, err
	}
	if math.IsNaN(threshold) {
		return nil, fmt.Errorf("invalid threshold")
	}
	if limit <= 0 {
		limit = 10
	}

	builder := &pgFilterBuilder{}
	vectorArg := builder.arg(vectorValue) + s.vectorCast()
	// similarity >= threshold is the same as cosine distance <= 1 - threshold.
	maxDistance := builder.arg(1 - threshold)
	conditions := []string{"embedding IS NOT NULL", fmt.Sprintf("(embedding <=> %s) <= %s::float8", vectorArg, maxDistance)}
	filters, err := builder.conditions(queries)
	if err != nil {
		return nil, err
	}
	conditions = append(conditions, filters...)
	sql := fmt.Sprintf(`SELECT id, metadata, 1 - (embedding <=> %s) AS score FROM %s%s ORDER BY embedding <=> %s LIMIT %s`,
		vectorArg, table, whereClause(conditions), vectorArg, builder.arg(limit))

	ctx, cancel := withTimeout(ctx, s.timeout)
	defer cancel()

	// Iterative index scans are enabled on every connection (registerPgVectorType), so this is
	// one round trip.
	rows, err := s.query(ctx, sql, builder.args...)
	if err != nil {
		return nil, fmt.Errorf("failed to search vectors: %w", err)
	}
	defer rows.Close()

	results := make([]SearchResult, 0, limit)
	for rows.Next() {
		var id string
		var raw []byte
		var score float64
		if err := rows.Scan(&id, &raw, &score); err != nil {
			return nil, fmt.Errorf("failed to read search result: %w", err)
		}
		properties, err := decodePgVectorMetadata(raw)
		if err != nil {
			return nil, err
		}
		score = clampSimilarity(score)
		results = append(results, SearchResult{ID: id, Score: &score, Properties: filterProperties(properties, selectFields)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to search vectors: %w", err)
	}
	return results, nil
}

// Delete removes an entry by id. Deleting an id that does not exist is not an error.
func (s *PgVectorStore) Delete(ctx context.Context, namespace string, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}
	table, err := s.table(namespace)
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(ctx, s.timeout)
	defer cancel()
	if _, err := s.exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, table), id); err != nil {
		return fmt.Errorf("failed to delete vector: %w", err)
	}
	return nil
}

// DeleteAll removes every entry matching the queries and reports each deleted id.
func (s *PgVectorStore) DeleteAll(ctx context.Context, namespace string, queries []Query) ([]DeleteResult, error) {
	table, err := s.table(namespace)
	if err != nil {
		return nil, err
	}
	builder := &pgFilterBuilder{}
	conditions, err := builder.conditions(queries)
	if err != nil {
		return nil, err
	}

	ctx, cancel := withTimeout(ctx, s.timeout)
	defer cancel()
	rows, err := s.query(ctx, fmt.Sprintf(`DELETE FROM %s%s RETURNING id`, table, whereClause(conditions)), builder.args...)
	if err != nil {
		return nil, fmt.Errorf("failed to delete vectors: %w", err)
	}
	defer rows.Close()

	results := []DeleteResult{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to read deleted id: %w", err)
		}
		results = append(results, DeleteResult{ID: id, Status: DeleteStatusSuccess})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to delete vectors: %w", err)
	}
	return results, nil
}

// DeleteExpired deletes at most limit entries whose expires_at (Unix seconds) is before the given
// time, and reports how many it deleted. It is meant to be called in a loop with a small limit, so
// each call is a short transaction that does not hold locks for long.
//
// The first call for a namespace creates an index on expires_at, named <namespace>_expires_idx, so
// finding the oldest expired rows does not scan the table. Rows that another transaction holds
// locked are skipped and picked up by a later call. With row_level_security on, the context must
// carry WithTenantAdmin, because the sweep crosses tenants on purpose.
func (s *PgVectorStore) DeleteExpired(ctx context.Context, namespace string, before time.Time, limit int) (int, error) {
	table, err := s.table(namespace)
	if err != nil {
		return 0, err
	}
	if limit <= 0 {
		return 0, fmt.Errorf("limit must be positive")
	}
	ctx, cancel := withTimeout(ctx, s.timeout)
	defer cancel()

	s.ensureExpiryIndex(ctx, namespace, table)
	deleted, err := s.exec(ctx, fmt.Sprintf(
		`DELETE FROM %[1]s WHERE id IN (SELECT id FROM %[1]s WHERE %[2]s < $1::float8 ORDER BY %[2]s LIMIT $2 FOR UPDATE SKIP LOCKED)`,
		table, pgvectorExpiresAtExpr), float64(before.Unix()), limit)
	if err != nil {
		return 0, fmt.Errorf("failed to delete expired vectors: %w", err)
	}
	return int(deleted), nil
}

// ensureExpiryIndex creates the expires_at index once per table. A role that may not create
// indexes just logs a warning: the sweep still works, with a scan.
func (s *PgVectorStore) ensureExpiryIndex(ctx context.Context, namespace, table string) {
	if _, done := s.expiryIndexed.LoadOrStore(table, true); done {
		return
	}
	name := pgx.Identifier{pgvectorTableName(namespace) + "_expires_idx"}.Sanitize()
	if _, err := s.pool.Exec(ctx, fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (%s)`, name, table, pgvectorExpiresAtExpr)); err != nil {
		s.logger.Warn("pgvector: could not create the expiry index on %s, expired entries are found by scanning the table: %v", table, err)
	}
}

// Close releases the connection pool.
func (s *PgVectorStore) Close(ctx context.Context, namespace string) error {
	s.pool.Close()
	return nil
}

// RequiresVectors returns false: the embedding column is nullable, so metadata-only
// entries are stored as they are.
func (s *PgVectorStore) RequiresVectors() bool {
	return false
}

// FiltersVectorReadsOnServer reports that a paged read applies its filter in the database.
func (s *PgVectorStore) FiltersVectorReadsOnServer() bool { return true }

// ---------------------------------------------------------------------------
// Vectors and results
// ---------------------------------------------------------------------------

// vectorArgument returns a vector ready to bind: the slice itself when connections encode it in
// pgvector's binary format, a text literal otherwise. Non-finite values are rejected either way.
func (s *PgVectorStore) vectorArgument(vector []float32) (any, error) {
	if !s.binaryVectors {
		return pgvectorLiteral(vector)
	}
	for i, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, fmt.Errorf("vector contains a non-finite value at index %d", i)
		}
	}
	return vector, nil
}

// vectorCast is the SQL cast that turns a bound vector argument into a pgvector value.
func (s *PgVectorStore) vectorCast() string {
	// A halfvec column takes the same binary vector argument and converts it on the server.
	if s.binaryVectors {
		if s.vectorType == pgvectorTypeHalfvec {
			return "::vector::halfvec"
		}
		return "::vector"
	}
	return "::text::" + s.vectorType
}

// embeddingColumn is the embedding as a plain vector or its text form, for reads. A halfvec value
// is widened to vector so the binary codec reads it.
func (s *PgVectorStore) embeddingColumn() string {
	if s.binaryVectors {
		if s.vectorType == pgvectorTypeHalfvec {
			return "embedding::vector"
		}
		return "embedding"
	}
	return "embedding::text"
}

// pgvectorCodec encodes []float32 in pgvector's binary format: an int16 dimension, an int16 that
// is always 0, then each element as a big-endian float4. That is about a third of the size of
// the text form and spares formatting and parsing every element.
type pgvectorCodec struct{}

func (pgvectorCodec) FormatSupported(format int16) bool { return format == pgtype.BinaryFormatCode }

func (pgvectorCodec) PreferredFormat() int16 { return pgtype.BinaryFormatCode }

func (pgvectorCodec) PlanEncode(m *pgtype.Map, oid uint32, format int16, value any) pgtype.EncodePlan {
	if format != pgtype.BinaryFormatCode {
		return nil
	}
	if _, ok := value.([]float32); ok {
		return pgvectorEncodePlan{}
	}
	return nil
}

func (pgvectorCodec) PlanScan(m *pgtype.Map, oid uint32, format int16, target any) pgtype.ScanPlan {
	if format != pgtype.BinaryFormatCode {
		return nil
	}
	if _, ok := target.(*[]float32); ok {
		return pgvectorScanPlan{}
	}
	return nil
}

func (pgvectorCodec) DecodeDatabaseSQLValue(m *pgtype.Map, oid uint32, format int16, src []byte) (driver.Value, error) {
	return nil, fmt.Errorf("pgvector values cannot be read through database/sql")
}

func (pgvectorCodec) DecodeValue(m *pgtype.Map, oid uint32, format int16, src []byte) (any, error) {
	if src == nil {
		return nil, nil
	}
	if format != pgtype.BinaryFormatCode {
		return nil, fmt.Errorf("unsupported pgvector format %d", format)
	}
	return decodePgVectorBinary(src)
}

type pgvectorEncodePlan struct{}

func (pgvectorEncodePlan) Encode(value any, buf []byte) ([]byte, error) {
	vector := value.([]float32)
	if len(vector) > pgvectorMaxDimension {
		return nil, fmt.Errorf("vector has %d dimensions, more than the %d pgvector supports", len(vector), pgvectorMaxDimension)
	}
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(vector)))
	buf = binary.BigEndian.AppendUint16(buf, 0)
	for _, element := range vector {
		buf = binary.BigEndian.AppendUint32(buf, math.Float32bits(element))
	}
	return buf, nil
}

type pgvectorScanPlan struct{}

func (pgvectorScanPlan) Scan(src []byte, target any) error {
	destination := target.(*[]float32)
	if src == nil {
		*destination = nil
		return nil
	}
	vector, err := decodePgVectorBinary(src)
	if err != nil {
		return err
	}
	*destination = vector
	return nil
}

func decodePgVectorBinary(src []byte) ([]float32, error) {
	if len(src) < 4 {
		return nil, fmt.Errorf("invalid pgvector value: %d bytes", len(src))
	}
	dimension := int(binary.BigEndian.Uint16(src[0:2]))
	if len(src) != 4+4*dimension {
		return nil, fmt.Errorf("invalid pgvector value: %d bytes for %d dimensions", len(src), dimension)
	}
	vector := make([]float32, dimension)
	for i := range vector {
		vector[i] = math.Float32frombits(binary.BigEndian.Uint32(src[4+4*i:]))
	}
	return vector, nil
}

// pgvectorLiteral renders a vector as a pgvector text literal such as "[0.1,0.2]". Non-finite
// values are rejected, since pgvector does not accept them.
func pgvectorLiteral(vector []float32) (string, error) {
	buf := make([]byte, 0, len(vector)*10+2)
	buf = append(buf, '[')
	for i, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return "", fmt.Errorf("vector contains a non-finite value at index %d", i)
		}
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = strconv.AppendFloat(buf, float64(value), 'g', -1, 32)
	}
	buf = append(buf, ']')
	return string(buf), nil
}

// parsePgVectorLiteral parses a pgvector text literal. A malformed literal yields nil.
func parsePgVectorLiteral(text string) []float32 {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "[")
	text = strings.TrimSuffix(text, "]")
	if text == "" {
		return nil
	}
	parts := strings.Split(text, ",")
	vector := make([]float32, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 32)
		if err != nil {
			return nil
		}
		vector = append(vector, float32(value))
	}
	return vector
}

// clampSimilarity keeps a similarity within [-1, 1]; rounding can push 1 - distance just outside.
func clampSimilarity(score float64) float64 {
	if score > 1 {
		return 1
	}
	if score < -1 {
		return -1
	}
	return score
}

func decodePgVectorMetadata(raw []byte) (map[string]interface{}, error) {
	properties := map[string]interface{}{}
	if len(raw) == 0 {
		return properties, nil
	}
	if err := json.Unmarshal(raw, &properties); err != nil {
		return nil, fmt.Errorf("failed to decode metadata: %w", err)
	}
	return properties, nil
}

// ---------------------------------------------------------------------------
// Query translation
// ---------------------------------------------------------------------------

func whereClause(conditions []string) string {
	if len(conditions) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conditions, " AND ")
}

// pgFilterBuilder turns Query filters into SQL conditions. Every value, and every field name,
// travels as a bind parameter, so no caller-supplied text is ever placed in the SQL itself.
type pgFilterBuilder struct {
	args []any
}

// arg adds a bind parameter and returns its placeholder.
func (b *pgFilterBuilder) arg(value any) string {
	b.args = append(b.args, value)
	return "$" + strconv.Itoa(len(b.args))
}

func (b *pgFilterBuilder) conditions(queries []Query) ([]string, error) {
	conditions := make([]string, 0, len(queries))
	for _, query := range queries {
		condition, err := b.condition(query)
		if err != nil {
			return nil, err
		}
		conditions = append(conditions, condition)
	}
	return conditions, nil
}

func (b *pgFilterBuilder) condition(query Query) (string, error) {
	if query.Field == "" {
		return "", fmt.Errorf("%w: a filter needs a field", ErrQuerySyntax)
	}
	switch query.Operator {
	case QueryOperatorEqual, QueryOperatorNotEqual:
		if query.Value == nil {
			return b.nullCondition(query.Field, query.Operator == QueryOperatorEqual), nil
		}
		document, err := json.Marshal(map[string]any{query.Field: query.Value})
		if err != nil {
			return "", fmt.Errorf("%w: cannot encode value for %q: %v", ErrQuerySyntax, query.Field, err)
		}
		contains := "metadata @> " + b.arg(string(document)) + "::text::jsonb"
		if query.Operator == QueryOperatorNotEqual {
			return "NOT (" + contains + ")", nil
		}
		return contains, nil

	case QueryOperatorGreaterThan, QueryOperatorLessThan, QueryOperatorGreaterThanOrEqual, QueryOperatorLessThanOrEqual:
		sqlOperator := map[QueryOperator]string{
			QueryOperatorGreaterThan:        ">",
			QueryOperatorLessThan:           "<",
			QueryOperatorGreaterThanOrEqual: ">=",
			QueryOperatorLessThanOrEqual:    "<=",
		}[query.Operator]
		field := b.arg(query.Field) + "::text"
		if number, ok := pgvectorNumber(query.Value); ok {
			// The CASE guarantees the cast only runs on numeric values.
			return fmt.Sprintf("(CASE WHEN jsonb_typeof(metadata -> %s) = 'number' THEN (metadata ->> %s)::float8 END) %s %s::float8",
				field, field, sqlOperator, b.arg(number)), nil
		}
		if text, ok := query.Value.(string); ok {
			return fmt.Sprintf("(metadata ->> %s) %s %s::text", field, sqlOperator, b.arg(text)), nil
		}
		return "", fmt.Errorf("%w: operator %s needs a number or string value for %q", ErrQuerySyntax, query.Operator, query.Field)

	case QueryOperatorLike:
		pattern, ok := query.Value.(string)
		if !ok {
			return "", fmt.Errorf("%w: Like needs a string value for %q", ErrQuerySyntax, query.Field)
		}
		return fmt.Sprintf("(metadata ->> %s::text) LIKE %s::text", b.arg(query.Field), b.arg(pgvectorLikePattern(pattern))), nil

	case QueryOperatorContainsAny, QueryOperatorContainsAll:
		values := pgvectorStrings(query.Value)
		if len(values) == 0 {
			return "", fmt.Errorf("%w: %s needs at least one value for %q", ErrQuerySyntax, query.Operator, query.Field)
		}
		operator := "?|"
		if query.Operator == QueryOperatorContainsAll {
			operator = "?&"
		}
		return fmt.Sprintf("(metadata -> %s::text) %s %s::text[]", b.arg(query.Field), operator, b.arg(values)), nil

	case QueryOperatorIsNull:
		return b.nullCondition(query.Field, true), nil
	case QueryOperatorIsNotNull:
		return b.nullCondition(query.Field, false), nil
	}
	return "", fmt.Errorf("%w: unsupported operator %q", ErrQuerySyntax, query.Operator)
}

// nullCondition matches a field that is absent or JSON null (isNull) or the opposite.
func (b *pgFilterBuilder) nullCondition(field string, isNull bool) string {
	placeholder := b.arg(field) + "::text"
	condition := fmt.Sprintf("((metadata -> %s) IS NULL OR jsonb_typeof(metadata -> %s) = 'null')", placeholder, placeholder)
	if isNull {
		return condition
	}
	return "NOT " + condition
}

// pgvectorLikePattern follows the other stores: a pattern without wildcards matches as a
// substring; "*" and "%" are wildcards. Underscore and backslash are literal.
func pgvectorLikePattern(pattern string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `_`, `\_`).Replace(pattern)
	if !strings.ContainsAny(pattern, "%*") {
		return "%" + escaped + "%"
	}
	return strings.ReplaceAll(escaped, "*", "%")
}

func pgvectorNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case float32:
		return float64(v), true
	case float64:
		return v, true
	case json.Number:
		number, err := v.Float64()
		return number, err == nil
	}
	return 0, false
}

func pgvectorStrings(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case []string:
		return v
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = append(out, fmt.Sprint(item))
		}
		return out
	}
	return nil
}
