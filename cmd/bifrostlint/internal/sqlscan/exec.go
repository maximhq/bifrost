package sqlscan

import "go/ast"

// dbExecMethods is the set of method names that execute a SQL statement.
// A string literal is considered "executed" if it is passed as an argument
// (directly or after some non-call wrapping) to a call on one of these names.
var dbExecMethods = map[string]bool{
	"Exec":            true,
	"ExecContext":     true,
	"MustExec":        true,
	"Query":           true,
	"QueryContext":    true,
	"QueryRow":        true,
	"QueryRowContext": true,
	"Raw":             true, // GORM
	"Select":          true, // sqlx
	"Get":             true, // sqlx
}

// ExecutingCall walks up stack looking for a *ast.CallExpr whose callee is a
// selector with a known SQL-execution method name. Returns the call node if
// found, or nil. The literal must appear as one of the call's args (directly
// or via a variable bound to the literal - we only handle the direct case).
func ExecutingCall(stack []ast.Node) *ast.CallExpr {
	for i := len(stack) - 1; i >= 0; i-- {
		call, ok := stack[i].(*ast.CallExpr)
		if !ok {
			continue
		}
		if !isDBExecCall(call) {
			continue
		}
		return call
	}
	return nil
}

func isDBExecCall(call *ast.CallExpr) bool {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return dbExecMethods[fn.Sel.Name]
	case *ast.Ident:
		return dbExecMethods[fn.Name]
	}
	return false
}
