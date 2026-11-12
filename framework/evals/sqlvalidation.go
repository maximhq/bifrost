package evals

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
				// Set before the authorizer, which denies PRAGMA from then on.
				if _, err := conn.Exec(fmt.Sprintf("PRAGMA max_page_count = %d", sqlValidationMaxPages), nil); err != nil {
					return err
				}
				conn.SetLimit(sqlite3.SQLITE_LIMIT_ATTACHED, 0)
				conn.SetLimit(sqlite3.SQLITE_LIMIT_SQL_LENGTH, sqlValidationMaxSQL)
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

func (sqliteValidationEvaluator) Evaluate(ctx context.Context, in Input) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, sqlValidationTimeout)
	defer cancel()
	if err := validateSQL(ctx, in.Variables[VariableOutput], in.Variables[VariableDBSchema]); err != nil {
		return Result{Score: NumberScore(0), Reasoning: "Invalid SQL query: " + err.Error()}, nil
	}
	return Result{Score: NumberScore(1), Reasoning: "SQL query is valid."}, nil
}

// validateSQL creates the schema in a fresh database and runs the query's first step.
func validateSQL(ctx context.Context, query, schema string) error {
	if strings.TrimSpace(query) == "" {
		return errors.New("the query is empty")
	}
	if !isSingleStatement(query) {
		return errors.New("you can only execute one statement at a time")
	}
	db, err := sqlValidationDB()
	if err != nil {
		return err
	}
	defer db.Close()
	if strings.TrimSpace(schema) != "" {
		if _, err := db.ExecContext(ctx, schema); err != nil {
			return describeSQLError(ctx, err)
		}
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return describeSQLError(ctx, err)
	}
	defer rows.Close()
	// One step is enough to surface run-time errors without reading a whole result.
	rows.Next()
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

// isSingleStatement reports whether text holds one SQL statement. Semicolons inside
// string literals, quoted identifiers and comments do not end a statement, and a
// trailing semicolon or comment is fine.
func isSingleStatement(text string) bool {
	ended := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			if ended {
				return false
			}
			i = skipQuoted(text, i, c)
		case c == '[':
			if ended {
				return false
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
			ended = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
		default:
			if ended {
				return false
			}
		}
	}
	return true
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
