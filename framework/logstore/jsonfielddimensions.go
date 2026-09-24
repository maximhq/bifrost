package logstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// JSON-extracted ranking dimensions: labels that live inside a JSON blob
// column instead of a real, indexed column - error classification, retry
// fail reasons, guardrail decisions. There is no migration or backfill
// behind any of this: the data already exists on every row (see
// Log.AfterFind / DeserializeFields), this only teaches GetDimensionRankings
// a second way to GROUP BY a value that was always stored there.
//
// This generalizes two precedents already in this package rather than
// inventing a third style:
//   - The cache_hit_type filter (rdb.go, applyFilters) proved that filtering
//     on a value buried in a JSON-in-text column works without a migration.
//     It uses a loose regex because hit_type is a unique, top-level key; the
//     dimensions here sit one level deeper (error_details.error.type,
//     guardrail_debug.judge_calls[].rule_name), where a flat regex cannot
//     reliably tell which "type" or which array it found. So extraction here
//     uses real JSON functions instead: Postgres's `::jsonb` cast guarded by
//     the `IS JSON` predicate (Postgres 16+), SQLite's json1 extension, and
//     ClickHouse's JSON functions.
//   - teamOrBUFanoutFrom / dimensionFanoutFrom (dialectsql.go) proved the
//     fan-out shape: a FROM subquery, aliased AS logs, that turns one row
//     with an array into one row per array element, so the same
//     GetDimensionRankings grouping machinery (Group/Order/applyRankingLimit)
//     works unchanged. That precedent fans out a JSON array of *strings*
//     (team_ids) with a parallel name array; the dimensions here fan out a
//     JSON array of *objects* (attempt_trail, guardrail_debug.judge_calls)
//     and extract one field per element - the array shape differs, but the
//     "pre-sanitize the argument to a safe empty array rather than filtering
//     after" discipline is carried over unchanged, since that is what makes
//     the fan-out safe against malformed or absent JSON without depending on
//     any particular WHERE/FROM evaluation order.
//
// None of these dimensions is bucketed: a row that never failed has no
// error_type, and a row nothing guardrailed has no guardrail_rule. That is a
// real, useful "not applicable" rather than "Unassigned" - a caller who wants
// a breakdown of errors already filters to status=error, so a bucketed
// "99% Unassigned" row would only be noise.
const (
	RankingDimensionErrorType       RankingDimension = "error_type"
	RankingDimensionErrorCode       RankingDimension = "error_code"
	RankingDimensionFailReason      RankingDimension = "fail_reason"
	RankingDimensionGuardrailRule   RankingDimension = "guardrail_rule"
	RankingDimensionGuardrailAction RankingDimension = "guardrail_action"
	RankingDimensionStatusCode      RankingDimension = "status_code"
)

// jsonFieldDimensionShape describes where one JSON-extracted dimension's
// label comes from. Every field name here is an internal constant (a Go
// struct field's json tag), never user input, so splicing them into SQL text
// carries no injection risk - the same trust model dimensionFanoutFrom
// documents for its own column-name parameters.
type jsonFieldDimensionShape struct {
	// column is the log row's raw JSON text column.
	column string
	// arrayField is the object key holding the array to fan out (e.g.
	// "judge_calls" inside guardrail_debug). Empty means column itself is
	// the JSON array (attempt_trail) - fannedOut is true either way.
	arrayField string
	// nestedUnder is the key a single-value dimension's label sits under,
	// one level inside column (e.g. "error" for error_details's nested
	// error.type / error.code). Only set when fannedOut is false.
	nestedUnder string
	// field is the key holding the label itself: within one array element
	// for a fanned-out dimension, or within the nestedUnder sub-object for a
	// single-value one.
	field string
	// number marks a single-value dimension whose label is a JSON number at the
	// top level of column (error_details.status_code), rendered as text so it
	// groups, orders and filters like every other label here.
	number bool
	// fannedOut is true for dimensions with zero-to-many values per row
	// (attempt_trail has one entry per retry attempt; judge_calls one per
	// guardrail rule evaluated), matching dimensionReadSource.FannedOut:
	// TotalAttributedRequests can then exceed TotalActualRequests, the same
	// as the existing team/customer/business-unit fan-out.
	fannedOut bool
}

var jsonFieldDimensions = map[RankingDimension]jsonFieldDimensionShape{
	RankingDimensionErrorType:       {column: "error_details", nestedUnder: "error", field: "type"},
	RankingDimensionErrorCode:       {column: "error_details", nestedUnder: "error", field: "code"},
	RankingDimensionFailReason:      {column: "attempt_trail", field: "fail_reason", fannedOut: true},
	RankingDimensionGuardrailRule:   {column: "guardrail_debug", arrayField: "judge_calls", field: "rule_name", fannedOut: true},
	RankingDimensionGuardrailAction: {column: "guardrail_debug", arrayField: "judge_calls", field: "action", fannedOut: true},
	RankingDimensionStatusCode:      {column: "error_details", field: "status_code", number: true},
}

// jsonFieldDimensionSource resolves a JSON-extracted dimension to a
// dimensionReadSource for the given dialect, the same type the
// column-backed dimensions (team, provider, ...) resolve to - which is what
// lets GetDimensionRankings run one grouping/ordering/limiting code path
// over both kinds of dimension. ok is false for a dimension this file does
// not know about, or a dialect with no implementation for it.
func jsonFieldDimensionSource(dialect string, dimension RankingDimension) (dimensionReadSource, bool) {
	shape, ok := jsonFieldDimensions[dimension]
	if !ok {
		return dimensionReadSource{}, false
	}
	if shape.number {
		expr, ok := jsonTopLevelNumberExpr(dialect, shape.column, shape.field)
		if !ok {
			return dimensionReadSource{}, false
		}
		return dimensionReadSource{IDExpr: expr, NameCol: expr}, true
	}
	if !shape.fannedOut {
		expr, ok := jsonObjectFieldExpr(dialect, shape.column, shape.nestedUnder, shape.field)
		if !ok {
			return dimensionReadSource{}, false
		}
		// id and name are the same expression: the extracted label
		// ("rate_limit_error") is already the human-readable value, exactly
		// like the "app" and "user_agent" dimensions where IDCol == NameCol.
		return dimensionReadSource{IDExpr: expr, NameCol: expr}, true
	}
	from, ok := jsonArrayFieldFanoutFrom(dialect, shape.column, shape.arrayField, shape.field)
	if !ok {
		return dimensionReadSource{}, false
	}
	return dimensionReadSource{FromSQL: from, IDExpr: "dim_id", NameCol: "dim_id", FannedOut: true}, true
}

// jsonObjectFieldExpr returns a SQL expression yielding one field nested one
// level inside a JSON object column, or ("", false) for an unsupported
// dialect. The expression evaluates to SQL NULL when column is NULL, is not
// JSON-object-shaped, has no nestedUnder key, or that sub-object has no
// field key - every one of those is "this row has no value here", not an
// error, since a row that never failed simply has no error_details.error.type.
func jsonObjectFieldExpr(dialect, column, nestedUnder, field string) (string, bool) {
	switch dialect {
	case "postgres":
		// IS JSON OBJECT is the Postgres 16+ predicate teamOrBUFanoutFrom
		// already relies on (as IS JSON ARRAY) to test JSON-shapedness
		// without risking the hard error a malformed ::jsonb cast raises.
		return fmt.Sprintf(
			`(CASE WHEN %[1]s IS NOT NULL AND %[1]s IS JSON OBJECT THEN %[1]s::jsonb -> '%[2]s' ->> '%[3]s' ELSE NULL END)`,
			column, nestedUnder, field,
		), true
	case "sqlite":
		// json_extract returns NULL for an absent path once its first
		// argument is confirmed valid JSON; it only throws on genuinely
		// malformed input, which json_valid guards against first.
		return fmt.Sprintf(
			`(CASE WHEN %[1]s IS NOT NULL AND json_valid(%[1]s) THEN json_extract(%[1]s, '$.%[2]s.%[3]s') ELSE NULL END)`,
			column, nestedUnder, field,
		), true
	case "clickhouse":
		// JSONExtractString takes a variadic key path and returns '' (never
		// an error) for a missing path or invalid JSON once isValidJSON
		// guards the latter; '' is normalized like NULL by the exclusion
		// clause GetJSONFieldDimensionRankings applies (JSONExtractString
		// never itself returns NULL - a ClickHouse String column has no
		// NULL to return).
		return fmt.Sprintf(
			`if(isValidJSON(ifNull(%[1]s, '')), JSONExtractString(ifNull(%[1]s, ''), '%[2]s', '%[3]s'), '')`,
			column, nestedUnder, field,
		), true
	}
	return "", false
}

// jsonTopLevelNumberExpr returns a SQL expression yielding a numeric field at
// the top level of a JSON object column as text ("429"), or ("", false) for an
// unsupported dialect. Like jsonObjectFieldExpr it evaluates to NULL (”, on
// ClickHouse) for a NULL, malformed or field-less column rather than erroring.
// Text rather than a number so the one expression serves GROUP BY, the
// not-empty exclusion and an IN filter alike, on every dialect.
func jsonTopLevelNumberExpr(dialect, column, field string) (string, bool) {
	switch dialect {
	case "postgres":
		return fmt.Sprintf(
			`(CASE WHEN %[1]s IS NOT NULL AND %[1]s IS JSON OBJECT THEN %[1]s::jsonb ->> '%[2]s' ELSE NULL END)`,
			column, field,
		), true
	case "sqlite":
		// json_extract yields an INTEGER here, and SQLite does not compare an
		// integer expression equal to text, so the cast is what lets '429' match.
		return fmt.Sprintf(
			`(CASE WHEN %[1]s IS NOT NULL AND json_valid(%[1]s) THEN CAST(json_extract(%[1]s, '$.%[2]s') AS TEXT) ELSE NULL END)`,
			column, field,
		), true
	case "clickhouse":
		// JSONExtractString returns '' for a number, so the raw JSON text is
		// taken instead: for a number that is its digits, and '' when absent.
		return fmt.Sprintf(
			`if(isValidJSON(ifNull(%[1]s, '')), JSONExtractRaw(ifNull(%[1]s, ''), '%[2]s'), '')`,
			column, field,
		), true
	}
	return "", false
}

// jsonArrayFieldFanoutFrom returns a FROM subquery, aliased AS logs, that
// fans each log row out to one row per element of a JSON array of objects
// (column itself when arrayField is "", otherwise the array at that key
// inside column), exposing the extracted `field` from each element as
// `dim_id` alongside every original log column (l.*), the same contract
// dimensionFanoutFrom's team/customer subqueries expose.
//
// Unlike that precedent there is no scalar-fallback branch: attempt_trail
// and guardrail_debug have no alternate single-value attribution to fall
// back to, so a row with no array (or an empty one, or an element missing
// `field`) simply contributes no row to the fan-out - which is the correct
// "not applicable" reading for a request that had no failed attempts or no
// guardrail hits, not an omission to paper over with a placeholder.
//
// Every dialect branch pre-sanitizes the array argument to a guaranteed-safe
// empty array rather than filtering unsafe rows out afterward, matching
// sqliteDimensionFanoutFrom's safeArray discipline: it is what keeps this
// correct without depending on any particular WHERE/FROM evaluation order.
func jsonArrayFieldFanoutFrom(dialect, column, arrayField, field string) (string, bool) {
	switch dialect {
	case "postgres":
		var arrayExpr string
		if arrayField == "" {
			arrayExpr = fmt.Sprintf(
				`CASE WHEN l.%[1]s IS NOT NULL AND l.%[1]s IS JSON ARRAY THEN l.%[1]s::jsonb ELSE '[]'::jsonb END`,
				column,
			)
		} else {
			arrayExpr = fmt.Sprintf(
				`CASE WHEN l.%[1]s IS NOT NULL AND l.%[1]s IS JSON OBJECT AND l.%[1]s::jsonb -> '%[2]s' IS JSON ARRAY THEN l.%[1]s::jsonb -> '%[2]s' ELSE '[]'::jsonb END`,
				column, arrayField,
			)
		}
		return fmt.Sprintf(`(
	SELECT l.*, fan.dim_id AS dim_id
	FROM logs l
	CROSS JOIN LATERAL (
		SELECT elem ->> '%[2]s' AS dim_id
		FROM jsonb_array_elements(%[1]s) AS elem
	) AS fan
) AS logs`, arrayExpr, field), true

	case "sqlite":
		var arrayExpr string
		if arrayField == "" {
			arrayExpr = fmt.Sprintf(
				`CASE WHEN l.%[1]s IS NOT NULL AND json_valid(l.%[1]s) AND json_type(l.%[1]s) = 'array' THEN l.%[1]s ELSE '[]' END`,
				column,
			)
		} else {
			// json_extract(l.column, '$.arrayField') is only reached once
			// json_valid(l.column) is confirmed true (SQLite's AND
			// short-circuits left to right), so it can never throw here even
			// though json_extract itself would on a malformed first argument.
			arrayExpr = fmt.Sprintf(
				`CASE WHEN l.%[1]s IS NOT NULL AND json_valid(l.%[1]s) AND json_type(json_extract(l.%[1]s, '$.%[2]s')) = 'array' THEN json_extract(l.%[1]s, '$.%[2]s') ELSE '[]' END`,
				column, arrayField,
			)
		}
		return fmt.Sprintf(`(
	SELECT l.*, json_extract(t.value, '$.%[2]s') AS dim_id
	FROM logs l
	JOIN json_each(%[1]s) t
) AS logs`, arrayExpr, field), true

	case "clickhouse":
		var arrayExpr string
		if arrayField == "" {
			arrayExpr = fmt.Sprintf(`JSONExtractArrayRaw(ifNull(l.%[1]s, ''))`, column)
		} else {
			arrayExpr = fmt.Sprintf(`JSONExtractArrayRaw(ifNull(l.%[1]s, ''), '%[2]s')`, column, arrayField)
		}
		// isValidJSON guards the outer column; JSONExtractArrayRaw on a
		// column that is valid JSON but not (or has no) array at the target
		// path returns an empty array on its own, so no second guard is
		// needed there - unlike Postgres/SQLite, ClickHouse's JSON functions
		// do not throw on a shape mismatch.
		return fmt.Sprintf(`(
	SELECT l.*, fan AS dim_id
	FROM logs AS l
	ARRAY JOIN if(
		isValidJSON(ifNull(l.%[3]s, '')),
		arrayMap(x -> JSONExtractString(x, '%[2]s'), %[1]s),
		[]
	) AS fan
) AS logs`, arrayExpr, field, column), true
	}
	return "", false
}

// GetJSONFieldDimensionRankings ranks a JSON-extracted dimension (error_type,
// error_code, fail_reason, guardrail_rule, guardrail_action) the same way
// GetDimensionRankings ranks a column-backed one: grouped, ordered by request
// count, limited, with a trend against the immediately preceding period. It
// is its own function rather than a branch threaded through
// GetDimensionRankings because these dimensions diverge from every
// column-backed one in two ways that would otherwise need an extra parameter
// on nearly every line of that function: there is no Postgres
// materialized-view fast path (the matviews carry no JSON-extracted values),
// and there is no Unassigned bucket - a row with nothing to contribute here
// (never failed, never guardrailed) is excluded, not counted as unassigned.
//
// Actual/attributed request counts are computed the same way for every
// dimension here, not only the array-shaped ones (fail_reason,
// guardrail_rule, guardrail_action): error_type and error_code are
// single-valued per row, so for them the two numbers always come out equal -
// the same relationship user/virtual_key already have in the column-backed
// path despite also going through the "compute both" branch there.
func (s *RDBLogStore) GetJSONFieldDimensionRankings(ctx context.Context, filters SearchFilters, dimension RankingDimension) (*DimensionRankingResult, error) {
	src, ok := jsonFieldDimensionSource(s.db.Dialector.Name(), dimension)
	if !ok {
		return nil, fmt.Errorf("invalid ranking dimension: %s", dimension)
	}
	groupExpr := src.IDExpr
	notEmpty := fmt.Sprintf("%s IS NOT NULL AND %s != ''", groupExpr, groupExpr)

	selectClause := fmt.Sprintf(`
		%s as id,
		MAX(%s) as name,
		COUNT(*) as total_requests,
		SUM(total_tokens) as total_tokens,
		COALESCE(SUM(cost), 0) as total_cost
	`, groupExpr, src.NameCol)

	currentQuery := src.base(s.ScopedDB(ctx))
	currentQuery = s.applyFilters(currentQuery, filters)
	currentQuery = currentQuery.Where("status IN ?", terminalLogStatuses)
	currentQuery = currentQuery.Where(notEmpty)

	var currentResults []struct {
		ID            string          `gorm:"column:id"`
		Name          string          `gorm:"column:name"`
		TotalRequests int64           `gorm:"column:total_requests"`
		TotalTokens   sql.NullInt64   `gorm:"column:total_tokens"`
		TotalCost     sql.NullFloat64 `gorm:"column:total_cost"`
	}

	if err := applyRankingLimit(currentQuery.
		Select(selectClause).
		Group(groupExpr).
		// Tiebreak on the group expression itself, matching
		// GetDimensionRankings: ClickHouse resolves a bare `id` in ORDER BY
		// to the base relation's column rather than the SELECT alias.
		Order("total_requests DESC, "+groupExpr+" ASC"), filters).
		Find(&currentResults).Error; err != nil {
		return nil, fmt.Errorf("failed to get dimension rankings for %s: %w", dimension, err)
	}

	var requestCounts struct {
		ActualRequests     int64
		AttributedRequests int64
	}
	actualQuery := s.ScopedDB(ctx).Model(&Log{})
	actualQuery = s.applyFilters(actualQuery, filters)
	actualQuery = actualQuery.Where("status IN ?", terminalLogStatuses)
	if err := actualQuery.Count(&requestCounts.ActualRequests).Error; err != nil {
		return nil, fmt.Errorf("failed to get dimension ranking totals for %s: %w", dimension, err)
	}

	// No row carries this dimension (e.g. no errors in the window): there is
	// nothing attributed, but the window's real traffic is still reported so
	// the UI can show "0 of N requests".
	if len(currentResults) == 0 {
		return &DimensionRankingResult{
			Rankings:            []DimensionRankingWithTrend{},
			Dimension:           dimension,
			TotalActualRequests: requestCounts.ActualRequests,
		}, nil
	}

	{
		attributedQuery := src.base(s.ScopedDB(ctx))
		attributedQuery = s.applyFilters(attributedQuery, filters)
		attributedQuery = attributedQuery.Where("status IN ?", terminalLogStatuses)
		attributedQuery = attributedQuery.Where(notEmpty)
		var attributed int64
		if err := attributedQuery.Count(&attributed).Error; err != nil {
			return nil, fmt.Errorf("failed to get attributed dimension ranking totals for %s: %w", dimension, err)
		}
		requestCounts.AttributedRequests = attributed
	}

	prevMap := make(map[string]DimensionRankingEntry)
	if filters.StartTime != nil && filters.EndTime != nil {
		duration := filters.EndTime.Sub(*filters.StartTime)
		prevStart := filters.StartTime.Add(-duration)
		prevEnd := filters.StartTime.Add(-time.Nanosecond)

		prevFilters := filters
		prevFilters.StartTime = &prevStart
		prevFilters.EndTime = &prevEnd

		prevQuery := src.base(s.ScopedDB(ctx))
		prevQuery = s.applyFilters(prevQuery, prevFilters)
		prevQuery = prevQuery.Where("status IN ?", terminalLogStatuses)
		prevQuery = prevQuery.Where(notEmpty)

		ids := make([]string, len(currentResults))
		for i, r := range currentResults {
			ids[i] = r.ID
		}
		prevQuery = prevQuery.Where(fmt.Sprintf("%s IN ?", groupExpr), ids)

		var prevResults []struct {
			ID            string          `gorm:"column:id"`
			TotalRequests int64           `gorm:"column:total_requests"`
			TotalTokens   sql.NullInt64   `gorm:"column:total_tokens"`
			TotalCost     sql.NullFloat64 `gorm:"column:total_cost"`
		}
		prevSelect := fmt.Sprintf(`
			%s as id,
			COUNT(*) as total_requests,
			SUM(total_tokens) as total_tokens,
			COALESCE(SUM(cost), 0) as total_cost
		`, groupExpr)
		if err := prevQuery.
			Select(prevSelect).
			Group(groupExpr).
			Find(&prevResults).Error; err != nil {
			return nil, fmt.Errorf("failed to get previous period dimension rankings for %s: %w", dimension, err)
		}
		for _, r := range prevResults {
			prevMap[r.ID] = DimensionRankingEntry{
				ID:            r.ID,
				TotalRequests: r.TotalRequests,
				TotalTokens:   r.TotalTokens.Int64,
				TotalCost:     r.TotalCost.Float64,
			}
		}
	}

	rankings := make([]DimensionRankingWithTrend, len(currentResults))
	for i, r := range currentResults {
		entry := DimensionRankingEntry{
			ID:            r.ID,
			Name:          r.Name,
			TotalRequests: r.TotalRequests,
			TotalTokens:   r.TotalTokens.Int64,
			TotalCost:     r.TotalCost.Float64,
		}
		var trend DimensionRankingTrend
		if prev, exists := prevMap[r.ID]; exists && prev.TotalRequests > 0 {
			trend.HasPreviousPeriod = true
			trend.RequestsTrend = pctChange(float64(prev.TotalRequests), float64(r.TotalRequests))
			trend.TokensTrend = metricTrend(float64(prev.TotalTokens), float64(r.TotalTokens.Int64))
			trend.CostTrend = metricTrend(prev.TotalCost, r.TotalCost.Float64)
		}
		rankings[i] = DimensionRankingWithTrend{DimensionRankingEntry: entry, Trend: trend}
	}

	return &DimensionRankingResult{
		Rankings:                rankings,
		Dimension:               dimension,
		TotalActualRequests:     requestCounts.ActualRequests,
		TotalAttributedRequests: requestCounts.AttributedRequests,
	}, nil
}
