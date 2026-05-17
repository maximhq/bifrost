package concurrent

type Result struct{ Error error }

type Dialector struct{ Name func() string }

type DB struct{ Dialector Dialector }

func (d *DB) Exec(q string, args ...interface{}) *Result { return &Result{} }

func unguarded(db *DB) {
	db.Exec(`CREATE INDEX idx_foo ON bar(x)`) // want `CREATE INDEX without CONCURRENTLY`
}

func withConcurrently(db *DB) {
	db.Exec(`CREATE INDEX CONCURRENTLY idx_foo ON bar(x)`)
}

func notExecuted(db *DB) {
	// Bare literal that is never executed - must not fire.
	const stmt = `CREATE INDEX idx_foo ON bar(x)`
	_ = stmt
}

func sqliteEarlyReturn(db *DB) {
	if db.Dialector.Name() == "postgres" {
		return
	}
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_foo ON bar(x)`)
}

func postgresEqualsElseBranch(db *DB) {
	if db.Dialector.Name() == "postgres" {
		db.Exec(`CREATE INDEX CONCURRENTLY idx_foo ON bar(x)`)
	} else {
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_foo ON bar(x)`)
	}
}

func dialectSqliteThenBranch(db *DB) {
	if db.Dialector.Name() == "sqlite" {
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_foo ON bar(x)`)
	}
}

func suppressed(db *DB) {
	db.Exec(`CREATE INDEX idx_foo ON bar(x)`) // bifrostlint:ignore sqlconcurrent test fixture
}
