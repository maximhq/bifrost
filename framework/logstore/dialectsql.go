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
