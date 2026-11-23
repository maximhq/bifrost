package relay

import (
	"slices"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// anyCharacter is how many characters a ? or a negated class can stand for, for ranking only.
const anyCharacter = 0x110000

// routeMatchOrder returns route indices in the order requests try them. Specific paths come first
// and wildcards last, and equal ranks keep their saved order. Routes with one method and path then
// reorder among their own positions: a route proven narrower goes first, an exact header value goes
// before a wildcard that can also take its requests, and nothing else moves. It mirrors matchOrder in
// ui/lib/utils/relayForm.ts; testdata/routeorder.json holds their shared cases.
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
// across different headers, stay together in their saved order.
func orderOnePath(routes []schemas.RelayRouteConfig, members []int) []int {
	n := len(members)
	headers := make([]map[string]string, n)
	for k, i := range members {
		headers[k] = make(map[string]string, len(routes[i].Headers))
		for name, value := range routes[i].Headers {
			headers[k][strings.ToLower(name)] = value
		}
	}
	goes := make([][]bool, n)
	for a := range n {
		goes[a] = make([]bool, n)
		for b := range n {
			goes[a][b] = a != b && headersGoBefore(headers[a], headers[b])
		}
	}
	group := cycleGroups(goes)
	placed := make([]bool, n)
	out := make([]int, 0, n)
	var place func(k int)
	place = func(k int) {
		for j := range n {
			if group[j] == group[k] {
				placed[j] = true
			}
		}
		for p := range n {
			if placed[p] {
				continue
			}
			for j := range n {
				if group[j] == group[k] && goes[p][j] {
					place(p)
					break
				}
			}
		}
		for j := range n {
			if group[j] == group[k] {
				out = append(out, members[j])
			}
		}
	}
	for k := range n {
		if !placed[k] {
			place(k)
		}
	}
	return out
}

// cycleGroups labels each route with the first route of the preference cycle it belongs to, or
// itself. Only routes sharing one method and path are compared, so the cubic closure stays small.
func cycleGroups(goes [][]bool) []int {
	n := len(goes)
	reach := make([][]bool, n)
	for a := range n {
		reach[a] = slices.Clone(goes[a])
	}
	for m := range n {
		for a := range n {
			if !reach[a][m] {
				continue
			}
			for b := range n {
				if reach[m][b] {
					reach[a][b] = true
				}
			}
		}
	}
	group := make([]int, n)
	for k := range n {
		group[k] = k
		for j := range k {
			if reach[j][k] && reach[k][j] {
				group[k] = j
				break
			}
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

// exactOverlapsWildcard reports an all-exact a whose value falls under a wildcard of b on the same
// header, with every other header they share accepting a's value too.
func exactOverlapsWildcard(a, b map[string]string) bool {
	if len(a) == 0 || !anyHeaderGlob(b) || anyHeaderGlob(a) {
		return false
	}
	underWildcard := false
	for name, value := range a {
		pattern, ok := b[name]
		if !ok {
			continue
		}
		if !headerGlobMatches(pattern, value) {
			return false
		}
		underWildcard = underWildcard || isHeaderGlob(pattern)
	}
	return underWildcard
}

func anyHeaderGlob(headers map[string]string) bool {
	for _, value := range headers {
		if isHeaderGlob(value) {
			return true
		}
	}
	return false
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
