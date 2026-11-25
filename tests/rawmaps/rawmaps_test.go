package rawmaps

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// checkSource runs Check over one file's source.
func checkSource(t *testing.T, src string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range Check("p", []*ast.File{file}) {
		out = append(out, f.Kind+" "+f.Name)
	}
	return out
}

// TestCheckReportsEachShape checks every reported shape, and the shapes left alone: a map on a
// struct without a mutex, an atomic.Pointer holding no map, an atomic.Value (its type does not say
// what it holds), a static lookup table, and a package map only init writes. A field that points to
// an atomic.Pointer holding maps is reported as the atomic.Pointer itself is. A package map counts
// as written by an increment or decrement too, by a write through nested indexes or parentheses
// (m[a][b] = v, m[a][b]++, delete(m[a], b), (m)[a] = v, (m[a])++), and by a function literal a
// package variable holds or init stores. A package map built by a function literal called at once
// (var m = func() map[K]V { ... }()) is a map like a literal or a make of one.
// A package variable of an anonymous struct type is checked as a struct, under the variable's name.
func TestCheckReportsEachShape(t *testing.T) {
	got := checkSource(t, `package p
import ("sync"; "sync/atomic")
type snapshot struct{ byID map[string]int }
type plain struct{ n int }
type holder struct {
	mu       sync.RWMutex
	byName   map[string]int
	seen     sync.Map
	current  atomic.Pointer[snapshot]
	raw      atomic.Pointer[map[string]int]
	rawPtr   *atomic.Pointer[map[string]int]
	other    atomic.Pointer[plain]
	otherPtr *atomic.Pointer[plain]
	latest   atomic.Value
}
type unlocked struct{ index map[string]int }
var lookup = map[string]int{"a": 1}
var registry = map[string]int{}
var bootOnly = map[string]int{}
var hits = map[string]int{}
var misses = map[string]int{}
var shared sync.Map
var sharedPtr = new(sync.Map)
var memo = struct {
	sync.Mutex
	data map[string]int
}{}
var counters struct {
	mu sync.Mutex
	m  map[string]int
}
var unguarded = struct{ index map[string]int }{}
var handlers = map[string]int{}
var addHandler = func(k string) { handlers[k] = 1 }
var plugins = map[string]int{}
var addPlugin func(string)
func init() { addPlugin = func(k string) { plugins[k] = 1 } }
func register(k string) { registry[k] = 1 }
func bump() { hits["x"]++ }
func unbump() { misses["x"]-- }
func init() { bootOnly["x"] = 1; bootOnly["y"]++; bootOnly["z"]--; (bootOnly["w"])++ }
var nested = map[string]map[string]int{}
var nestedBumped = map[string]map[string]int{}
var nestedDeleted = map[string]map[string]int{}
var nestedBootOnly = map[string]map[string]int{}
var parened = map[string]int{}
var parenBumped = map[string]int{}
var parenUnbumped = map[string]int{}
func setNested() { nested["a"]["b"] = 1 }
func bumpNested() { nestedBumped["a"]["b"]++ }
func dropNested() { delete(nestedDeleted["a"], "b") }
func setParened() { (parened)["a"] = 1 }
func bumpParened() { (parenBumped["a"])++ }
func unbumpParened() { (parenUnbumped["a"])-- }
func init() { nestedBootOnly["a"]["b"] = 1; nestedBootOnly["a"]["c"]++; delete(nestedBootOnly["a"], "d") }
var built = func() map[string]bool { return map[string]bool{"a": true} }()
var builtUnwritten = func() map[string]bool { return map[string]bool{"a": true} }()
func addBuilt(k string) { built[k] = true }
`)
	want := []string{
		"atomicmaps holder.current",
		"atomicmaps holder.raw",
		"atomicmaps holder.rawPtr",
		"lockedmap counters.m",
		"lockedmap holder.byName",
		"lockedmap memo.data",
		"pkgmap built",
		"pkgmap handlers",
		"pkgmap hits",
		"pkgmap misses",
		"pkgmap nested",
		"pkgmap nestedBumped",
		"pkgmap nestedDeleted",
		"pkgmap parenBumped",
		"pkgmap parenUnbumped",
		"pkgmap parened",
		"pkgmap plugins",
		"pkgmap registry",
		"pkgsyncmap shared",
		"pkgsyncmap sharedPtr",
		"syncmap holder.seen",
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestCheckFollowsPackageMapTypes checks that a map type the package declares by name counts as a
// map in the atomic.Pointer and package-map rules: held in an atomic.Pointer directly, through a
// pointer, or as a struct's field, and as a package variable declared with it, made with it or set
// to a literal of it that a function writes. A field of one beside a mutex is left alone, and so is
// a package variable of one that no function writes.
func TestCheckFollowsPackageMapTypes(t *testing.T) {
	got := checkSource(t, `package p
import ("sync"; "sync/atomic")
type cache map[string]int
type alias = map[string]int
type list []int
type view struct{ byID cache }
type holder struct {
	current atomic.Pointer[cache]
	ptr     atomic.Pointer[*cache]
	aliased atomic.Pointer[alias]
	snap    atomic.Pointer[view]
	items   atomic.Pointer[list]
}
type locked struct {
	mu sync.Mutex
	c  cache
}
var entries cache
var made = make(cache)
var literal = cache{}
var readOnly = cache{"a": 1}
func put() { entries["a"] = 1; made["a"]++; delete(literal, "a") }
`)
	want := []string{
		"atomicmaps holder.aliased",
		"atomicmaps holder.current",
		"atomicmaps holder.ptr",
		"atomicmaps holder.snap",
		"pkgmap entries",
		"pkgmap literal",
		"pkgmap made",
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestCompareNamesUnlistedAndStaleEntries checks the allowlist is exact: a finding it does not
// name and an entry that names nothing are both reported, and an entry needs a group and a reason.
func TestCompareNamesUnlistedAndStaleEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allowlist.txt")
	write := func(lines ...string) {
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	findings := []Finding{{SyncMap, "a", "T.x"}, {LockedMap, "b", "U.y"}}
	write("# comment", "", "syncmap\ta\tT.x\thandle: a connection per upstream", "pkgmap\tc\tgone\tregistry: removed since")
	unlisted, stale, err := Compare(findings, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(unlisted) != 1 || unlisted[0].Name != "U.y" {
		t.Fatalf("unlisted = %v, want U.y", unlisted)
	}
	if len(stale) != 1 || !strings.HasSuffix(stale[0], "gone") {
		t.Fatalf("stale = %v, want the gone entry", stale)
	}
	for _, entry := range []string{"syncmap\ta\tT.x", "syncmap\ta\tT.x\thandle: ", "syncmap\ta\tT.x\t: why", "syncmap\ta\tT.x\t : "} {
		write(entry)
		if _, _, err := Compare(findings, path); err == nil {
			t.Fatalf("an entry without a group and a reason must be refused: %q", entry)
		}
	}
}

// TestRepositoryKeepsDataInTheLiveStore fails on any raw map in this repository the allowlist
// does not name, and on any allowlist entry that no longer names one. A new finding belongs in the
// live store; when it cannot go there, add it to allowlist.txt with its group and reason. It scans
// the repository root, two levels up; tests/ (this module included), examples/ and the git-ignored
// scratch directory are not scanned.
func TestRepositoryKeepsDataInTheLiveStore(t *testing.T) {
	findings, err := Scan(filepath.Join("..", ".."), "tests", "examples", "tmp")
	if err != nil {
		t.Fatal(err)
	}
	unlisted, stale, err := Compare(findings, "allowlist.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range unlisted {
		t.Errorf("raw map outside the live store: %s (move it into livestore, or allowlist it with its reason)", f)
	}
	for _, entry := range stale {
		t.Errorf("allowlist entry names nothing any more, remove it: %s", entry)
	}
}
