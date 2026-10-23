package generic

import (
	"path"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// resolvedUsage is a usage mapping with the provider defaults and a route's overrides merged.
type resolvedUsage struct {
	modelPath        string
	inputTokensPath  string
	outputTokensPath string
	totalTokensPath  string
	costPath         string
	costPerRequest   float64
	unitsPath        string
	unitsSource      schemas.GenericUnitsSource
	unitsMeasure     schemas.GenericUnitsMeasure
	costPerUnit      float64
	unitsBlockSize   float64 // bill per started block of this many raw units; 0 means exact
	unitsMinimum     float64 // least number of billed units once a quantity was read
	optimistic       bool
	// ignoreUpstreamCost drops the cost_path (and the optimistic usage.cost) while the flat fee and
	// unit pricing, which the operator configured, still apply. Set from ignore_provider_cost.
	ignoreUpstreamCost bool
	eventPaths         []string // every response path worth a look per SSE event; built once, see collectEventPaths
}

// compiledRoute is one routes[] entry with its glob split once at construction.
type compiledRoute struct {
	method   string
	glob     routeGlob
	headers  []headerMatch // every one must match; nil for a route on method and path alone
	billable bool
	stream   *bool
	usage    resolvedUsage
}

// headerMatch is one required request header: a lower-cased name and a value glob. AWS JSON
// APIs (Textract, Translate) send every operation to POST / and name it in X-Amz-Target, so a
// route on method and path alone cannot tell them apart.
type headerMatch struct {
	name    string
	glob    string
	literal bool // no glob metacharacter: compare equal instead of matching
}

// matchHeaders reports whether every required header is present with a matching value. Names are
// looked up lower-cased, the form the transport collects them in; values match case-sensitively
// with the same single-segment glob paths use, except that "/" is an ordinary character here
// (application/x-amz-json-1.1).
func (route *compiledRoute) matchHeaders(headers map[string]string) bool {
	for _, h := range route.headers {
		value, ok := headers[h.name]
		if !ok {
			return false
		}
		if h.literal {
			if value != h.glob {
				return false
			}
			continue
		}
		if ok, _ := path.Match(strings.ReplaceAll(h.glob, "/", "\x01"), strings.ReplaceAll(value, "/", "\x01")); !ok {
			return false
		}
	}
	return true
}

func compileHeaders(headers map[string]string) []headerMatch {
	if len(headers) == 0 {
		return nil
	}
	out := make([]headerMatch, 0, len(headers))
	for name, glob := range headers {
		out = append(out, headerMatch{name: strings.ToLower(name), glob: glob, literal: !strings.ContainsAny(glob, "*?[\\")})
	}
	// A fixed order keeps matching deterministic whatever the map's iteration order.
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// routeGlob is a rooted segment pattern. prefix is set for a trailing /**.
type routeGlob struct {
	segments []string
	prefix   bool
}

func compileGlob(glob string) routeGlob {
	segments := strings.Split(strings.TrimPrefix(glob, "/"), "/")
	out := routeGlob{segments: segments}
	if n := len(segments); n > 0 && segments[n-1] == "**" {
		out.segments = segments[:n-1]
		out.prefix = true
	}
	return out
}

// match reports whether a stripped request path ("/a/b") satisfies the glob. Each pattern segment
// is matched with path.Match against one path segment; a prefix glob accepts any remainder,
// including none. A trailing slash is an extra empty segment and only a prefix glob admits it.
func (g routeGlob) match(requestPath string) bool {
	return g.matchSegments(strings.Split(strings.TrimPrefix(requestPath, "/"), "/"))
}

// matchSegments is match on a path already split once, so a request checked against many routes
// splits its path a single time.
func (g routeGlob) matchSegments(segments []string) bool {
	if g.prefix {
		if len(segments) < len(g.segments) {
			return false
		}
	} else if len(segments) != len(g.segments) {
		return false
	}
	for i, pattern := range g.segments {
		if ok, _ := path.Match(pattern, segments[i]); !ok {
			return false
		}
	}
	return true
}

func resolveUsage(base *schemas.GenericUsageConfig, override *schemas.GenericUsageConfig) resolvedUsage {
	out := resolvedUsage{optimistic: true, unitsSource: schemas.GenericUnitsFromResponse, unitsMeasure: schemas.GenericUnitsValue}
	apply := func(u *schemas.GenericUsageConfig) {
		if u == nil {
			return
		}
		if u.ModelPath != "" {
			out.modelPath = u.ModelPath
		}
		if u.InputTokensPath != "" {
			out.inputTokensPath = u.InputTokensPath
		}
		if u.OutputTokensPath != "" {
			out.outputTokensPath = u.OutputTokensPath
		}
		if u.TotalTokensPath != "" {
			out.totalTokensPath = u.TotalTokensPath
		}
		if u.CostPath != "" {
			out.costPath = u.CostPath
		}
		if u.CostPerRequest != nil {
			out.costPerRequest = *u.CostPerRequest
		}
		if u.UnitsPath != "" {
			out.unitsPath = u.UnitsPath
		}
		if u.UnitsSource != "" {
			out.unitsSource = u.UnitsSource
		}
		if u.UnitsMeasure != "" {
			out.unitsMeasure = u.UnitsMeasure
		}
		if u.CostPerUnit != nil {
			out.costPerUnit = *u.CostPerUnit
		}
		if u.UnitsBlockSize != nil {
			out.unitsBlockSize = *u.UnitsBlockSize
		}
		if u.UnitsMinimum != nil {
			out.unitsMinimum = *u.UnitsMinimum
		}
		if u.DisableOptimisticParsing {
			out.optimistic = false
		}
	}
	apply(base)
	apply(override)
	out.eventPaths = out.collectEventPaths()
	return out
}

// billableByDefault resolves a route's billable flag: the explicit value when set, else the
// method default shared with unrouted operations. Only POST is billable by default: on most
// APIs GET and HEAD are polls and listings, PUT and PATCH update settings, DELETE cleans up (an
// Azure avatar job, a Tavily crawl), and charging those would spend a caller's allowance on
// nothing. A route therefore never flips a method's billing just by existing, which matters for
// routes imported wholesale from an OpenAPI document; set billable: true where an API bills
// another method.
func billableByDefault(method string, explicit *bool) bool {
	if explicit != nil {
		return *explicit
	}
	return strings.EqualFold(method, "POST")
}

func compileRoutes(cfg *schemas.GenericProviderConfig) []compiledRoute {
	if cfg == nil || len(cfg.Routes) == 0 {
		return nil
	}
	routes := make([]compiledRoute, 0, len(cfg.Routes))
	for _, route := range cfg.Routes {
		compiled := compiledRoute{
			method:   strings.ToUpper(route.Method),
			glob:     compileGlob(route.Path),
			headers:  compileHeaders(route.Headers),
			billable: billableByDefault(route.Method, route.Billable),
			stream:   route.Stream,
			usage:    resolveUsage(cfg.Usage, route.Usage),
		}
		routes = append(routes, compiled)
	}
	return routes
}

func compileStreamGlobs(cfg *schemas.GenericProviderConfig) []routeGlob {
	if cfg == nil || cfg.Streaming == nil || len(cfg.Streaming.Paths) == 0 {
		return nil
	}
	globs := make([]routeGlob, 0, len(cfg.Streaming.Paths))
	for _, glob := range cfg.Streaming.Paths {
		globs = append(globs, compileGlob(glob))
	}
	return globs
}

// matchRoute returns the first routes[] entry whose method, glob and required headers fit, or
// nil. headers is the lower-cased caller header map; nil matches only routes without headers.
func (provider *GenericProvider) matchRoute(method, requestPath string, headers map[string]string) *compiledRoute {
	if len(provider.routes) == 0 {
		return nil
	}
	// The method is upper-cased and the path split once, not once per route: with a routes list
	// the size of an OpenAPI import that is the difference between 2 and 200 allocations.
	upper := method
	if !isUpper(method) {
		upper = strings.ToUpper(method)
	}
	segments := strings.Split(strings.TrimPrefix(requestPath, "/"), "/")
	for i := range provider.routes {
		route := &provider.routes[i]
		if route.method == upper && route.glob.matchSegments(segments) && (len(route.headers) == 0 || route.matchHeaders(headers)) {
			return route
		}
	}
	return nil
}

// isUpper reports whether s has no lower-case ASCII letter, the common case for an HTTP method.
func isUpper(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'a' && s[i] <= 'z' {
			return false
		}
	}
	return true
}
