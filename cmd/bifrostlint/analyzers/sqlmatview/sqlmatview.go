// Package sqlmatview implements rule 2: CREATE/REFRESH MATERIALIZED VIEW must
// not run on the boot path.
//
// The analyzer flags any string literal that looks like a materialized-view
// DDL/refresh unless the enclosing function is one of:
//   - annotated with the // bifrostlint:background doc-comment
//   - named with a known background prefix (periodicallyRefresh*, ensurePostStartup*, runInBackground*, backgroundRefresh*)
//   - the literal is reached inside a `go func() { ... }()` block
//
// Per-line suppression: // bifrostlint:ignore sqlmatview <reason>
package sqlmatview

import (
	"go/ast"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/maximhq/bifrost/cmd/bifrostlint/internal/ignore"
	"github.com/maximhq/bifrost/cmd/bifrostlint/internal/sqlscan"
)

const ruleID = "sqlmatview"

var Analyzer = &analysis.Analyzer{
	Name: ruleID,
	Doc:  "flags CREATE/REFRESH MATERIALIZED VIEW on the boot path; require an explicit background context",
	Run:  run,
}

var backgroundPrefixes = []string{
	"periodicallyRefresh",
	"ensurePostStartup",
	"runInBackground",
	"backgroundRefresh",
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
	if val == "" || !sqlscan.IsMaterializedViewOp(val) {
		return
	}
	// Only flag when the literal is an argument to a SQL execution call.
	// Bare constants, return values from DDL-building helpers, and other
	// non-executing references are harmless.
	if sqlscan.ExecutingCall(v.stack) == nil {
		return
	}
	if v.inBackgroundContext() {
		return
	}
	if ignore.HasDirective(v.pass.Fset, v.file, lit.Pos(), ruleID) {
		return
	}
	v.pass.Reportf(lit.Pos(),
		"materialized view operation on potentially blocking path - run in a goroutine or annotate the enclosing function with // bifrostlint:background")
}

func (v *visitor) inBackgroundContext() bool {
	for i := len(v.stack) - 1; i >= 0; i-- {
		switch node := v.stack[i].(type) {
		case *ast.GoStmt:
			return true
		case *ast.FuncDecl:
			if hasBackgroundName(node.Name.Name) {
				return true
			}
			if node.Doc != nil {
				for _, c := range node.Doc.List {
					if strings.Contains(c.Text, "bifrostlint:background") {
						return true
					}
				}
			}
		}
	}
	return false
}

func hasBackgroundName(name string) bool {
	for _, p := range backgroundPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
