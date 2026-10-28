package schemas

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// RelayAuthType is how a relay presents its credential to the upstream.
type RelayAuthType string

const (
	RelayAuthBearer   RelayAuthType = "bearer"    // Authorization: Bearer <value>
	RelayAuthHeader   RelayAuthType = "header"    // <header_name>: <value_prefix><value>
	RelayAuthQuery    RelayAuthType = "query"     // ?<query_param>=<value>
	RelayAuthBasic    RelayAuthType = "basic"     // Authorization: Basic base64(<value>), value is user:password
	RelayAuthAWSSigV4 RelayAuthType = "aws_sigv4" // SigV4 with the endpoint's credential.aws
	RelayAuthNone     RelayAuthType = "none"      // no credential injected
)

var endpointAuthTypes = map[RelayAuthType]struct{}{
	RelayAuthBearer: {}, RelayAuthHeader: {}, RelayAuthQuery: {}, RelayAuthBasic: {}, RelayAuthAWSSigV4: {}, RelayAuthNone: {},
}

// endpointNameRe is the endpoint names the /relay/{endpoint} route serves; an endpoint is
// refused at creation when its name falls outside it, so it can never be unreachable.
var endpointNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// ValidRelayName reports whether name can be served at /relay/{name}. "." and ".." match the
// pattern but are dot segments routers collapse, so a relay named either could never be reached.
func ValidRelayName(name string) bool {
	return name != "." && name != ".." && endpointNameRe.MatchString(name)
}

var endpointAWSServiceRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// endpointCredentialHeaders are the credential headers IsSensitiveHeader does not name: Azure
// Functions and API Management keys. A caller's value is dropped before forwarding, and an
// operator's extra header under these names is a credential that management responses mask.
var endpointCredentialHeaders = map[string]struct{}{
	"ocp-apim-subscription-key": {}, "subscription-key": {}, "x-functions-key": {},
}

// isEndpointCredentialHeader reports whether name carries a credential on the relay path.
func isEndpointCredentialHeader(name string) bool {
	if IsSensitiveHeader(name) {
		return true
	}
	_, ok := endpointCredentialHeaders[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

var endpointRouteMethods = map[string]struct{}{
	"GET": {}, "POST": {}, "PUT": {}, "PATCH": {}, "DELETE": {}, "HEAD": {},
}

// RelayAuthConfig selects the credential scheme for a relay.
type RelayAuthConfig struct {
	Type        RelayAuthType       `json:"type"`                   // bearer | header | query | basic | aws_sigv4 | none (default bearer)
	HeaderName  string              `json:"header_name,omitempty"`  // header: the header that carries the key
	QueryParam  string              `json:"query_param,omitempty"`  // query: the query parameter that carries the key
	ValuePrefix string              `json:"value_prefix,omitempty"` // header: literal prefix before the key value
	AWS         *RelayAWSAuthConfig `json:"aws,omitempty"`          // aws_sigv4 settings
}

// RelayAWSAuthConfig names the AWS service a SigV4-signed relay targets. The signing region
// is the endpoint's credential.aws.region.
type RelayAWSAuthConfig struct {
	Service string `json:"service"` // SigV4 signing name, e.g. textract, translate
}

// RelayModelSource names where in the caller's request the model is read.
type RelayModelSource string

const (
	RelayModelFromBody  RelayModelSource = "body"  // default: a JSON field of the request body (model, model_id)
	RelayModelFromQuery RelayModelSource = "query" // a query parameter (Deepgram's ?model=nova-3)
)

// RelayUnitsSource names the body a billable quantity is read from.
type RelayUnitsSource string

const (
	RelayUnitsFromResponse RelayUnitsSource = "response" // default: a count the upstream reports (Textract pages)
	RelayUnitsFromRequest  RelayUnitsSource = "request"  // what the caller sent, which the upstream bills on (Translate text)
)

// RelayUnitsMeasure names how the quantity is taken from the body.
type RelayUnitsMeasure string

const (
	RelayUnitsValue     RelayUnitsMeasure = "value"      // default: the number at units_path
	RelayUnitsChars     RelayUnitsMeasure = "chars"      // the character count of the string at units_path
	RelayUnitsBodyBytes RelayUnitsMeasure = "body_bytes" // the byte length of the whole body; units_path is ignored
	RelayUnitsBodyChars RelayUnitsMeasure = "body_chars" // the character (rune) count of the whole body, any content type; units_path is ignored
)

// RelayUsageConfig maps a relayed request/response onto usage and cost. Paths are gjson paths;
// an empty path reads nothing: only the fields a mapping names are read.
type RelayUsageConfig struct {
	ModelPath        string            `json:"model_path,omitempty"`         // where the model is read: a request body path, or a query parameter with model_source query
	ModelSource      RelayModelSource  `json:"model_source,omitempty"`       // body (default) or query
	InputTokensPath  string            `json:"input_tokens_path,omitempty"`  // response body path for input tokens
	OutputTokensPath string            `json:"output_tokens_path,omitempty"` // response body path for output tokens
	TotalTokensPath  string            `json:"total_tokens_path,omitempty"`  // response body path for total tokens
	CostPath         string            `json:"cost_path,omitempty"`          // response body path for a provider-reported cost (arrays are summed)
	CostPerRequest   *float64          `json:"cost_per_request,omitempty"`   // flat USD fee added to every billable call; a route may set 0 to waive the endpoint fee
	UnitsPath        string            `json:"units_path,omitempty"`         // path of the billable quantity (pages, credits, text)
	UnitsSource      RelayUnitsSource  `json:"units_source,omitempty"`       // response (default) or request
	UnitsMeasure     RelayUnitsMeasure `json:"units_measure,omitempty"`      // value (default), chars, body_bytes or body_chars
	CostPerUnit      *float64          `json:"cost_per_unit,omitempty"`      // USD per unit; cost += units * cost_per_unit
	UnitsBlockSize   *float64          `json:"units_block_size,omitempty"`   // bill per started block of this many raw units (100 characters); default 1, exact
	UnitsMinimum     *float64          `json:"units_minimum,omitempty"`      // least number of billed units once a quantity was read; default 0
}

// RelayStreamingConfig decides which relayed requests are served as streams.
type RelayStreamingConfig struct {
	Paths []string `json:"paths,omitempty"` // route globs that always stream
}

// RelayExtraHeaderConflict returns an extra_headers name that appears under two spellings
// (X-Amz-Target and x-amz-target). An endpoint matches routes on the operator's value of a
// header, so two spellings could approve one operation while the other is sent.
func RelayExtraHeaderConflict(headers map[string]SecretVar) (string, bool) {
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

// RelayJobConfig makes a route the start of a provider-side job that is billed once it
// completes, out of band: the job id is read from the start response, the upstream is polled
// with the given request until the status is terminal, and the terminal response is priced with
// the job's usage mapping. The start call itself still counts as one request and carries the
// route's own fee. Settlement runs on the job accounting sweeper, with the endpoint's credential,
// and charges the budgets the start call was subject to.
type RelayJobConfig struct {
	IDPath          string            `json:"id_path"`                    // start response path of the job id (Textract: JobId)
	Poll            RelayJobPoll      `json:"poll"`                       // the request that reports where the job stands
	StatusPath      string            `json:"status_path"`                // poll response path of the status (JobStatus)
	Succeeded       []string          `json:"succeeded"`                  // statuses that end the job with work to bill
	Failed          []string          `json:"failed,omitempty"`           // statuses that end the job at no charge
	IntervalSeconds int               `json:"interval_seconds,omitempty"` // seconds between polls (default 30)
	TimeoutSeconds  int               `json:"timeout_seconds,omitempty"`  // give up polling after this long (default 3600)
	Usage           *RelayUsageConfig `json:"usage,omitempty"`            // terminal poll mapping over the route's; only its own cost_per_request is charged
}

// RelayJobPoll is the request the sweeper sends to check a job. {{job.id}} in the path, a
// header value or the body is replaced with the job id.
type RelayJobPoll struct {
	Method      string            `json:"method,omitempty"`       // default POST
	Path        string            `json:"path"`                   // rooted path, may contain {{job.id}}
	Headers     map[string]string `json:"headers,omitempty"`      // literal headers, values may contain {{job.id}}
	Body        string            `json:"body,omitempty"`         // literal body, may contain {{job.id}}
	ContentType string            `json:"content_type,omitempty"` // default application/json when a body is set
}

// JobIDPlaceholder is replaced with the job id in a poll request.
const JobIDPlaceholder = "{{job.id}}"

// RelayScopedJobID ties an upstream job id to the call that started it, for the job row and its
// settlement. Some APIs let the caller choose the id and reuse it after a delete (Azure batch
// synthesis), so the upstream id alone would make a second job land on the first job's row and
// never be billed. startID is the start call's log id, which can be the caller's own x-request-id,
// so it is query-escaped: a ':' in it becomes %3A, and the first ':' is always the separator.
func RelayScopedJobID(startID, upstreamID string) string {
	// The separator is written even with no start id, so the split below always round-trips an
	// upstream id that itself carries ':' (an ARN).
	return url.QueryEscape(startID) + ":" + upstreamID
}

// RelayUpstreamJobID returns the upstream part of a scoped job id, the id the poll is sent with.
func RelayUpstreamJobID(scoped string) string {
	if _, upstream, ok := strings.Cut(scoped, ":"); ok {
		return upstream
	}
	return scoped
}

// RelayRouteConfig overrides the endpoint defaults for one upstream operation.
type RelayRouteConfig struct {
	Method   string            `json:"method"`             // GET | POST | PUT | PATCH | DELETE | HEAD
	Path     string            `json:"path"`               // segment glob; a trailing /** matches the prefix and everything below
	Headers  map[string]string `json:"headers,omitempty"`  // request headers that must also match: name (case-insensitive) to value glob (X-Amz-Target: Textract.Get*)
	Enabled  *bool             `json:"enabled,omitempty"`  // false: requests matching the route are refused with 404; nil is enabled
	Billable *bool             `json:"billable,omitempty"` // false: the call is relayed but not counted or charged
	Stream   *bool             `json:"stream,omitempty"`   // forces the streaming or buffered handler
	Usage    *RelayUsageConfig `json:"usage,omitempty"`    // field-by-field override of the endpoint usage mapping
	Job      *RelayJobConfig   `json:"-"`                  // not configurable yet: async jobs are kept for a later release
}

// RelayConfig is one relay: a raw HTTP relay to a non-LLM API served at
// /relay/{name}/{path}. Bifrost injects the endpoint's single credential, strips the caller's,
// pins the upstream host, prices each call with the usage mapping, and charges the result to the
// caller's overall budgets. It is not a provider: no key selection, routing rules or model budgets.
type RelayConfig struct {
	Name               string                `json:"name"`                           // served at /relay/{name}; immutable once created
	Description        string                `json:"description,omitempty"`          // free text shown in the UI
	BaseURL            string                `json:"base_url"`                       // upstream origin and base path every request is pinned under
	Auth               RelayAuthConfig       `json:"auth"`                           // how the credential is presented
	Credential         *RelayCredential      `json:"credential,omitempty"`           // the one secret the endpoint signs in with; nil for auth none
	ExtraHeaders       map[string]SecretVar  `json:"extra_headers,omitempty"`        // operator headers sent on every request; a caller cannot replace them
	Network            *RelayNetworkConfig   `json:"network,omitempty"`              // timeouts and connection limits
	ProxyConfig        *ProxyConfig          `json:"proxy_config,omitempty"`         // outbound proxy; nil inherits the global proxy when it is enabled for inference
	Usage              *RelayUsageConfig     `json:"usage,omitempty"`                // endpoint-level usage mapping every route inherits
	Streaming          *RelayStreamingConfig `json:"streaming,omitempty"`            // which requests are relayed as streams
	Routes             []RelayRouteConfig    `json:"routes,omitempty"`               // upstream operations, first match wins
	SkipRouteCheck     bool                  `json:"skip_route_check,omitempty"`     // relay requests matching no routes[] entry instead of refusing them with 404
	IgnoreUpstreamCost bool                  `json:"ignore_upstream_cost,omitempty"` // drop cost_path values; the operator's fee and unit pricing still apply
}

// RelayCredential is the single secret an endpoint signs in with. Value carries the token for
// bearer, header, query and basic (user:password) auth; AWS carries the credentials for aws_sigv4.
type RelayCredential struct {
	Value *SecretVar          `json:"value,omitempty"`
	AWS   *RelayAWSCredential `json:"aws,omitempty"`
}

// RelayAWSCredential is a SigV4 credential. With no access key the default AWS credential chain
// is used; RoleARN assumes a role first.
type RelayAWSCredential struct {
	AccessKey    *SecretVar `json:"access_key,omitempty"`
	SecretKey    *SecretVar `json:"secret_key,omitempty"`
	SessionToken *SecretVar `json:"session_token,omitempty"`
	Region       *SecretVar `json:"region"` // signing region, required
	RoleARN      *SecretVar `json:"role_arn,omitempty"`
	ExternalID   *SecretVar `json:"external_id,omitempty"`
	SessionName  *SecretVar `json:"session_name,omitempty"`
}

// RelayNetworkConfig bounds the upstream connection. Zero values take the defaults below.
type RelayNetworkConfig struct {
	TimeoutSeconds             int    `json:"timeout_seconds,omitempty"`                // whole-response timeout for buffered calls (default 60)
	StreamIdleTimeoutInSeconds int    `json:"stream_idle_timeout_in_seconds,omitempty"` // max silence between stream chunks (default 60)
	MaxConnsPerHost            int    `json:"max_conns_per_host,omitempty"`             // default 5000
	InsecureSkipVerify         bool   `json:"insecure_skip_verify,omitempty"`           // skip upstream TLS verification
	CACertPEM                  string `json:"ca_cert_pem,omitempty"`                    // extra CA to trust for the upstream
}

// Default network bounds for an endpoint.
const (
	DefaultEndpointTimeoutSeconds           = 60
	DefaultEndpointStreamIdleTimeoutSeconds = 60
	DefaultEndpointMaxConnsPerHost          = 5000
)

// AuthType is the configured auth type, bearer when unset.
func (c *RelayConfig) AuthType() RelayAuthType {
	if c == nil || c.Auth.Type == "" {
		return RelayAuthBearer
	}
	return c.Auth.Type
}

// ValidateEndpoint checks everything Validate checks plus the endpoint-level fields: the name, the
// base URL, the credential the auth type needs, and the extra headers.
func (c *RelayConfig) ValidateEndpoint() error {
	if c == nil {
		return fmt.Errorf("relay config is required")
	}
	if !ValidRelayName(c.Name) {
		return fmt.Errorf("name %q must match [A-Za-z0-9._-]{1,64}", c.Name)
	}
	base, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return fmt.Errorf("base_url %q must be an absolute http(s) URL", c.BaseURL)
	}
	if base.RawQuery != "" || base.Fragment != "" || base.User != nil {
		return fmt.Errorf("base_url must not carry a query, fragment or user info")
	}
	if name, dup := RelayExtraHeaderConflict(c.ExtraHeaders); dup {
		return fmt.Errorf("extra_headers names %q under two spellings", name)
	}
	for name := range c.ExtraHeaders {
		if !httpguts.ValidHeaderFieldName(name) {
			return fmt.Errorf("extra_headers: %q is not a valid header name", name)
		}
	}
	switch c.AuthType() {
	case RelayAuthNone:
	case RelayAuthAWSSigV4:
		if c.Credential == nil || c.Credential.AWS == nil || c.Credential.AWS.Region == nil || strings.TrimSpace(c.Credential.AWS.Region.GetValue()) == "" {
			return fmt.Errorf("credential.aws.region is required for auth.type aws_sigv4")
		}
	default:
		if c.Credential == nil || c.Credential.Value == nil || (strings.TrimSpace(c.Credential.Value.GetValue()) == "" && !c.Credential.Value.IsFromEnv()) {
			return fmt.Errorf("credential.value is required for auth.type %s", c.AuthType())
		}
	}
	if n := c.Network; n != nil && (n.TimeoutSeconds < 0 || n.StreamIdleTimeoutInSeconds < 0 || n.MaxConnsPerHost < 0) {
		return fmt.Errorf("network: timeouts and max_conns_per_host must not be negative")
	}
	return c.Validate()
}

// Redacted returns a copy whose credential, secret extra headers and proxy secrets are redacted,
// for API responses.
func (c *RelayConfig) Redacted() *RelayConfig {
	if c == nil {
		return nil
	}
	out := c.Clone()
	if out.Credential != nil {
		out.Credential = out.Credential.redacted()
	}
	if len(out.ExtraHeaders) > 0 {
		headers := make(map[string]SecretVar, len(out.ExtraHeaders))
		for k, v := range out.ExtraHeaders {
			v := v
			// A literal value is shown unless the header carries a credential (X-Api-Key, Authorization,
			// X-Functions-Key).
			if isEndpointCredentialHeader(k) {
				headers[k] = *v.Redacted()
			} else {
				headers[k] = *v.RedactedIfSecret()
			}
		}
		out.ExtraHeaders = headers
	}
	// The provider proxy redaction: a proxy URL can embed credentials and the password is fully masked.
	out.ProxyConfig = out.ProxyConfig.Redacted()
	return out
}

func (c *RelayCredential) redacted() *RelayCredential {
	out := &RelayCredential{Value: c.Value.Redacted()}
	if c.AWS != nil {
		out.AWS = &RelayAWSCredential{
			AccessKey:    c.AWS.AccessKey.Redacted(),
			SecretKey:    c.AWS.SecretKey.Redacted(),
			SessionToken: c.AWS.SessionToken.Redacted(),
			Region:       c.AWS.Region,
			RoleARN:      c.AWS.RoleARN,
			ExternalID:   c.AWS.ExternalID.Redacted(),
			SessionName:  c.AWS.SessionName,
		}
	}
	return out
}

// MergeRedacted keeps stored secrets where an update sends back their redacted form, so a UI that
// only ever sees redacted values can save without wiping the credential.
func (c *RelayConfig) MergeRedacted(stored *RelayConfig) {
	if c == nil || stored == nil {
		return
	}
	// Only a display placeholder keeps the stored value; an env. or vault reference is a real
	// change (IsRedacted treats every reference as redacted), as the Agent Gateway does.
	keep := func(in, old *SecretVar) *SecretVar {
		if in.IsMaskedPlaceholder() && old != nil {
			return old
		}
		return in
	}
	if c.Credential != nil && stored.Credential != nil {
		c.Credential.Value = keep(c.Credential.Value, stored.Credential.Value)
		if c.Credential.AWS != nil && stored.Credential.AWS != nil {
			a, o := c.Credential.AWS, stored.Credential.AWS
			a.AccessKey = keep(a.AccessKey, o.AccessKey)
			a.SecretKey = keep(a.SecretKey, o.SecretKey)
			a.SessionToken = keep(a.SessionToken, o.SessionToken)
			a.ExternalID = keep(a.ExternalID, o.ExternalID)
		}
	}
	for name, v := range c.ExtraHeaders {
		v := v
		if old, ok := stored.ExtraHeaders[name]; ok && v.IsMaskedPlaceholder() {
			c.ExtraHeaders[name] = old
		}
	}
	if c.ProxyConfig != nil && stored.ProxyConfig != nil {
		p, o := c.ProxyConfig, stored.ProxyConfig
		p.URL = keep(p.URL, o.URL)
		p.Username = keep(p.Username, o.Username)
		p.Password = keep(p.Password, o.Password)
		p.CACertPEM = keep(p.CACertPEM, o.CACertPEM)
	}
}

// Validate checks the config is internally consistent. It runs when an endpoint is created or
// updated, never on the request path.
func (c *RelayConfig) Validate() error {
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
			if err := validateEndpointRouteGlob(glob); err != nil {
				return fmt.Errorf("streaming.paths[%d]: %w", i, err)
			}
		}
	}
	for i, route := range c.Routes {
		if _, ok := endpointRouteMethods[strings.ToUpper(route.Method)]; !ok {
			return fmt.Errorf("routes[%d]: unsupported method %q", i, route.Method)
		}
		if err := validateEndpointRouteGlob(route.Path); err != nil {
			return fmt.Errorf("routes[%d]: %w", i, err)
		}
		seenHeaders := make(map[string]struct{}, len(route.Headers))
		for name, glob := range route.Headers {
			if !httpguts.ValidHeaderFieldName(name) {
				return fmt.Errorf("routes[%d].headers: %q is not a valid header name", i, name)
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

func (j *RelayJobConfig) validate(field string, route RelayRouteConfig, base *RelayUsageConfig) error {
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
		if _, ok := endpointRouteMethods[strings.ToUpper(j.Poll.Method)]; !ok {
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
	// The job's mapping inherits route over endpoint, field by field, so a job that sets only a
	// rate is valid when either of them names the units path.
	merged := &RelayUsageConfig{}
	for _, u := range []*RelayUsageConfig{base, route.Usage} {
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
	var source RelayUnitsSource
	var rate *float64
	for _, u := range []*RelayUsageConfig{base, route.Usage, j.Usage} {
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
	if source == RelayUnitsFromRequest && rate != nil && *rate > 0 {
		return fmt.Errorf("%s.usage: a job is priced from its terminal poll response, so request-side units are not billed at settlement; set units_source: response on the job", field)
	}
	return j.Usage.validate(field+".usage", merged)
}

func (a RelayAuthConfig) validate() error {
	if a.Type == "" {
		return nil
	}
	if _, ok := endpointAuthTypes[a.Type]; !ok {
		return fmt.Errorf("auth.type: unsupported value %q", a.Type)
	}
	switch a.Type {
	case RelayAuthHeader:
		if strings.TrimSpace(a.HeaderName) == "" {
			return fmt.Errorf("auth.header_name is required for auth.type header")
		}
		if !httpguts.ValidHeaderFieldName(a.HeaderName) {
			return fmt.Errorf("auth.header_name %q is not a valid header name", a.HeaderName)
		}
	case RelayAuthQuery:
		if strings.TrimSpace(a.QueryParam) == "" {
			return fmt.Errorf("auth.query_param is required for auth.type query")
		}
	case RelayAuthAWSSigV4:
		if a.AWS == nil || strings.TrimSpace(a.AWS.Service) == "" {
			return fmt.Errorf("auth.aws.service is required for auth.type aws_sigv4")
		}
		if !endpointAWSServiceRe.MatchString(a.AWS.Service) {
			return fmt.Errorf("auth.aws.service %q must match [a-z0-9-]+", a.AWS.Service)
		}
	}
	return nil
}

// validate checks one usage block. base is the endpoint block a route inherits from, so a route
// that sets only cost_per_unit is judged on the merged units_path and units_measure.
func (u *RelayUsageConfig) validate(field string, base *RelayUsageConfig) error {
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
	case "", RelayModelFromBody, RelayModelFromQuery:
	default:
		return fmt.Errorf("%s.model_source: unsupported value %q", field, u.ModelSource)
	}
	switch u.UnitsSource {
	case "", RelayUnitsFromResponse, RelayUnitsFromRequest:
	default:
		return fmt.Errorf("%s.units_source: unsupported value %q", field, u.UnitsSource)
	}
	switch u.UnitsMeasure {
	case "", RelayUnitsValue, RelayUnitsChars, RelayUnitsBodyBytes, RelayUnitsBodyChars:
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
	// would otherwise keep the endpoint's rate and silently bill no units.
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
func unitsMeasureIsWholeBody(m RelayUnitsMeasure) bool {
	return m == RelayUnitsBodyBytes || m == RelayUnitsBodyChars
}

// validateEndpointRouteGlob accepts a rooted segment glob: "/a/*/b", "/a/**". path.Match reports
// syntax errors for a malformed segment pattern.
func validateEndpointRouteGlob(glob string) error {
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
func (u *RelayUsageConfig) clone() *RelayUsageConfig {
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
func (c *RelayConfig) Clone() *RelayConfig {
	if c == nil {
		return nil
	}
	out := *c
	// Every SecretVar is copied too, so a live relay built from the clone shares no credential
	// with the config the caller keeps editing.
	if c.Credential != nil {
		cred := RelayCredential{Value: c.Credential.Value.Clone()}
		if c.Credential.AWS != nil {
			cred.AWS = &RelayAWSCredential{
				AccessKey: c.Credential.AWS.AccessKey.Clone(), SecretKey: c.Credential.AWS.SecretKey.Clone(), SessionToken: c.Credential.AWS.SessionToken.Clone(),
				Region: c.Credential.AWS.Region.Clone(), RoleARN: c.Credential.AWS.RoleARN.Clone(), ExternalID: c.Credential.AWS.ExternalID.Clone(), SessionName: c.Credential.AWS.SessionName.Clone(),
			}
		}
		out.Credential = &cred
	}
	if c.ExtraHeaders != nil {
		out.ExtraHeaders = make(map[string]SecretVar, len(c.ExtraHeaders))
		for k, v := range c.ExtraHeaders {
			out.ExtraHeaders[k] = *v.Clone()
		}
	}
	if c.Network != nil {
		network := *c.Network
		out.Network = &network
	}
	if c.ProxyConfig != nil {
		proxy := *c.ProxyConfig
		proxy.URL, proxy.Username, proxy.Password, proxy.CACertPEM = c.ProxyConfig.URL.Clone(), c.ProxyConfig.Username.Clone(), c.ProxyConfig.Password.Clone(), c.ProxyConfig.CACertPEM.Clone()
		out.ProxyConfig = &proxy
	}
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
		out.Routes = make([]RelayRouteConfig, len(c.Routes))
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
			if route.Enabled != nil {
				out.Routes[i].Enabled = Ptr(*route.Enabled)
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
