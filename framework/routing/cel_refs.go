package routing

import (
	"sync"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/parser"
)

var celExpressionIdentifierRefCache sync.Map

// CELExpressionReferencesIdentifier reports whether expr reads the given CEL identifier.
// It ignores string literals, field names, and comprehension-local variables that shadow
// the identifier name.
func CELExpressionReferencesIdentifier(expr string, identifier string) bool {
	if expr == "" || identifier == "" {
		return false
	}

	cacheKey := identifier + "\x00" + expr
	if cached, ok := celExpressionIdentifierRefCache.Load(cacheKey); ok {
		if result, ok := cached.(bool); ok {
			return result
		}
	}

	result := false
	p, err := parser.NewParser(parser.Macros(parser.AllMacros...))
	if err != nil {
		celExpressionIdentifierRefCache.Store(cacheKey, result)
		return result
	}

	parsed, errs := p.Parse(common.NewTextSource(expr))
	if errs != nil && len(errs.GetErrors()) > 0 {
		celExpressionIdentifierRefCache.Store(cacheKey, result)
		return result
	}
	if parsed != nil {
		result = celExprReferencesIdentifier(parsed.Expr(), identifier, nil)
	}

	celExpressionIdentifierRefCache.Store(cacheKey, result)
	return result
}

// CELAstReferencesIdentifier reports whether the compiled CEL AST reads the given identifier.
func CELAstReferencesIdentifier(ast *cel.Ast, identifier string) bool {
	if ast == nil || ast.NativeRep() == nil || identifier == "" {
		return false
	}
	return celExprReferencesIdentifier(ast.NativeRep().Expr(), identifier, nil)
}

func celExprReferencesIdentifier(expr celast.Expr, identifier string, scopedIdents map[string]int) bool {
	if expr == nil {
		return false
	}

	switch expr.Kind() {
	case celast.IdentKind:
		return expr.AsIdent() == identifier && scopedIdents[identifier] == 0
	case celast.CallKind:
		call := expr.AsCall()
		if celExprReferencesIdentifier(call.Target(), identifier, scopedIdents) {
			return true
		}
		for _, arg := range call.Args() {
			if celExprReferencesIdentifier(arg, identifier, scopedIdents) {
				return true
			}
		}
	case celast.ComprehensionKind:
		comp := expr.AsComprehension()
		if celExprReferencesIdentifier(comp.IterRange(), identifier, scopedIdents) {
			return true
		}

		scoped := addScopedCELIdentifiers(scopedIdents, comp.IterVar(), comp.IterVar2(), comp.AccuVar())
		if celExprReferencesIdentifier(comp.AccuInit(), identifier, scoped) {
			return true
		}
		if celExprReferencesIdentifier(comp.LoopCondition(), identifier, scoped) {
			return true
		}
		if celExprReferencesIdentifier(comp.LoopStep(), identifier, scoped) {
			return true
		}
		if celExprReferencesIdentifier(comp.Result(), identifier, scoped) {
			return true
		}
	case celast.ListKind:
		for _, elem := range expr.AsList().Elements() {
			if celExprReferencesIdentifier(elem, identifier, scopedIdents) {
				return true
			}
		}
	case celast.MapKind:
		for _, entry := range expr.AsMap().Entries() {
			if entry.Kind() != celast.MapEntryKind {
				continue
			}
			mapEntry := entry.AsMapEntry()
			if celExprReferencesIdentifier(mapEntry.Key(), identifier, scopedIdents) ||
				celExprReferencesIdentifier(mapEntry.Value(), identifier, scopedIdents) {
				return true
			}
		}
	case celast.SelectKind:
		return celExprReferencesIdentifier(expr.AsSelect().Operand(), identifier, scopedIdents)
	case celast.StructKind:
		for _, field := range expr.AsStruct().Fields() {
			if field.Kind() != celast.StructFieldKind {
				continue
			}
			if celExprReferencesIdentifier(field.AsStructField().Value(), identifier, scopedIdents) {
				return true
			}
		}
	}

	return false
}

func addScopedCELIdentifiers(parent map[string]int, identifiers ...string) map[string]int {
	scoped := make(map[string]int, len(parent)+len(identifiers))
	for identifier, count := range parent {
		scoped[identifier] = count
	}
	for _, identifier := range identifiers {
		if identifier != "" {
			scoped[identifier]++
		}
	}
	return scoped
}
