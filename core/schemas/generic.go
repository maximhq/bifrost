package schemas

import (
	"fmt"
	"path"
	"regexp"
	"strings"
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

var genericAWSServiceRe = regexp.MustCompile(`^[a-z0-9-]+$`)

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

// GenericAWSAuthConfig names the AWS service a SigV4-signed relay targets.
type GenericAWSAuthConfig struct {
	Service string `json:"service"`          // SigV4 signing name, e.g. textract, translate
	Region  string `json:"region,omitempty"` // falls back to the key's bedrock_key_config.region
}

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
// an empty path keeps the optimistic default for that field.
type GenericUsageConfig struct {
	ModelPath                string              `json:"model_path,omitempty"`         // request body path for the model (default "model")
	InputTokensPath          string              `json:"input_tokens_path,omitempty"`  // response body path for input tokens
	OutputTokensPath         string              `json:"output_tokens_path,omitempty"` // response body path for output tokens
	TotalTokensPath          string              `json:"total_tokens_path,omitempty"`  // response body path for total tokens
	CostPath                 string              `json:"cost_path,omitempty"`          // response body path for a provider-reported cost (arrays are summed)
	CostPerRequest           *float64            `json:"cost_per_request,omitempty"`   // flat USD fee added to every billable call; a route may set 0 to waive the provider fee
	UnitsPath                string              `json:"units_path,omitempty"`         // path of the billable quantity (pages, credits, text)
	UnitsSource              GenericUnitsSource  `json:"units_source,omitempty"`       // response (default) or request
	UnitsMeasure             GenericUnitsMeasure `json:"units_measure,omitempty"`      // value (default), chars, body_bytes or body_chars
	CostPerUnit              *float64            `json:"cost_per_unit,omitempty"`      // USD per unit; cost += units * cost_per_unit
	DisableOptimisticParsing bool                `json:"disable_optimistic_parsing"`   // only the configured paths are read
}

// GenericStreamingConfig decides which relayed requests are served as streams.
type GenericStreamingConfig struct {
	Paths                  []string `json:"paths,omitempty"`           // route globs that always stream
	DetectFromAcceptHeader bool     `json:"detect_from_accept_header"` // Accept: text/event-stream selects streaming
}

// GenericRouteConfig overrides provider defaults for one upstream operation.
type GenericRouteConfig struct {
	Method   string              `json:"method"`             // GET | POST | PUT | PATCH | DELETE | HEAD
	Path     string              `json:"path"`               // segment glob; a trailing /** matches the prefix and everything below
	Billable *bool               `json:"billable,omitempty"` // false: the call is relayed but not counted or charged
	Stream   *bool               `json:"stream,omitempty"`   // forces the streaming or buffered handler
	Usage    *GenericUsageConfig `json:"usage,omitempty"`    // field-by-field override of the provider usage mapping
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
		if err := route.Usage.validate(fmt.Sprintf("routes[%d].usage", i), c.Usage); err != nil {
			return err
		}
	}
	return nil
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
		if strings.ContainsAny(a.HeaderName, " \t\r\n:") {
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
	if u.CostPerUnit != nil && *u.CostPerUnit > 0 && !unitsMeasureIsWholeBody(unitsMeasure) && unitsPath == "" {
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
	if c.Usage != nil {
		usage := *c.Usage
		if c.Usage.CostPerRequest != nil {
			usage.CostPerRequest = Ptr(*c.Usage.CostPerRequest)
		}
		if c.Usage.CostPerUnit != nil {
			usage.CostPerUnit = Ptr(*c.Usage.CostPerUnit)
		}
		out.Usage = &usage
	}
	if c.Streaming != nil {
		streaming := *c.Streaming
		streaming.Paths = append([]string(nil), c.Streaming.Paths...)
		out.Streaming = &streaming
	}
	if c.Routes != nil {
		out.Routes = make([]GenericRouteConfig, len(c.Routes))
		for i, route := range c.Routes {
			out.Routes[i] = route
			if route.Billable != nil {
				out.Routes[i].Billable = Ptr(*route.Billable)
			}
			if route.Stream != nil {
				out.Routes[i].Stream = Ptr(*route.Stream)
			}
			if route.Usage != nil {
				usage := *route.Usage
				if route.Usage.CostPerRequest != nil {
					usage.CostPerRequest = Ptr(*route.Usage.CostPerRequest)
				}
				if route.Usage.CostPerUnit != nil {
					usage.CostPerUnit = Ptr(*route.Usage.CostPerUnit)
				}
				out.Routes[i].Usage = &usage
			}
		}
	}
	return &out
}
