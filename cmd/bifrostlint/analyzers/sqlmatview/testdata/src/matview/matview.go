package matview

type Result struct{ Error error }

type DB struct{}

func (d *DB) Exec(q string, args ...interface{}) *Result { return &Result{} }

func bootMigrations(db *DB) error {
	db.Exec(`CREATE MATERIALIZED VIEW mv_logs_hourly AS SELECT 1`) // want `materialized view operation on potentially blocking path`
	return nil
}

func bareConstantNotFlagged() {
	// String constants and DDL-building helpers that don't execute should not fire.
	const stmt = `CREATE MATERIALIZED VIEW mv_thing AS SELECT 1`
	_ = stmt
}

// bifrostlint:background
func ensureMatviewShapesInBackground(db *DB) error {
	db.Exec(`CREATE MATERIALIZED VIEW mv_filter_models AS SELECT 1`)
	return nil
}

func periodicallyRefreshMatViews(db *DB) {
	db.Exec(`REFRESH MATERIALIZED VIEW CONCURRENTLY mv_logs_hourly`)
}

func bootGoroutine(db *DB) {
	go func() {
		db.Exec(`REFRESH MATERIALIZED VIEW CONCURRENTLY mv_logs_hourly`)
	}()
}

func suppressed(db *DB) {
	db.Exec(`CREATE MATERIALIZED VIEW mv_thing AS SELECT 1`) // bifrostlint:ignore sqlmatview test fixture
}
