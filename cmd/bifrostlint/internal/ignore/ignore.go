// Package ignore parses // bifrostlint:ignore <rule-id> <reason> directives.
//
// A directive on the same line as the offending node (trailing comment) or on
// the doc-comment of the enclosing declaration suppresses the named rule for
// that node. A non-empty reason is required.
package ignore

import (
	"go/ast"
	"go/token"
	"strings"
)

const directivePrefix = "bifrostlint:ignore"

// HasDirective reports whether any comment in cmap suppresses ruleID for the
// node at pos. It checks trailing comments on the same line and doc-comments
// on enclosing declarations passed via decls.
func HasDirective(fset *token.FileSet, file *ast.File, pos token.Pos, ruleID string) bool {
	if file == nil {
		return false
	}
	line := fset.Position(pos).Line
	for _, cg := range file.Comments {
		cgLine := fset.Position(cg.End()).Line
		// trailing comment on the same line
		if cgLine == line {
			if matches(cg, ruleID) {
				return true
			}
		}
		// doc comment one or more lines above the node
		startLine := fset.Position(cg.Pos()).Line
		if startLine < line && cgLine == line-1 {
			if matches(cg, ruleID) {
				return true
			}
		}
	}
	return false
}

// HasFileDirective reports whether the file-level package doc contains a
// directive suppressing ruleID. Use this for file-scoped suppression.
func HasFileDirective(file *ast.File, ruleID string) bool {
	if file == nil || file.Doc == nil {
		return false
	}
	return matches(file.Doc, ruleID)
}

func matches(cg *ast.CommentGroup, ruleID string) bool {
	for _, c := range cg.List {
		text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(c.Text), "//"))
		text = strings.TrimSpace(strings.TrimPrefix(text, "/*"))
		text = strings.TrimSpace(strings.TrimSuffix(text, "*/"))
		if !strings.HasPrefix(text, directivePrefix) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(text, directivePrefix))
		fields := strings.Fields(rest)
		if len(fields) < 2 {
			// reason missing - treat as not a valid suppression
			continue
		}
		if fields[0] != ruleID {
			continue
		}
		return true
	}
	return false
}
