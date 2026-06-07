package dialect

type Result struct{ Error error }

type DB struct{ Dialect string }

func (d *DB) Name() string                                { return d.Dialect }
func (d *DB) Exec(q string, args ...interface{}) *Result { return &Result{} }

func ungated(db *DB) {
	db.Exec(`CREATE INDEX CONCURRENTLY idx_foo ON bar(x)`) // want `Postgres-only SQL outside a dialect check`
}

func gatedByIf(db *DB) {
	if db.Name() == "postgres" {
		db.Exec(`CREATE INDEX CONCURRENTLY idx_foo ON bar(x)`)
	}
}

func gatedBySwitch(db *DB) {
	switch db.Dialect {
	case "postgres":
		db.Exec(`CREATE MATERIALIZED VIEW mv AS SELECT 1`)
	}
}

func bareConstantNotFlagged() {
	const stmt = `CREATE INDEX CONCURRENTLY idx_foo ON bar(x)`
	_ = stmt
}

func suppressed(db *DB) {
	db.Exec(`CREATE INDEX CONCURRENTLY idx ON bar(x)`) // bifrostlint:ignore sqldialect test fixture
}
