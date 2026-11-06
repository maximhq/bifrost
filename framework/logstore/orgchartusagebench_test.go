package logstore

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

// TestOrgChartUsageReadsBenchmark times the log reads one org chart level costs,
// before and after batching, on a seeded volume. It is opt-in: set
// BIFROST_USAGE_BENCH_ROWS to the number of log rows to seed.
func TestOrgChartUsageReadsBenchmark(t *testing.T) {
	rows, _ := strconv.Atoi(os.Getenv("BIFROST_USAGE_BENCH_ROWS"))
	if rows <= 0 {
		t.Skip("set BIFROST_USAGE_BENCH_ROWS to run")
	}
	t.Run("postgres", func(t *testing.T) {
		store, db := setupPerfTestDB(t)
		seedBenchPostgres(t, db, rows)
		runOrgChartBench(t, store)
	})
	t.Run("clickhouse", func(t *testing.T) {
		ch := trySetupClickHouseStore(t)
		seedBenchClickHouse(t, ch.db, rows)
		runOrgChartBench(t, ch.RDBLogStore)
	})
}

// benchUnits is how many business units the seed spreads traffic over.
const benchUnits = 13

// seedBenchPostgres writes rows over the last 60 days, each credited to one or two
// business units through the array columns, plus a tenth as many MCP tool calls.
func seedBenchPostgres(t *testing.T, db *gorm.DB, rows int) {
	start := time.Now()
	require.NoError(t, db.Exec(fmt.Sprintf(`
		INSERT INTO logs (id, timestamp, created_at, object_type, provider, model, status, cost, total_tokens,
			business_unit_ids, business_unit_names, team_ids, team_names, user_id)
		SELECT 'b-' || g, ts, ts, 'chat_completion',
			CASE WHEN g %% 3 = 0 THEN 'anthropic' ELSE 'openai' END,
			'model-' || (g %% 12), 'success', (g %% 100) / 1000.0, g %% 2000,
			bu, bu, '["team-' || (g %% 50) || '"]', '["team-' || (g %% 50) || '"]', 'user-' || (g %% 2000)
		FROM (
			SELECT g, now() - (g %% 5184000) * interval '1 second' AS ts,
				CASE WHEN g %% 4 = 0 THEN '["bu-' || (g %% %[1]d) || '","bu-' || ((g + 1) %% %[1]d) || '"]'
				     ELSE '["bu-' || (g %% %[1]d) || '"]' END AS bu
			FROM generate_series(1, %[2]d) g
		) s`, benchUnits, rows)).Error)
	require.NoError(t, db.Exec(fmt.Sprintf(`
		INSERT INTO mcp_tool_logs (id, request_id, timestamp, created_at, tool_name, status, business_unit_id)
		SELECT 'm-' || g, 'm-' || g, now() - (g %% 5184000) * interval '1 second', now(), 'tool-' || (g %% 40), 'success', 'bu-' || (g %% %d)
		FROM generate_series(1, %d) g`, benchUnits, rows/10)).Error)
	require.NoError(t, db.Exec("ANALYZE logs").Error)
	require.NoError(t, db.Exec("ANALYZE mcp_tool_logs").Error)
	t.Logf("seeded %d rows in %s", rows, time.Since(start).Round(time.Millisecond))
}

// seedBenchClickHouse writes the same shape of rows as seedBenchPostgres.
func seedBenchClickHouse(t *testing.T, db *gorm.DB, rows int) {
	start := time.Now()
	require.NoError(t, db.Exec(fmt.Sprintf(`
		INSERT INTO logs (id, timestamp, created_at, object_type, provider, model, status, cost, total_tokens,
			business_unit_ids, business_unit_names, team_ids, team_names, user_id)
		SELECT concat('b-', toString(number)), ts, ts, 'chat_completion',
			if(number %% 3 = 0, 'anthropic', 'openai'),
			concat('model-', toString(number %% 12)), 'success', (number %% 100) / 1000.0, number %% 2000,
			bu, bu, concat('["team-', toString(number %% 50), '"]'), concat('["team-', toString(number %% 50), '"]'), concat('user-', toString(number %% 2000))
		FROM (
			SELECT number, now64(3) - toIntervalSecond(number %% 5184000) AS ts,
				if(number %% 4 = 0,
					concat('["bu-', toString(number %% %[1]d), '","bu-', toString((number + 1) %% %[1]d), '"]'),
					concat('["bu-', toString(number %% %[1]d), '"]')) AS bu
			FROM numbers(%[2]d)
		)`, benchUnits, rows)).Error)
	require.NoError(t, db.Exec(fmt.Sprintf(`
		INSERT INTO mcp_tool_logs (id, request_id, timestamp, created_at, tool_name, status, business_unit_id)
		SELECT concat('m-', toString(number)), concat('m-', toString(number)), now64(3) - toIntervalSecond(number %% 5184000), now64(3),
			concat('tool-', toString(number %% 40)), 'success', concat('bu-', toString(number %% %d))
		FROM numbers(%d)`, benchUnits, rows/10)).Error)
	t.Logf("seeded %d rows in %s", rows, time.Since(start).Round(time.Millisecond))
}

// runOrgChartBench times the reads /children?parent_type=org&period=30d&limit=5
// issues: the old ranking plus a model and a tool read per page node (eight at a
// time, as the handler ran them), against one usage read plus one model and one
// tool read for the page.
func runOrgChartBench(t *testing.T, s *RDBLogStore) {
	ctx := context.Background()
	end := time.Now()
	start := end.Add(-30 * 24 * time.Hour)
	units := make([]string, benchUnits)
	for i := range units {
		units[i] = fmt.Sprintf("bu-%d", i)
	}
	page := units[:5]
	window := SearchFilters{StartTime: &start, EndTime: &end, BusinessUnitIDs: units}
	mcpWindow := MCPToolLogSearchFilters{StartTime: &start, EndTime: &end}

	before := func() error {
		unlimited := 0
		ranking := window
		ranking.RankingLimit = &unlimited
		if _, err := s.GetDimensionRankings(ctx, ranking, RankingDimensionBusinessUnit); err != nil {
			return err
		}
		g, _ := errgroup.WithContext(ctx)
		g.SetLimit(8)
		for _, id := range page {
			g.Go(func() error {
				f := window
				f.BusinessUnitIDs = []string{id}
				if _, err := s.GetModelRankings(ctx, f); err != nil {
					return err
				}
				m := mcpWindow
				m.BusinessUnitIDs = []string{id}
				_, err := s.GetMCPTopTools(ctx, m, 100)
				return err
			})
		}
		return g.Wait()
	}
	after := func() error {
		if _, err := s.GetDimensionUsage(ctx, window, RankingDimensionBusinessUnit, true); err != nil {
			return err
		}
		g, _ := errgroup.WithContext(ctx)
		g.Go(func() error {
			f := window
			f.BusinessUnitIDs = page
			_, err := s.GetDimensionModelUsage(ctx, f, RankingDimensionBusinessUnit)
			return err
		})
		g.Go(func() error {
			m := mcpWindow
			m.BusinessUnitIDs = page
			_, err := s.GetMCPToolCountsByDimension(ctx, m, RankingDimensionBusinessUnit)
			return err
		})
		return g.Wait()
	}
	ranking := func() error {
		unlimited := 0
		f := window
		f.RankingLimit = &unlimited
		_, err := s.GetDimensionRankings(ctx, f, RankingDimensionBusinessUnit)
		return err
	}
	usage := func() error {
		_, err := s.GetDimensionUsage(ctx, window, RankingDimensionBusinessUnit, true)
		return err
	}
	for _, c := range []struct {
		name string
		fn   func() error
	}{{"level before", before}, {"level after", after}, {"ranking read only", ranking}, {"usage read only", usage}} {
		require.NoError(t, c.fn(), c.name) // warm up
		var runs []time.Duration
		for range 5 {
			began := time.Now()
			require.NoError(t, c.fn(), c.name)
			runs = append(runs, time.Since(began))
		}
		sort.Slice(runs, func(i, j int) bool { return runs[i] < runs[j] })
		t.Logf("%-18s median %8s  min %8s  max %8s", c.name, runs[2].Round(time.Millisecond), runs[0].Round(time.Millisecond), runs[4].Round(time.Millisecond))
	}
}
