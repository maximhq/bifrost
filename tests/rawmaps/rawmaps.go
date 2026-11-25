// Package rawmaps is the lint rule that keeps a process's data in the live store: it finds data
// held in raw maps outside framework/livestore, and checks what it finds against an allowlist that
// gives each exception its reason. It is a module of its own under tests/, with no dependencies, so
// any module can run the same rule over its own tree.
//
// It reports five shapes: a sync.Map field; a map field on a struct that also holds a mutex; a
// field holding a map, or a struct with map fields, in an atomic.Pointer or a pointer to one; a
// package-level map a function other than init writes; and a package-level sync.Map. The fields of
// a package variable of an anonymous struct type are checked like a named struct's. A map type the
// package declares by name (type cache map[K]V) counts as a map in the atomic.Pointer and
// package-level map rules, not in the mutex rule. Static lookup tables, maps a struct builds and
// never guards, request-scoped values and test files are not reported, and neither is an
// atomic.Value: a syntax check cannot see what one holds.
package rawmaps

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The kinds of finding.
const (
	SyncMap        = "syncmap"    // a sync.Map field
	LockedMap      = "lockedmap"  // a map field on a struct that also holds a mutex
	AtomicMaps     = "atomicmaps" // maps held in an atomic.Pointer
	PackageMap     = "pkgmap"     // a package-level map a function writes
	PackageSyncMap = "pkgsyncmap" // a package-level sync.Map
)

// Finding is one raw map: its kind, the package directory relative to the scanned root, and the
// field (Type.field) or variable.
type Finding struct {
	Kind, Package, Name string
}

// String renders the finding the way an allowlist line starts.
func (f Finding) String() string {
	return f.Kind + "\t" + f.Package + "\t" + f.Name
}

// Scan reports the raw maps in the non-test Go files under root. It skips hidden directories,
// testdata, vendor and node_modules, every directory whose path relative to root starts with one
// of skip, and the live store's own packages (any path segment starting with "livestore").
func Scan(root string, skip ...string) ([]Finding, error) {
	dirs := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			name := d.Name()
			if rel != "." && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor" || name == "node_modules" || strings.HasPrefix(name, "livestore") || skipped(rel, skip)) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			dir := filepath.Dir(path)
			dirs[dir] = append(dirs[dir], path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var out []Finding
	for dir, paths := range dirs {
		fset := token.NewFileSet()
		files := make([]*ast.File, 0, len(paths))
		for _, path := range paths {
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return nil, err
			}
			files = append(files, file)
		}
		rel, _ := filepath.Rel(root, dir)
		out = append(out, Check(filepath.ToSlash(rel), files)...)
	}
	sortFindings(out)
	return out, nil
}

// skipped reports whether rel is one of skip or under one.
func skipped(rel string, skip []string) bool {
	for _, s := range skip {
		if rel == s || strings.HasPrefix(rel, s+"/") {
			return true
		}
	}
	return false
}

// sortFindings orders findings by package, then name, then kind.
func sortFindings(out []Finding) {
	slices.SortFunc(out, func(a, b Finding) int {
		return strings.Compare(a.Package+"\x00"+a.Name+"\x00"+a.Kind, b.Package+"\x00"+b.Name+"\x00"+b.Kind)
	})
}

// Check reports the raw maps in one package's parsed files, pkg naming the package in findings.
func Check(pkg string, files []*ast.File) []Finding {
	structs := map[string]*ast.StructType{}
	mapTypes := map[string]bool{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			if spec, ok := n.(*ast.TypeSpec); ok {
				switch t := spec.Type.(type) {
				case *ast.StructType:
					structs[spec.Name.Name] = t
				case *ast.MapType:
					mapTypes[spec.Name.Name] = true
				}
			}
			return true
		})
	}
	var out []Finding
	for name, st := range structs {
		out = append(out, checkStruct(pkg, name, st, structs, mapTypes)...)
	}
	for name, st := range anonymousStructVars(files) {
		out = append(out, checkStruct(pkg, name, st, structs, mapTypes)...)
	}
	out = append(out, writtenPackageMaps(pkg, files, mapTypes)...)
	out = append(out, packageSyncMaps(pkg, files)...)
	sortFindings(out)
	return out
}

// anonymousStructVars returns the package-level variables of an anonymous struct type, by name:
// declared with one (var v struct{...}) or set to a literal of one, with or without &.
func anonymousStructVars(files []*ast.File) map[string]*ast.StructType {
	out := map[string]*ast.StructType{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				value := spec.(*ast.ValueSpec)
				for i, id := range value.Names {
					if st, ok := value.Type.(*ast.StructType); ok {
						out[id.Name] = st
					} else if i < len(value.Values) {
						if st := structLiteral(value.Values[i]); st != nil {
							out[id.Name] = st
						}
					}
				}
			}
		}
	}
	return out
}

// structLiteral returns the type of an anonymous struct literal, with or without &, and nil for
// any other value.
func structLiteral(expr ast.Expr) *ast.StructType {
	if unary, ok := expr.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		expr = unary.X
	}
	if lit, ok := expr.(*ast.CompositeLit); ok {
		if st, ok := lit.Type.(*ast.StructType); ok {
			return st
		}
	}
	return nil
}

// packageSyncMaps reports the package-level sync.Map variables: one exists only to be written, so
// every one is reported.
func packageSyncMaps(pkg string, files []*ast.File) []Finding {
	var out []Finding
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				value := spec.(*ast.ValueSpec)
				for i, id := range value.Names {
					if value.Type != nil && qualified(value.Type) == "sync.Map" ||
						i < len(value.Values) && newsSyncMap(value.Values[i]) {
						out = append(out, Finding{Kind: PackageSyncMap, Package: pkg, Name: id.Name})
					}
				}
			}
		}
	}
	return out
}

// newsSyncMap reports whether a value is a sync.Map literal or new(sync.Map), with or without &.
func newsSyncMap(expr ast.Expr) bool {
	switch x := expr.(type) {
	case *ast.UnaryExpr:
		return newsSyncMap(x.X)
	case *ast.CompositeLit:
		return qualified(x.Type) == "sync.Map"
	case *ast.CallExpr:
		id, ok := x.Fun.(*ast.Ident)
		return ok && id.Name == "new" && len(x.Args) == 1 && qualified(x.Args[0]) == "sync.Map"
	}
	return false
}

// checkStruct reports a struct's sync.Map fields, its map fields when it holds a mutex, and its
// atomics holding maps. mapTypes names the package's map types.
func checkStruct(pkg, name string, st *ast.StructType, structs map[string]*ast.StructType, mapTypes map[string]bool) []Finding {
	locked := false
	for _, field := range st.Fields.List {
		switch qualified(field.Type) {
		case "sync.Mutex", "sync.RWMutex":
			locked = true
		}
	}
	var out []Finding
	for _, field := range st.Fields.List {
		names := []string{"(embedded)"}
		if len(field.Names) > 0 {
			names = names[:0]
			for _, id := range field.Names {
				names = append(names, id.Name)
			}
		}
		kind := ""
		switch q := qualified(field.Type); {
		case q == "sync.Map":
			kind = SyncMap
		case isMap(field.Type) && locked:
			kind = LockedMap
		case q == "atomic.Pointer" && holdsMaps(field.Type, structs, mapTypes):
			kind = AtomicMaps
		}
		if kind == "" {
			continue
		}
		for _, n := range names {
			out = append(out, Finding{Kind: kind, Package: pkg, Name: name + "." + n})
		}
	}
	return out
}

// holdsMaps reports whether an atomic.Pointer's type argument is a map, a pointer to one, or a
// struct of this package with a map field, where a map may be one of the package's map types,
// named in mapTypes. expr is the field's type: the atomic.Pointer or a pointer to it.
func holdsMaps(expr ast.Expr, structs map[string]*ast.StructType, mapTypes map[string]bool) bool {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	index, ok := expr.(*ast.IndexExpr)
	if !ok {
		return false
	}
	arg := index.Index
	if star, ok := arg.(*ast.StarExpr); ok {
		arg = star.X
	}
	if isMapType(arg, mapTypes) {
		return true
	}
	id, ok := arg.(*ast.Ident)
	if !ok {
		return false
	}
	st, ok := structs[id.Name]
	if !ok {
		return false
	}
	for _, field := range st.Fields.List {
		if isMapType(field.Type, mapTypes) {
			return true
		}
	}
	return false
}

// writtenPackageMaps reports the package-level maps a function other than init assigns, indexes
// into on the left of an assignment or of an increment or decrement, deletes from or clears, at any
// depth of indexing (m[a][b] = v writes m). A function literal counts as a function too, whether a
// package variable's value holds it or init builds it: init's own writes are boot-time setup, but a
// literal it stores can run at any time. A variable declared with, made with or set to a literal
// of one of the package's map types, named in mapTypes, is a map too.
func writtenPackageMaps(pkg string, files []*ast.File, mapTypes map[string]bool) []Finding {
	maps := map[string]bool{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				value := spec.(*ast.ValueSpec)
				for i, id := range value.Names {
					if value.Type != nil && isMapType(value.Type, mapTypes) || i < len(value.Values) && makesMap(value.Values[i], mapTypes) {
						maps[id.Name] = true
					}
				}
			}
		}
	}
	written := map[string]bool{}
	writes := func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range x.Lhs {
				if id, ok := baseOfIndex(lhs).(*ast.Ident); ok && maps[id.Name] {
					written[id.Name] = true
				}
			}
		case *ast.IncDecStmt:
			if _, ok := ast.Unparen(x.X).(*ast.IndexExpr); ok {
				if id, ok := baseOfIndex(x.X).(*ast.Ident); ok && maps[id.Name] {
					written[id.Name] = true
				}
			}
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok && (id.Name == "delete" || id.Name == "clear") && len(x.Args) > 0 {
				if arg, ok := baseOfIndex(x.Args[0]).(*ast.Ident); ok && maps[arg.Name] {
					written[arg.Name] = true
				}
			}
		}
		return true
	}
	for _, file := range files {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Body == nil {
					continue
				}
				if d.Recv == nil && d.Name.Name == "init" {
					inspectFuncLits(d.Body, writes)
					continue
				}
				ast.Inspect(d.Body, writes)
			case *ast.GenDecl:
				if d.Tok == token.VAR {
					inspectFuncLits(d, writes)
				}
			}
		}
	}
	out := make([]Finding, 0, len(written))
	for name := range written {
		out = append(out, Finding{Kind: PackageMap, Package: pkg, Name: name})
	}
	return out
}

// baseOfIndex strips index expressions and parentheses down to the expression they index, so a
// write through m[a][b] or (m)[a] is seen as a write to m.
func baseOfIndex(e ast.Expr) ast.Expr {
	for {
		switch x := e.(type) {
		case *ast.IndexExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		default:
			return e
		}
	}
}

// inspectFuncLits runs inspect over the body of every function literal in node and leaves the rest
// of node alone; inspect also walks any literal a body nests.
func inspectFuncLits(node ast.Node, inspect func(ast.Node) bool) {
	ast.Inspect(node, func(n ast.Node) bool {
		if lit, ok := n.(*ast.FuncLit); ok {
			ast.Inspect(lit.Body, inspect)
			return false
		}
		return true
	})
}

// makesMap reports whether a value is a map literal, a make of a map, or a function literal called
// at once that returns a map, the map type spelled out or one of the package's, named in mapTypes.
func makesMap(expr ast.Expr, mapTypes map[string]bool) bool {
	switch x := expr.(type) {
	case *ast.CompositeLit:
		return isMapType(x.Type, mapTypes)
	case *ast.CallExpr:
		if lit, ok := x.Fun.(*ast.FuncLit); ok {
			results := lit.Type.Results
			return results != nil && len(results.List) == 1 && len(results.List[0].Names) <= 1 && isMapType(results.List[0].Type, mapTypes)
		}
		id, ok := x.Fun.(*ast.Ident)
		return ok && id.Name == "make" && len(x.Args) > 0 && isMapType(x.Args[0], mapTypes)
	}
	return false
}

// isMap reports whether expr is a map type spelled out. The mutex rule uses it alone, so a field
// of a map type the package names is not a locked map.
func isMap(expr ast.Expr) bool {
	_, ok := expr.(*ast.MapType)
	return ok
}

// isMapType reports whether expr is a map type spelled out or the name of one the package declares,
// as mapTypes lists them.
func isMapType(expr ast.Expr, mapTypes map[string]bool) bool {
	if id, ok := expr.(*ast.Ident); ok {
		return mapTypes[id.Name]
	}
	return isMap(expr)
}

// qualified names a field's type as package.Name when it is a selector, generic or not, and
// through one pointer; "" otherwise.
func qualified(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.SelectorExpr:
		if pkg, ok := x.X.(*ast.Ident); ok {
			return pkg.Name + "." + x.Sel.Name
		}
	case *ast.IndexExpr:
		return qualified(x.X)
	case *ast.IndexListExpr:
		return qualified(x.X)
	case *ast.StarExpr:
		return qualified(x.X)
	}
	return ""
}

// Compare checks findings against the allowlist file at path: one entry per line, the finding's
// three tab-separated fields and then its reason ("group: why"); blank lines and lines starting
// with # are skipped. It returns the findings the allowlist does not name, and the entries that
// name nothing found any more, so the list stays exact. An entry without a group and a reason is an
// error.
func Compare(findings []Finding, path string) (unlisted []Finding, stale []string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	allowed := map[string]bool{}
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		if strings.TrimSpace(text) == "" || strings.HasPrefix(text, "#") {
			continue
		}
		parts := strings.SplitN(text, "\t", 4)
		if len(parts) < 4 || !hasReason(parts[3]) {
			return nil, nil, fmt.Errorf("%s:%d: want kind, package, name and \"group: reason\", tab-separated", path, line)
		}
		allowed[strings.Join(parts[:3], "\t")] = true
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	found := map[string]bool{}
	for _, f := range findings {
		found[f.String()] = true
		if !allowed[f.String()] {
			unlisted = append(unlisted, f)
		}
	}
	for entry := range allowed {
		if !found[entry] {
			stale = append(stale, entry)
		}
	}
	slices.Sort(stale)
	return unlisted, stale, nil
}

// hasReason reports whether an allowlist entry's last field is "group: why" with neither part blank.
func hasReason(field string) bool {
	group, why, ok := strings.Cut(field, ": ")
	return ok && strings.TrimSpace(group) != "" && strings.TrimSpace(why) != ""
}
