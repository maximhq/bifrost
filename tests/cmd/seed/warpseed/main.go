// Command warpseed inserts realistic synthetic LLM request logs directly into
// a local Bifrost Postgres logs store, for exercising the Warp feature (log
// search, chat, and RCA) without spending real provider quota.
//
// It writes 500 chat-completion logs: ~450 successes across varied everyday
// use cases, ~20 scattered one-off failures of different kinds, and a tight
// 30-request cluster of identical Anthropic "overloaded_error" failures
// packed into an ~18 minute window -- a synthetic incident meant to give
// Warp's RCA something coherent to find (one root cause, many symptoms).
//
// On top of that it writes a quieter prior week (successes only, so week over
// week has a direction), and three pinned rows an e2e check can name exactly:
// the slowest request of the last day, the most expensive of the week, and one
// whose logged prompt carries a prompt-injection attempt.
//
// The e2e suite (tests/e2e/api/runners/individual/run-newman-warp-tests.sh)
// runs it twice against a throwaway database: -reset-db before Bifrost boots,
// so the server migrates an empty database, then a seeding run with -env-out,
// whose KEY=VALUE file carries the incident window and pinned ids to newman.
package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func main() {
	dsn := flag.String("dsn", "postgres://bifrost:bifrost_password@localhost:5433/bifrost?sslmode=disable", "logs Postgres DSN")
	seed := flag.Int64("seed", time.Now().UnixNano(), "rng seed")
	days := flag.Int("days", 7, "spread success/scattered-failure timestamps over this many past days")
	priorWeek := flag.Int("prior-week", 250, "successes to spread over the week before the seeded window")
	resetDB := flag.Bool("reset-db", false, "drop and recreate the database named in -dsn, then exit without seeding")
	envOut := flag.String("env-out", "", "write the incident window and pinned row ids to this file as KEY=VALUE lines")
	flag.Parse()
	if *days < 1 {
		log.Fatalf("-days must be at least 1, got %d", *days)
	}
	windowDays = *days

	if *resetDB {
		if err := recreateDatabase(*dsn); err != nil {
			log.Fatalf("reset db: %v", err)
		}
		fmt.Println("recreated database")
		return
	}

	db, err := gorm.Open(postgres.Open(*dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("open logs db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("get sql.DB: %v", err)
	}
	defer sqlDB.Close()
	if err := sqlDB.Ping(); err != nil {
		log.Fatalf("ping logs db: %v", err)
	}

	rng := rand.New(rand.NewSource(*seed))
	now := time.Now().UTC()

	var rows []logstore.Log
	for i := range successCount {
		rows = append(rows, buildSuccessLog(rng, now, i))
	}
	for i := range diverseFailures {
		rows = append(rows, buildDiverseFailureLog(rng, now, i))
	}
	incidentOffsetHours := 20
	if windowHours := windowDays * 24; incidentOffsetHours >= windowHours {
		incidentOffsetHours = windowHours / 2
	}
	incidentStart := now.Add(-time.Duration(incidentOffsetHours) * time.Hour).Add(7 * time.Minute)
	for i := range incidentCount {
		rows = append(rows, buildIncidentLog(rng, incidentStart, i))
	}
	for range *priorWeek {
		rows = append(rows, buildPriorWeekLog(rng, now))
	}
	slowest := buildSlowestLog(rng, now)
	mostExpensive := buildMostExpensiveLog(rng, now)
	injection := buildInjectionLog(rng, now)
	rows = append(rows, slowest, mostExpensive, injection)

	rng.Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })

	ptrs := make([]*logstore.Log, len(rows))
	for i := range rows {
		ptrs[i] = &rows[i]
	}

	const batchSize = 100
	inserted := 0
	for start := 0; start < len(ptrs); start += batchSize {
		end := start + batchSize
		if end > len(ptrs) {
			end = len(ptrs)
		}
		batch := ptrs[start:end]
		result := db.Omit("inc_number").Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoNothing: true,
		}).Create(&batch)
		if result.Error != nil {
			log.Fatalf("insert batch [%d:%d]: %v", start, end, result.Error)
		}
		inserted += len(batch)
	}

	// The server's aggregate tools read materialized views it refreshes on a
	// timer, and rows inserted here bypass the write path that would mark them
	// stale. Without this, the first questions after seeding saw an empty week
	// ("0 errors across 1 request") while the logs table held all of it.
	var views []string
	if err := db.Raw("SELECT matviewname FROM pg_matviews WHERE schemaname = current_schema()").Scan(&views).Error; err != nil {
		log.Fatalf("list materialized views: %v", err)
	}
	for _, view := range views {
		if err := db.Exec(`REFRESH MATERIALIZED VIEW "` + strings.ReplaceAll(view, `"`, `""`) + `"`).Error; err != nil {
			log.Fatalf("refresh %s: %v", view, err)
		}
	}

	incidentEnd := incidentStart.Add(18 * time.Minute)
	fmt.Printf("inserted %d logs: %d successes, %d scattered failures, %d incident-cluster failures, %d prior-week successes, 3 pinned\n",
		inserted, successCount, len(diverseFailures), incidentCount, *priorWeek)
	fmt.Printf("refreshed %d materialized views\n", len(views))
	fmt.Printf("incident window: %s -> %s (provider=anthropic model=%s type=overloaded_error)\n",
		incidentStart.Format(time.RFC3339), incidentEnd.Format(time.RFC3339), incidentModel)

	if *envOut != "" {
		env := []string{
			"warp_seed_now=" + now.Format(time.RFC3339),
			"warp_incident_start=" + incidentStart.Format(time.RFC3339),
			"warp_incident_end=" + incidentEnd.Format(time.RFC3339),
			"warp_incident_model=" + incidentModel,
			"warp_incident_count=" + fmt.Sprint(incidentCount),
			"warp_slowest_id=" + slowest.ID,
			"warp_most_expensive_id=" + mostExpensive.ID,
			"warp_injection_id=" + injection.ID,
			"warp_injection_host=" + injectionHost,
		}
		if err := os.WriteFile(*envOut, []byte(strings.Join(env, "\n")+"\n"), 0o644); err != nil {
			log.Fatalf("write -env-out: %v", err)
		}
		fmt.Printf("wrote %s\n", *envOut)
	}
}

// recreateDatabase drops and recreates the database the DSN names, connecting
// through the server's "postgres" maintenance database. It lets the e2e runner
// start every run from an empty logs table without needing psql on the host.
func recreateDatabase(dsn string) error {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("parse dsn: %w", err)
	}
	name := strings.TrimPrefix(parsed.Path, "/")
	if name == "" || name == "postgres" {
		return fmt.Errorf("refusing to recreate database %q", name)
	}
	parsed.Path = "/postgres"
	admin, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("open maintenance db: %w", err)
	}
	sqlDB, err := admin.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	if err := admin.Exec("DROP DATABASE IF EXISTS " + quoted + " WITH (FORCE)").Error; err != nil {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	if err := admin.Exec("CREATE DATABASE " + quoted).Error; err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	return nil
}
