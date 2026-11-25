package relay

import (
	"slices"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// anyCharacter is how many characters a ? or a negated class can stand for, for ranking only.
const anyCharacter = 0x110000

// RouteMatchOrder is the order the gateway tries these routes in, as compileRoutes ranks them.
func RouteMatchOrder(routes []schemas.RelayRouteConfig) []int {
	return routeMatchOrder(routes)
}

// routeMatchOrder returns route indices in the order requests try them. Specific paths come first
// and wildcards last, and equal ranks keep their saved order. Routes with one method and path then
// reorder among their own positions: a route proven narrower goes first, an exact header value goes
// before a wildcard that can also take its requests, and nothing else moves. testdata/routeorder.json
// holds its cases.
func routeMatchOrder(routes []schemas.RelayRouteConfig) []int {
	order := make([]int, len(routes))
	for i := range routes {
		order[i] = i
	}
	sort.SliceStable(order, func(x, y int) bool {
		return compareRoutePaths(routes[order[x]].Path, routes[order[y]].Path) < 0
	})
	slots := make(map[string][]int, len(routes))
	for at, i := range order {
		key := strings.ToUpper(routes[i].Method) + " " + routes[i].Path
		slots[key] = append(slots[key], at)
	}
	for _, positions := range slots {
		if len(positions) < 2 {
			continue
		}
		members := make([]int, len(positions))
		for k, at := range positions {
			members[k] = order[at]
		}
		for k, i := range orderOnePath(routes, members) {
			order[positions[k]] = i
		}
	}
	return order
}

// orderOnePath keeps the saved order and moves a route only ahead of one it must beat: the routes
// that must go before a route are placed just before it. Routes in a preference cycle, possible
// across different headers, stay together in their saved order. Building the preference matrix is
// quadratic in the routes sharing one method and path; grouping and placement add no more.
func orderOnePath(routes []schemas.RelayRouteConfig, members []int) []int {
	n := len(members)
	headers := make([]map[string]string, n)
	for k, i := range members {
		headers[k] = make(map[string]string, len(routes[i].Headers))
		for name, value := range routes[i].Headers {
			headers[k][strings.ToLower(name)] = value
		}
	}
	// after[a] lists the routes a must go before.
	after := make([][]int, n)
	for a := range n {
		for b := range n {
			if a != b && headersGoBefore(headers[a], headers[b]) {
				after[a] = append(after[a], b)
			}
		}
	}
	group := cycleGroups(after)
	// For each group, its members and the routes that must go before one of them, in saved order.
	inGroup := make([][]int, n)
	before := make([][]int, n)
	for k := range n {
		inGroup[group[k]] = append(inGroup[group[k]], k)
	}
	for p := range n {
		for _, j := range after[p] {
			if g := group[j]; g != group[p] && (len(before[g]) == 0 || before[g][len(before[g])-1] != p) {
				before[g] = append(before[g], p)
			}
		}
	}
	placed := make([]bool, n)
	out := make([]int, 0, n)
	var place func(g int)
	place = func(g int) {
		for _, j := range inGroup[g] {
			placed[j] = true
		}
		for _, p := range before[g] {
			if !placed[p] {
				place(group[p])
			}
		}
		for _, j := range inGroup[g] {
			out = append(out, members[j])
		}
	}
	for k := range n {
		if !placed[k] {
			place(group[k])
		}
	}
	return out
}

// cycleGroups labels each route with the first route of the preference cycle it belongs to, or
// itself, using Tarjan's strongly connected components: linear in the preference edges.
func cycleGroups(after [][]int) []int {
	n := len(after)
	index := make([]int, n)
	low := make([]int, n)
	onStack := make([]bool, n)
	for k := range index {
		index[k] = -1
	}
	group := make([]int, n)
	stack := make([]int, 0, n)
	next := 0
	var visit func(v int)
	visit = func(v int) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range after[v] {
			if index[w] < 0 {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] != index[v] {
			return
		}
		first, size := n, 0
		for i := len(stack) - 1; ; i-- {
			first = min(first, stack[i])
			size++
			if stack[i] == v {
				break
			}
		}
		for range size {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[w] = false
			group[w] = first
		}
	}
	for v := range n {
		if index[v] < 0 {
			visit(v)
		}
	}
	return group
}

func headersGoBefore(a, b map[string]string) bool {
	return headersNarrower(a, b) || (exactOverlapsWildcard(a, b) && !headersNarrower(b, a))
}

// headersNarrower reports whether every request a matches provably also fits b, and a is stricter.
func headersNarrower(a, b map[string]string) bool {
	if len(a) < len(b) {
		return false
	}
	strict := len(a) > len(b)
	for name, value := range b {
		own, ok := a[name]
		if !ok {
			return false
		}
		// a* and a** match the same values, so neither is narrower.
		narrow, broad := collapseStars(own), collapseStars(value)
		if narrow == broad {
			continue
		}
		if !headerValueNarrower(narrow, broad) {
			return false
		}
		strict = true
	}
	return strict
}

// collapseStars turns a run of * into one, leaving escapes and character classes as written.
func collapseStars(pattern string) string {
	if !strings.Contains(pattern, "**") {
		return pattern
	}
	var out strings.Builder
	star := false
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		if c == '*' {
			if !star {
				out.WriteByte(c)
			}
			star = true
			continue
		}
		star = false
		out.WriteByte(c)
		switch c {
		case '\\':
			if i+1 < len(pattern) {
				i++
				out.WriteByte(pattern[i])
			}
		case '[':
			for i++; i < len(pattern); i++ {
				out.WriteByte(pattern[i])
				if pattern[i] == '\\' && i+1 < len(pattern) {
					i++
					out.WriteByte(pattern[i])
				} else if pattern[i] == ']' {
					break
				}
			}
		}
	}
	return out.String()
}

// exactOverlapsWildcard reports an a that is exact on every header it shares with b, with at least
// one of those values under a wildcard of b and every other shared header accepting a's value too.
// Headers only one of them matches on do not decide which is more exact, wildcards or not.
func exactOverlapsWildcard(a, b map[string]string) bool {
	underWildcard := false
	for name, value := range a {
		pattern, ok := b[name]
		if !ok {
			continue
		}
		if isHeaderGlob(value) || !headerGlobMatches(pattern, value) {
			return false
		}
		underWildcard = underWildcard || isHeaderGlob(pattern)
	}
	return underWildcard
}

// headerValueNarrower is true only when containment is proven: under *, an exact value under a
// glob it matches, or a pattern whose fixed start extends the fixed text of a trailing-* glob.
func headerValueNarrower(narrow, broad string) bool {
	if !isHeaderGlob(broad) {
		return false
	}
	if broad == "*" {
		return true
	}
	if !isHeaderGlob(narrow) {
		return headerGlobMatches(broad, narrow)
	}
	prefix, trailing := strings.CutSuffix(broad, "*")
	if !trailing || isHeaderGlob(prefix) {
		return false
	}
	return strings.HasPrefix(narrow[:strings.IndexAny(narrow, "*?[\\")], prefix)
}

// compareRoutePaths ranks paths: literal, then globs, then a middle **, then a trailing ** deepest
// first. Equal ranks are left to the saved order.
func compareRoutePaths(a, b string) int {
	return slices.Compare(routePathRank(a), routePathRank(b))
}

func routePathRank(path string) []int {
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	catchAll := segments[len(segments)-1] == "**"
	gap := !catchAll && slices.Contains(segments, "**")
	fixed := slices.DeleteFunc(slices.Clone(segments), func(s string) bool { return s == "**" })
	glob := slices.ContainsFunc(fixed, isRouteGlob)
	literal, positions, stars, singles := routeGlobShape(strings.Join(fixed, "/"))
	rank := make([]int, 7)
	switch {
	case catchAll:
		rank[0] = 2
	case gap:
		rank[0] = 1
	}
	if catchAll || gap {
		rank[1] = -len(fixed)
	}
	if glob {
		rank[2], rank[3], rank[4], rank[5], rank[6] = 1, len(fixed), -(literal + positions), stars, singles
	}
	return rank
}

func isRouteGlob(s string) bool { return strings.ContainsAny(s, "*?[") }

// routeGlobShape counts a path.Match pattern's literal characters, its single-character positions
// (each ? or class), its * wildcards and the breadth of those positions: a class counts the
// characters it matches, and a ? all of them. A position is fixed width like a literal, so
// /reports/[0-9]* ranks ahead of /reports/*, which it is narrower than. A run of * counts once.
func routeGlobShape(pattern string) (literal, positions, stars, singles int) {
	p := []rune(pattern)
	star := false
	for i := 0; i < len(p); i++ {
		run := star
		star = false
		switch c := p[i]; {
		case c == '\\' && i+1 < len(p):
			literal++
			i++
		case c == '*':
			if !run {
				stars++
			}
			star = true
		case c == '?':
			positions++
			singles += anyCharacter
		case c == '[':
			positions++
			negated := i+1 < len(p) && p[i+1] == '^'
			j := i + 1
			if negated {
				j = i + 2
			}
			size := 0
			for ; j < len(p) && p[j] != ']'; j++ {
				lo := p[j]
				if lo == '\\' && j+1 < len(p) {
					j++
					lo = p[j]
				}
				if j+2 < len(p) && p[j+1] == '-' && p[j+2] != ']' {
					hi := p[j+2]
					j += 2
					if hi == '\\' && j+1 < len(p) {
						j++
						hi = p[j]
					}
					size += max(1, int(hi)-int(lo)+1)
				} else {
					size++
				}
			}
			if negated {
				singles += anyCharacter - size
			} else {
				singles += size
			}
			i = j
		default:
			literal++
		}
	}
	return literal, positions, stars, singles
}
