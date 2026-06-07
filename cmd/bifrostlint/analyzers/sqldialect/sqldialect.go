// Package sqldialect implements rule 3: Postgres-only SQL tokens in string
// literals must be inside a code path that has checked the SQL dialect.
//
// The analyzer scans string literals for Postgres-only keywords. If a hit is
// found, it walks the ancestor stack looking for an enclosing if/switch whose
// condition references one of the known dialect-indicator identifiers
// (Dialect, Dialector, Driver, DB.Name, etc.). If no such gate is found, it
// reports.
//
// Per-line suppression: // bifrostlint:ignore sqldialect <reason>
// File-level suppression: place the directive in the package doc comment.
package sqldialect

import (
	"go/ast"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/maximhq/bifrost/cmd/bifrostlint/internal/ignore"
	"github.com/maximhq/bifrost/cmd/bifrostlint/internal/sqlscan"
)

const ruleID = "sqldialect"

var Analyzer = &analysis.Analyzer{
	Name: ruleID,
	Doc:  "flags Postgres-only SQL not gated by a dialect check",
	Run:  run,
}

// Postgres-only single-word tokens that must not appear in dialect-agnostic
// code paths.
var postgresOnlyKeywords = []string{
	"CONCURRENTLY",
	"pg_try_advisory_lock",
	"pg_advisory_lock",
	"pg_advisory_unlock",
}

// Identifier names whose presence in an enclosing if/switch condition is
// treated as a dialect check.
var dialectIdents = map[string]bool{
	"Dialect":   true,
	"Dialector": true,
	"Driver":    true,
	"DriverName": true,
	"DBType":    true,
	"IsPostgres": true,
	"IsPostgreSQL": true,
}

// Method names invoked on a DB/dialect object that constitute a dialect check.
var dialectMethods = map[string]bool{
	"Name":       true,
	"Dialect":    true,
	"Dialector":  true,
	"DriverName": true,
}

func run(pass *analysis.Pass) (interface{}, error) {
	for _, file := range pass.Files {
		if ignore.HasFileDirective(file, ruleID) {
			continue
		}
		v := &visitor{pass: pass, file: file}
		ast.Walk(v, file)
	}
	return nil, nil
}

type visitor struct {
	pass  *analysis.Pass
	file  *ast.File
	stack []ast.Node
}

func (v *visitor) Visit(n ast.Node) ast.Visitor {
	if n == nil {
		v.stack = v.stack[:len(v.stack)-1]
		return nil
	}
	v.stack = append(v.stack, n)
	if lit, ok := n.(*ast.BasicLit); ok {
		v.check(lit)
	}
	return v
}

func (v *visitor) check(lit *ast.BasicLit) {
	val := sqlscan.LiteralValue(lit)
	if val == "" {
		return
	}
	if !sqlscan.ContainsAnyKeyword(val, postgresOnlyKeywords...) && !sqlscan.IsMaterializedViewOp(val) {
		return
	}
	// Only fire when the literal is executed - constants and DDL-builders are fine.
	if sqlscan.ExecutingCall(v.stack) == nil {
		return
	}
	if v.inDialectGuard() {
		return
	}
	if ignore.HasDirective(v.pass.Fset, v.file, lit.Pos(), ruleID) {
		return
	}
	v.pass.Reportf(lit.Pos(),
		"Postgres-only SQL outside a dialect check - gate this branch on db dialect or annotate with // bifrostlint:ignore sqldialect <reason>")
}

func (v *visitor) inDialectGuard() bool {
	for i := len(v.stack) - 1; i >= 0; i-- {
		switch node := v.stack[i].(type) {
		case *ast.IfStmt:
			if exprMentionsDialect(node.Cond) {
				return true
			}
		case *ast.SwitchStmt:
			if exprMentionsDialect(node.Tag) {
				return true
			}
		case *ast.TypeSwitchStmt:
			if stmtMentionsDialect(node.Assign) {
				return true
			}
		}
	}
	return false
}

func exprMentionsDialect(e ast.Expr) bool {
	if e == nil {
		return false
	}
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if dialectIdents[x.Name] {
				found = true
				return false
			}
			lower := strings.ToLower(x.Name)
			if strings.Contains(lower, "postgres") || strings.Contains(lower, "sqlite") || strings.Contains(lower, "mysql") {
				found = true
				return false
			}
		case *ast.SelectorExpr:
			if dialectMethods[x.Sel.Name] || dialectIdents[x.Sel.Name] {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func stmtMentionsDialect(s ast.Stmt) bool {
	if s == nil {
		return false
	}
	found := false
	ast.Inspect(s, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && dialectIdents[id.Name] {
			found = true
			return false
		}
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if dialectMethods[sel.Sel.Name] || dialectIdents[sel.Sel.Name] {
				found = true
				return false
			}
		}
		return true
	})
	return found
}
