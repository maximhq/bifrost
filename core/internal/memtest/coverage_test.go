package memtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// rawJSONMutators are the helpers that reserialise an entire JSON document on
// every call (they wrap sjson). Called once, that is fine. Called in a loop over
// something that scales with the request payload, it is O(elements x document),
// which is the shape that made StripEmptyThinkingBlocks 23.6% of all allocation
// in production.
var rawJSONMutators = map[string]bool{
	"DeleteJSONField": true,
	"SetJSONField":    true,
	"SetRawJSONField": true,
}

// isRawJSONMutation reports whether a call reserialises a whole JSON document.
//
// Two forms count. The providerUtils helpers above, and raw sjson.Set*/Delete*
// calls, which bypass those helpers entirely: core/encryptedreasoning.go does
// exactly that inside a nested loop, and an earlier version of this scan could
// not see it.
func isRawJSONMutation(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		if ident, isIdent := call.Fun.(*ast.Ident); isIdent {
			return rawJSONMutators[ident.Name]
		}
		return false
	}
	if rawJSONMutators[sel.Sel.Name] {
		return true
	}
	pkg, isIdent := sel.X.(*ast.Ident)
	if !isIdent || pkg.Name != "sjson" {
		return false
	}
	return strings.HasPrefix(sel.Sel.Name, "Set") || strings.HasPrefix(sel.Sel.Name, "Delete")
}

// scanRoots are the trees walked for the shape. Every layer on the request path
// qualifies: the watcher leak that pinned ~80% of a production heap lived in
// transports, not in a provider.
var scanRoots = []string{
	filepath.Join("..", ".."), // core
	filepath.Join("..", "..", "..", "framework"),
	filepath.Join("..", "..", "..", "transports"),
	filepath.Join("..", "..", "..", "plugins"),
}

type siteStatus int

const (
	// covered: a memtest asserts this function's allocation scaling.
	covered siteStatus = iota
	// bounded: the loop is over a fixed or config-sized collection, so N does
	// not grow with the request payload and the rewrite cost stays constant.
	bounded
	// elementSized: the loop writes element-sized documents rather than
	// rewriting the whole request body once per element, so it is already linear.
	elementSized
)

type siteReview struct {
	status siteStatus
	// test names the Test function asserting this site's allocation scaling.
	// Required when status is covered, and checked to actually exist, so the
	// label cannot drift into a claim nobody is backing.
	test string
	// reason justifies a bounded or elementSized classification.
	reason string
}

// reviewedSites records every raw-JSON mutation sitting inside a loop, and what
// was concluded about it. A site missing from this map fails the test below.
//
// The point is to invert the default: a new provider, a new loop, or a deleted
// memtest all show up as a failure rather than as silence. The scan cannot tell
// a payload-scaling loop from a bounded one, and it cannot tell a whole-document
// rewrite from an element-sized one, so that judgement is recorded here by a
// human once and re-checked automatically forever after.
var reviewedSites = map[string]siteReview{
	// A function that drops OFF this list has graduated: its loop no longer mutates
	// raw JSON at all, because the edits were batched into a single write. Its
	// AssertAllocScaling test stays behind as the regression guard.
	// StripAutoInjectableTools and gemini.stripFunctionResponseMediaRefs both left
	// this way.
	//
	// --- payload-scaling: N grows with the request, so these need assertions ---
	"anthropic.StripEmptyThinkingBlocks": {
		status: covered,
		test:   "TestStripEmptyThinkingBlocks_AllocationScaling",
	},
	"anthropic.RemapRawToolVersionsForProvider": {
		status: covered,
		test:   "TestRemapRawToolVersionsForProvider_AllocationScaling",
	},
	"anthropic.StripUnsupportedFieldsFromRawBody": {
		status: covered,
		test:   "TestStripUnsupportedFieldsFromRawBody_AllocationScaling",
	},
	"anthropic.normalizeBase64TextSources": {
		status: covered,
		test:   "TestNormalizeBase64TextSources_AllocationScaling",
	},
	"core.stripRawAnthropicChatThinking": {
		status: covered,
		test:   "TestStripRawAnthropicChatThinking_AllocationScaling",
	},
	"core.stripRawResponsesEncryptedContent": {
		status: covered,
		test:   "TestStripRawResponsesEncryptedContent_AllocationScaling",
	},
	"databricks.normalizeReasoningBlocks": {
		status: covered,
		test:   "TestNormalizeReasoningBlocks_AllocationScaling",
	},

	// --- bounded: the loop is over config or a fixed literal, so N is constant ---
	"schemas.RawSchemaJSON": {
		status: bounded,
		reason: "loops over the 3-element literal {name, strict, schema}",
	},
	"integrations.createAnthropicMessagesRouteConfig": {
		status: bounded,
		reason: "the sjson call sits in a ResponsesResponseConverter closure defined inside a " +
			"2-element literal path loop; it runs once per response, not once per iteration",
	},
	"anthropic.NormalizeSchemaForAnthropicRaw": {
		status: elementSized,
		reason: "writes accumulate into small local values (a single schema branch, an enum array), " +
			"never the request body; the quadratic term is in enum cardinality, which is schema-bounded",
	},
	"anthropic.BuildAnthropicChatRequestBody": {
		status: bounded,
		reason: "loops over cfg.ExcludeFields, which is provider config and does not grow with the request payload",
	},
	"anthropic.BuildAnthropicResponsesRequestBody": {
		status: bounded,
		reason: "loops over cfg.ExcludeFields, which is provider config and does not grow with the request payload",
	},
	"anthropic.AddMissingBetaHeadersToContextFromRawBody": {
		status: bounded,
		reason: "loops over the 2-element literal {messages, tools} it trims before the probe decode",
	},
	"gemini.NormalizeRawGenerateContentRequestForCompatibility": {
		status: bounded,
		reason: "loops over a 5-element literal slice of generationConfig paths",
	},
	"gemini.wrapGeminiCountTokensBody": {
		status: bounded,
		reason: "loops over the fixed geminiCountTokensUnsupportedFields package var",
	},
	"vertex.stripVertexCountTokensUnsupportedFields": {
		status: bounded,
		reason: "loops over the fixed vertexCountTokensUnsupportedFields package var",
	},
}

type foundSite struct {
	key      string
	file     string
	line     int
	function string
}

// scanLoopBoundJSONMutations walks core/providers and reports every raw-JSON
// mutation helper called from inside a for/range statement.
func scanLoopBoundJSONMutations(t *testing.T) []foundSite {
	t.Helper()

	var sites []foundSite
	fset := token.NewFileSet()
	scanned := 0

	for _, root := range scanRoots {
		abs, err := filepath.Abs(root)
		if err != nil {
			t.Fatalf("resolving %s: %v", root, err)
		}
		if _, statErr := os.Stat(abs); statErr != nil {
			// A module may be absent when core is consumed standalone. Skip it
			// rather than fail: the roots that do exist still get scanned.
			continue
		}
		repoRel := filepath.Dir(abs)

		walkErr := filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// Vendored and generated trees are not ours to classify.
				if name := d.Name(); name == "vendor" || name == "node_modules" || name == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return fmt.Errorf("parsing %s: %w", path, parseErr)
			}
			scanned++

			rel, relErr := filepath.Rel(repoRel, path)
			if relErr != nil {
				rel = path
			}
			pkg := filepath.Base(filepath.Dir(path))

			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					var body *ast.BlockStmt
					switch loop := n.(type) {
					case *ast.RangeStmt:
						body = loop.Body
					case *ast.ForStmt:
						body = loop.Body
					default:
						return true
					}
					ast.Inspect(body, func(inner ast.Node) bool {
						call, isCall := inner.(*ast.CallExpr)
						if !isCall || !isRawJSONMutation(call) {
							return true
						}
						sites = append(sites, foundSite{
							key:      pkg + "." + fn.Name.Name,
							file:     filepath.ToSlash(rel),
							line:     fset.Position(call.Pos()).Line,
							function: fn.Name.Name,
						})
						return true
					})
					return true
				})
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("scanning %s: %v", root, walkErr)
		}
	}

	if scanned == 0 {
		t.Fatal("scan parsed no Go files at all; the scanRoots paths have moved")
	}
	return sites
}

// TestEveryLoopBoundJSONMutationIsReviewed fails when a provider gains a
// raw-JSON mutation inside a loop that nobody has classified.
//
// This is the part that makes the memtest suite survive a provider added six
// months from now. The per-provider allocation tests only cover what someone
// remembered to register; this test makes forgetting loud.
func TestEveryLoopBoundJSONMutationIsReviewed(t *testing.T) {
	sites := scanLoopBoundJSONMutations(t)
	if len(sites) == 0 {
		t.Fatal("scan found no loop-bound raw-JSON mutations at all, which means the scan itself " +
			"has broken (the helpers were renamed, or the providers path moved)")
	}

	byKey := map[string][]foundSite{}
	for _, s := range sites {
		byKey[s.key] = append(byKey[s.key], s)
	}

	var unreviewed []string
	for key, group := range byKey {
		if _, ok := reviewedSites[key]; ok {
			continue
		}
		locs := make([]string, 0, len(group))
		for _, s := range group {
			locs = append(locs, fmt.Sprintf("%s:%d", s.file, s.line))
		}
		sort.Strings(locs)
		unreviewed = append(unreviewed, fmt.Sprintf("  %q: {} // %s", key, strings.Join(locs, ", ")))
	}
	sort.Strings(unreviewed)

	if len(unreviewed) > 0 {
		t.Errorf("%d loop-bound raw-JSON mutation site(s) are not classified in reviewedSites.\n\n"+
			"Each rewrites the whole document per iteration. Decide which it is and record it:\n"+
			"  - loop scales with the request payload  -> add an AssertAllocScaling test, mark {status: covered}\n"+
			"  - loop is over config or a fixed list    -> mark {status: bounded, reason: \"...\"}\n"+
			"  - each write is element-sized, not whole-document -> mark {status: elementSized, reason: \"...\"}\n\n"+
			"Add to reviewedSites in coverage_test.go:\n%s",
			len(unreviewed), strings.Join(unreviewed, "\n"))
	}

	// A stale entry is as misleading as a missing one: it implies coverage that
	// no longer corresponds to any code.
	var stale []string
	for key := range reviewedSites {
		if _, ok := byKey[key]; !ok {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("reviewedSites lists %d entr(ies) with no matching code; the function was renamed "+
			"or the loop removed, so drop them: %s", len(stale), strings.Join(stale, ", "))
	}
}

// TestCoveredSitesHaveARealTest keeps `covered` honest.
//
// Without this, marking a site covered is a claim nobody checks, and the whole
// registry degrades into a list of good intentions. Here a covered entry must
// name a Test function that actually exists in that provider's package.
func TestCoveredSitesHaveARealTest(t *testing.T) {
	// package name -> set of Test function names declared in its _test.go files.
	// Walks the same roots as the site scanner: a covered entry in core/ or
	// transports/ must be able to find its test just as a provider one does.
	testsByProvider := map[string]map[string]bool{}
	fset := token.NewFileSet()
	for _, root := range scanRoots {
		abs, err := filepath.Abs(root)
		if err != nil {
			t.Fatalf("resolving %s: %v", root, err)
		}
		if _, statErr := os.Stat(abs); statErr != nil {
			continue
		}
		walkErr := filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if name := d.Name(); name == "vendor" || name == "node_modules" || name == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return fmt.Errorf("parsing %s: %w", path, parseErr)
			}
			pkg := filepath.Base(filepath.Dir(path))
			if testsByProvider[pkg] == nil {
				testsByProvider[pkg] = map[string]bool{}
			}
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && strings.HasPrefix(fn.Name.Name, "Test") {
					testsByProvider[pkg][fn.Name.Name] = true
				}
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("scanning tests under %s: %v", root, walkErr)
		}
	}

	var problems []string
	for key, review := range reviewedSites {
		provider := key[:strings.Index(key, ".")]
		switch review.status {
		case covered:
			if review.test == "" {
				problems = append(problems, fmt.Sprintf("%s is marked covered but names no test", key))
				continue
			}
			if !testsByProvider[provider][review.test] {
				problems = append(problems, fmt.Sprintf(
					"%s is marked covered by %s, but no such Test function exists in package %s",
					key, review.test, provider))
			}
		case bounded, elementSized:
			if review.reason == "" {
				problems = append(problems, fmt.Sprintf(
					"%s is marked bounded/elementSized but gives no reason; the next reader needs to know why", key))
			}
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}
