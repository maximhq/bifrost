// Package sqlconcurrent implements rule 1: CREATE INDEX statements must use
// CONCURRENTLY so migrations do not lock hot tables on Postgres.
//
// The analyzer scans string literals for CREATE INDEX. A literal passes if any
// of the following hold:
//
//   - the literal itself contains CONCURRENTLY
//   - the literal is in a code path that has been guarded against running on
//     Postgres (early-return pattern: `if dialect == "postgres" { return ... }`
//     before the literal, or an if/else branch whose condition mentions the
//     dialect and which excludes Postgres)
//
// Per-line suppression: // bifrostlint:ignore sqlconcurrent <reason>
package sqlconcurrent

import (
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/maximhq/bifrost/cmd/bifrostlint/internal/ignore"
	"github.com/maximhq/bifrost/cmd/bifrostlint/internal/sqlscan"
)

const ruleID = "sqlconcurrent"

var Analyzer = &analysis.Analyzer{
	Name: ruleID,
	Doc:  "flags CREATE INDEX without CONCURRENTLY outside of a non-Postgres code path",
	Run:  run,
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
	if val == "" || !sqlscan.IsCreateIndex(val) {
		return
	}
	if sqlscan.HasConcurrently(val) {
		return
	}
	if sqlscan.ExecutingCall(v.stack) == nil {
		return
	}
	if v.inNonPostgresBranch(lit.Pos()) {
		return
	}
	if v.hasPostgresEarlyReturn(lit.Pos()) {
		return
	}
	if ignore.HasDirective(v.pass.Fset, v.file, lit.Pos(), ruleID) {
		return
	}
	v.pass.Reportf(lit.Pos(),
		"CREATE INDEX without CONCURRENTLY - locks the table on Postgres; add CONCURRENTLY or guard the path against running on Postgres")
}

// inNonPostgresBranch reports whether the literal sits inside an if/else whose
// condition mentions the SQL dialect and whose branch excludes Postgres.
func (v *visitor) inNonPostgresBranch(litPos token.Pos) bool {
	for i := len(v.stack) - 1; i >= 0; i-- {
		ifStmt, ok := v.stack[i].(*ast.IfStmt)
		if !ok {
			continue
		}
		cond := ifStmt.Cond
		op, lhs, rhs := dialectCompare(cond)
		if op == token.ILLEGAL {
			continue
		}
		other := otherSide(lhs, rhs)
		if other == "" {
			continue
		}
		inThen := containsPos(ifStmt.Body, litPos)
		inElse := ifStmt.Else != nil && containsPos(ifStmt.Else, litPos)
		switch op {
		case token.EQL: // cond is dialect == "X"
			if other == "postgres" || other == "postgresql" {
				if inElse {
					return true
				}
			} else if inThen {
				// dialect == "sqlite" etc - then branch excludes postgres
				return true
			}
		case token.NEQ: // cond is dialect != "X"
			if other == "postgres" || other == "postgresql" {
				if inThen {
					return true
				}
			} else if inElse {
				return true
			}
		}
	}
	return false
}

// hasPostgresEarlyReturn reports whether the enclosing function exits early
// when running on Postgres before the given position.
func (v *visitor) hasPostgresEarlyReturn(litPos token.Pos) bool {
	// find enclosing FuncDecl or FuncLit body
	var body *ast.BlockStmt
	for i := len(v.stack) - 1; i >= 0; i-- {
		switch fn := v.stack[i].(type) {
		case *ast.FuncDecl:
			body = fn.Body
		case *ast.FuncLit:
			body = fn.Body
		}
		if body != nil {
			break
		}
	}
	if body == nil {
		return false
	}
	for _, stmt := range body.List {
		if stmt.Pos() >= litPos {
			break
		}
		ifStmt, ok := stmt.(*ast.IfStmt)
		if !ok {
			continue
		}
		op, lhs, rhs := dialectCompare(ifStmt.Cond)
		if op != token.EQL {
			continue
		}
		other := otherSide(lhs, rhs)
		if other != "postgres" && other != "postgresql" {
			continue
		}
		// check body for early-return / break / continue
		for _, s := range ifStmt.Body.List {
			switch s.(type) {
			case *ast.ReturnStmt, *ast.BranchStmt:
				return true
			}
		}
	}
	return false
}

// dialectCompare inspects a comparison expression. If it looks like a dialect
// check (one side mentions Dialector/Driver/Dialect etc., other side is a
// string literal), it returns the operator and the two sides. Otherwise
// returns token.ILLEGAL.
func dialectCompare(e ast.Expr) (token.Token, ast.Expr, ast.Expr) {
	bin, ok := e.(*ast.BinaryExpr)
	if !ok {
		return token.ILLEGAL, nil, nil
	}
	if bin.Op != token.EQL && bin.Op != token.NEQ {
		return token.ILLEGAL, nil, nil
	}
	if mentionsDialect(bin.X) || mentionsDialect(bin.Y) {
		return bin.Op, bin.X, bin.Y
	}
	return token.ILLEGAL, nil, nil
}

var dialectMarkers = []string{
	"Dialect", "Dialector", "Driver", "DriverName", "DBType",
}

func mentionsDialect(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			for _, m := range dialectMarkers {
				if x.Name == m {
					found = true
					return false
				}
			}
		case *ast.SelectorExpr:
			for _, m := range dialectMarkers {
				if x.Sel.Name == m {
					found = true
					return false
				}
			}
			if x.Sel.Name == "Name" {
				// likely Dialector.Name()
				if mentionsDialect(x.X) {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

func otherSide(lhs, rhs ast.Expr) string {
	if s := stringValue(lhs); s != "" {
		return s
	}
	return stringValue(rhs)
}

func stringValue(e ast.Expr) string {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	return strings.ToLower(strings.Trim(lit.Value, "\"`"))
}

func containsPos(node ast.Node, pos token.Pos) bool {
	if node == nil {
		return false
	}
	return pos >= node.Pos() && pos <= node.End()
}
