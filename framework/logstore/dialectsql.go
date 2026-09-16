package logstore

import "fmt"

// unixBucketExpr returns a SQL expression that truncates the `timestamp` column
// to a bucket boundary and yields an integer unix-seconds value, per dialect.
// The returned string is a complete SQL fragment (the sqlite branch contains a
// literal strftime '%s' specifier, which is safe to pass as an argument to a
// later fmt.Sprintf since only the format string is scanned for verbs).
//
// Keeping the per-dialect bucket math in one place lets the ~17 histogram
// queries in rdb.go share a single, dialect-correct expression instead of
// branching inline (which previously routed ClickHouse into the Postgres
// EXTRACT(EPOCH ...) path - invalid ClickHouse SQL).
func unixBucketExpr(dialect string, bucketSizeSeconds int64) string {
	switch dialect {
	case "sqlite":
		return fmt.Sprintf("(CAST(strftime('%%s', timestamp) AS INTEGER) / %d) * %d", bucketSizeSeconds, bucketSizeSeconds)
	case "mysql":
		return fmt.Sprintf("(FLOOR(UNIX_TIMESTAMP(timestamp) / %d) * %d)", bucketSizeSeconds, bucketSizeSeconds)
	case "clickhouse":
		return fmt.Sprintf("toInt64(intDiv(toUnixTimestamp(timestamp), %d) * %d)", bucketSizeSeconds, bucketSizeSeconds)
	default:
		// PostgreSQL (and others)
		return fmt.Sprintf("CAST(FLOOR(EXTRACT(EPOCH FROM timestamp) / %d) * %d AS BIGINT)", bucketSizeSeconds, bucketSizeSeconds)
	}
}

// fanoutDimensionColumns maps a scalar dimension id column to its JSON-array
// id/name columns and its scalar name column. The org-hierarchy dimensions are
// single-valued on the scalar column (set by the VK path and by pre-migration
// rows) and multi-valued on the JSON-array column (set by the enterprise
// user/AP path, which is the only writer that knows a request's full hierarchy).
// ok is false for dimensions that have no array column at all (user, virtual
// key, provider, app, user agent) — those keep plain scalar attribution.
func fanoutDimensionColumns(idCol string) (arrIDs, arrNames, scalarName string, ok bool) {
	switch idCol {
	case "team_id":
		return "team_ids", "team_names", "team_name", true
	case "business_unit_id":
		return "business_unit_ids", "business_unit_names", "business_unit_name", true
	case "customer_id":
		return "customer_ids", "customer_names", "customer_name", true
	}
	return "", "", "", false
}

// dimensionFanoutFrom returns a FROM subquery, aliased AS logs, that fans each
// log row out to one row per associated dimension value, exposing derived
// `dim_id` / `dim_name` columns alongside all original log columns (l.*) so DAC
// scope and the regular filter predicates still resolve. Rows whose JSON-array
// column is set are unnested with the id and name aligned by position; rows
// without it (pre-upgrade rows, or the VK path, which only writes the scalar)
// fall back to the scalar id/name. The two branches are mutually exclusive, so
// no row is counted twice for the same dimension value.
//
// Returns ("", false) when the dimension has no array column, or when the
// dialect has no fan-out implementation — callers then read the scalar column
// exactly as before. No bind args: every identifier is an internal constant.
func dimensionFanoutFrom(dialect, idCol string) (string, bool) {
	arrIDs, arrNames, scalarName, ok := fanoutDimensionColumns(idCol)
	if !ok {
		return "", false
	}
	switch dialect {
	case "postgres":
		// Delegated so the Postgres text stays byte-identical to what the
		// filter matviews were built from (see teamOrBUFanoutFrom).
		return teamOrBUFanoutFrom(idCol)
	case "sqlite":
		return sqliteDimensionFanoutFrom(arrIDs, arrNames, idCol, scalarName), true
	case "clickhouse":
		return clickhouseDimensionFanoutFrom(arrIDs, arrNames, idCol, scalarName), true
	}
	return "", false
}

// sqliteDimensionFanoutFrom builds the SQLite fan-out subquery using the JSON1
// extension (always compiled in: the bundled mattn/go-sqlite3 amalgamation
// registers json_each unconditionally, and JSON has been on by default since
// SQLite 3.38).
//
// Two details are load-bearing:
//
//   - json_each() raises "malformed JSON" on a non-JSON argument, and SQLite
//     gives no guarantee that the branch's WHERE runs before the table-valued
//     function is invoked for the row. Rewriting the argument to '[]' via CASE
//     makes the array branch yield zero rows for NULL/garbage/non-array values
//     instead of failing the whole query, which is what keeps the two branches
//     mutually exclusive without depending on evaluation order.
//   - the name is read positionally with json_extract(names, '$[<key>]')
//     rather than a second json_each in a LEFT JOIN, because json_each sets
//     `key` to the 0-based array index and json_extract accepts a runtime-built
//     path. An id with no matching name yields NULL -> ”.
//
// json_valid must precede json_type in both guards: json_type() throws on
// malformed input and SQLite evaluates AND left to right.
func sqliteDimensionFanoutFrom(arrIDs, arrNames, idCol, scalarName string) string {
	isArray := func(col string) string {
		return fmt.Sprintf("l.%[1]s IS NOT NULL AND json_valid(l.%[1]s) AND json_type(l.%[1]s) = 'array'", col)
	}
	// Rewrites a possibly-NULL / non-array column to a safe JSON array literal.
	safeArray := func(col string) string {
		return fmt.Sprintf("CASE WHEN %s THEN l.%s ELSE '[]' END", isArray(col), col)
	}
	return fmt.Sprintf(`(
	SELECT l.*, t.value AS dim_id, COALESCE(json_extract(%[2]s, '$[' || t.key || ']'), '') AS dim_name
	FROM logs l
	JOIN json_each(%[1]s) t
	UNION ALL
	SELECT l.*, COALESCE(l.%[3]s, '') AS dim_id, COALESCE(l.%[4]s, '') AS dim_name
	FROM logs l
	WHERE NOT (%[5]s)
) AS logs`, safeArray(arrIDs), safeArray(arrNames), idCol, scalarName, isArray(arrIDs))
}

// clickhouseDimensionFanoutFrom builds the ClickHouse fan-out subquery. The
// array columns are Nullable(String) holding the same JSON text as the other
// backends, so every JSON function is guarded with ifNull.
//
// A single ARRAY JOIN over an if() replaces the UNION ALL used elsewhere: the
// array branch and the scalar branch are mutually exclusive by construction, so
// a row can never be counted twice. Plain ARRAY JOIN (not LEFT ARRAY JOIN) drops
// rows whose array is empty, which reproduces the Postgres lateral's semantics.
// arrayEnumerate yields 1-based indices matching ClickHouse array indexing, and
// the length guard is the equivalent of the Postgres LEFT JOIN ON ordinality.
func clickhouseDimensionFanoutFrom(arrIDs, arrNames, idCol, scalarName string) string {
	ids := fmt.Sprintf("JSONExtract(ifNull(l.%s, ''), 'Array(String)')", arrIDs)
	names := fmt.Sprintf("JSONExtract(ifNull(l.%s, ''), 'Array(String)')", arrNames)
	return fmt.Sprintf(`(
	SELECT l.*, fan.1 AS dim_id, fan.2 AS dim_name
	FROM logs AS l
	ARRAY JOIN if(
		isValidJSON(ifNull(l.%[3]s, '')) AND JSONType(ifNull(l.%[3]s, '')) = 'Array',
		arrayMap(i -> (%[1]s[i], if(i <= length(%[2]s), %[2]s[i], '')), arrayEnumerate(%[1]s)),
		[(ifNull(l.%[4]s, ''), ifNull(l.%[5]s, ''))]
	) AS fan
) AS logs`, ids, names, arrIDs, idCol, scalarName)
}
