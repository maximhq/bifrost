package evals

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-sqlite3"
)

// BuiltinSQLiteValidation is the catalog key of the SQLite validation metric.
const BuiltinSQLiteValidation = "sqlite_validation"

// VariableDBSchema holds the SQL that creates the tables a query is validated against.
const VariableDBSchema = "db_schema"

// Limits on the throwaway database a query is validated in.
const (
	sqlValidationDriver   = "sqlite3_bifrost_evals"
	sqlValidationTimeout  = 2 * time.Second
	sqlValidationMaxPages = 4096 // 16 MB at the default 4 KB page size
	sqlValidationMaxSQL   = 1 << 20
	// sqlValidationMaxValue caps one computed string or blob. The page cap bounds stored
	// data only, so without this a short query such as hex(zeroblob(n)) builds any size.
	sqlValidationMaxValue = 1 << 20
)

var registerSQLValidationDriver sync.Once

// sqlValidationDB opens a private in-memory SQLite database that cannot reach the host.
// The query and the schema are model or user text, so the connection refuses anything
// that touches a file: ATTACH, DETACH, VACUUM INTO (which attaches), PRAGMA and
// extension loading. It is also capped in size.
func sqlValidationDB() (*sql.DB, error) {
	registerSQLValidationDriver.Do(func() {
		sql.Register(sqlValidationDriver, &sqlite3.SQLiteDriver{
			ConnectHook: func(conn *sqlite3.SQLiteConn) error {
				// Set before the authorizer, which denies PRAGMA from then on. TEMP tables
				// live in memory with the same cap as the main database: SQLite's default
				// would put them in a host file with no size bound.
				for _, pragma := range []string{
					fmt.Sprintf("PRAGMA max_page_count = %d", sqlValidationMaxPages),
					"PRAGMA temp_store = MEMORY",
					fmt.Sprintf("PRAGMA temp.max_page_count = %d", sqlValidationMaxPages),
				} {
					if _, err := conn.Exec(pragma, nil); err != nil {
						return err
					}
				}
				conn.SetLimit(sqlite3.SQLITE_LIMIT_ATTACHED, 0)
				conn.SetLimit(sqlite3.SQLITE_LIMIT_SQL_LENGTH, sqlValidationMaxSQL)
				conn.SetLimit(sqlite3.SQLITE_LIMIT_LENGTH, sqlValidationMaxValue)
				conn.RegisterAuthorizer(func(op int, arg1, arg2, _ string) int {
					switch op {
					case sqlite3.SQLITE_ATTACH, sqlite3.SQLITE_DETACH, sqlite3.SQLITE_PRAGMA:
						return sqlite3.SQLITE_DENY
					case sqlite3.SQLITE_FUNCTION:
						if strings.EqualFold(arg2, "load_extension") {
							return sqlite3.SQLITE_DENY
						}
					}
					return sqlite3.SQLITE_OK
				})
				return nil
			},
		})
	})
	db, err := sql.Open(sqlValidationDriver, ":memory:")
	if err != nil {
		return nil, err
	}
	// One connection, so the schema and the query see the same in-memory database.
	db.SetMaxOpenConns(1)
	return db, nil
}

// sqliteValidationEvaluator scores 1 when a query runs against a schema in SQLite and
// 0 when it does not, with SQLite's own message as the reasoning.
type sqliteValidationEvaluator struct{}

func (sqliteValidationEvaluator) RequiredVariables() []string {
	return []string{VariableOutput, VariableDBSchema}
}

// Evaluate scores only what the query and schema decide. A run its caller cancelled and
// a database that could not be set up say nothing about the query, so they are errors,
// not a score of 0.
func (sqliteValidationEvaluator) Evaluate(parent context.Context, in Input) (Result, error) {
	ctx, cancel := context.WithTimeout(parent, sqlValidationTimeout)
	defer cancel()
	if err := validateSQL(ctx, in.Variables[VariableOutput], in.Variables[VariableDBSchema]); err != nil {
		if parent.Err() != nil {
			return Result{}, parent.Err()
		}
		var setup *sqlSetupError
		if errors.As(err, &setup) {
			return Result{}, err
		}
		return Result{Score: NumberScore(0), Reasoning: "Invalid SQL query: " + err.Error()}, nil
	}
	return Result{Score: NumberScore(1), Reasoning: "SQL query is valid."}, nil
}

// sqlSetupError is a failure to prepare the throwaway database, before the schema or
// the query ran.
type sqlSetupError struct{ err error }

func (e *sqlSetupError) Error() string {
	return "failed to prepare the validation database: " + e.err.Error()
}
func (e *sqlSetupError) Unwrap() error { return e.err }

// validateSQL creates the schema in a fresh database and runs the query to the end.
func validateSQL(ctx context.Context, query, schema string) error {
	if strings.TrimSpace(query) == "" {
		return errors.New("the query is empty")
	}
	query, ok := singleStatement(query)
	if !ok {
		return errors.New("you can only execute one statement at a time")
	}
	db, err := sqlValidationDB()
	if err != nil {
		return &sqlSetupError{err: err}
	}
	defer db.Close()
	// The connection is taken first so a failure of its limits and pragmas (the connect
	// hook) is told apart from a failure of the schema or the query.
	conn, err := db.Conn(ctx)
	if err != nil {
		return &sqlSetupError{err: err}
	}
	defer conn.Close()
	if strings.TrimSpace(schema) != "" {
		if _, err := conn.ExecContext(ctx, schema); err != nil {
			return describeSQLError(ctx, err)
		}
	}
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return describeSQLError(ctx, err)
	}
	defer rows.Close()
	// Every row is stepped: a run-time error, such as malformed JSON, can sit on any row.
	// The deadline and the size caps bound the walk.
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		return describeSQLError(ctx, err)
	}
	return nil
}

func describeSQLError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("the statement did not finish within %s", sqlValidationTimeout)
	}
	return err
}

// singleStatement returns the one SQL statement in text, without the semicolon that
// ends it or anything after, and reports whether text holds exactly one. Semicolons
// inside string literals, quoted identifiers and comments do not end a statement, and a
// trailing semicolon or comment is fine. The driver is given only the statement: it
// re-runs a statement whose tail is a comment instead of stopping.
func singleStatement(text string) (string, bool) {
	ended, end := false, len(text)
	// A trigger's body is BEGIN ... END with its own semicolons; CASE ... END can sit inside
	// it. Inside that body a semicolon does not end the statement.
	trigger, depth := createTriggerPattern.MatchString(text), 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			if ended {
				return "", false
			}
			i = skipQuoted(text, i, c)
		case c == '[':
			if ended {
				return "", false
			}
			if end := strings.IndexByte(text[i:], ']'); end >= 0 {
				i += end
			} else {
				i = len(text)
			}
		case c == '-' && i+1 < len(text) && text[i+1] == '-':
			if end := strings.IndexByte(text[i:], '\n'); end >= 0 {
				i += end
			} else {
				i = len(text)
			}
		case c == '/' && i+1 < len(text) && text[i+1] == '*':
			if end := strings.Index(text[i+2:], "*/"); end >= 0 {
				i += end + 3
			} else {
				i = len(text)
			}
		case c == ';':
			if !ended && depth == 0 {
				ended, end = true, i
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
		default:
			if ended {
				return "", false
			}
			if trigger && isWordByte(c) {
				j := i
				for j < len(text) && isWordByte(text[j]) {
					j++
				}
				switch strings.ToUpper(text[i:j]) {
				case "BEGIN", "CASE":
					depth++
				case "END":
					if depth > 0 {
						depth--
					}
				}
				i = j - 1
			}
		}
	}
	return text[:end], true
}

// createTriggerPattern matches a statement that creates a trigger.
var createTriggerPattern = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:TEMP\s+|TEMPORARY\s+)?TRIGGER\b`)

func isWordByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// skipQuoted returns the index of the quote closing the literal that opens at start. A
// doubled quote inside the literal is an escaped quote.
func skipQuoted(text string, start int, quote byte) int {
	for i := start + 1; i < len(text); i++ {
		if text[i] != quote {
			continue
		}
		if i+1 < len(text) && text[i+1] == quote {
			i++
			continue
		}
		return i
	}
	return len(text)
}
