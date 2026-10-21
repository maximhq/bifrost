package generic

import (
	"path"
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
	optimistic       bool
}

// compiledRoute is one routes[] entry with its glob split once at construction.
type compiledRoute struct {
	method   string
	glob     routeGlob
	billable bool
	stream   *bool
	usage    resolvedUsage
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
	segments := strings.Split(strings.TrimPrefix(requestPath, "/"), "/")
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
		if u.DisableOptimisticParsing {
			out.optimistic = false
		}
	}
	apply(base)
	apply(override)
	return out
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
			billable: route.Billable == nil || *route.Billable,
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

// matchRoute returns the first routes[] entry whose method and glob fit, or nil.
func (provider *GenericProvider) matchRoute(method, requestPath string) *compiledRoute {
	for i := range provider.routes {
		route := &provider.routes[i]
		if route.method == strings.ToUpper(method) && route.glob.match(requestPath) {
			return route
		}
	}
	return nil
}
