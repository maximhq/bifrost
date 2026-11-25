package relay

import (
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// resolvedUsage is a usage mapping with the endpoint defaults and a route's overrides merged.
type resolvedUsage struct {
	modelPath        string
	modelSource      schemas.RelayModelSource // body (default) or query
	inputTokensPath  string
	outputTokensPath string
	totalTokensPath  string
	costPath         string
	costPerRequest   float64
	unitsPath        string
	unitsSource      schemas.RelayUnitsSource
	unitsMeasure     schemas.RelayUnitsMeasure
	costPerUnit      float64
	unitsBlockSize   float64 // bill per started block of this many raw units; 0 means exact
	unitsMinimum     float64 // least number of billed units once a quantity was read
	// ignoreUpstreamCost drops the cost_path while the flat fee and
	// unit pricing, which the operator configured, still apply. Set from ignore_upstream_cost.
	ignoreUpstreamCost bool
	eventPaths         []string // every response path worth a look per SSE event; built once, see collectEventPaths
}

// compiledRoute is one routes[] entry with its glob split once at construction.
type compiledRoute struct {
	configIndex int // its position in the saved routes[], which matching does not follow
	method      string
	glob        routeGlob
	headers     []headerMatch // every one must match; nil for a route on method and path alone
	billable    bool
	disabled    bool // matched requests are refused rather than falling through to a later route
	stream      *bool
	usage       resolvedUsage
	// bodyOverrides are the forced body fields, the endpoint's merged under the route's.
	bodyOverrides []bodyOverride
	// job, jobParams and jobInterval are set for a route that starts an upstream job: the
	// block itself, its settlement snapshot (built once here, attached to every start response)
	// and the seconds until the first poll.
	job         *schemas.RelayJobConfig
	jobParams   string
	jobInterval int
	jobRouteKey string // names the job's status check in its row (JobPollKey), so editing the start route keeps running jobs
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
func (route *compiledRoute) matchHeaders(headers, operator map[string]string) bool {
	for _, h := range route.headers {
		value, ok := headerValue(h.name, headers, operator)
		if !ok {
			return false
		}
		if h.literal {
			if value != h.glob {
				return false
			}
			continue
		}
		if !headerGlobMatches(h.glob, value) {
			return false
		}
	}
	return true
}

// headerValue is the value a header will carry upstream, in the order a passthrough call applies
// them: the caller's header (headers already holds the caller's x-bf-eh-* values beneath it, see
// routeHeaders), else the operator's extra header. The transport lower-cases caller names; a Go SDK
// caller may not, so a miss falls back to a case-insensitive scan.
// matchedHeaders returns, by name, the value that selected the route for each header the route
// matches on: the caller's when the caller sent that header, else the operator's. nil for a route
// without header matchers.
func (route *compiledRoute) matchedHeaders(headers, operator map[string]string) map[string]string {
	if len(route.headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(route.headers))
	for _, h := range route.headers {
		if value, ok := headerValue(h.name, headers, operator); ok {
			out[h.name] = value
		}
	}
	return out
}

// headerValue returns the value the upstream receives for a header: the relay's own extra header
// when it sets one, since a caller cannot replace it, else the caller's.
func headerValue(name string, headers, operator map[string]string) (string, bool) {
	if v, ok := operator[name]; ok {
		return v, true
	}
	if v, ok := headers[name]; ok {
		return v, true
	}
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return "", false
}

func compileHeaders(headers map[string]string) []headerMatch {
	if len(headers) == 0 {
		return nil
	}
	out := make([]headerMatch, 0, len(headers))
	for name, glob := range headers {
		out = append(out, headerMatch{name: strings.ToLower(name), glob: glob, literal: !isHeaderGlob(glob)})
	}
	// A fixed order keeps matching deterministic whatever the map's iteration order.
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// maxGapSegments is the most segments a middle ** takes: as many as the longest job id can hold.
const maxGapSegments = (maxRelayJobIDLength + 1) / 2

// routeGlob is a rooted segment pattern. prefix is set for a trailing /**; tail counts the segments
// after a middle **, which is not kept in segments.
type routeGlob struct {
	segments []string
	prefix   bool
	tail     int32
}

func compileGlob(glob string) routeGlob {
	segments := strings.Split(strings.TrimPrefix(glob, "/"), "/")
	n := len(segments)
	if segments[n-1] == "**" {
		return routeGlob{segments: segments[:n-1], prefix: true}
	}
	if at := slices.Index(segments, "**"); at >= 0 {
		return routeGlob{segments: slices.Delete(segments, at, at+1), tail: int32(n - at - 1)}
	}
	return routeGlob{segments: segments}
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
	if g.tail > 0 {
		return g.matchGap(segments)
	}
	if g.prefix {
		if len(segments) < len(g.segments) {
			return false
		}
	} else if len(segments) != len(g.segments) {
		return false
	}
	for i, pattern := range g.segments {
		if !segmentMatch(pattern, segments[i]) {
			return false
		}
	}
	if g.prefix {
		// A trailing ** refuses dot segments as a middle one does; an empty one is a trailing slash.
		for _, segment := range segments[len(g.segments):] {
			if segment != "" && !gapSegmentOK(segment) {
				return false
			}
		}
	}
	return true
}

// matchGap matches a path with a middle **, which takes one or more segments. The fixed segments
// sit at known positions, so there is no backtracking.
func (g routeGlob) matchGap(segments []string) bool {
	gap := len(segments) - len(g.segments)
	if gap < 1 || gap > maxGapSegments {
		return false
	}
	head := len(g.segments) - int(g.tail)
	for i, pattern := range g.segments {
		at := i
		if i >= head {
			at = i + gap
		}
		if !segmentMatch(pattern, segments[at]) {
			return false
		}
	}
	for _, segment := range segments[head : head+gap] {
		if !gapSegmentOK(segment) {
			return false
		}
	}
	return true
}

// segmentMatch matches one pattern segment against one path segment. A dot segment is taken only by
// the same literal, never by a wildcard: /public/*/admin must not take /public/../admin, which an
// upstream may resolve to /admin past a disabled catch-all.
func segmentMatch(pattern, segment string) bool {
	if segment != "" && !gapSegmentOK(segment) {
		return pattern == segment
	}
	ok, _ := path.Match(pattern, segment)
	return ok
}

// gapSegmentOK refuses empty and dot segments, with or without ;params, since an upstream can resolve
// them to a different path than the free route was written for.
func gapSegmentOK(segment string) bool {
	if i := strings.IndexByte(segment, ';'); i >= 0 {
		segment = segment[:i]
	}
	return segment != "" && segment != "." && segment != ".."
}

// tidyPath drops empty segments from a request path (a trailing or doubled slash); the path is
// returned as is when it has none, with no allocation.
func tidyPath(requestPath string) string {
	trimmed := strings.TrimPrefix(requestPath, "/")
	// A leading doubled slash (//secret) leaves trimmed starting with "/", an empty first segment.
	if !strings.HasPrefix(trimmed, "/") && !strings.Contains(trimmed, "//") && !strings.HasSuffix(trimmed, "/") {
		return requestPath
	}
	var b strings.Builder
	for segment := range strings.SplitSeq(trimmed, "/") {
		if segment != "" {
			b.WriteByte('/')
			b.WriteString(segment)
		}
	}
	if b.Len() == 0 {
		return "/"
	}
	return b.String()
}

// hasDotSegment reports a "." or ".." segment in a request path, with or without ;params.
func hasDotSegment(requestPath string) bool {
	for segment := range strings.SplitSeq(strings.TrimPrefix(requestPath, "/"), "/") {
		if segment != "" && !gapSegmentOK(segment) {
			return true
		}
	}
	return false
}

func resolveUsage(base *schemas.RelayUsageConfig, override *schemas.RelayUsageConfig) resolvedUsage {
	out := resolvedUsage{unitsSource: schemas.RelayUnitsFromResponse, unitsMeasure: schemas.RelayUnitsValue}
	apply := func(u *schemas.RelayUsageConfig) {
		if u == nil {
			return
		}
		if u.ModelPath != "" {
			out.modelPath = u.ModelPath
		}
		if u.ModelSource != "" {
			out.modelSource = u.ModelSource
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

func compileRoutes(cfg *schemas.RelayConfig) []compiledRoute {
	return compileRoutesWithCostPolicy(cfg, false)
}

// compileRoutesWithCostPolicy is compileRoutes with the endpoint's ignore_upstream_cost, which a
// job snapshot has to carry since settlement runs without the endpoint in hand.
func compileRoutesWithCostPolicy(cfg *schemas.RelayConfig, ignoreUpstreamCost bool) []compiledRoute {
	if cfg == nil || len(cfg.Routes) == 0 {
		return nil
	}
	routes := make([]compiledRoute, 0, len(cfg.Routes))
	for i, route := range cfg.Routes {
		compiled := compiledRoute{
			configIndex: i,
			method:      strings.ToUpper(route.Method),
			glob:        compileGlob(route.Path),
			headers:     compileHeaders(route.Headers),
			billable:    billableByDefault(route.Method, route.Billable),
			disabled:    route.Enabled != nil && !*route.Enabled,
			stream:      route.Stream,
			usage:       resolveUsage(cfg.Usage, route.Usage),

			bodyOverrides: compileBodyOverrides(route.EffectiveBodyOverrides(cfg.BodyOverrides)),
		}
		if route.Job != nil {
			// Validate ran on the config, so the snapshot marshals; a failure here would be a
			// programming error and the route simply starts no job.
			if params, err := jobSnapshotFor(cfg, route, ignoreUpstreamCost); err == nil {
				compiled.job = route.Job
				compiled.jobParams = params
				compiled.jobInterval = (&JobSnapshot{Job: *route.Job}).Interval()
				compiled.jobRouteKey = JobPollKey(route.Job.Poll)
			}
		}
		routes = append(routes, compiled)
	}
	// Specific routes are tried first and wildcards last, however the config lists them.
	ordered := make([]compiledRoute, len(routes))
	for k, i := range routeMatchOrder(cfg.Routes) {
		ordered[k] = routes[i]
	}
	return ordered
}

func compileStreamGlobs(cfg *schemas.RelayConfig) []routeGlob {
	if cfg == nil || cfg.Streaming == nil || len(cfg.Streaming.Paths) == 0 {
		return nil
	}
	globs := make([]routeGlob, 0, len(cfg.Streaming.Paths))
	for _, glob := range cfg.Streaming.Paths {
		globs = append(globs, compileGlob(glob))
	}
	return globs
}

// matchRoute returns the first routes[] entry, most specific first (see routeMatchOrder), whose method, glob and required headers fit, or
// nil. headers is the lower-cased caller header map; nil matches only routes without headers.
func (e *endpoint) matchRoute(method, requestPath string, headers map[string]string) *compiledRoute {
	if len(e.routes) == 0 {
		return nil
	}
	// The method is upper-cased and the path split once, not once per route: with a routes list
	// the size of an OpenAPI import that is the difference between 2 and 200 allocations.
	upper := method
	if !isUpper(method) {
		upper = strings.ToUpper(method)
	}
	segments := strings.Split(strings.TrimPrefix(requestPath, "/"), "/")
	for i := range e.routes {
		route := &e.routes[i]
		if route.method == upper && route.glob.matchSegments(segments) && (len(route.headers) == 0 || route.matchHeaders(headers, e.operatorHeaders)) {
			return route
		}
	}
	return nil
}

// isUpper reports whether s has no lower-case ASCII letter, the common case for an HTTP method.
// ambiguousHeader returns a header name that headers holds under two spellings (X-Amz-Target and
// x-amz-target). Route matching reads one spelling and forwarding could send the other, so such a
// request is refused. Only a Go SDK caller can build one: the HTTP transport lower-cases names, so
// on that path this is a scan with no allocation.
func ambiguousHeader(headers map[string]string) (string, bool) {
	mixed := false
	for name := range headers {
		if hasUpper(name) {
			mixed = true
			break
		}
	}
	if !mixed {
		return "", false
	}
	seen := make(map[string]struct{}, len(headers))
	for name := range headers {
		lower := strings.ToLower(name)
		if _, dup := seen[lower]; dup {
			return name, true
		}
		seen[lower] = struct{}{}
	}
	return "", false
}

func hasUpper(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			return true
		}
	}
	return false
}

func isUpper(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'a' && s[i] <= 'z' {
			return false
		}
	}
	return true
}

// headerGlobMatches matches a header value against a glob; / is swapped out so * can span it.
func headerGlobMatches(pattern, value string) bool {
	ok, _ := path.Match(strings.ReplaceAll(pattern, "/", "\x01"), strings.ReplaceAll(value, "/", "\x01"))
	return ok
}

// isHeaderGlob reports whether a header matcher is a glob rather than an exact value.
func isHeaderGlob(value string) bool { return strings.ContainsAny(value, "*?[\\") }
