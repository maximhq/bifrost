package logstore

// Three-way LogStore parity suite: seeds identical fixtures into SQLite,
// Postgres (raw path, no matviews), and ClickHouse, runs every LogStore
// interface method on all three, and asserts that the non-reference backends
// return results equal to Postgres (the reference). This is the executable
// form of "every backend is 100% compatible": every interface method is
// exercised - reads, writes, mutations, async jobs, webhook deliveries, and
// deletes - including
// every dialect branch in rdb.go (metadata JSON, cache-hit extraction,
// histogram bucket math, routing-engine matching, distinct queries) and the
// DAC queryscope path.
//
// The Postgres store is built bare (no ensureMatViews), so matViewsReady stays
// false and Postgres deterministically takes the same raw-table path the
// matviews approximate. Known, deliberate divergences are NOT asserted here:
// multi-value team_ids array matching and team/BU dimension fan-out
// (postgres-only features; fixtures carry scalar ids only - the fan-out's
// attributed-totals metadata is excluded from the contract), ILIKE
// case-insensitivity (fixtures use exact case), FTS-vs-LIKE content search
// semantics (the fixture term matches under all three), and inc_number
// (Postgres-assigned; NULL elsewhere - excluded from projections).
//
// Postgres (5432) and ClickHouse (native 9001) must be reachable via
// framework/docker-compose.yml; the suite skips otherwise. SQLite uses a temp
// file. Subtests mutate shared state and run in declaration order - do not
// reorder or parallelize them.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// parityReference is the backend the others are compared against.
const parityReference = "postgres"

// parityBackends connects all three backends (skipping when Postgres or
// ClickHouse is unavailable), resets their tables, and returns them keyed by
// name.
func parityBackends(t *testing.T) map[string]LogStore {
	t.Helper()
	ch := trySetupClickHouseStore(t)

	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available, skipping parity test")
	}
	// Clean slate - same approach as setupPerfTestDB. Matviews are dropped and
	// never recreated, so matViewsReady stays false and every Postgres query
	// takes the raw-table path (the honest comparison target).
	dropAllManagedMatViews(db)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS mcp_tool_logs CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS async_jobs CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS webhook_deliveries CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS logs CASCADE").Error)
	require.NoError(t, db.Exec("CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("DELETE FROM migrations").Error)
	require.NoError(t, triggerMigrations(context.Background(), db, testLogger{}))
	pg := &RDBLogStore{db: db, logger: testLogger{}}

	sq, err := newSqliteLogStore(context.Background(), &SQLiteConfig{
		Path: filepath.Join(t.TempDir(), "parity.db"),
	}, testLogger{})
	require.NoError(t, err)

	return map[string]LogStore{"sqlite": sq, parityReference: pg, "clickhouse": ch}
}

func strPtrP(s string) *string         { return &s }
func f64PtrP(f float64) *float64       { return &f }
func intPtrP(i int) *int               { return &i }
func timePtrP(ts time.Time) *time.Time { return &ts }

// parityLogSpec builds one fixture row. All timestamps are whole seconds so
// the GORM ClickHouse driver's seconds-precision formatting of time.Time
// filter args cannot skew range comparisons.
type parityLogSpec struct {
	id           string
	offsetSec    int // subtracted from base
	object       string
	provider     string
	model        string
	status       string
	alias        *string
	canonical    *string
	selectedKey  string
	vkID, vkName *string
	teamID       *string
	customerID   *string
	buID         *string
	userID       *string
	cost         *float64
	latency      *float64
	tokens       [3]int // prompt, completion, total
	stopReason   *string
	routing      *string
	metadata     *string
	cacheDebug   string
	content      string
	parentID     *string
	nodeID       *string
	budgetIDs    *string
	rateLimitIDs *string
}

func (s parityLogSpec) toLog(base time.Time) *Log {
	ts := base.Add(-time.Duration(s.offsetSec) * time.Second)
	return &Log{
		ID:                    s.id,
		Timestamp:             ts,
		Object:                s.object,
		Provider:              s.provider,
		Model:                 s.model,
		Status:                s.status,
		Alias:                 s.alias,
		CanonicalModelName:    s.canonical,
		SelectedKeyID:         s.selectedKey,
		VirtualKeyID:          s.vkID,
		VirtualKeyName:        s.vkName,
		TeamID:                s.teamID,
		CustomerID:            s.customerID,
		BusinessUnitID:        s.buID,
		UserID:                s.userID,
		Cost:                  s.cost,
		Latency:               s.latency,
		PromptTokens:          s.tokens[0],
		CompletionTokens:      s.tokens[1],
		TotalTokens:           s.tokens[2],
		StopReason:            s.stopReason,
		RoutingEnginesUsedStr: s.routing,
		Metadata:              s.metadata,
		CacheDebug:            s.cacheDebug,
		ContentSummary:        s.content,
		ParentRequestID:       s.parentID,
		ClusterNodeID:         s.nodeID,
		BudgetIDs:             s.budgetIDs,
		RateLimitIDs:          s.rateLimitIDs,
		CreatedAt:             ts,
	}
}

func paritySpecs() []parityLogSpec {
	return []parityLogSpec{
		{id: "p1", offsetSec: 100, object: "chat.completion", provider: "openai", model: "gpt-4o", status: "success",
			alias: strPtrP("a1"), canonical: strPtrP("gpt-4o-2024-11-20"), selectedKey: "sk1", vkID: strPtrP("vk1"), vkName: strPtrP("VK One"),
			teamID: strPtrP("t1"), customerID: strPtrP("c1"), buID: strPtrP("b1"), userID: strPtrP("u1"),
			cost: f64PtrP(0.5), latency: f64PtrP(100), tokens: [3]int{100, 50, 150}, stopReason: strPtrP("stop"),
			routing: strPtrP("governance,loadbalancing"), metadata: strPtrP(`{"env":"prod"}`),
			cacheDebug: `{"hit_type":"direct"}`, content: "alpha bravo hello", parentID: strPtrP("sess1")},
		{id: "p2", offsetSec: 90, object: "chat.completion", provider: "openai", model: "gpt-4o", status: "success",
			vkID: strPtrP("vk1"), vkName: strPtrP("VK One"), teamID: strPtrP("t1"), userID: strPtrP("u2"),
			cost: f64PtrP(1.25), latency: f64PtrP(250), tokens: [3]int{200, 100, 300}, stopReason: strPtrP("length"),
			routing: strPtrP("governance"), metadata: strPtrP(`{"env":"dev"}`),
			cacheDebug: `{"hit_type":"semantic"}`, content: "charlie delta", parentID: strPtrP("sess1")},
		{id: "p3", offsetSec: 80, object: "chat.completion", provider: "openai", model: "gpt-4o-mini", status: "error",
			vkID: strPtrP("vk2"), vkName: strPtrP("VK Two"), teamID: strPtrP("t2"), userID: strPtrP("u2"),
			latency: f64PtrP(50), content: "echo error", parentID: strPtrP("sess1")},
		{id: "p4", offsetSec: 70, object: "chat.completion", provider: "anthropic", model: "claude-3", status: "success",
			vkID: strPtrP("vk2"), vkName: strPtrP("VK Two"), teamID: strPtrP("t2"), userID: strPtrP("u3"),
			cost: f64PtrP(2.5), latency: f64PtrP(400), tokens: [3]int{400, 100, 500}, stopReason: strPtrP("tool_calls"),
			metadata: strPtrP(`{"env":"prod","region":"us"}`), content: "echo foxtrot"},
		{id: "p5", offsetSec: 60, object: "chat.completion", provider: "anthropic", model: "claude-3", status: "processing",
			vkID: strPtrP("vk1"), vkName: strPtrP("VK One"), teamID: strPtrP("t1"), userID: strPtrP("u1")},
		{id: "p6", offsetSec: 50, object: "embedding", provider: "openai", model: "gpt-4o", status: "success",
			cost: f64PtrP(0), latency: f64PtrP(75), tokens: [3]int{10, 5, 15}},
		{id: "p7", offsetSec: 40, object: "chat.completion", provider: "mistral", model: "mistral-small", status: "success",
			alias: strPtrP("a2"), canonical: strPtrP("mistral-small-2409"), vkID: strPtrP("vk3"), vkName: strPtrP("VK Three"), customerID: strPtrP("c2"),
			buID: strPtrP("b2"), userID: strPtrP("u4"), cost: f64PtrP(3.0), latency: f64PtrP(800),
			tokens: [3]int{900, 100, 1000}, stopReason: strPtrP("stop"), content: "golf hotel"},
		{id: "p8", offsetSec: 30, object: "chat.completion", provider: "openai", model: "gpt-4o", status: "success",
			vkID: strPtrP("vk3"), vkName: strPtrP("VK Three"), teamID: strPtrP("t3"), userID: strPtrP("u1"),
			cost: f64PtrP(0.75), latency: f64PtrP(120), tokens: [3]int{50, 25, 75}, stopReason: strPtrP("content_filter"),
			routing: strPtrP("routing-rule"), metadata: strPtrP(`{"env":"prod"}`), content: "india juliet"},
		// Cluster-governance rows for GetNodeUsageAfter parity.
		{id: "p9", offsetSec: 20, object: "chat.completion", provider: "openai", model: "gpt-4o", status: "success",
			cost: f64PtrP(1.0), latency: f64PtrP(90), tokens: [3]int{40, 20, 60},
			nodeID: strPtrP("pnode"), budgetIDs: strPtrP(`["bud1"]`), rateLimitIDs: strPtrP(`["rl1"]`)},
		{id: "p10", offsetSec: 10, object: "chat.completion", provider: "openai", model: "gpt-4o", status: "success",
			cost: f64PtrP(2.0), latency: f64PtrP(110), tokens: [3]int{80, 40, 120},
			nodeID: strPtrP("pnode"), budgetIDs: strPtrP(`["bud1","bud2"]`), rateLimitIDs: strPtrP(`["rl1"]`)},
	}
}

func parityMCPLogs(base time.Time) []*MCPToolLog {
	mk := func(id string, offsetSec int, tool, label, status string, latency, cost *float64, vkID, vkName *string) *MCPToolLog {
		ts := base.Add(-time.Duration(offsetSec) * time.Second)
		return &MCPToolLog{
			ID: id, Timestamp: ts, ToolName: tool, ServerLabel: label, Status: status,
			Latency: latency, Cost: cost, VirtualKeyID: vkID, VirtualKeyName: vkName, CreatedAt: ts,
		}
	}
	return []*MCPToolLog{
		mk("m1", 95, "search_web", "srv1", "success", f64PtrP(120), f64PtrP(0.01), strPtrP("vk1"), strPtrP("VK One")),
		mk("m2", 85, "search_web", "srv2", "error", f64PtrP(80), nil, strPtrP("vk2"), strPtrP("VK Two")),
		mk("m3", 75, "calculator", "srv1", "success", f64PtrP(30), f64PtrP(0.002), strPtrP("vk1"), strPtrP("VK One")),
		mk("m4", 65, "calculator", "srv1", "processing", nil, nil, nil, nil),
	}
}

// --- Tolerant deep comparison over JSON-normalized values ---

// jsonNormalize round-trips v through JSON so every store's results reduce to
// the same generic shape (maps/slices/float64/string/bool/nil).
func jsonNormalize(t *testing.T, v any) any {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	var out any
	require.NoError(t, json.Unmarshal(data, &out))
	return out
}

// canonicalizeOrder makes presentation-only ordering deterministic before
// diffing: plain string lists (provider/model/dimension series - the buckets
// key their data by name, so list order is cosmetic) are sorted, and
// "rankings" arrays are sorted by a canonical rendering of each entry because
// backends order ties arbitrarily (ORDER BY SUM(...) DESC with equal sums has
// no deterministic tiebreaker on any backend).
func canonicalizeOrder(key string, v any) any {
	switch tv := v.(type) {
	case map[string]any:
		for k, val := range tv {
			tv[k] = canonicalizeOrder(k, val)
		}
		return tv
	case []any:
		allStrings := len(tv) > 0
		for _, e := range tv {
			if _, ok := e.(string); !ok {
				allStrings = false
				break
			}
		}
		if allStrings {
			sort.Slice(tv, func(i, j int) bool { return tv[i].(string) < tv[j].(string) })
			return tv
		}
		for i := range tv {
			tv[i] = canonicalizeOrder("", tv[i])
		}
		if key == "rankings" {
			keys := make([]string, len(tv))
			for i, e := range tv {
				keys[i] = canonicalEntryKey(e)
			}
			sort.SliceStable(tv, func(i, j int) bool { return keys[i] < keys[j] })
		}
		return tv
	default:
		return v
	}
}

// canonicalEntryKey renders a ranking entry as a stable sort key: sorted map
// keys with floats rounded so sub-tolerance drift cannot reorder entries.
func canonicalEntryKey(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Sprint(v)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for _, k := range keys {
		switch val := m[k].(type) {
		case float64:
			out += fmt.Sprintf("%s=%.3f;", k, val)
		default:
			out += fmt.Sprintf("%s=%v;", k, val)
		}
	}
	return out
}

// collectDiffs walks two JSON-normalized values and records human-readable
// differences. Numbers compare with a relative/absolute tolerance so
// aggregation-order float drift and percentile interpolation differences
// don't read as incompatibility.
func collectDiffs(path string, a, b any, tol float64, diffs *[]string) {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			*diffs = append(*diffs, fmt.Sprintf("%s: type mismatch %T vs %T", path, a, b))
			return
		}
		keys := map[string]struct{}{}
		for k := range av {
			keys[k] = struct{}{}
		}
		for k := range bv {
			keys[k] = struct{}{}
		}
		for k := range keys {
			aval, aok := av[k]
			bval, bok := bv[k]
			if aok != bok {
				*diffs = append(*diffs, fmt.Sprintf("%s.%s: key presence mismatch", path, k))
				continue
			}
			collectDiffs(path+"."+k, aval, bval, tol, diffs)
		}
	case []any:
		bv, ok := b.([]any)
		if !ok {
			*diffs = append(*diffs, fmt.Sprintf("%s: type mismatch %T vs %T", path, a, b))
			return
		}
		if len(av) != len(bv) {
			*diffs = append(*diffs, fmt.Sprintf("%s: length %d vs %d", path, len(av), len(bv)))
			return
		}
		for i := range av {
			collectDiffs(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i], tol, diffs)
		}
	case float64:
		bv, ok := b.(float64)
		if !ok {
			*diffs = append(*diffs, fmt.Sprintf("%s: %v vs %v", path, a, b))
			return
		}
		limit := math.Max(tol, tol*math.Max(math.Abs(av), math.Abs(bv)))
		if math.Abs(av-bv) > limit {
			*diffs = append(*diffs, fmt.Sprintf("%s: %v vs %v", path, av, bv))
		}
	default:
		if !assert.ObjectsAreEqual(a, b) {
			*diffs = append(*diffs, fmt.Sprintf("%s: %v vs %v", path, a, b))
		}
	}
}

// assertParity runs f against every backend and requires each non-reference
// backend's result to tolerantly equal the Postgres reference, reporting
// per-field paths on mismatch.
func assertParity(t *testing.T, stores map[string]LogStore, tol float64, f func(context.Context, LogStore) (any, error)) {
	t.Helper()
	ctx := context.Background()
	refVal, err := f(ctx, stores[parityReference])
	require.NoError(t, err, parityReference)
	refNorm := canonicalizeOrder("", jsonNormalize(t, refVal))
	for name, s := range stores {
		if name == parityReference {
			continue
		}
		val, err := f(ctx, s)
		require.NoError(t, err, name)
		var diffs []string
		collectDiffs("$", refNorm, canonicalizeOrder("", jsonNormalize(t, val)), tol, &diffs)
		assert.Empty(t, diffs, "%s vs %s mismatch", parityReference, name)
	}
}

// runOnAll executes op on every backend, failing on any error. Used for the
// write-path phases where the operation itself is the subject.
func runOnAll(t *testing.T, stores map[string]LogStore, op func(context.Context, LogStore) error) {
	t.Helper()
	for name, s := range stores {
		require.NoError(t, op(context.Background(), s), name)
	}
}

// searchProjection reduces a SearchResult to the fields all backends must
// agree on, dropping backend-managed noise (inc_number is Postgres-assigned
// and NULL elsewhere by design).
func searchProjection(r *SearchResult) any {
	logs := make([]map[string]any, 0, len(r.Logs))
	for _, l := range r.Logs {
		logs = append(logs, logProjection(&l))
	}
	return map[string]any{
		"total":    r.Pagination.TotalCount,
		"has_logs": r.HasLogs,
		"stats":    r.Stats,
		"logs":     logs,
	}
}

func logProjection(l *Log) map[string]any {
	return map[string]any{
		"id": l.ID, "ts_ms": l.Timestamp.UnixMilli(), "object": l.Object,
		"provider": l.Provider, "model": l.Model, "status": l.Status,
		"alias": l.Alias, "selected_key_id": l.SelectedKeyID,
		"virtual_key_id": l.VirtualKeyID, "team_id": l.TeamID,
		"customer_id": l.CustomerID, "business_unit_id": l.BusinessUnitID,
		"user_id": l.UserID, "cost": l.Cost, "latency": l.Latency,
		"prompt_tokens": l.PromptTokens, "completion_tokens": l.CompletionTokens,
		"total_tokens": l.TotalTokens, "stop_reason": l.StopReason,
		"content_summary": l.ContentSummary,
	}
}

func mcpProjection(logs []MCPToolLog) []map[string]any {
	out := make([]map[string]any, 0, len(logs))
	for _, l := range logs {
		out = append(out, map[string]any{
			"id": l.ID, "ts_ms": l.Timestamp.UnixMilli(), "tool": l.ToolName,
			"label": l.ServerLabel, "status": l.Status, "latency": l.Latency,
			"cost": l.Cost, "virtual_key_id": l.VirtualKeyID,
		})
	}
	return out
}

func asyncJobProjection(j *AsyncJob) map[string]any {
	out := map[string]any{
		"id": j.ID, "status": j.Status, "request_type": j.RequestType,
		"response": j.Response, "status_code": j.StatusCode, "error": j.Error,
		"virtual_key_id": j.VirtualKeyID, "created_ms": j.CreatedAt.UnixMilli(),
	}
	if j.ExpiresAt != nil {
		out["expires_ms"] = j.ExpiresAt.UnixMilli()
	}
	return out
}

func webhookDeliveryProjection(d *WebhookDelivery) map[string]any {
	out := map[string]any{
		"id": d.ID, "webhook_id": d.WebhookID, "endpoint_id": d.EndpointID,
		"async_job_id": d.AsyncJobID, "event": d.Event, "attempt_no": d.AttemptNo,
		"outcome": d.Outcome, "status_code": d.StatusCode, "error": d.Error,
		"created_ms": d.CreatedAt.UnixMilli(),
	}
	if d.ExpiresAt != nil {
		out["expires_ms"] = d.ExpiresAt.UnixMilli()
	}
	return out
}

func webhookDeliverySearchProjection(r *WebhookDeliverySearchResult) any {
	deliveries := make([]map[string]any, 0, len(r.Deliveries))
	for _, d := range r.Deliveries {
		deliveries = append(deliveries, webhookDeliveryProjection(&d))
	}
	return map[string]any{"total": r.Pagination.TotalCount, "deliveries": deliveries}
}

// remainingLogIDs lists surviving log ids - the post-state assertion used
// after every destructive operation.
func remainingLogIDs(ctx context.Context, s LogStore) (any, error) {
	logs, err := s.FindAll(ctx, map[string]interface{}{}, "id")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(logs))
	for _, l := range logs {
		ids = append(ids, l.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

func remainingMCPIDs(ctx context.Context, s LogStore) (any, error) {
	r, err := s.SearchMCPToolLogs(ctx, MCPToolLogSearchFilters{}, PaginationOptions{Limit: 100, SortBy: "timestamp", Order: "desc"})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(r.Logs))
	for _, l := range r.Logs {
		ids = append(ids, l.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

func TestLogStoreParity(t *testing.T) {
	stores := parityBackends(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	// --- Phase: create paths (Create, CreateIfNotExists, batch variants) ---

	specs := paritySpecs()
	runOnAll(t, stores, func(ctx context.Context, s LogStore) error {
		// p1 through the singular paths, the rest through the batch path, so
		// Create, CreateIfNotExists, and BatchCreateIfNotExists all execute.
		if err := s.Create(ctx, specs[0].toLog(base)); err != nil {
			return err
		}
		if err := s.CreateIfNotExists(ctx, specs[1].toLog(base)); err != nil {
			return err
		}
		var rest []*Log
		for _, spec := range specs[2:] {
			rest = append(rest, spec.toLog(base))
		}
		if err := s.BatchCreateIfNotExists(ctx, rest); err != nil {
			return err
		}
		mcp := parityMCPLogs(base)
		if err := s.CreateMCPToolLog(ctx, mcp[0]); err != nil {
			return err
		}
		return s.BatchCreateMCPToolLogsIfNotExists(ctx, mcp[1:])
	})

	t.Run("Ping", func(t *testing.T) {
		runOnAll(t, stores, func(ctx context.Context, s LogStore) error { return s.Ping(ctx) })
	})

	windowStart := base.Add(-2 * time.Minute)
	windowEnd := base.Add(time.Minute)
	window := SearchFilters{StartTime: timePtrP(windowStart), EndTime: timePtrP(windowEnd)}
	page := PaginationOptions{Limit: 50, SortBy: "timestamp", Order: "desc"}

	// --- Phase: reads ---

	searchCases := map[string]SearchFilters{
		"all":             window,
		"providers":       {Providers: []string{"openai"}},
		"models":          {Models: []string{"gpt-4o", "claude-3"}},
		"status":          {Status: []string{"success"}},
		"stop_reasons":    {StopReasons: []string{"stop"}},
		"objects":         {Objects: []string{"embedding"}},
		"aliases":         {Aliases: []string{"a1"}},
		"selected_keys":   {SelectedKeyIDs: []string{"sk1"}},
		"virtual_keys":    {VirtualKeyIDs: []string{"vk1"}},
		"teams":           {TeamIDs: []string{"t1", "t3"}},
		"customers":       {CustomerIDs: []string{"c1"}},
		"users":           {UserIDs: []string{"u1"}},
		"business_units":  {BusinessUnitIDs: []string{"b1"}},
		"routing_engines": {RoutingEngineUsed: []string{"loadbalancing", "routing-rule"}},
		"time_range":      {StartTime: timePtrP(base.Add(-75 * time.Second)), EndTime: timePtrP(base.Add(-25 * time.Second))},
		"latency_range":   {MinLatency: f64PtrP(80), MaxLatency: f64PtrP(260)},
		"token_range":     {MinTokens: intPtrP(100), MaxTokens: intPtrP(600)},
		"cost_range":      {MinCost: f64PtrP(1.0), MaxCost: f64PtrP(2.6)},
		"missing_cost":    {MissingCostOnly: true},
		"cache_direct":    {CacheHitTypes: []string{"direct"}},
		"cache_semantic":  {CacheHitTypes: []string{"semantic"}},
		"metadata":        {MetadataFilters: map[string]string{"env": "prod"}},
		"content_search":  {ContentSearch: "charlie"},
		"parent_request":  {ParentRequestID: "sess1"},
	}
	for name, filters := range searchCases {
		t.Run("SearchLogs/"+name, func(t *testing.T) {
			assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
				r, err := s.SearchLogs(ctx, filters, page)
				if err != nil {
					return nil, err
				}
				return searchProjection(r), nil
			})
		})
	}

	t.Run("SearchLogs/sort_and_pagination", func(t *testing.T) {
		// MinCost 0 keeps only non-NULL costs: NULL ordering defaults differ
		// across backends and the UI always filters or sorts on populated
		// columns.
		filters := SearchFilters{MinCost: f64PtrP(0)}
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			r, err := s.SearchLogs(ctx, filters, PaginationOptions{Limit: 3, Offset: 1, SortBy: "cost", Order: "desc"})
			if err != nil {
				return nil, err
			}
			return searchProjection(r), nil
		})
	})

	t.Run("GetStats", func(t *testing.T) {
		for name, filters := range map[string]SearchFilters{"window": window, "team_t1": {TeamIDs: []string{"t1"}}} {
			t.Run(name, func(t *testing.T) {
				assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
					return s.GetStats(ctx, filters)
				})
			})
		}
	})

	histogramCalls := map[string]func(context.Context, LogStore) (any, error){
		"requests": func(ctx context.Context, s LogStore) (any, error) { return s.GetHistogram(ctx, window, 60) },
		"tokens":   func(ctx context.Context, s LogStore) (any, error) { return s.GetTokenHistogram(ctx, window, 60) },
		"cost":     func(ctx context.Context, s LogStore) (any, error) { return s.GetCostHistogram(ctx, window, 60) },
		"model":    func(ctx context.Context, s LogStore) (any, error) { return s.GetModelHistogram(ctx, window, 60) },
		"latency":  func(ctx context.Context, s LogStore) (any, error) { return s.GetLatencyHistogram(ctx, window, 60) },
		"provider_cost": func(ctx context.Context, s LogStore) (any, error) {
			return s.GetProviderCostHistogram(ctx, window, 60)
		},
		"provider_tokens": func(ctx context.Context, s LogStore) (any, error) {
			return s.GetProviderTokenHistogram(ctx, window, 60)
		},
		"provider_latency": func(ctx context.Context, s LogStore) (any, error) {
			return s.GetProviderLatencyHistogram(ctx, window, 60)
		},
		"throughput": func(ctx context.Context, s LogStore) (any, error) {
			return s.GetThroughputHistogram(ctx, window, 60)
		},
		"provider_throughput": func(ctx context.Context, s LogStore) (any, error) {
			return s.GetProviderThroughputHistogram(ctx, window, 60)
		},
		"dimension_cost": func(ctx context.Context, s LogStore) (any, error) {
			return s.GetDimensionCostHistogram(ctx, window, 60, DimensionProvider)
		},
		"dimension_tokens": func(ctx context.Context, s LogStore) (any, error) {
			return s.GetDimensionTokenHistogram(ctx, window, 60, DimensionUser)
		},
		"dimension_latency": func(ctx context.Context, s LogStore) (any, error) {
			return s.GetDimensionLatencyHistogram(ctx, window, 60, DimensionProvider)
		},
		// Filter on the same column the SELECT aliases (SUM(cost) AS cost):
		// without prefer_column_name_to_alias=1 ClickHouse resolves the WHERE
		// identifier to the aggregate alias and errors (code 184).
		"cost_with_cost_filter": func(ctx context.Context, s LogStore) (any, error) {
			f := window
			f.MinCost = f64PtrP(0.6)
			return s.GetCostHistogram(ctx, f, 60)
		},
	}
	for name, call := range histogramCalls {
		t.Run("Histogram/"+name, func(t *testing.T) {
			// Latency percentiles: percentile_cont (PG) vs quantile (CH) vs
			// Go-side interpolation (SQLite) agree on small exact sets; the
			// loose tolerance absorbs interpolation differences without hiding
			// real drift.
			assertParity(t, stores, 1e-3, call)
		})
	}

	t.Run("Rankings/models", func(t *testing.T) {
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			return s.GetModelRankings(ctx, window)
		})
	})
	t.Run("Rankings/users", func(t *testing.T) {
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			return s.GetUserRankings(ctx, window)
		})
	})
	t.Run("Rankings/dimension_virtual_key", func(t *testing.T) {
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			return s.GetDimensionRankings(ctx, window, RankingDimensionVirtualKey)
		})
	})
	t.Run("Rankings/dimension_team", func(t *testing.T) {
		// Fixtures carry scalar team_id only, so the Postgres fan-out's scalar
		// fallback and the other backends' plain group-by must agree on the
		// rankings. TotalActual/AttributedRequests are documented as
		// fan-out-only (Postgres) metadata and excluded from the contract.
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			r, err := s.GetDimensionRankings(ctx, window, RankingDimensionTeam)
			if err != nil {
				return nil, err
			}
			return map[string]any{"rankings": r.Rankings, "dimension": r.Dimension}, nil
		})
	})

	t.Run("Distinct", func(t *testing.T) {
		sorted := func(v []string, err error) (any, error) {
			if err != nil {
				return nil, err
			}
			sort.Strings(v)
			return v, nil
		}
		calls := map[string]func(context.Context, LogStore) (any, error){
			"models":  func(ctx context.Context, s LogStore) (any, error) { return sorted(s.GetDistinctModels(ctx, 50, "")) },
			"aliases": func(ctx context.Context, s LogStore) (any, error) { return sorted(s.GetDistinctAliases(ctx, 50, "")) },
			"routing_engines": func(ctx context.Context, s LogStore) (any, error) {
				return sorted(s.GetDistinctRoutingEngines(ctx, 50, ""))
			},
			"stop_reasons": func(ctx context.Context, s LogStore) (any, error) {
				return sorted(s.GetDistinctStopReasons(ctx, 50, ""))
			},
			"key_pairs": func(ctx context.Context, s LogStore) (any, error) {
				pairs, err := s.GetDistinctKeyPairs(ctx, "virtual_key_id", "virtual_key_name", 50, "")
				if err != nil {
					return nil, err
				}
				sort.Slice(pairs, func(i, j int) bool { return pairs[i].ID < pairs[j].ID })
				return pairs, nil
			},
			"metadata_keys": func(ctx context.Context, s LogStore) (any, error) {
				m, err := s.GetDistinctMetadataKeys(ctx, 50, "")
				if err != nil {
					return nil, err
				}
				for k := range m {
					sort.Strings(m[k])
				}
				return m, nil
			},
		}
		for name, call := range calls {
			t.Run(name, func(t *testing.T) { assertParity(t, stores, 1e-6, call) })
		}
	})

	t.Run("Sessions", func(t *testing.T) {
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			r, err := s.GetSessionLogs(ctx, "sess1", page)
			if err != nil {
				return nil, err
			}
			logs := make([]map[string]any, 0, len(r.Logs))
			for _, l := range r.Logs {
				logs = append(logs, logProjection(&l))
			}
			return map[string]any{"total": r.Pagination.TotalCount, "logs": logs}, nil
		})
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			return s.GetSessionSummary(ctx, "sess1")
		})
	})

	t.Run("Lookups", func(t *testing.T) {
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			l, err := s.FindByID(ctx, "p1")
			if err != nil {
				return nil, err
			}
			return logProjection(l), nil
		})
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			l, err := s.FindFirst(ctx, map[string]interface{}{"provider": "mistral"}, "id", "provider", "status")
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": l.ID, "provider": l.Provider, "status": l.Status}, nil
		})
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			logs, err := s.FindAll(ctx, map[string]interface{}{"status": "success"}, "id")
			if err != nil {
				return nil, err
			}
			ids := make([]string, 0, len(logs))
			for _, l := range logs {
				ids = append(ids, l.ID)
			}
			sort.Strings(ids)
			return ids, nil
		})
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			logs, err := s.FindAllDistinct(ctx, map[string]interface{}{"status": "success"}, "provider")
			if err != nil {
				return nil, err
			}
			providers := make([]string, 0, len(logs))
			for _, l := range logs {
				providers = append(providers, l.Provider)
			}
			sort.Strings(providers)
			return providers, nil
		})
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			return s.HasLogs(ctx)
		})
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			present, err := s.IsLogEntryPresent(ctx, "p3")
			if err != nil {
				return nil, err
			}
			missing, err := s.IsLogEntryPresent(ctx, "nope")
			if err != nil {
				return nil, err
			}
			return []bool{present, missing}, nil
		})
	})

	t.Run("QueryScopeDAC", func(t *testing.T) {
		// Same closure shape the enterprise DAC wrapper builds (dacscope.go
		// logScope): OR-joined IN predicates over ownership columns.
		scope := queryscope.QueryScope(func(db *gorm.DB) *gorm.DB {
			return db.Where("(user_id IN ? OR virtual_key_id IN ?)", []string{"u1"}, []string{"vk2"})
		})
		scopedCtx := queryscope.WithQueryScope(context.Background(), scope)
		refResult, err := stores[parityReference].SearchLogs(scopedCtx, window, page)
		require.NoError(t, err)
		refNorm := canonicalizeOrder("", jsonNormalize(t, searchProjection(refResult)))
		for name, s := range stores {
			if name == parityReference {
				continue
			}
			r, err := s.SearchLogs(scopedCtx, window, page)
			require.NoError(t, err, name)
			var diffs []string
			collectDiffs("$", refNorm, canonicalizeOrder("", jsonNormalize(t, searchProjection(r))), 1e-6, &diffs)
			assert.Empty(t, diffs, "%s: DAC-scoped results must match", name)
		}

		// Fail-closed parity: the no-dimension principal shape.
		closed := queryscope.WithQueryScope(context.Background(), func(db *gorm.DB) *gorm.DB {
			return db.Where("1 = 0")
		})
		for name, s := range stores {
			r, err := s.SearchLogs(closed, window, page)
			require.NoError(t, err, name)
			assert.Zero(t, r.Pagination.TotalCount, name)
			assert.Empty(t, r.Logs, name)
		}
	})

	t.Run("MCP", func(t *testing.T) {
		mcpWindow := MCPToolLogSearchFilters{StartTime: timePtrP(windowStart), EndTime: timePtrP(windowEnd)}
		mcpPage := PaginationOptions{Limit: 50, SortBy: "timestamp", Order: "desc"}
		calls := map[string]func(context.Context, LogStore) (any, error){
			"search": func(ctx context.Context, s LogStore) (any, error) {
				r, err := s.SearchMCPToolLogs(ctx, mcpWindow, mcpPage)
				if err != nil {
					return nil, err
				}
				return map[string]any{"total": r.Pagination.TotalCount, "logs": mcpProjection(r.Logs)}, nil
			},
			"search_filtered": func(ctx context.Context, s LogStore) (any, error) {
				r, err := s.SearchMCPToolLogs(ctx, MCPToolLogSearchFilters{ToolNames: []string{"search_web"}, Status: []string{"success", "error"}}, mcpPage)
				if err != nil {
					return nil, err
				}
				return map[string]any{"total": r.Pagination.TotalCount, "logs": mcpProjection(r.Logs)}, nil
			},
			"stats": func(ctx context.Context, s LogStore) (any, error) { return s.GetMCPToolLogStats(ctx, mcpWindow) },
			"histogram": func(ctx context.Context, s LogStore) (any, error) {
				return s.GetMCPHistogram(ctx, mcpWindow, 60)
			},
			"cost_histogram": func(ctx context.Context, s LogStore) (any, error) {
				return s.GetMCPCostHistogram(ctx, mcpWindow, 60)
			},
			"top_tools": func(ctx context.Context, s LogStore) (any, error) { return s.GetMCPTopTools(ctx, mcpWindow, 10) },
			"tool_names": func(ctx context.Context, s LogStore) (any, error) {
				v, err := s.GetAvailableToolNames(ctx, 50, "")
				if err != nil {
					return nil, err
				}
				sort.Strings(v)
				return v, nil
			},
			"server_labels": func(ctx context.Context, s LogStore) (any, error) {
				v, err := s.GetAvailableServerLabels(ctx, 50, "")
				if err != nil {
					return nil, err
				}
				sort.Strings(v)
				return v, nil
			},
			"virtual_keys": func(ctx context.Context, s LogStore) (any, error) {
				logs, err := s.GetAvailableMCPVirtualKeys(ctx, 50, "")
				if err != nil {
					return nil, err
				}
				type vk struct{ ID, Name string }
				out := make([]vk, 0, len(logs))
				for _, l := range logs {
					pair := vk{}
					if l.VirtualKeyID != nil {
						pair.ID = *l.VirtualKeyID
					}
					if l.VirtualKeyName != nil {
						pair.Name = *l.VirtualKeyName
					}
					out = append(out, pair)
				}
				sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
				return out, nil
			},
			"find": func(ctx context.Context, s LogStore) (any, error) {
				l, err := s.FindMCPToolLog(ctx, "m1")
				if err != nil {
					return nil, err
				}
				return mcpProjection([]MCPToolLog{*l}), nil
			},
			"has_logs": func(ctx context.Context, s LogStore) (any, error) { return s.HasMCPToolLogs(ctx) },
		}
		for name, call := range calls {
			t.Run(name, func(t *testing.T) { assertParity(t, stores, 1e-3, call) })
		}
	})

	t.Run("NodeUsage", func(t *testing.T) {
		// inc_number is Postgres-assigned and NULL elsewhere, so the cursor's
		// IncNumber field is excluded; everything the budget reconciler
		// consumes must match.
		project := func(a *NodeUsageAggregate) any {
			return map[string]any{
				"budget_costs": a.BudgetCosts, "rl_requests": a.RateLimitRequests,
				"rl_tokens": a.RateLimitTokens, "rows": a.RowCount,
				"max_ts_ms": a.MaxTimestamp.UnixMilli(), "max_log_id": a.MaxLogID,
			}
		}
		start := NodeUsageCursor{Timestamp: base.Add(-time.Hour)}
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			a, err := s.GetNodeUsageAfter(ctx, "pnode", start)
			if err != nil {
				return nil, err
			}
			return project(a), nil
		})
		// Cursor advance parity: a second scan from each store's own returned
		// cursor must aggregate nothing on every backend.
		for name, s := range stores {
			first, err := s.GetNodeUsageAfter(ctx, "pnode", start)
			require.NoError(t, err, name)
			second, err := s.GetNodeUsageAfter(ctx, "pnode", first.NextCursor)
			require.NoError(t, err, name)
			assert.Zero(t, second.RowCount, "%s: cursor must not re-aggregate", name)
		}
	})

	// --- Phase: mutations ---

	t.Run("Mutations", func(t *testing.T) {
		t.Run("update_map", func(t *testing.T) {
			runOnAll(t, stores, func(ctx context.Context, s LogStore) error {
				return s.Update(ctx, "p6", map[string]interface{}{
					"status": "error", "latency": 99.0, "stop_reason": "content_filter",
				})
			})
			assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
				l, err := s.FindByID(ctx, "p6")
				if err != nil {
					return nil, err
				}
				return logProjection(l), nil
			})
		})
		t.Run("update_struct", func(t *testing.T) {
			// Struct updates write non-zero fields only, on every backend.
			runOnAll(t, stores, func(ctx context.Context, s LogStore) error {
				return s.Update(ctx, "p8", &Log{Model: "gpt-4o-turbo", Cost: f64PtrP(0.85)})
			})
			assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
				l, err := s.FindByID(ctx, "p8")
				if err != nil {
					return nil, err
				}
				return logProjection(l), nil
			})
		})
		t.Run("update_missing_row", func(t *testing.T) {
			for name, s := range stores {
				err := s.Update(ctx, "missing-id", map[string]interface{}{"status": "success"})
				assert.ErrorIs(t, err, ErrNotFound, name)
			}
		})
		t.Run("create_if_not_exists_keeps_existing", func(t *testing.T) {
			// The retried "processing" insert after an update must be a no-op
			// on every backend (ClickHouse would be last-write-wins without
			// its existence check).
			runOnAll(t, stores, func(ctx context.Context, s LogStore) error {
				return s.CreateIfNotExists(ctx, paritySpecs()[5].toLog(base))
			})
			assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
				l, err := s.FindByID(ctx, "p6")
				if err != nil {
					return nil, err
				}
				return logProjection(l), nil
			})
		})
		t.Run("bulk_update_cost", func(t *testing.T) {
			runOnAll(t, stores, func(ctx context.Context, s LogStore) error {
				return s.BulkUpdateCost(ctx, map[string]float64{"p1": 0.9, "p4": 2.9})
			})
			assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
				var costs []map[string]any
				for _, id := range []string{"p1", "p4"} {
					l, err := s.FindByID(ctx, id)
					if err != nil {
						return nil, err
					}
					costs = append(costs, map[string]any{"id": l.ID, "cost": l.Cost})
				}
				return costs, nil
			})
		})
		t.Run("update_mcp_map_and_struct", func(t *testing.T) {
			runOnAll(t, stores, func(ctx context.Context, s LogStore) error {
				if err := s.UpdateMCPToolLog(ctx, "m3", map[string]interface{}{"status": "error", "latency": 45.0}); err != nil {
					return err
				}
				return s.UpdateMCPToolLog(ctx, "m2", &MCPToolLog{Status: "success", Cost: f64PtrP(0.02)})
			})
			assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
				var out []map[string]any
				for _, id := range []string{"m2", "m3"} {
					l, err := s.FindMCPToolLog(ctx, id)
					if err != nil {
						return nil, err
					}
					out = append(out, mcpProjection([]MCPToolLog{*l})...)
				}
				return out, nil
			})
			for name, s := range stores {
				err := s.UpdateMCPToolLog(ctx, "missing-id", map[string]interface{}{"status": "success"})
				assert.ErrorIs(t, err, ErrNotFound, name)
			}
		})
	})

	// --- Phase: async jobs ---

	t.Run("AsyncJobs", func(t *testing.T) {
		mkJob := func(id string, status string, createdOffset time.Duration, expires *time.Time) *AsyncJob {
			return &AsyncJob{
				ID: id, Status: schemas.AsyncJobStatus(status), RequestType: schemas.RequestType("chat"),
				Response: `{"ok":true}`, ResultTTL: 3600, ExpiresAt: expires,
				CreatedAt: base.Add(createdOffset),
			}
		}
		runOnAll(t, stores, func(ctx context.Context, s LogStore) error {
			if err := s.CreateAsyncJob(ctx, mkJob("j1", "processing", -time.Minute, nil)); err != nil {
				return err
			}
			// j2 expired ten minutes ago; j3 is a stale processing job.
			if err := s.CreateAsyncJob(ctx, mkJob("j2", "completed", -time.Hour, timePtrP(base.Add(-10*time.Minute)))); err != nil {
				return err
			}
			return s.CreateAsyncJob(ctx, mkJob("j3", "processing", -2*time.Hour, nil))
		})

		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			j, err := s.FindAsyncJobByID(ctx, "j1")
			if err != nil {
				return nil, err
			}
			return asyncJobProjection(j), nil
		})
		// Expired jobs are invisible on every backend.
		for name, s := range stores {
			_, err := s.FindAsyncJobByID(ctx, "j2")
			assert.ErrorIs(t, err, ErrNotFound, name)
		}

		runOnAll(t, stores, func(ctx context.Context, s LogStore) error {
			return s.UpdateAsyncJob(ctx, "j1", map[string]interface{}{
				"status": "completed", "status_code": 200, "response": `{"done":true}`,
			})
		})
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			j, err := s.FindAsyncJobByID(ctx, "j1")
			if err != nil {
				return nil, err
			}
			return asyncJobProjection(j), nil
		})

		// Stale cleanup removes j3 (processing, created 2h ago) and reports
		// the same count everywhere.
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			return s.DeleteStaleAsyncJobs(ctx, base.Add(-time.Hour))
		})
		// Expired cleanup removes j2 with the same count everywhere.
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			return s.DeleteExpiredAsyncJobs(ctx)
		})
	})

	// --- Phase: webhook deliveries ---

	t.Run("WebhookDeliveries", func(t *testing.T) {
		mkDelivery := func(id, webhookID, endpointID string, attemptNo int, outcome WebhookDeliveryOutcome, statusCode int, errMsg string, createdOffset time.Duration, expires *time.Time) *WebhookDelivery {
			return &WebhookDelivery{
				ID: id, WebhookID: webhookID, EndpointID: endpointID, AsyncJobID: "j1",
				Event: tables.WebhookEventAsyncJobCompleted, AttemptNo: attemptNo,
				Outcome: outcome, StatusCode: statusCode, Error: errMsg,
				CreatedAt: base.Add(createdOffset), ExpiresAt: expires,
			}
		}
		runOnAll(t, stores, func(ctx context.Context, s LogStore) error {
			// wd1/wd2 are two attempts of the same delivery on one endpoint;
			// wd3 belongs to another endpoint and expired ten minutes ago.
			if err := s.CreateWebhookDelivery(ctx, mkDelivery("wd1", "wh1", "wh-ep-1", 1, WebhookDeliveryOutcomeRetryableFailure, 503, "upstream unavailable", -2*time.Minute, nil)); err != nil {
				return err
			}
			if err := s.CreateWebhookDelivery(ctx, mkDelivery("wd2", "wh1", "wh-ep-1", 2, WebhookDeliveryOutcomeDelivered, 200, "", -time.Minute, nil)); err != nil {
				return err
			}
			return s.CreateWebhookDelivery(ctx, mkDelivery("wd3", "wh2", "wh-ep-2", 1, WebhookDeliveryOutcomeExhausted, 500, "gave up", -time.Hour, timePtrP(base.Add(-10*time.Minute))))
		})

		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			d, err := s.FindWebhookDeliveryByID(ctx, "wd1")
			if err != nil {
				return nil, err
			}
			return webhookDeliveryProjection(d), nil
		})
		for name, s := range stores {
			_, err := s.FindWebhookDeliveryByID(ctx, "missing-delivery")
			assert.ErrorIs(t, err, ErrNotFound, name)
		}

		// Endpoint-scoped history pages newest-first on every backend.
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			res, err := s.SearchWebhookDeliveries(ctx, "wh-ep-1", PaginationOptions{Limit: 10})
			if err != nil {
				return nil, err
			}
			return webhookDeliverySearchProjection(res), nil
		})
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			res, err := s.SearchWebhookDeliveries(ctx, "wh-ep-1", PaginationOptions{Limit: 1, Offset: 1})
			if err != nil {
				return nil, err
			}
			return webhookDeliverySearchProjection(res), nil
		})

		// Expired cleanup removes wd3 with the same count everywhere.
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			return s.DeleteExpiredWebhookDeliveries(ctx)
		})
		assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
			res, err := s.SearchWebhookDeliveries(ctx, "wh-ep-2", PaginationOptions{Limit: 10})
			if err != nil {
				return nil, err
			}
			return webhookDeliverySearchProjection(res), nil
		})
	})

	// --- Phase: deletes (destructive; order matters) ---

	t.Run("Deletes", func(t *testing.T) {
		t.Run("flush_processing", func(t *testing.T) {
			// Flush drops processing rows older than since: p5 (base-60s).
			runOnAll(t, stores, func(ctx context.Context, s LogStore) error {
				return s.Flush(ctx, base.Add(-55*time.Second))
			})
			assertParity(t, stores, 1e-6, remainingLogIDs)
		})
		t.Run("delete_log", func(t *testing.T) {
			runOnAll(t, stores, func(ctx context.Context, s LogStore) error {
				return s.DeleteLog(ctx, "p1")
			})
			for name, s := range stores {
				_, err := s.FindByID(ctx, "p1")
				assert.ErrorIs(t, err, ErrNotFound, name)
			}
			assertParity(t, stores, 1e-6, remainingLogIDs)
		})
		t.Run("delete_logs", func(t *testing.T) {
			runOnAll(t, stores, func(ctx context.Context, s LogStore) error {
				return s.DeleteLogs(ctx, []string{"p2", "p3"})
			})
			assertParity(t, stores, 1e-6, remainingLogIDs)
		})
		t.Run("delete_logs_batch", func(t *testing.T) {
			// Deletes rows created before base-45s (p4, p6 remain from that
			// window) and must report the same count on every backend
			// (ClickHouse mutations report 0 rows affected natively).
			assertParity(t, stores, 1e-6, func(ctx context.Context, s LogStore) (any, error) {
				return s.DeleteLogsBatch(ctx, base.Add(-45*time.Second), 100)
			})
			assertParity(t, stores, 1e-6, remainingLogIDs)
		})
		t.Run("flush_mcp", func(t *testing.T) {
			// m4 is the only processing MCP row; FlushMCPToolLogs drops it.
			runOnAll(t, stores, func(ctx context.Context, s LogStore) error {
				return s.FlushMCPToolLogs(ctx, base)
			})
			assertParity(t, stores, 1e-6, remainingMCPIDs)
		})
		t.Run("delete_mcp", func(t *testing.T) {
			runOnAll(t, stores, func(ctx context.Context, s LogStore) error {
				return s.DeleteMCPToolLogs(ctx, []string{"m1"})
			})
			for name, s := range stores {
				_, err := s.FindMCPToolLog(ctx, "m1")
				assert.ErrorIs(t, err, ErrNotFound, name)
			}
			assertParity(t, stores, 1e-6, remainingMCPIDs)
		})
	})

	// --- Phase: shutdown ---

	t.Run("Close", func(t *testing.T) {
		runOnAll(t, stores, func(ctx context.Context, s LogStore) error {
			return s.Close(ctx)
		})
	})
}
