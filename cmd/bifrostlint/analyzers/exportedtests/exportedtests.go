// Package exportedtests implements rule 5: every exported func/method/type
// in a package must be referenced by at least one *_test.go file in the same
// package (internal) or by an external test package (foo_test).
//
// Unlike the other analyzers, this one needs cross-file/cross-package
// information about test references, which is awkward inside the per-package
// go/analysis model. It is therefore implemented as a subcommand of
// bifrostlint that loads packages directly via golang.org/x/tools/go/packages.
//
// Suppression options for a single exported symbol:
//   - Inline directive on the decl line: // bifrostlint:ignore exportedtests <reason>
//   - Add the symbol to the baseline file: one "<import-path>.<name>" per line
//
// Generate / regenerate the baseline:
//
//	bifrostlint exportedtests -emit-baseline ./... > cmd/bifrostlint/baseline.txt
package exportedtests

import (
	"bufio"
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

type symbol struct {
	pkg  string
	name string
}

func (s symbol) key() string { return s.pkg + "." + s.name }

// Run executes the exportedtests subcommand. Returns a process exit code.
func Run() int {
	fs := flag.NewFlagSet("exportedtests", flag.ContinueOnError)
	baselinePath := fs.String("baseline", "", "path to baseline file of pre-existing untested exported symbols")
	emitBaseline := fs.Bool("emit-baseline", false, "print baseline entries for all unreferenced exported symbols (no failures)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}
	patterns := fs.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}

	baseline, err := loadBaseline(*baselinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bifrostlint exportedtests: %v\n", err)
		return 2
	}

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedDeps | packages.NeedImports | packages.NeedModule,
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bifrostlint exportedtests: load: %v\n", err)
		return 2
	}
	hasLoadErrors := false
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			fmt.Fprintf(os.Stderr, "bifrostlint exportedtests: %s: %v\n", p.PkgPath, e)
			hasLoadErrors = true
		}
	})
	if hasLoadErrors {
		// Continue - load errors are often noise in multi-module repos
	}

	exported := map[string]token.Pos{}
	exportedFile := map[string]*ast.File{}
	suppressed := map[string]bool{}
	referenced := map[string]bool{}
	var fset *token.FileSet

	for _, p := range pkgs {
		if fset == nil {
			fset = p.Fset
		}
		basePkgPath := strings.TrimSuffix(p.PkgPath, "_test")
		basePkgPath = strings.TrimSuffix(basePkgPath, ".test")

		for i, file := range p.Syntax {
			path := ""
			if i < len(p.CompiledGoFiles) {
				path = p.CompiledGoFiles[i]
			} else {
				path = p.Fset.Position(file.Pos()).Filename
			}
			isTest := strings.HasSuffix(path, "_test.go")
			if !isTest {
				collectExported(file, basePkgPath, exported, exportedFile, suppressed, p.Fset)
			} else {
				collectReferenced(file, p.TypesInfo, basePkgPath, referenced)
			}
		}
	}

	if *emitBaseline {
		w := bufio.NewWriter(os.Stdout)
		var keys []string
		for k := range exported {
			if referenced[k] || suppressed[k] {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintln(w, k)
		}
		w.Flush()
		return 0
	}

	failed := false
	var keys []string
	for k := range exported {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if referenced[k] || suppressed[k] || baseline[k] {
			continue
		}
		pos := fset.Position(exported[k])
		fmt.Fprintf(os.Stderr, "%s:%d:%d: exported %s has no test reference (rule: exportedtests)\n",
			pos.Filename, pos.Line, pos.Column, k)
		failed = true
	}
	if failed {
		return 1
	}
	return 0
}

func collectExported(
	file *ast.File,
	pkgPath string,
	exported map[string]token.Pos,
	exportedFile map[string]*ast.File,
	suppressed map[string]bool,
	fset *token.FileSet,
) {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name == nil || !d.Name.IsExported() {
				continue
			}
			// methods on unexported receivers are still exported on the receiver type;
			// only flag methods whose receiver type is exported
			if d.Recv != nil && len(d.Recv.List) > 0 {
				if !receiverTypeExported(d.Recv.List[0].Type) {
					continue
				}
			}
			name := symbolName(d)
			key := symbol{pkgPath, name}.key()
			exported[key] = d.Name.Pos()
			exportedFile[key] = file
			if hasIgnoreDirective(d.Doc, "exportedtests") {
				suppressed[key] = true
			}
		case *ast.GenDecl:
			if d.Tok != token.TYPE && d.Tok != token.VAR && d.Tok != token.CONST {
				continue
			}
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if !s.Name.IsExported() {
						continue
					}
					key := symbol{pkgPath, s.Name.Name}.key()
					exported[key] = s.Name.Pos()
					exportedFile[key] = file
					if hasIgnoreDirective(d.Doc, "exportedtests") || hasIgnoreDirective(s.Doc, "exportedtests") {
						suppressed[key] = true
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if !n.IsExported() {
							continue
						}
						key := symbol{pkgPath, n.Name}.key()
						exported[key] = n.Pos()
						exportedFile[key] = file
						if hasIgnoreDirective(d.Doc, "exportedtests") || hasIgnoreDirective(s.Doc, "exportedtests") {
							suppressed[key] = true
						}
					}
				}
			}
		}
	}
}

func symbolName(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name
	}
	return receiverTypeName(d.Recv.List[0].Type) + "." + d.Name.Name
}

func receiverTypeName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return receiverTypeName(x.X)
	case *ast.IndexExpr:
		return receiverTypeName(x.X)
	case *ast.IndexListExpr:
		return receiverTypeName(x.X)
	}
	return "?"
}

func receiverTypeExported(e ast.Expr) bool {
	name := receiverTypeName(e)
	if name == "" || name == "?" {
		return false
	}
	first := name[0]
	return first >= 'A' && first <= 'Z'
}

func collectReferenced(
	file *ast.File,
	info *types.Info,
	pkgPath string,
	referenced map[string]bool,
) {
	if file == nil {
		return
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if info == nil {
				return true
			}
			obj := info.Uses[x]
			if obj == nil {
				return true
			}
			if obj.Pkg() == nil {
				return true
			}
			recordRef(obj, referenced)
		case *ast.SelectorExpr:
			if info == nil {
				return true
			}
			if obj := info.Uses[x.Sel]; obj != nil && obj.Pkg() != nil {
				recordRef(obj, referenced)
			}
		}
		return true
	})
	_ = pkgPath
}

func recordRef(obj types.Object, referenced map[string]bool) {
	pkg := obj.Pkg().Path()
	pkg = strings.TrimSuffix(pkg, "_test")
	pkg = strings.TrimSuffix(pkg, ".test")
	name := obj.Name()
	if fn, ok := obj.(*types.Func); ok {
		if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
			recv := sig.Recv().Type()
			if ptr, ok := recv.(*types.Pointer); ok {
				recv = ptr.Elem()
			}
			if named, ok := recv.(*types.Named); ok {
				name = named.Obj().Name() + "." + fn.Name()
			}
		}
	}
	referenced[symbol{pkg, name}.key()] = true
}

func hasIgnoreDirective(cg *ast.CommentGroup, ruleID string) bool {
	if cg == nil {
		return false
	}
	for _, c := range cg.List {
		text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(c.Text), "//"))
		if !strings.HasPrefix(text, "bifrostlint:ignore") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(text, "bifrostlint:ignore"))
		fields := strings.Fields(rest)
		if len(fields) >= 2 && fields[0] == ruleID {
			return true
		}
	}
	return false
}

func loadBaseline(path string) (map[string]bool, error) {
	out := map[string]bool{}
	if path == "" {
		return out, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[line] = true
	}
	return out, sc.Err()
}
