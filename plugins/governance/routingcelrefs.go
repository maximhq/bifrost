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
	if parsed := parseCELExpression(expr); parsed != nil {
		result = celExprReferencesIdentifier(parsed.Expr(), identifier, nil)
	}

	celExpressionIdentifierRefCache.Store(cacheKey, result)
	return result
}

// parseCELExpression parses expr with the full macro set. It returns nil when
// the parser cannot be constructed or the expression is malformed.
func parseCELExpression(expr string) *celast.AST {
	p, err := parser.NewParser(parser.Macros(parser.AllMacros...))
	if err != nil {
		return nil
	}
	parsed, errs := p.Parse(common.NewTextSource(expr))
	if errs != nil && len(errs.GetErrors()) > 0 {
		return nil
	}
	return parsed
}

func celASTReferencesIdentifier(ast *cel.Ast, identifier string) bool {
	if ast == nil || ast.NativeRep() == nil || identifier == "" {
		return false
	}
	return celExprReferencesIdentifier(ast.NativeRep().Expr(), identifier, nil)
}

func celExprReferencesIdentifier(expr celast.Expr, identifier string, scopedIdents map[string]int) bool {
	found := false
	walkCELExpr(expr, scopedIdents, func(e celast.Expr, scoped map[string]int) bool {
		if e.Kind() == celast.IdentKind && e.AsIdent() == identifier && scoped[identifier] == 0 {
			found = true
			return false
		}
		return true
	})
	return found
}

// walkCELExpr pre-order walks expr, tracking identifiers shadowed by
// comprehension scopes, and calls visit on every node with the scope active at
// that node. visit returning false stops the walk; walkCELExpr reports whether
// the walk ran to completion.
func walkCELExpr(expr celast.Expr, scopedIdents map[string]int, visit func(celast.Expr, map[string]int) bool) bool {
	if expr == nil {
		return true
	}
	if !visit(expr, scopedIdents) {
		return false
	}

	switch expr.Kind() {
	case celast.CallKind:
		call := expr.AsCall()
		if !walkCELExpr(call.Target(), scopedIdents, visit) {
			return false
		}
		for _, arg := range call.Args() {
			if !walkCELExpr(arg, scopedIdents, visit) {
				return false
			}
		}
	case celast.ComprehensionKind:
		comp := expr.AsComprehension()
		if !walkCELExpr(comp.IterRange(), scopedIdents, visit) {
			return false
		}
		scoped := addScopedCELIdentifiers(scopedIdents, comp.IterVar(), comp.IterVar2(), comp.AccuVar())
		for _, sub := range []celast.Expr{comp.AccuInit(), comp.LoopCondition(), comp.LoopStep(), comp.Result()} {
			if !walkCELExpr(sub, scoped, visit) {
				return false
			}
		}
	case celast.ListKind:
		for _, elem := range expr.AsList().Elements() {
			if !walkCELExpr(elem, scopedIdents, visit) {
				return false
			}
		}
	case celast.MapKind:
		for _, entry := range expr.AsMap().Entries() {
			if entry.Kind() != celast.MapEntryKind {
				continue
			}
			mapEntry := entry.AsMapEntry()
			if !walkCELExpr(mapEntry.Key(), scopedIdents, visit) ||
				!walkCELExpr(mapEntry.Value(), scopedIdents, visit) {
				return false
			}
		}
	case celast.SelectKind:
		return walkCELExpr(expr.AsSelect().Operand(), scopedIdents, visit)
	case celast.StructKind:
		for _, field := range expr.AsStruct().Fields() {
			if field.Kind() != celast.StructFieldKind {
				continue
			}
			if !walkCELExpr(field.AsStructField().Value(), scopedIdents, visit) {
				return false
			}
		}
	}
	return true
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
	visitComplexityTierComparisonLiterals(ast.NativeRep().Expr(), func(value string, _ celast.Expr) {
		if _, ok := validTiers[value]; ok {
			return
		}
		if _, dup := seen[value]; dup {
			return
		}
		seen[value] = struct{}{}
		invalid = append(invalid, value)
	})
	return invalid
}

// visitComplexityTierComparisonLiterals calls collect for every string literal
// the complexity_tier identifier is directly compared against: equality and
// inequality operands, and membership lists (complexity_tier in [...]). The
// literal node is passed alongside its value so callers can map it back to
// source offsets.
func visitComplexityTierComparisonLiterals(root celast.Expr, collect func(value string, literal celast.Expr)) {
	walkCELExpr(root, nil, func(expr celast.Expr, scoped map[string]int) bool {
		if expr.Kind() != celast.CallKind {
			return true
		}
		call := expr.AsCall()
		args := call.Args()
		switch call.FunctionName() {
		case "_==_", "_!=_":
			if len(args) == 2 {
				checkComplexityTierComparisonPair(args[0], args[1], scoped, collect)
				checkComplexityTierComparisonPair(args[1], args[0], scoped, collect)
			}
		case "@in":
			if len(args) == 2 && isComplexityTierIdent(args[0], scoped) && args[1].Kind() == celast.ListKind {
				for _, elem := range args[1].AsList().Elements() {
					if value, ok := stringConstantValue(elem); ok {
						collect(value, elem)
					}
				}
			}
		}
		return true
	})
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

// legacyTierReasoning is the removed complexity tier name: REASONING was
// merged into COMPLEX. Stored and file-authored rules are deliberately not
// migrated, so both the rule write path and the runtime compile path alias the
// removed name instead.
const legacyTierReasoning = "REASONING"

// ReasoningTierDeprecationWarning is the single deprecation notice for routing
// rules whose cel_expression still references the removed REASONING complexity
// tier. Every warn site (runtime compile, rule write API, config.json load)
// reuses it, prefixed with its own rule context, so the wording cannot drift.
const ReasoningTierDeprecationWarning = "cel_expression compares complexity_tier against the removed " + legacyTierReasoning + " tier, which was merged into " + complexity.TierComplex + "; the rule is applied with " + complexity.TierComplex + " instead. Replace " + legacyTierReasoning + " with " + complexity.TierComplex + " — this compatibility alias is deprecated"

// NormalizeDeprecatedComplexityTierLiterals rewrites comparisons of
// complexity_tier against the removed REASONING tier to COMPLEX and reports
// whether the expression changed. Only the string literals the parsed AST
// identifies as complexity_tier comparison values are edited, at their source
// offsets, so an unrelated literal spelling the same name elsewhere in the
// rule is left untouched. Literals whose source text is not the plain quoted
// name (escapes, raw or triple-quoted strings) are skipped; compilation then
// reports them as invalid tier values. Malformed expressions are returned
// unchanged — compilation reports the real error.
func NormalizeDeprecatedComplexityTierLiterals(expr string) (string, bool) {
	if !strings.Contains(expr, `"`+legacyTierReasoning+`"`) &&
		!strings.Contains(expr, `'`+legacyTierReasoning+`'`) {
		return expr, false
	}

	parsed := parseCELExpression(expr)
	if parsed == nil {
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
	visitComplexityTierComparisonLiterals(parsed.Expr(), func(value string, literal celast.Expr) {
		if value != legacyTierReasoning {
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
			edits = append(edits, edit{start: start, end: end, replacement: quote + complexity.TierComplex + quote})
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
