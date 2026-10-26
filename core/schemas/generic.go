package schemas

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// GenericAuthType is how the generic relay presents the selected key to the upstream.
type GenericAuthType string

const (
	GenericAuthBearer   GenericAuthType = "bearer"    // Authorization: Bearer <value>
	GenericAuthHeader   GenericAuthType = "header"    // <header_name>: <value_prefix><value>
	GenericAuthQuery    GenericAuthType = "query"     // ?<query_param>=<value>
	GenericAuthBasic    GenericAuthType = "basic"     // Authorization: Basic base64(<value>), value is user:password
	GenericAuthAWSSigV4 GenericAuthType = "aws_sigv4" // SigV4 with the key's bedrock_key_config credentials
	GenericAuthNone     GenericAuthType = "none"      // no credential injected
)

var genericAuthTypes = map[GenericAuthType]struct{}{
	GenericAuthBearer: {}, GenericAuthHeader: {}, GenericAuthQuery: {}, GenericAuthBasic: {}, GenericAuthAWSSigV4: {}, GenericAuthNone: {},
}

// genericProviderNameRe is the provider names the /passthrough/{provider} route serves; a generic
// provider is refused at creation when its name falls outside it, so it can never be unreachable.
var genericProviderNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// ValidGenericProviderName reports whether name can be served at /passthrough/{name}.
func ValidGenericProviderName(name string) bool {
	return genericProviderNameRe.MatchString(name)
}

var genericAWSServiceRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// genericUnmatchableHeaders are request headers a route matcher may not name: hop-by-hop headers
// and the Google key header, which the transport drops before the routes are consulted, and the
// credential headers IsSensitiveHeader misses, which it drops before forwarding. With
// IsSensitiveHeader they are the names whose caller value never reaches the upstream.
var genericUnmatchableHeaders = map[string]struct{}{
	"host": {}, "connection": {}, "transfer-encoding": {}, "accept-encoding": {}, "x-goog-api-key": {},
	"keep-alive": {}, "te": {}, "trailer": {}, "upgrade": {}, "proxy-connection": {}, "proxy-authenticate": {}, // hop-by-hop: the transport drops them before the routes are consulted
	"ocp-apim-subscription-key": {}, "subscription-key": {}, "x-functions-key": {},
	"content-length": {}, // the relay sends the body's real length, never the caller's
}

var genericRouteMethods = map[string]struct{}{
	"GET": {}, "POST": {}, "PUT": {}, "PATCH": {}, "DELETE": {}, "HEAD": {},
}

// GenericAuthConfig selects the credential scheme for a generic provider.
type GenericAuthConfig struct {
	Type        GenericAuthType       `json:"type"`                   // bearer | header | query | basic | aws_sigv4 | none (default bearer)
	HeaderName  string                `json:"header_name,omitempty"`  // header: the header that carries the key
	QueryParam  string                `json:"query_param,omitempty"`  // query: the query parameter that carries the key
	ValuePrefix string                `json:"value_prefix,omitempty"` // header: literal prefix before the key value
	AWS         *GenericAWSAuthConfig `json:"aws,omitempty"`          // aws_sigv4 settings
}

// GenericAWSAuthConfig names the AWS service a SigV4-signed relay targets. The signing region
// is the key's bedrock_key_config.region.
type GenericAWSAuthConfig struct {
	Service string `json:"service"` // SigV4 signing name, e.g. textract, translate
}

// GenericModelSource names where in the caller's request the model is read.
type GenericModelSource string

const (
	GenericModelFromBody  GenericModelSource = "body"  // default: a JSON field of the request body (model, model_id)
	GenericModelFromQuery GenericModelSource = "query" // a query parameter (Deepgram's ?model=nova-3)
)

// GenericUnitsSource names the body a billable quantity is read from.
type GenericUnitsSource string

const (
	GenericUnitsFromResponse GenericUnitsSource = "response" // default: a count the upstream reports (Textract pages)
	GenericUnitsFromRequest  GenericUnitsSource = "request"  // what the caller sent, which the upstream bills on (Translate text)
)

// GenericUnitsMeasure names how the quantity is taken from the body.
type GenericUnitsMeasure string

const (
	GenericUnitsValue     GenericUnitsMeasure = "value"      // default: the number at units_path
	GenericUnitsChars     GenericUnitsMeasure = "chars"      // the character count of the string at units_path
	GenericUnitsBodyBytes GenericUnitsMeasure = "body_bytes" // the byte length of the whole body; units_path is ignored
	GenericUnitsBodyChars GenericUnitsMeasure = "body_chars" // the character (rune) count of the whole body, any content type; units_path is ignored
)

// GenericUsageConfig maps a relayed request/response onto usage and cost. Paths are gjson paths;
// an empty path reads nothing: only the fields a mapping names are read.
type GenericUsageConfig struct {
	ModelPath        string              `json:"model_path,omitempty"`         // where the model is read: a request body path, or a query parameter with model_source query
	ModelSource      GenericModelSource  `json:"model_source,omitempty"`       // body (default) or query
	InputTokensPath  string              `json:"input_tokens_path,omitempty"`  // response body path for input tokens
	OutputTokensPath string              `json:"output_tokens_path,omitempty"` // response body path for output tokens
	TotalTokensPath  string              `json:"total_tokens_path,omitempty"`  // response body path for total tokens
	CostPath         string              `json:"cost_path,omitempty"`          // response body path for a provider-reported cost (arrays are summed)
	CostPerRequest   *float64            `json:"cost_per_request,omitempty"`   // flat USD fee added to every billable call; a route may set 0 to waive the provider fee
	UnitsPath        string              `json:"units_path,omitempty"`         // path of the billable quantity (pages, credits, text)
	UnitsSource      GenericUnitsSource  `json:"units_source,omitempty"`       // response (default) or request
	UnitsMeasure     GenericUnitsMeasure `json:"units_measure,omitempty"`      // value (default), chars, body_bytes or body_chars
	CostPerUnit      *float64            `json:"cost_per_unit,omitempty"`      // USD per unit; cost += units * cost_per_unit
	UnitsBlockSize   *float64            `json:"units_block_size,omitempty"`   // bill per started block of this many raw units (100 characters); default 1, exact
	UnitsMinimum     *float64            `json:"units_minimum,omitempty"`      // least number of billed units once a quantity was read; default 0
}

// GenericStreamingConfig decides which relayed requests are served as streams.
type GenericStreamingConfig struct {
	Paths                  []string `json:"paths,omitempty"`           // route globs that always stream
	DetectFromAcceptHeader bool     `json:"detect_from_accept_header"` // Accept: text/event-stream selects streaming
	DetectFromBody         bool     `json:"detect_from_body"`          // "stream": true in a JSON body, or ?stream=true on a GET, selects streaming
}

// GenericExtraHeaderConflict returns an extra_headers name that appears under two spellings
// (X-Amz-Target and x-amz-target). A generic relay matches routes on the operator's value of a
// header, so two spellings could approve one operation while the other is sent.
func GenericExtraHeaderConflict(headers map[string]SecretVar) (string, bool) {
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

// GenericJobConfig makes a route the start of a provider-side job that is billed once it
// completes, out of band: the job id is read from the start response, the upstream is polled
// with the given request until the status is terminal, and the terminal response is priced with
// the job's usage mapping. The start call itself still counts as one request and carries the
// route's own fee. Settlement runs on the job accounting sweeper, with the key that started the
// job, and charges the budgets the start call was subject to.
type GenericJobConfig struct {
	IDPath          string              `json:"id_path"`                    // start response path of the job id (Textract: JobId)
	Poll            GenericJobPoll      `json:"poll"`                       // the request that reports where the job stands
	StatusPath      string              `json:"status_path"`                // poll response path of the status (JobStatus)
	Succeeded       []string            `json:"succeeded"`                  // statuses that end the job with work to bill
	Failed          []string            `json:"failed,omitempty"`           // statuses that end the job at no charge
	IntervalSeconds int                 `json:"interval_seconds,omitempty"` // seconds between polls (default 30)
	TimeoutSeconds  int                 `json:"timeout_seconds,omitempty"`  // give up polling after this long (default 3600)
	Usage           *GenericUsageConfig `json:"usage,omitempty"`            // mapping applied to the terminal poll response, over the route's
}

// GenericJobPoll is the request the sweeper sends to check a job. {{job.id}} in the path, a
// header value or the body is replaced with the job id.
type GenericJobPoll struct {
	Method      string            `json:"method,omitempty"`       // default POST
	Path        string            `json:"path"`                   // rooted path, may contain {{job.id}}
	Headers     map[string]string `json:"headers,omitempty"`      // literal headers, values may contain {{job.id}}
	Body        string            `json:"body,omitempty"`         // literal body, may contain {{job.id}}
	ContentType string            `json:"content_type,omitempty"` // default application/json when a body is set
}

// JobIDPlaceholder is replaced with the job id in a poll request.
const JobIDPlaceholder = "{{job.id}}"

// GenericScopedJobID ties an upstream job id to the call that started it, for the job row and its
// settlement. Some APIs let the caller choose the id and reuse it after a delete (Azure batch
// synthesis), so the upstream id alone would make a second job land on the first job's row and
// never be billed. startID is the start call's log id, which can be the caller's own x-request-id,
// so it is query-escaped: a ':' in it becomes %3A, and the first ':' is always the separator.
func GenericScopedJobID(startID, upstreamID string) string {
	// The separator is written even with no start id, so the split below always round-trips an
	// upstream id that itself carries ':' (an ARN).
	return url.QueryEscape(startID) + ":" + upstreamID
}

// GenericUpstreamJobID returns the upstream part of a scoped job id, the id the poll is sent with.
func GenericUpstreamJobID(scoped string) string {
	if _, upstream, ok := strings.Cut(scoped, ":"); ok {
		return upstream
	}
	return scoped
}

// GenericRouteConfig overrides provider defaults for one upstream operation.
type GenericRouteConfig struct {
	Method   string              `json:"method"`             // GET | POST | PUT | PATCH | DELETE | HEAD
	Path     string              `json:"path"`               // segment glob; a trailing /** matches the prefix and everything below
	Headers  map[string]string   `json:"headers,omitempty"`  // request headers that must also match: name (case-insensitive) to value glob (X-Amz-Target: Textract.Get*)
	Billable *bool               `json:"billable,omitempty"` // false: the call is relayed but not counted or charged
	Stream   *bool               `json:"stream,omitempty"`   // forces the streaming or buffered handler
	Usage    *GenericUsageConfig `json:"usage,omitempty"`    // field-by-field override of the provider usage mapping
	Job      *GenericJobConfig   `json:"job,omitempty"`      // the call starts a job billed once it completes
}

// GenericProviderConfig configures a custom provider whose base type is Generic: a raw HTTP relay
// to a non-LLM API that still carries key injection, logging, governance and plugins.
type GenericProviderConfig struct {
	Auth         GenericAuthConfig       `json:"auth"`
	Usage        *GenericUsageConfig     `json:"usage,omitempty"`
	Streaming    *GenericStreamingConfig `json:"streaming,omitempty"`
	Routes       []GenericRouteConfig    `json:"routes,omitempty"`
	StrictRoutes bool                    `json:"strict_routes,omitempty"` // refuse requests matching no routes[] entry with 404
}

// Validate checks the config is internally consistent. It runs when a provider is created or
// updated, never on the request path.
func (c *GenericProviderConfig) Validate() error {
	if c == nil {
		return nil
	}
	if err := c.Auth.validate(); err != nil {
		return err
	}
	if err := c.Usage.validate("usage", nil); err != nil {
		return err
	}
	if c.Streaming != nil {
		for i, glob := range c.Streaming.Paths {
			if err := validateGenericRouteGlob(glob); err != nil {
				return fmt.Errorf("streaming.paths[%d]: %w", i, err)
			}
		}
	}
	for i, route := range c.Routes {
		if _, ok := genericRouteMethods[strings.ToUpper(route.Method)]; !ok {
			return fmt.Errorf("routes[%d]: unsupported method %q", i, route.Method)
		}
		if err := validateGenericRouteGlob(route.Path); err != nil {
			return fmt.Errorf("routes[%d]: %w", i, err)
		}
		seenHeaders := make(map[string]struct{}, len(route.Headers))
		for name, glob := range route.Headers {
			if !httpguts.ValidHeaderFieldName(name) {
				return fmt.Errorf("routes[%d].headers: %q is not a valid header name", i, name)
			}
			injected := c.Auth.Type == GenericAuthHeader && strings.EqualFold(name, c.Auth.HeaderName)
			if _, dropped := genericUnmatchableHeaders[strings.ToLower(name)]; dropped || injected || IsSensitiveHeader(name) || strings.HasPrefix(strings.ToLower(name), "x-bf-") {
				return fmt.Errorf("routes[%d].headers: %q is never presented to route matching (credentials, gateway and hop-by-hop headers are dropped before the routes are consulted)", i, name)
			}
			// Under SigV4 the relay forwards no caller x-amz-* header but X-Amz-Target, so a matcher on
			// another one would pick a route (and its price) from a header the upstream never receives.
			if lower := strings.ToLower(name); c.Auth.Type == GenericAuthAWSSigV4 && strings.HasPrefix(lower, "x-amz-") && lower != "x-amz-target" {
				return fmt.Errorf("routes[%d].headers: %q is not forwarded under aws_sigv4 (only X-Amz-Target is), so it cannot select a route", i, name)
			}
			// Names match case-insensitively, so two spellings of one header would both have to
			// match a single value and the route would silently never fire.
			if _, dup := seenHeaders[strings.ToLower(name)]; dup {
				return fmt.Errorf("routes[%d].headers: %q duplicates a header name that differs only by case", i, name)
			}
			seenHeaders[strings.ToLower(name)] = struct{}{}
			if strings.TrimSpace(glob) == "" {
				return fmt.Errorf("routes[%d].headers[%s]: a value glob is required", i, name)
			}
			if _, err := path.Match(glob, ""); err != nil {
				return fmt.Errorf("routes[%d].headers[%s]: %w", i, name, err)
			}
		}
		if err := route.Usage.validate(fmt.Sprintf("routes[%d].usage", i), c.Usage); err != nil {
			return err
		}
		if route.Job != nil {
			if err := route.Job.validate(fmt.Sprintf("routes[%d].job", i), route, c.Usage); err != nil {
				return err
			}
		}
	}
	return nil
}

func (j *GenericJobConfig) validate(field string, route GenericRouteConfig, base *GenericUsageConfig) error {
	if strings.TrimSpace(j.IDPath) == "" {
		return fmt.Errorf("%s.id_path is required", field)
	}
	if strings.TrimSpace(j.StatusPath) == "" {
		return fmt.Errorf("%s.status_path is required", field)
	}
	if len(j.Succeeded) == 0 {
		return fmt.Errorf("%s.succeeded must name at least one status", field)
	}
	if j.IntervalSeconds < 0 || j.TimeoutSeconds < 0 {
		return fmt.Errorf("%s: interval_seconds and timeout_seconds must not be negative", field)
	}
	if !strings.HasPrefix(j.Poll.Path, "/") {
		return fmt.Errorf("%s.poll.path %q must start with /", field, j.Poll.Path)
	}
	if j.Poll.Method != "" {
		if _, ok := genericRouteMethods[strings.ToUpper(j.Poll.Method)]; !ok {
			return fmt.Errorf("%s.poll.method: unsupported method %q", field, j.Poll.Method)
		}
	}
	for name := range j.Poll.Headers {
		if !httpguts.ValidHeaderFieldName(name) {
			return fmt.Errorf("%s.poll.headers: %q is not a valid header name", field, name)
		}
	}
	// The start call's reply carries the job id, which only the buffered relay reads.
	if route.Stream != nil && *route.Stream {
		return fmt.Errorf("%s: a route that starts a job is served buffered; stream: true does not apply", field)
	}
	// The start call must be counted, or the job has no grant to settle against.
	if route.Billable != nil && !*route.Billable {
		return fmt.Errorf("%s: a route that starts a job must be billable", field)
	}
	if route.Billable == nil && !strings.EqualFold(route.Method, "POST") {
		return fmt.Errorf("%s: a %s route that starts a job must set billable: true", field, strings.ToUpper(route.Method))
	}
	// The job's mapping inherits route over provider, field by field, so a job that sets only a
	// rate is valid when either of them names the units path.
	merged := &GenericUsageConfig{}
	for _, u := range []*GenericUsageConfig{base, route.Usage} {
		if u == nil {
			continue
		}
		if u.UnitsPath != "" {
			merged.UnitsPath = u.UnitsPath
		}
		if u.UnitsMeasure != "" {
			merged.UnitsMeasure = u.UnitsMeasure
		}
		if u.CostPerUnit != nil {
			merged.CostPerUnit = u.CostPerUnit
		}
	}
	// The job is priced from its terminal poll response; a request-side quantity measured at start
	// is not carried to settlement, so a job mapping that would bill one is refused.
	var source GenericUnitsSource
	var rate *float64
	for _, u := range []*GenericUsageConfig{base, route.Usage, j.Usage} {
		if u == nil {
			continue
		}
		if u.UnitsSource != "" {
			source = u.UnitsSource
		}
		if u.CostPerUnit != nil {
			rate = u.CostPerUnit
		}
	}
	if source == GenericUnitsFromRequest && rate != nil && *rate > 0 {
		return fmt.Errorf("%s.usage: a job is priced from its terminal poll response, so request-side units are not billed at settlement; set units_source: response on the job", field)
	}
	return j.Usage.validate(field+".usage", merged)
}

func (a GenericAuthConfig) validate() error {
	if a.Type == "" {
		return nil
	}
	if _, ok := genericAuthTypes[a.Type]; !ok {
		return fmt.Errorf("auth.type: unsupported value %q", a.Type)
	}
	switch a.Type {
	case GenericAuthHeader:
		if strings.TrimSpace(a.HeaderName) == "" {
			return fmt.Errorf("auth.header_name is required for auth.type header")
		}
		if !httpguts.ValidHeaderFieldName(a.HeaderName) {
			return fmt.Errorf("auth.header_name %q is not a valid header name", a.HeaderName)
		}
	case GenericAuthQuery:
		if strings.TrimSpace(a.QueryParam) == "" {
			return fmt.Errorf("auth.query_param is required for auth.type query")
		}
	case GenericAuthAWSSigV4:
		if a.AWS == nil || strings.TrimSpace(a.AWS.Service) == "" {
			return fmt.Errorf("auth.aws.service is required for auth.type aws_sigv4")
		}
		if !genericAWSServiceRe.MatchString(a.AWS.Service) {
			return fmt.Errorf("auth.aws.service %q must match [a-z0-9-]+", a.AWS.Service)
		}
	}
	return nil
}

// validate checks one usage block. base is the provider block a route inherits from, so a route
// that sets only cost_per_unit is judged on the merged units_path and units_measure.
func (u *GenericUsageConfig) validate(field string, base *GenericUsageConfig) error {
	if u == nil {
		return nil
	}
	if u.CostPerRequest != nil && *u.CostPerRequest < 0 {
		return fmt.Errorf("%s.cost_per_request must not be negative", field)
	}
	if u.CostPerUnit != nil && *u.CostPerUnit < 0 {
		return fmt.Errorf("%s.cost_per_unit must not be negative", field)
	}
	if u.UnitsBlockSize != nil && *u.UnitsBlockSize <= 0 {
		return fmt.Errorf("%s.units_block_size must be above zero", field)
	}
	if u.UnitsMinimum != nil && *u.UnitsMinimum < 0 {
		return fmt.Errorf("%s.units_minimum must not be negative", field)
	}
	switch u.ModelSource {
	case "", GenericModelFromBody, GenericModelFromQuery:
	default:
		return fmt.Errorf("%s.model_source: unsupported value %q", field, u.ModelSource)
	}
	switch u.UnitsSource {
	case "", GenericUnitsFromResponse, GenericUnitsFromRequest:
	default:
		return fmt.Errorf("%s.units_source: unsupported value %q", field, u.UnitsSource)
	}
	switch u.UnitsMeasure {
	case "", GenericUnitsValue, GenericUnitsChars, GenericUnitsBodyBytes, GenericUnitsBodyChars:
	default:
		return fmt.Errorf("%s.units_measure: unsupported value %q", field, u.UnitsMeasure)
	}
	// A zero cost_per_unit is "no unit pricing" (a route may set it to waive the provider's), so
	// it needs no path to read a quantity from.
	unitsPath, unitsMeasure := u.UnitsPath, u.UnitsMeasure
	if base != nil {
		if unitsPath == "" {
			unitsPath = base.UnitsPath
		}
		if unitsMeasure == "" {
			unitsMeasure = base.UnitsMeasure
		}
	}
	// The rate is inherited too: a route that only switches the measure to one needing a path
	// would otherwise keep the provider's rate and silently bill no units.
	rate := u.CostPerUnit
	if rate == nil && base != nil {
		rate = base.CostPerUnit
	}
	if rate != nil && *rate > 0 && !unitsMeasureIsWholeBody(unitsMeasure) && unitsPath == "" {
		return fmt.Errorf("%s.units_path is required with cost_per_unit unless units_measure is body_bytes or body_chars", field)
	}
	return nil
}

// unitsMeasureIsWholeBody reports whether a measure reads the whole body and so needs no path.
func unitsMeasureIsWholeBody(m GenericUnitsMeasure) bool {
	return m == GenericUnitsBodyBytes || m == GenericUnitsBodyChars
}

// validateGenericRouteGlob accepts a rooted segment glob: "/a/*/b", "/a/**". path.Match reports
// syntax errors for a malformed segment pattern.
func validateGenericRouteGlob(glob string) error {
	if !strings.HasPrefix(glob, "/") {
		return fmt.Errorf("path %q must start with /", glob)
	}
	segments := strings.Split(strings.TrimPrefix(glob, "/"), "/")
	for i, segment := range segments {
		if segment == "**" {
			if i != len(segments)-1 {
				return fmt.Errorf("path %q: ** is only allowed as the last segment", glob)
			}
			continue
		}
		if _, err := path.Match(segment, ""); err != nil {
			return fmt.Errorf("path %q: %w", glob, err)
		}
	}
	return nil
}

// clone returns a copy of u that shares none of its pointer fields; nil stays nil.
func (u *GenericUsageConfig) clone() *GenericUsageConfig {
	if u == nil {
		return nil
	}
	out := *u
	if u.CostPerRequest != nil {
		out.CostPerRequest = Ptr(*u.CostPerRequest)
	}
	if u.CostPerUnit != nil {
		out.CostPerUnit = Ptr(*u.CostPerUnit)
	}
	if u.UnitsBlockSize != nil {
		out.UnitsBlockSize = Ptr(*u.UnitsBlockSize)
	}
	if u.UnitsMinimum != nil {
		out.UnitsMinimum = Ptr(*u.UnitsMinimum)
	}
	return &out
}

// Clone returns a copy that shares no mutable state with c.
func (c *GenericProviderConfig) Clone() *GenericProviderConfig {
	if c == nil {
		return nil
	}
	out := *c
	if c.Auth.AWS != nil {
		aws := *c.Auth.AWS
		out.Auth.AWS = &aws
	}
	out.Usage = c.Usage.clone()
	if c.Streaming != nil {
		streaming := *c.Streaming
		streaming.Paths = append([]string(nil), c.Streaming.Paths...)
		out.Streaming = &streaming
	}
	if c.Routes != nil {
		out.Routes = make([]GenericRouteConfig, len(c.Routes))
		for i, route := range c.Routes {
			out.Routes[i] = route
			if route.Headers != nil {
				out.Routes[i].Headers = make(map[string]string, len(route.Headers))
				for k, v := range route.Headers {
					out.Routes[i].Headers[k] = v
				}
			}
			if route.Billable != nil {
				out.Routes[i].Billable = Ptr(*route.Billable)
			}
			if route.Stream != nil {
				out.Routes[i].Stream = Ptr(*route.Stream)
			}
			if route.Job != nil {
				job := *route.Job
				job.Succeeded = append([]string(nil), route.Job.Succeeded...)
				job.Failed = append([]string(nil), route.Job.Failed...)
				if route.Job.Poll.Headers != nil {
					job.Poll.Headers = make(map[string]string, len(route.Job.Poll.Headers))
					for k, v := range route.Job.Poll.Headers {
						job.Poll.Headers[k] = v
					}
				}
				job.Usage = route.Job.Usage.clone()
				out.Routes[i].Job = &job
			}
			out.Routes[i].Usage = route.Usage.clone()
		}
	}
	return &out
}
