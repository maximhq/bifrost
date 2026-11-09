package evals

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ordersSchema = `
CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER REFERENCES customers(id), total REAL);
INSERT INTO customers (id, name) VALUES (1, 'Ada');
`

func runSQLValidation(t *testing.T, query, schema string) Result {
	t.Helper()
	engine := NewEngine(EngineOptions{})
	RegisterStatisticalEvaluators(engine, nil)
	return engine.Run(context.Background(), statisticalDefinition(BuiltinSQLiteValidation, ""),
		Input{Variables: map[string]string{"output": query, "db_schema": schema}})
}

func TestSQLiteValidation(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		wantScore  float64
		wantReason string
	}{
		{"valid select", "SELECT c.name, SUM(o.total) FROM customers c JOIN orders o ON o.customer_id = c.id GROUP BY c.name", 1, "SQL query is valid."},
		{"valid with trailing semicolon and comment", "SELECT name FROM customers; -- done", 1, "SQL query is valid."},
		{"valid insert", "INSERT INTO orders (customer_id, total) VALUES (1, 9.5)", 1, "SQL query is valid."},
		{"unknown table", "SELECT * FROM invoices", 0, "no such table: invoices"},
		{"unknown column", "SELECT email FROM customers", 0, "no such column: email"},
		{"syntax error", "SELEC name FROM customers", 0, "syntax error"},
		{"constraint violation at run time", "INSERT INTO customers (id) VALUES (2)", 0, "NOT NULL"},
		{"two statements", "SELECT 1; SELECT 2", 0, "you can only execute one statement at a time"},
		{"semicolon inside a string is one statement", "SELECT 'a; b' FROM customers", 1, "SQL query is valid."},
		{"empty query", "   ", 0, "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := runSQLValidation(t, tc.query, ordersSchema)
			require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
			require.NotNil(t, result.Score.Number)
			assert.Equal(t, tc.wantScore, *result.Score.Number, result.Reasoning)
			assert.Contains(t, result.Reasoning, tc.wantReason)
		})
	}
}

func TestSQLiteValidation_BadSchemaIsReported(t *testing.T) {
	result := runSQLValidation(t, "SELECT 1", "CREATE TABL oops (id)")
	require.Equal(t, ScoreStatusOK, result.Score.Status)
	assert.Equal(t, float64(0), *result.Score.Number)
	assert.Contains(t, result.Reasoning, "Invalid SQL query")
}

func TestSQLiteValidation_RequiresSchemaVariable(t *testing.T) {
	engine := NewEngine(EngineOptions{})
	RegisterStatisticalEvaluators(engine, nil)
	result := engine.Run(context.Background(), statisticalDefinition(BuiltinSQLiteValidation, ""), Input{Variables: map[string]string{"output": "SELECT 1"}})
	assert.Equal(t, ScoreStatusNA, result.Score.Status)
	assert.Equal(t, "Missing variables: db_schema", result.Reasoning)
}

// The query and the schema are model or user text. They must not be able to reach the
// gateway's filesystem, however they are phrased.
func TestSQLiteValidation_CannotTouchTheFilesystem(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "leak.db")
	attacks := []struct{ name, query, schema string }{
		{"attach in the query", "ATTACH DATABASE '" + target + "' AS leak", ""},
		{"attach in the schema", "SELECT 1", "ATTACH DATABASE '" + target + "' AS leak; CREATE TABLE leak.t (x);"},
		{"vacuum into a file", "VACUUM INTO '" + target + "'", "CREATE TABLE t (x);"},
		{"load an extension", "SELECT load_extension('" + target + "')", ""},
	}
	for _, attack := range attacks {
		t.Run(attack.name, func(t *testing.T) {
			result := runSQLValidation(t, attack.query, attack.schema)
			require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
			assert.Equal(t, float64(0), *result.Score.Number, "the statement is refused: %s", result.Reasoning)
			_, err := os.Stat(target)
			assert.True(t, os.IsNotExist(err), "no file was created on the host")
		})
	}
}

// A query that would run forever is cut off instead of holding a worker.
func TestSQLiteValidation_RunawaySchemaIsCutOff(t *testing.T) {
	runaway := "CREATE TABLE n (x); WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) INSERT INTO n SELECT x FROM c;"
	result := runSQLValidation(t, "SELECT 1", runaway)
	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.Equal(t, float64(0), *result.Score.Number)
}
