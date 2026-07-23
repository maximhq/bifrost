package governance

import (
	"sort"
	"strings"
	"sync"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/parser"

	"github.com/maximhq/bifrost/plugins/governance/complexity"
)

// Most routing variables are cheap: createCELEnvironment declares them,
// extractRoutingVariables populates them, and evaluateCELExpression passes them
// to CEL for evaluation. complexity_tier is different because populating it
// means extracting text from the request body and running the complexity
// analyzer (we dont have the value yet without these steps). Keep that work lazy by
// first checking whether a CEL rule actually references the identifier.

// Walk the parsed CEL AST instead of using strings.Contains so string literals
// like "complexity_tier" and scoped macro variables do not accidentally trigger
// analysis. The same check is used during program compilation so only
// complexity-aware rules enable partial evaluation for the unavailable/unknown
// complexity_tier path.

var celExpressionIdentifierRefCache sync.Map

func celExpressionReferencesIdentifier(expr string, identifier string) bool {
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

func celASTReferencesIdentifier(ast *cel.Ast, identifier string) bool {
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

// invalidComplexityTierLiterals walks a compiled routing expression and
// collects string literals that the complexity_tier identifier is compared
// against but that are not valid tier values. Rules comparing against a value
// the analyzer never emits (a removed tier like "REASONING", or a case typo
// like "complex") would otherwise compile fine and silently never match.
//
// Only direct comparisons against string constants are checked: equality and
// inequality operands, and membership lists (complexity_tier in [...]).
// Dynamic comparisons (against headers, variables, etc.) are left alone.
func invalidComplexityTierLiterals(ast *cel.Ast, validTiers map[string]struct{}) []string {
	if ast == nil || ast.NativeRep() == nil {
		return nil
	}
	var invalid []string
	seen := map[string]struct{}{}
	collect := func(value string, _ celast.Expr) {
		if _, ok := validTiers[value]; ok {
			return
		}
		if _, dup := seen[value]; dup {
			return
		}
		seen[value] = struct{}{}
		invalid = append(invalid, value)
	}
	walkComplexityTierComparisons(ast.NativeRep().Expr(), nil, collect)
	return invalid
}

func walkComplexityTierComparisons(expr celast.Expr, scopedIdents map[string]int, collect func(string, celast.Expr)) {
	if expr == nil {
		return
	}

	switch expr.Kind() {
	case celast.CallKind:
		call := expr.AsCall()
		args := call.Args()
		switch call.FunctionName() {
		case "_==_", "_!=_":
			if len(args) == 2 {
				checkComplexityTierComparisonPair(args[0], args[1], scopedIdents, collect)
				checkComplexityTierComparisonPair(args[1], args[0], scopedIdents, collect)
			}
		case "@in":
			if len(args) == 2 && isComplexityTierIdent(args[0], scopedIdents) && args[1].Kind() == celast.ListKind {
				for _, elem := range args[1].AsList().Elements() {
					if value, ok := stringConstantValue(elem); ok {
						collect(value, elem)
					}
				}
			}
		}
		walkComplexityTierComparisons(call.Target(), scopedIdents, collect)
		for _, arg := range args {
			walkComplexityTierComparisons(arg, scopedIdents, collect)
		}
	case celast.ComprehensionKind:
		comp := expr.AsComprehension()
		walkComplexityTierComparisons(comp.IterRange(), scopedIdents, collect)
		scoped := addScopedCELIdentifiers(scopedIdents, comp.IterVar(), comp.IterVar2(), comp.AccuVar())
		walkComplexityTierComparisons(comp.AccuInit(), scoped, collect)
		walkComplexityTierComparisons(comp.LoopCondition(), scoped, collect)
		walkComplexityTierComparisons(comp.LoopStep(), scoped, collect)
		walkComplexityTierComparisons(comp.Result(), scoped, collect)
	case celast.ListKind:
		for _, elem := range expr.AsList().Elements() {
			walkComplexityTierComparisons(elem, scopedIdents, collect)
		}
	case celast.MapKind:
		for _, entry := range expr.AsMap().Entries() {
			if entry.Kind() != celast.MapEntryKind {
				continue
			}
			mapEntry := entry.AsMapEntry()
			walkComplexityTierComparisons(mapEntry.Key(), scopedIdents, collect)
			walkComplexityTierComparisons(mapEntry.Value(), scopedIdents, collect)
		}
	case celast.SelectKind:
		walkComplexityTierComparisons(expr.AsSelect().Operand(), scopedIdents, collect)
	case celast.StructKind:
		for _, field := range expr.AsStruct().Fields() {
			if field.Kind() != celast.StructFieldKind {
				continue
			}
			walkComplexityTierComparisons(field.AsStructField().Value(), scopedIdents, collect)
		}
	}
}

func checkComplexityTierComparisonPair(identSide, valueSide celast.Expr, scopedIdents map[string]int, collect func(string, celast.Expr)) {
	if !isComplexityTierIdent(identSide, scopedIdents) {
		return
	}
	if value, ok := stringConstantValue(valueSide); ok {
		collect(value, valueSide)
	}
}

func isComplexityTierIdent(expr celast.Expr, scopedIdents map[string]int) bool {
	return expr != nil &&
		expr.Kind() == celast.IdentKind &&
		expr.AsIdent() == "complexity_tier" &&
		scopedIdents["complexity_tier"] == 0
}

func stringConstantValue(expr celast.Expr) (string, bool) {
	if expr == nil || expr.Kind() != celast.LiteralKind {
		return "", false
	}
	value, ok := expr.AsLiteral().Value().(string)
	return value, ok
}

// legacyComplexityTierAliases maps removed complexity tier names to the tier
// that replaced them. REASONING was merged into COMPLEX; stored and
// file-authored rules are deliberately not migrated, so both the rule write
// path and the runtime compile path alias the removed name instead.
var legacyComplexityTierAliases = map[string]string{
	"REASONING": complexity.TierComplex,
}

// NormalizeDeprecatedComplexityTierLiterals rewrites comparisons of
// complexity_tier against a removed tier name to its replacement tier and
// reports whether the expression changed. Only the string literals the parsed
// AST identifies as complexity_tier comparison values are edited, at their
// source offsets, so an unrelated literal spelling the same name elsewhere in
// the rule is left untouched. Literals whose source text is not the plain
// quoted name (escapes, raw or triple-quoted strings) are skipped; compilation
// then reports them as invalid tier values. Malformed expressions are returned
// unchanged — compilation reports the real error.
func NormalizeDeprecatedComplexityTierLiterals(expr string) (string, bool) {
	containsLegacy := false
	for legacy := range legacyComplexityTierAliases {
		if strings.Contains(expr, `"`+legacy+`"`) || strings.Contains(expr, `'`+legacy+`'`) {
			containsLegacy = true
			break
		}
	}
	if !containsLegacy {
		return expr, false
	}

	p, err := parser.NewParser(parser.Macros(parser.AllMacros...))
	if err != nil {
		return expr, false
	}
	parsed, errs := p.Parse(common.NewTextSource(expr))
	if parsed == nil || (errs != nil && len(errs.GetErrors()) > 0) {
		return expr, false
	}

	// Parser offsets are rune-based, so edits are computed in rune space.
	source := []rune(expr)
	type edit struct {
		start, end  int
		replacement string
	}
	var edits []edit
	edited := map[int64]struct{}{}
	walkComplexityTierComparisons(parsed.Expr(), nil, func(value string, literal celast.Expr) {
		replacement, ok := legacyComplexityTierAliases[value]
		if !ok {
			return
		}
		if _, dup := edited[literal.ID()]; dup {
			return
		}
		offsets, ok := parsed.SourceInfo().GetOffsetRange(literal.ID())
		if !ok {
			return
		}
		start := int(offsets.Start)
		for _, quote := range []string{`"`, `'`} {
			quoted := []rune(quote + value + quote)
			end := start + len(quoted)
			if start < 0 || end > len(source) || string(source[start:end]) != string(quoted) {
				continue
			}
			edited[literal.ID()] = struct{}{}
			edits = append(edits, edit{start: start, end: end, replacement: quote + replacement + quote})
			break
		}
	})
	if len(edits) == 0 {
		return expr, false
	}

	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	for _, e := range edits {
		source = append(source[:e.start], append([]rune(e.replacement), source[e.end:]...)...)
	}
	rewritten := string(source)
	return rewritten, rewritten != expr
}
