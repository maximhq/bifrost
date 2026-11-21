package vectorstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

// PgVectorConfig configures the pgvector store. It carries no connection
// settings: the store keeps its tables in the logs store's Postgres database,
// so a deployment that already runs Postgres needs no other service.
type PgVectorConfig struct{}

// PgVectorStore is a VectorStore backed by the pgvector extension, in the
// database the logs store already uses.
//
// Each namespace is a table of its own, because pgvector indexes only a column
// of fixed dimension and namespaces differ in dimension. The table name is
// derived from the namespace and its dimension rather than the namespace
// itself: Postgres caps identifiers at 63 bytes, and callers derive namespace
// names from content. vector_store_namespaces maps one to the other and is
// what ListNamespaces reads.
//
// Properties are one jsonb column. A GIN index over it serves Equal and the
// Contains operators; integer properties also get a btree index on their
// value, which the range operators use (warp filters every read by timestamp).
//
// The connection belongs to the logs store, so its session settings are the
// operator's, and the store does not rely on them:
//   - Transactions that take a namespace or registry lock and then read the
//     registry run at READ COMMITTED (pgLockTxOptions), so the read after the
//     lock wait sees what the lock holder committed.
//   - Searches SET LOCAL the hnsw settings they need.
//   - Field names are escape string literals, E'...', which read the same
//     whatever standard_conforming_strings is, and string
//     ranges compare COLLATE "C" rather than in the database collation.
//   - Startup checks what cannot be pinned: the pgvector version, and that its
//     types resolve on the search_path.
type PgVectorStore struct {
	db     *gorm.DB
	logger schemas.Logger

	mu         sync.RWMutex
	namespaces map[string]pgNamespace
}

// pgLockTxOptions is the isolation of every transaction that takes an advisory
// lock and then reads the registry. Under REPEATABLE READ the snapshot is
// fixed at the first statement, the lock itself, so a replica that waited for
// the lock would read the registry as it was before the holder committed.
var pgLockTxOptions = &sql.TxOptions{Isolation: sql.LevelReadCommitted}

// pgNamespace is what a read needs to know about a namespace besides its name.
type pgNamespace struct {
	table     string
	dimension int
}

const (
	pgRegistryTable = "vector_store_namespaces"

	// pgMinVersion is the oldest pgvector with iterative index scans. Before
	// them an HNSW search filters only the ef_search candidates it found, so a
	// selective filter can return nothing although matching rows exist.
	pgMinVersion = "0.8.0"

	// pgVectorMaxIndexedDimension and pgHalfvecMaxIndexedDimension are the
	// largest dimensions pgvector's HNSW indexes accept for vector and halfvec.
	// Above the second a namespace is unindexed and every search is exact.
	pgVectorMaxIndexedDimension  = 2000
	pgHalfvecMaxIndexedDimension = 4000
	// pgMaxDimension is the largest vector column pgvector stores.
	pgMaxDimension = 16000

	// pgDefaultEfSearch is hnsw.ef_search's default. An iterative scan gathers
	// ef_search candidates per pass, so a larger limit raises it.
	pgDefaultEfSearch = 40
	pgMaxEfSearch     = 1000

	pgDefaultPageLimit = 100

	// pgHalfvecRerankExtra is how many candidates past the limit a halfvec search
	// reads. Halfvec ranks rows whose scores differ by about 2e-4 in either order,
	// so the rows it ranks just past the limit are re-ranked at full precision
	// with the rest; see GetNearest.
	pgHalfvecRerankExtra = 8
	// pgMaxPageLimit bounds a page so limit+1, the row that says another page
	// follows, cannot overflow.
	pgMaxPageLimit = math.MaxInt32
)

// newPgVectorStore opens the store on the logs store's database, installing
// the extension and the namespace registry if they are missing.
func newPgVectorStore(ctx context.Context, _ *PgVectorConfig, deps Dependencies, logger schemas.Logger) (*PgVectorStore, error) {
	if deps.LogStore == nil {
		return nil, errors.New("pgvector keeps its vectors in the logs store's Postgres database, and no logs store is configured")
	}
	db := deps.LogStore.ScopedDB(context.Background())
	if db == nil {
		return nil, errors.New("pgvector keeps its vectors in the logs store's Postgres database, and the logs store exposes no database")
	}
	if name := db.Dialector.Name(); name != "postgres" {
		return nil, fmt.Errorf("pgvector keeps its vectors in the logs store's Postgres database, and the logs store is %s", name)
	}

	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Replicas start together; serialize the extension and registry DDL so
		// two CREATE ... IF NOT EXISTS cannot race into a unique violation.
		if err := pgAdvisoryXactLock(tx, "bifrost.vectorstore.registry"); err != nil {
			return err
		}
		installed, err := pgExtensionVersion(tx)
		if err != nil {
			return err
		}
		if installed == "" {
			if err := tx.Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
				return fmt.Errorf("the pgvector extension is not installed in the logs store database, and Bifrost's database user could not install it (%w). Have a database administrator run CREATE EXTENSION vector; in that database", err)
			}
			if installed, err = pgExtensionVersion(tx); err != nil {
				return err
			}
		}
		// Checked before anything is created, so a role that could not create
		// the registry still gets the upgrade instruction.
		if compareVersions(installed, pgMinVersion) < 0 {
			return fmt.Errorf("pgvector %s is installed, and Bifrost needs %s or later; have a database administrator run ALTER EXTENSION vector UPDATE;", installed, pgMinVersion)
		}
		if err := pgCheckTypesVisible(tx); err != nil {
			return err
		}
		return tx.Exec(`CREATE TABLE IF NOT EXISTS ` + pgRegistryTable + ` (
			name text PRIMARY KEY,
			table_name text NOT NULL UNIQUE,
			dimension integer NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now()
		)`).Error
	}, pgLockTxOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare pgvector: %w", err)
	}

	return &PgVectorStore{
		db:         db,
		logger:     logger,
		namespaces: make(map[string]pgNamespace),
	}, nil
}

// pgExtensionVersion returns the installed pgvector version, or "" when the
// extension is not installed.
func pgExtensionVersion(tx *gorm.DB) (string, error) {
	var versions []string
	if err := tx.Raw("SELECT extversion FROM pg_extension WHERE extname = 'vector'").Scan(&versions).Error; err != nil {
		return "", fmt.Errorf("failed to read the pgvector extension version: %w", err)
	}
	if len(versions) == 0 {
		return "", nil
	}
	return versions[0], nil
}

// pgCheckTypesVisible fails when the extension is installed in a schema the
// connection's search_path does not include: the catalog lists it, but every
// statement naming vector or halfvec would fail. The error names the schema
// to add and a command that keeps the connection's current search_path
// entries, since ALTER ROLE ... SET search_path replaces the whole value.
func pgCheckTypesVisible(tx *gorm.DB) error {
	var rows []struct {
		Visible    bool
		Schema     string
		Role       string
		SearchPath string
	}
	if err := tx.Raw(`SELECT to_regtype('vector') IS NOT NULL AND to_regtype('halfvec') IS NOT NULL AS visible, n.nspname AS schema, current_user AS role, current_setting('search_path') AS search_path
		FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = 'vector'`).Scan(&rows).Error; err != nil {
		return fmt.Errorf("failed to check the pgvector types are visible: %w", err)
	}
	if len(rows) == 0 || rows[0].Visible {
		return nil
	}
	row := rows[0]
	return fmt.Errorf("the pgvector extension is installed in schema %q, which is not on the search_path of Bifrost's database user (currently %s); have a database administrator add it while keeping every current entry, for example ALTER ROLE %s SET search_path = %s, %s; (check the role's own setting first, then restart Bifrost), or install the extension in a schema already on the search path",
		row.Schema, row.SearchPath, pgQuoteIdentifier(row.Role), row.SearchPath, pgQuoteIdentifier(row.Schema))
}

// pgQuoteIdentifier quotes s as a SQL identifier.
func pgQuoteIdentifier(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// pgAdvisoryXactLock takes a transaction-scoped advisory lock named by key,
// released at commit or rollback whichever pooled connection the
// transaction ran on.
func pgAdvisoryXactLock(tx *gorm.DB, key string) error {
	if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", key).Error; err != nil {
		return fmt.Errorf("failed to take advisory lock %s: %w", key, err)
	}
	return nil
}

// pgAdvisoryXactLockShared takes the shared form of the lock pgAdvisoryXactLock
// takes: any number of holders at once, none while the exclusive one is held.
func pgAdvisoryXactLockShared(tx *gorm.DB, key string) error {
	if err := tx.Exec("SELECT pg_advisory_xact_lock_shared(hashtextextended(?, 0))", key).Error; err != nil {
		return fmt.Errorf("failed to take shared advisory lock %s: %w", key, err)
	}
	return nil
}

// pgNamespaceLockKey names the advisory lock CreateNamespace and
// DeleteNamespace take exclusively, and a retry in withNamespace takes shared.
func pgNamespaceLockKey(namespace string) string {
	return "bifrost.vectorstore.namespace:" + namespace
}

// compareVersions compares dotted numeric versions; a missing or non-numeric
// part counts as 0, so "0.8" equals "0.8.0".
func compareVersions(a, b string) int {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(left), len(right)); i++ {
		var l, r int
		if i < len(left) {
			l, _ = strconv.Atoi(left[i])
		}
		if i < len(right) {
			r, _ = strconv.Atoi(right[i])
		}
		if l != r {
			if l < r {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Ping checks that the database answers.
func (s *PgVectorStore) Ping(ctx context.Context) error {
	return s.db.WithContext(ctx).Exec("SELECT 1").Error
}

// pgTableName names a namespace's table. The dimension is part of the name so
// a namespace dropped and recreated at another dimension is another table: a
// prepared statement cached against the old one could otherwise come back with
// a different result type (SQLSTATE 0A000).
func pgTableName(namespace string, dimension int) string {
	sum := sha256.Sum256([]byte(namespace))
	return "vector_store_" + hex.EncodeToString(sum[:8]) + "_" + strconv.Itoa(dimension)
}

// pgUsesHalfvec reports whether a namespace's index, and so its search
// expression, is halfvec: above pgVectorMaxIndexedDimension and up to the
// halfvec limit.
func pgUsesHalfvec(dimension int) bool {
	return dimension > pgVectorMaxIndexedDimension && dimension <= pgHalfvecMaxIndexedDimension
}

// pgDistanceSQL is the cosine-distance expression a search orders by, with one
// placeholder for the query vector. It must match the HNSW index expression
// pgCreateNamespaceSQL builds, or the planner falls back to an exact scan.
func pgDistanceSQL(dimension int) string {
	if pgUsesHalfvec(dimension) {
		d := strconv.Itoa(dimension)
		return "(embedding::halfvec(" + d + ") <=> ?::halfvec(" + d + "))"
	}
	return "(embedding <=> ?::vector)"
}

// pgCreateNamespaceSQL returns the statements that create a namespace's table
// and indexes. Every one is IF NOT EXISTS, so rerunning them is harmless.
//
// The indexes are built with the table, while it is empty, so building them
// costs nothing and blocks nothing. Properties added to an existing namespace
// later are stored and filterable but not indexed.
func pgCreateNamespaceSQL(table string, dimension int, properties map[string]VectorStoreProperties) []string {
	d := strconv.Itoa(dimension)
	statements := []string{
		`CREATE TABLE IF NOT EXISTS ` + table + ` (
			id text PRIMARY KEY,
			embedding vector(` + d + `),
			properties jsonb NOT NULL DEFAULT '{}'::jsonb
		)`,
		`CREATE INDEX IF NOT EXISTS ` + table + `_props ON ` + table + ` USING gin (properties jsonb_path_ops)`,
	}
	switch {
	case dimension <= pgVectorMaxIndexedDimension:
		statements = append(statements, `CREATE INDEX IF NOT EXISTS `+table+`_hnsw ON `+table+` USING hnsw (embedding vector_cosine_ops)`)
	case dimension <= pgHalfvecMaxIndexedDimension:
		statements = append(statements, `CREATE INDEX IF NOT EXISTS `+table+`_hnsw ON `+table+` USING hnsw ((embedding::halfvec(`+d+`)) halfvec_cosine_ops)`)
	}
	fields := make([]string, 0, len(properties))
	for field, property := range properties {
		if property.DataType == VectorStorePropertyTypeInteger {
			fields = append(fields, field)
		}
	}
	// Sorted so the statements, and so the tests, are deterministic.
	sort.Strings(fields)
	for _, field := range fields {
		sum := sha256.Sum256([]byte(field))
		statements = append(statements, `CREATE INDEX IF NOT EXISTS `+table+`_p`+hex.EncodeToString(sum[:4])+` ON `+table+` ((properties->`+pgQuoteLiteral(field)+`)) WHERE jsonb_typeof((properties->`+pgQuoteLiteral(field)+`)) = 'number'`)
	}
	return statements
}

// CreateNamespace creates the namespace's table and indexes and records it,
// or confirms an existing namespace has the requested dimension. Idempotent:
// callers invoke it on every startup.
func (s *PgVectorStore) CreateNamespace(ctx context.Context, namespace string, dimension int, properties map[string]VectorStoreProperties) error {
	if strings.TrimSpace(namespace) == "" {
		return fmt.Errorf("namespace is required")
	}
	if dimension <= 0 {
		return fmt.Errorf("dimension must be positive, got %d", dimension)
	}
	if dimension > pgMaxDimension {
		return fmt.Errorf("dimension must be at most %d, got %d", pgMaxDimension, dimension)
	}
	for field := range properties {
		if err := validatePgField(field); err != nil {
			return err
		}
	}

	table := pgTableName(namespace, dimension)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := pgAdvisoryXactLock(tx, pgNamespaceLockKey(namespace)); err != nil {
			return err
		}
		var existing []int
		if err := tx.Raw("SELECT dimension FROM "+pgRegistryTable+" WHERE name = ?", namespace).Scan(&existing).Error; err != nil {
			return fmt.Errorf("failed to look up namespace %s: %w", namespace, err)
		}
		if len(existing) > 0 && existing[0] != dimension {
			return dimensionMismatchError(namespace, existing[0], dimension, "table "+pgTableName(namespace, existing[0]))
		}
		// An existing table is left as it is: a CREATE INDEX for a property
		// added since would lock the populated table against writes until it
		// was built. A table someone dropped by hand is recreated whole.
		var tableExists bool
		if err := tx.Raw("SELECT to_regclass(?) IS NOT NULL", table).Scan(&tableExists).Error; err != nil {
			return fmt.Errorf("failed to look up the table of namespace %s: %w", namespace, err)
		}
		if !tableExists {
			for _, statement := range pgCreateNamespaceSQL(table, dimension, properties) {
				if err := tx.Exec(statement).Error; err != nil {
					return fmt.Errorf("failed to create namespace %s: %w", namespace, err)
				}
			}
		}
		if len(existing) == 0 {
			if err := tx.Exec("INSERT INTO "+pgRegistryTable+" (name, table_name, dimension) VALUES (?, ?, ?)", namespace, table, dimension).Error; err != nil {
				return fmt.Errorf("failed to record namespace %s: %w", namespace, err)
			}
		}
		return nil
	}, pgLockTxOptions)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.namespaces[namespace] = pgNamespace{table: table, dimension: dimension}
	s.mu.Unlock()
	return nil
}

// namespace returns a namespace's table and dimension, from memory when this
// process has seen it and from the registry otherwise (another replica may
// have created it). A namespace that is not recorded is ErrNotFound.
func (s *PgVectorStore) namespace(ctx context.Context, namespace string) (pgNamespace, error) {
	s.mu.RLock()
	ns, ok := s.namespaces[namespace]
	s.mu.RUnlock()
	if ok {
		return ns, nil
	}
	var rows []struct {
		TableName string
		Dimension int
	}
	if err := s.db.WithContext(ctx).Raw("SELECT table_name, dimension FROM "+pgRegistryTable+" WHERE name = ?", namespace).Scan(&rows).Error; err != nil {
		return pgNamespace{}, fmt.Errorf("failed to look up namespace %s: %w", namespace, err)
	}
	if len(rows) == 0 {
		return pgNamespace{}, fmt.Errorf("%w: namespace %s", ErrNotFound, namespace)
	}
	ns = pgNamespace{table: rows[0].TableName, dimension: rows[0].Dimension}
	s.mu.Lock()
	s.namespaces[namespace] = ns
	s.mu.Unlock()
	return ns, nil
}

// missingTable reports whether err is Postgres saying the table does not
// exist, which happens when another replica dropped the namespace after this
// one cached it. The cached entry is forgotten so the next call asks the
// registry again.
func (s *PgVectorStore) missingTable(namespace string, err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42P01" {
		return false
	}
	s.forget(namespace)
	return true
}

// forget drops a namespace's cached entry, so the next lookup reads the registry.
func (s *PgVectorStore) forget(namespace string) {
	s.mu.Lock()
	delete(s.namespaces, namespace)
	s.mu.Unlock()
}

// withNamespace runs fn against the namespace's table, on the connection fn
// is given, correcting for another replica having dropped or recreated the
// namespace since this one cached it.
//
// The cached mapping is only a hint. fn runs first against it, with no lock
// and no extra round trip. When the cached dimension differs from the one the
// caller's vector has (0 means none), or fn finds the table gone, fn runs
// again inside a transaction that holds the namespace's lock shared and reads
// the registry there: CreateNamespace and DeleteNamespace take that lock
// exclusively, so the namespace cannot be dropped or recreated while the retry
// runs, and its result describes the namespace as it is. ErrNotFound from the
// retry means the registry does not record the namespace, or records it but
// its table is gone (dropped by hand); either way no entry of it exists.
func (s *PgVectorStore) withNamespace(ctx context.Context, namespace string, dimension int, fn func(db *gorm.DB, ns pgNamespace) error) error {
	ns, err := s.namespace(ctx, namespace)
	if err != nil {
		return err
	}
	if dimension == 0 || dimension == ns.dimension {
		err = fn(s.db.WithContext(ctx), ns)
		if err == nil || !s.missingTable(namespace, err) {
			return err
		}
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := pgAdvisoryXactLockShared(tx, pgNamespaceLockKey(namespace)); err != nil {
			return err
		}
		var rows []struct {
			TableName string
			Dimension int
		}
		if err := tx.Raw("SELECT table_name, dimension FROM "+pgRegistryTable+" WHERE name = ?", namespace).Scan(&rows).Error; err != nil {
			return fmt.Errorf("failed to look up namespace %s: %w", namespace, err)
		}
		if len(rows) == 0 {
			s.forget(namespace)
			return fmt.Errorf("%w: namespace %s", ErrNotFound, namespace)
		}
		current := pgNamespace{table: rows[0].TableName, dimension: rows[0].Dimension}
		s.mu.Lock()
		s.namespaces[namespace] = current
		s.mu.Unlock()
		if err := fn(tx, current); err != nil {
			if s.missingTable(namespace, err) {
				return fmt.Errorf("%w: namespace %s", ErrNotFound, namespace)
			}
			return err
		}
		return nil
	}, pgLockTxOptions)
}

// readError maps a failed read to ErrNotFound when the namespace's table is
// gone, and wraps it with what was being done otherwise.
func (s *PgVectorStore) readError(namespace, action string, err error) error {
	if s.missingTable(namespace, err) {
		return fmt.Errorf("%w: namespace %s", ErrNotFound, namespace)
	}
	return fmt.Errorf("failed to %s in namespace %s: %w", action, namespace, err)
}

// ListNamespaces returns the recorded namespaces beginning with prefix, in
// byte order like every other store.
func (s *PgVectorStore) ListNamespaces(ctx context.Context, prefix string) ([]string, error) {
	var names []string
	if err := s.db.WithContext(ctx).Raw(`SELECT name FROM `+pgRegistryTable+` WHERE starts_with(name, ?) ORDER BY name COLLATE "C"`, prefix).Scan(&names).Error; err != nil {
		return nil, fmt.Errorf("failed to list namespaces: %w", err)
	}
	if names == nil {
		names = []string{}
	}
	return names, nil
}

// DeleteNamespace drops a namespace's table and forgets it. Idempotent:
// deleting a missing namespace is not an error.
func (s *PgVectorStore) DeleteNamespace(ctx context.Context, namespace string) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := pgAdvisoryXactLock(tx, pgNamespaceLockKey(namespace)); err != nil {
			return err
		}
		var tables []string
		if err := tx.Raw("SELECT table_name FROM "+pgRegistryTable+" WHERE name = ?", namespace).Scan(&tables).Error; err != nil {
			return fmt.Errorf("failed to look up namespace %s: %w", namespace, err)
		}
		if len(tables) == 0 {
			return nil
		}
		if err := tx.Exec("DROP TABLE IF EXISTS " + tables[0]).Error; err != nil {
			return fmt.Errorf("failed to drop namespace %s: %w", namespace, err)
		}
		return tx.Exec("DELETE FROM "+pgRegistryTable+" WHERE name = ?", namespace).Error
	}, pgLockTxOptions)
	if err != nil {
		return err
	}
	s.forget(namespace)
	return nil
}

// GetChunk retrieves a single entry by ID.
func (s *PgVectorStore) GetChunk(ctx context.Context, namespace string, id string) (SearchResult, error) {
	if strings.TrimSpace(id) == "" {
		return SearchResult{}, fmt.Errorf("id is required")
	}
	results, err := s.GetChunks(ctx, namespace, []string{id})
	if err != nil {
		return SearchResult{}, err
	}
	if len(results) == 0 {
		return SearchResult{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return results[0], nil
}

// GetChunks retrieves entries by ID. Missing IDs are skipped, as on every
// other store.
func (s *PgVectorStore) GetChunks(ctx context.Context, namespace string, ids []string) ([]SearchResult, error) {
	wanted := make([]string, 0, len(ids))
	for _, id := range ids {
		// A text column cannot hold a NUL byte, so such an id names no entry;
		// left in, it would make Postgres reject the whole jsonb batch.
		if strings.TrimSpace(id) != "" && !strings.ContainsRune(id, 0) {
			wanted = append(wanted, id)
		}
	}
	if len(wanted) == 0 {
		return []SearchResult{}, nil
	}
	// One bound jsonb array rather than a placeholder per id, which GORM
	// would expand into, and which the protocol caps at 65535.
	wantedJSON, err := pgJSON(wanted)
	if err != nil {
		return nil, err
	}
	var results []SearchResult
	err = s.withNamespace(ctx, namespace, 0, func(db *gorm.DB, ns pgNamespace) error {
		rows, err := db.Raw("SELECT id, properties FROM "+ns.table+" WHERE id = ANY(ARRAY(SELECT jsonb_array_elements_text(?::jsonb)))", wantedJSON).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		results = make([]SearchResult, 0, len(wanted))
		for rows.Next() {
			var id string
			var raw []byte
			if err := rows.Scan(&id, &raw); err != nil {
				return fmt.Errorf("failed to read entry in namespace %s: %w", namespace, err)
			}
			results = append(results, SearchResult{ID: id, Properties: decodePgProperties(raw)})
		}
		return rows.Err()
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, err
		}
		return nil, s.readError(namespace, "get entries", err)
	}
	return results, nil
}

// GetAll pages through the entries matching queries in id order. The cursor is
// the last id of the previous page, so a page costs what it holds however deep
// into the namespace it is.
func (s *PgVectorStore) GetAll(ctx context.Context, namespace string, queries []Query, selectFields []string, cursor *string, limit int64) ([]SearchResult, *string, error) {
	if limit <= 0 {
		limit = pgDefaultPageLimit
	}
	limit = min(limit, pgMaxPageLimit)
	where, args, err := pgWhere(queries)
	if err != nil {
		return nil, nil, err
	}
	if cursor != nil && *cursor != "" {
		where = append(where, "id > ?")
		args = append(args, *cursor)
	}
	includeVectors := IncludeVectorsRequested(ctx)
	columns := "id, properties"
	if includeVectors {
		columns += ", embedding::text"
	}
	args = append(args, limit+1)

	var page []SearchResult
	more := false
	err = s.withNamespace(ctx, namespace, 0, func(db *gorm.DB, ns pgNamespace) error {
		rows, err := db.Raw("SELECT "+columns+" FROM "+ns.table+pgWhereClause(where)+" ORDER BY id LIMIT ?", args...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		page = make([]SearchResult, 0, min(limit, pgDefaultPageLimit))
		more = false
		for rows.Next() {
			if int64(len(page)) == limit {
				more = true
				break
			}
			var id string
			var raw []byte
			var vector sql.NullString
			targets := []any{&id, &raw}
			if includeVectors {
				targets = append(targets, &vector)
			}
			if err := rows.Scan(targets...); err != nil {
				return fmt.Errorf("failed to read entry in namespace %s: %w", namespace, err)
			}
			result := SearchResult{ID: id, Properties: applyChromemSelectFields(decodePgProperties(raw), selectFields)}
			if includeVectors && vector.Valid {
				if result.Vector, err = parsePgVector(vector.String); err != nil {
					return fmt.Errorf("failed to read the vector of %s in namespace %s: %w", id, namespace, err)
				}
			}
			page = append(page, result)
		}
		return rows.Err()
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil, err
		}
		return nil, nil, s.readError(namespace, "read entries", err)
	}
	if !more {
		return page, nil, nil
	}
	next := page[len(page)-1].ID
	return page, &next, nil
}

// FiltersVectorReadsOnServer reports that GetAll filters in SQL, so a filtered
// read costs what it returns.
func (s *PgVectorStore) FiltersVectorReadsOnServer() bool { return true }

// GetNearest returns the entries closest to vector by cosine distance, scored
// as cosine similarity like every other store.
//
// The threshold is applied to the ordered rows rather than in SQL: a
// predicate on the distance would stop the planner using the HNSW index, and
// cutting an ordered list at the threshold gives the same rows.
func (s *PgVectorStore) GetNearest(ctx context.Context, namespace string, vector []float32, queries []Query, selectFields []string, threshold float64, limit int64) ([]SearchResult, error) {
	if len(vector) == 0 {
		return nil, fmt.Errorf("vector is required")
	}
	if pgZeroNorm(vector) {
		return nil, fmt.Errorf("query vector has zero norm, and cosine similarity is undefined for it")
	}
	if limit <= 0 {
		limit = 10
	}
	limit = min(limit, pgMaxPageLimit)
	where, filterArgs, err := pgWhere(queries)
	if err != nil {
		return nil, err
	}
	where = append([]string{"embedding IS NOT NULL"}, where...)
	// A halfvec namespace is searched in two stages, the pattern pgvector gives
	// for its quantised indexes. Half precision can move a score across the
	// threshold (0.79988 reads as 0.80006) and swap two near-equal rows, so the
	// index only picks candidates: limit plus pgHalfvecRerankExtra of them,
	// each carrying its full-precision distance. They are re-ranked by that
	// distance, thresholded on it, and cut to limit. Other namespaces rank and
	// threshold by the same full-precision distance, so they read limit rows
	// and stop at the first one below the threshold.
	//
	// The arguments are built once, outside the retry, with the extra column a
	// constant, so the common, non-halfvec search pays nothing for it.
	// len(vector) is the namespace's dimension; the search checks that before
	// using them.
	halfvec := pgUsesHalfvec(len(vector))
	fetch := limit
	exactColumn := ""
	// Capacity for the common search only: two interfaces fit the 32 bytes Go
	// keeps on the stack, and a halfvec search's extra argument grows it.
	args := make([]any, 0, len(filterArgs)+2)
	args = append(args, formatPgVector(vector))
	if halfvec {
		exactColumn = ", (embedding <=> ?::vector) AS exact_distance"
		args = append(args, args[0])
		fetch = limit + pgHalfvecRerankExtra
	}
	args = append(args, filterArgs...)
	args = append(args, fetch)

	var results []SearchResult
	err = s.withNamespace(ctx, namespace, len(vector), func(db *gorm.DB, ns pgNamespace) error {
		if len(vector) != ns.dimension {
			return fmt.Errorf("query vector dimension %d does not match namespace dimension %d", len(vector), ns.dimension)
		}
		query := "SELECT id, properties, " + pgDistanceSQL(ns.dimension) + " AS distance" + exactColumn + " FROM " + ns.table +
			pgWhereClause(where) + " ORDER BY distance LIMIT ?"
		return db.Transaction(func(tx *gorm.DB) error {
			// SET LOCAL lasts until the transaction ends, so the pooled connection
			// goes back with its defaults. Without iterative scans, a filter
			// applied after the index scan can leave fewer than limit rows
			// although more match further out.
			if err := tx.Exec("SET LOCAL hnsw.iterative_scan = strict_order").Error; err != nil {
				return err
			}
			if fetch > pgDefaultEfSearch {
				// Each scan iteration gathers ef_search candidates; a list at least
				// as long as the rows read fills it in one pass.
				if err := tx.Exec("SET LOCAL hnsw.ef_search = " + strconv.FormatInt(min(fetch, pgMaxEfSearch), 10)).Error; err != nil {
					return err
				}
			}
			rows, err := tx.Raw(query, args...).Rows()
			if err != nil {
				return err
			}
			defer rows.Close()
			results = make([]SearchResult, 0, min(fetch, pgDefaultPageLimit))
			for rows.Next() {
				var id string
				var raw []byte
				var distance float64
				// Scanned without a shared targets slice, which would cost the
				// common, non-halfvec search an allocation per row.
				if halfvec {
					var approximate float64
					if err := rows.Scan(&id, &raw, &approximate, &distance); err != nil {
						return err
					}
				} else if err := rows.Scan(&id, &raw, &distance); err != nil {
					return err
				}
				score := 1 - distance
				// A stored zero vector scores NaN, which no threshold comparison
				// accepts.
				if !(score >= threshold) {
					if halfvec {
						// Ordered by the halfvec distance, so a later candidate can
						// still clear the threshold at full precision.
						continue
					}
					// Ordered by this distance, so every later row is below the
					// threshold too. Postgres sorts NaN last.
					break
				}
				results = append(results, SearchResult{
					ID:         id,
					Score:      &score,
					Properties: applyChromemSelectFields(decodePgProperties(raw), selectFields),
				})
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if halfvec {
				// Full-precision order, which can differ from the halfvec one
				// between near-equal scores, then back to limit.
				sort.SliceStable(results, func(i, j int) bool { return *results[i].Score > *results[j].Score })
				if int64(len(results)) > limit {
					results = results[:limit]
				}
			}
			return nil
		})
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, err
		}
		return nil, s.readError(namespace, "search", err)
	}
	return results, nil
}

// pgZeroNorm reports whether every element of vector is zero.
func pgZeroNorm(vector []float32) bool {
	for _, value := range vector {
		if value != 0 {
			return false
		}
	}
	return true
}

// RequiresVectors returns false: the embedding column is nullable, so an
// entry can be stored for its properties alone.
func (s *PgVectorStore) RequiresVectors() bool {
	return false
}

// Add upserts an entry. An empty embedding stores the properties alone.
func (s *PgVectorStore) Add(ctx context.Context, namespace string, id string, embedding []float32, metadata map[string]interface{}) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}
	var vector any
	if len(embedding) > 0 {
		vector = formatPgVector(embedding)
	}
	properties := []byte("{}")
	if len(metadata) > 0 {
		var err error
		if properties, err = pgMarshal(metadata); err != nil {
			return fmt.Errorf("failed to encode metadata for %s: %w", id, err)
		}
	}
	err := s.withNamespace(ctx, namespace, len(embedding), func(db *gorm.DB, ns pgNamespace) error {
		if len(embedding) > 0 && len(embedding) != ns.dimension {
			return fmt.Errorf("embedding dimension %d does not match namespace dimension %d", len(embedding), ns.dimension)
		}
		return db.Exec("INSERT INTO "+ns.table+" (id, embedding, properties) VALUES (?, ?::vector, ?::jsonb) "+
			"ON CONFLICT (id) DO UPDATE SET embedding = EXCLUDED.embedding, properties = EXCLUDED.properties",
			id, vector, string(properties)).Error
	})
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: call CreateNamespace first", err)
	}
	if err != nil {
		return s.readError(namespace, "add "+id, err)
	}
	return nil
}

// Delete removes one entry. Deleting a missing entry, or from a missing
// namespace, is not an error: the caller wanted it absent, and it is.
func (s *PgVectorStore) Delete(ctx context.Context, namespace string, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}
	err := s.withNamespace(ctx, namespace, 0, func(db *gorm.DB, ns pgNamespace) error {
		return db.Exec("DELETE FROM "+ns.table+" WHERE id = ?", id).Error
	})
	// ErrNotFound comes from a registry read under the namespace's lock, so
	// the namespace, and with it the entry, really is absent.
	if err == nil || errors.Is(err, ErrNotFound) {
		return nil
	}
	return fmt.Errorf("failed to delete %s from namespace %s: %w", id, namespace, err)
}

// DeleteAll removes the entries matching queries (all of them when there are
// none) in one statement and reports each one removed.
func (s *PgVectorStore) DeleteAll(ctx context.Context, namespace string, queries []Query) ([]DeleteResult, error) {
	where, args, err := pgWhere(queries)
	if err != nil {
		return nil, err
	}
	var ids []string
	err = s.withNamespace(ctx, namespace, 0, func(db *gorm.DB, ns pgNamespace) error {
		return db.Raw("DELETE FROM "+ns.table+pgWhereClause(where)+" RETURNING id", args...).Scan(&ids).Error
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, err
		}
		return nil, s.readError(namespace, "delete entries", err)
	}
	results := make([]DeleteResult, len(ids))
	for i, id := range ids {
		results[i] = DeleteResult{ID: id, Status: DeleteStatusSuccess}
	}
	return results, nil
}

// Close is a no-op: the connection pool belongs to the logs store.
func (s *PgVectorStore) Close(ctx context.Context, namespace string) error {
	return nil
}

// pgWhereClause joins conditions into a WHERE clause, or "" when there are none.
func pgWhereClause(conditions []string) string {
	if len(conditions) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conditions, " AND ")
}

// pgWhere translates queries into SQL conditions over the properties column,
// with ? placeholders and their arguments. Field names are inlined as
// literals, not bound, so the planner can match them to the expression
// indexes; validatePgField and pgQuoteLiteral make that safe.
//
// The semantics follow the chromem store, which is the reference in-process
// implementation: a missing or null field matches only IsNull, values compare
// across int and float, and the range operators compare only like with like.
func pgWhere(queries []Query) ([]string, []any, error) {
	conditions := make([]string, 0, len(queries))
	var args []any
	for _, query := range queries {
		condition, queryArgs, err := pgCondition(query)
		if err != nil {
			return nil, nil, err
		}
		conditions = append(conditions, condition)
		args = append(args, queryArgs...)
	}
	return conditions, args, nil
}

func pgCondition(query Query) (string, []any, error) {
	if err := validatePgField(query.Field); err != nil {
		return "", nil, err
	}
	query.Value = pgWidenFloat32(query.Value)
	key := pgQuoteLiteral(query.Field)
	field := "(properties->" + key + ")"
	present := field + " IS NOT NULL AND " + field + " <> 'null'::jsonb"

	switch query.Operator {
	case QueryOperatorIsNull:
		return "(" + field + " IS NULL OR " + field + " = 'null'::jsonb)", nil, nil
	case QueryOperatorIsNotNull:
		return "(" + present + ")", nil, nil
	case QueryOperatorEqual, QueryOperatorNotEqual,
		QueryOperatorGreaterThan, QueryOperatorLessThan, QueryOperatorGreaterThanOrEqual, QueryOperatorLessThanOrEqual,
		QueryOperatorLike, QueryOperatorContainsAny, QueryOperatorContainsAll:
	default:
		return "", nil, fmt.Errorf("%w: unsupported operator %q", ErrQuerySyntax, query.Operator)
	}
	if query.Value == nil {
		// Nothing compares with nil; see matchesChromemQuery.
		if query.Operator == QueryOperatorNotEqual {
			return "(" + present + ")", nil, nil
		}
		return "FALSE", nil, nil
	}

	switch query.Operator {
	case QueryOperatorEqual:
		if !pgComparable(query.Value) {
			// Chromem compares only numbers, strings and booleans; a list, a
			// map or a typed nil equals nothing.
			return "FALSE", nil, nil
		}
		// Containment rather than equality so the GIN index serves it.
		contains, err := pgJSON(map[string]any{query.Field: query.Value})
		if err != nil {
			return "", nil, err
		}
		return "properties @> ?::jsonb", []any{contains}, nil
	case QueryOperatorNotEqual:
		if !pgComparable(query.Value) {
			// Incomparable, so unequal to every present value, as in chromem.
			return "(" + present + ")", nil, nil
		}
		value, err := pgJSON(query.Value)
		if err != nil {
			return "", nil, err
		}
		return "(" + present + " AND " + field + " <> ?::jsonb)", []any{value}, nil
	case QueryOperatorGreaterThan, QueryOperatorLessThan, QueryOperatorGreaterThanOrEqual, QueryOperatorLessThanOrEqual:
		if _, isNumber := chromemToFloat(query.Value); !isNumber {
			if _, isString := query.Value.(string); !isString {
				return "", nil, fmt.Errorf("%w: %s on %s needs a number or a string, got %T", ErrQuerySyntax, query.Operator, query.Field, query.Value)
			}
		}
		operator := map[QueryOperator]string{
			QueryOperatorGreaterThan:        ">",
			QueryOperatorLessThan:           "<",
			QueryOperatorGreaterThanOrEqual: ">=",
			QueryOperatorLessThanOrEqual:    "<=",
		}[query.Operator]
		// Only like compares with like: jsonb orders values of different types
		// by type, so without the type check a string field would compare
		// greater than any number.
		if text, isString := query.Value.(string); isString {
			// jsonb compares strings in the database collation; COLLATE "C"
			// compares bytes, as chromem does.
			return "(jsonb_typeof(" + field + ") = 'string' AND (properties->>" + key + `) COLLATE "C" ` + operator + " ?)", []any{text}, nil
		}
		value, err := pgJSON(query.Value)
		if err != nil {
			return "", nil, err
		}
		// Compared as jsonb so the integer property's expression index serves it.
		return "(jsonb_typeof(" + field + ") = 'number' AND " + field + " " + operator + " ?::jsonb)", []any{value}, nil
	case QueryOperatorLike:
		pattern, ok := query.Value.(string)
		if !ok {
			return "", nil, fmt.Errorf("%w: Like on %s needs a string, got %T", ErrQuerySyntax, query.Field, query.Value)
		}
		return "(jsonb_typeof(" + field + ") = 'string' AND properties->>" + key + " LIKE ?)", []any{pgLikePattern(pattern)}, nil
	case QueryOperatorContainsAny, QueryOperatorContainsAll:
		values := chromemToSlice(query.Value)
		if len(values) == 0 {
			return "FALSE", nil, nil
		}
		joiner := " OR "
		if query.Operator == QueryOperatorContainsAll {
			joiner = " AND "
		}
		parts := make([]string, 0, len(values))
		args := make([]any, 0, 2*len(values))
		for _, value := range values {
			value = pgWidenFloat32(value)
			if !pgComparable(value) {
				// Chromem compares only numbers, strings and booleans, so nil, a
				// map or a list matches nothing. Containment would match a null
				// property, or a stored object by subset.
				parts = append(parts, "FALSE")
				continue
			}
			// The field holds the value either as an array element or as itself.
			inArray, err := pgJSON(map[string]any{query.Field: []any{value}})
			if err != nil {
				return "", nil, err
			}
			scalar, err := pgJSON(map[string]any{query.Field: value})
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, "(properties @> ?::jsonb OR properties @> ?::jsonb)")
			args = append(args, inArray, scalar)
		}
		return "(" + strings.Join(parts, joiner) + ")", args, nil
	default:
		return "", nil, fmt.Errorf("%w: unsupported operator %q", ErrQuerySyntax, query.Operator)
	}
}

// validatePgField rejects field names that cannot be inlined as a literal.
func validatePgField(field string) error {
	if field == "" {
		return fmt.Errorf("%w: field is required", ErrQuerySyntax)
	}
	if strings.ContainsRune(field, 0) {
		return fmt.Errorf("%w: field %q contains a NUL byte", ErrQuerySyntax, field)
	}
	return nil
}

// pgLiteralEscaper escapes a string for an escape string constant (E'...').
// Backslashes are escaped, so the literal reads the same whatever
// standard_conforming_strings is, and ? becomes \x3f because GORM takes every ?
// in a statement for a placeholder, inside a quoted literal or not.
var pgLiteralEscaper = strings.NewReplacer(`\`, `\\`, `'`, `''`, `?`, `\x3f`)

// pgQuoteLiteral quotes s as a SQL string literal. Postgres decodes it to the
// same constant as a plain '...' literal, so the planner still matches it to
// the expression indexes.
func pgQuoteLiteral(s string) string {
	return "E'" + pgLiteralEscaper.Replace(s) + "'"
}

// pgLikePattern turns a Like value into a SQL LIKE pattern with chromem's
// meaning: % and * are wildcards, and a pattern without either matches as a
// substring. A literal _ or backslash is escaped, since LIKE gives them meaning.
func pgLikePattern(pattern string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `_`, `\_`, `*`, `%`).Replace(pattern)
	if !strings.Contains(escaped, "%") {
		return "%" + escaped + "%"
	}
	return escaped
}

// pgWidenFloat32 widens a float32 operand to float64 the way chromem's
// chromemToFloat does, so float32(0.1) is 0.10000000149011612 on both stores
// rather than 0.1 here, which JSON encoding a float32 would give.
func pgWidenFloat32(value any) any {
	if f, ok := value.(float32); ok {
		return float64(f)
	}
	return value
}

// pgComparable reports whether chromem's compareChromemValues can compare
// value with a stored scalar: a number, a string or a boolean.
func pgComparable(value any) bool {
	if _, isNumber := chromemToFloat(value); isNumber {
		return true
	}
	switch value.(type) {
	case string, bool:
		return true
	}
	return false
}

// pgMarshal encodes the metadata Add stores. The shared sorted encoder takes
// about half the time of encoding/json on a semantic cache entry and allocates
// a seventh as often, but it passes invalid UTF-8 through, which jsonb rejects.
// On that rare input it falls back to encoding/json, which writes U+FFFD in place
// of the bad byte, as pgJSON does for filter values, so a write and a filter on
// the same value agree.
func pgMarshal(v any) ([]byte, error) {
	encoded, err := providerUtils.MarshalSorted(v)
	if err != nil {
		return nil, err
	}
	if utf8.Valid(encoded) {
		return encoded, nil
	}
	return json.Marshal(v)
}

// pgJSON encodes a filter value. It stays on encoding/json: these values are tens
// of bytes, and sonic's pooled 4 KB buffer, refilled after each GC between
// searches, cost a filtered GetNearest about 3 KB more per call than it saved.
func pgJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrQuerySyntax, err)
	}
	return string(encoded), nil
}

// decodePgProperties decodes the properties column into the types the
// interface declares, the same way the chromem store decodes its metadata.
func decodePgProperties(raw []byte) map[string]interface{} {
	properties := map[string]interface{}{}
	if len(raw) == 0 {
		return properties
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var decoded map[string]interface{}
	if err := decoder.Decode(&decoded); err != nil {
		return properties
	}
	for key, value := range decoded {
		properties[key] = normalizeChromemDecoded(value)
	}
	return properties
}

// formatPgVector writes a vector in pgvector's text form, [1,2,3].
func formatPgVector(vector []float32) string {
	buf := make([]byte, 0, 2+len(vector)*10)
	buf = append(buf, '[')
	for i, value := range vector {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = strconv.AppendFloat(buf, float64(value), 'g', -1, 32)
	}
	return string(append(buf, ']'))
}

// parsePgVector reads pgvector's text form back into a vector.
func parsePgVector(text string) ([]float32, error) {
	text = strings.TrimSpace(text)
	if len(text) < 2 || text[0] != '[' || text[len(text)-1] != ']' {
		return nil, fmt.Errorf("malformed vector %q", text)
	}
	body := text[1 : len(text)-1]
	if body == "" {
		return []float32{}, nil
	}
	parts := strings.Split(body, ",")
	vector := make([]float32, len(parts))
	for i, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 32)
		if err != nil {
			return nil, fmt.Errorf("malformed vector element %q: %w", part, err)
		}
		vector[i] = float32(value)
	}
	return vector, nil
}
