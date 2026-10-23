package generic

import (
	"errors"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// UsagePreviewInput is one hypothetical relayed call: what the caller would send and what the
// upstream would answer. Bodies are the operator's own samples, never fetched from anywhere.
type UsagePreviewInput struct {
	Method              string `json:"method"`
	Path                string `json:"path"`
	Accept              string `json:"accept,omitempty"`
	RequestContentType  string `json:"request_content_type,omitempty"`
	RequestBody         string `json:"request_body,omitempty"`
	StatusCode          int    `json:"status_code,omitempty"` // default 200
	ResponseContentType string `json:"response_content_type,omitempty"`
	ResponseBody        string `json:"response_body,omitempty"`
}

// UsagePreview is what the relay would record for that call: the route it matched, whether it
// counts, what it read from each body, and the cost those numbers add up to.
type UsagePreview struct {
	RouteIndex     *int                        `json:"route_index"`   // index into routes[], null when none matched
	RouteAllowed   bool                        `json:"route_allowed"` // false under strict_routes with no match
	Billable       bool                        `json:"billable"`      // the route's or the method default
	Streams        bool                        `json:"streams"`       // served by the streaming relay
	Counted        bool                        `json:"counted"`       // reports usage: billable and a 2xx status
	ModelPath      string                      `json:"model_path"`    // where the model is read from, "" for none
	Model          string                      `json:"model"`         // what that path yielded from the request body
	UnitsSource    schemas.GenericUnitsSource  `json:"units_source"`
	UnitsMeasure   schemas.GenericUnitsMeasure `json:"units_measure"`
	UnitsPath      string                      `json:"units_path"`
	Units          *float64                    `json:"units"` // quantity read, null when none
	CostPerUnit    float64                     `json:"cost_per_unit"`
	CostPerRequest float64                     `json:"cost_per_request"`
	ExtractedCost  *float64                    `json:"extracted_cost"` // cost_path or usage.cost, null when none
	Tokens         *schemas.BifrostLLMUsage    `json:"tokens"`         // token paths, null when none
	Cost           float64                     `json:"cost"`           // what the log row and budgets would carry
	Notes          []string                    `json:"notes"`          // why a field came out empty
}

// PreviewUsage runs one hypothetical call through the same route matching and usage mapping the
// relay applies, so an operator can check a mapping against a sample before saving it. It builds
// no client and sends nothing. Config-time only, never on the request path.
func PreviewUsage(cfg *schemas.GenericProviderConfig, in UsagePreviewInput) (*UsagePreview, error) {
	if cfg == nil {
		cfg = &schemas.GenericProviderConfig{}
	} else {
		cfg = cfg.Clone()
	}
	if cfg.Auth.Type == "" {
		cfg.Auth.Type = schemas.GenericAuthBearer
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	if method == "" {
		method = "POST"
	}
	path := in.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	status := in.StatusCode
	if status == 0 {
		status = 200
	}
	if in.ResponseContentType == "" && in.ResponseBody != "" && !gjson.Valid(in.ResponseBody) {
		return nil, errors.New("response_content_type is required when the response body is not JSON")
	}

	provider := &GenericProvider{cfg: cfg, usage: resolveUsage(cfg.Usage, nil), routes: compileRoutes(cfg), streamGlobs: compileStreamGlobs(cfg)}
	route := provider.matchRoute(method, path)
	usage, billable := provider.usage, billableByDefault(method, nil)
	out := &UsagePreview{RouteAllowed: !cfg.StrictRoutes || route != nil}
	if route != nil {
		usage, billable = route.usage, route.billable
		for i := range provider.routes {
			if &provider.routes[i] == route {
				out.RouteIndex = schemas.Ptr(i)
			}
		}
	}
	reqBody, respBody := []byte(in.RequestBody), []byte(in.ResponseBody)
	bodyStream := len(reqBody) > 0 && isJSONContentType(in.RequestContentType) && gjson.GetBytes(reqBody, "stream").Type == gjson.True
	out.Billable = billable
	out.Streams = provider.streams(route, path, in.Accept, bodyStream)
	out.ModelPath = usage.effectiveModelPath()
	out.UnitsSource, out.UnitsMeasure, out.UnitsPath = usage.unitsSource, usage.unitsMeasure, usage.unitsPath
	out.CostPerUnit, out.CostPerRequest = usage.costPerUnit, usage.costPerRequest
	out.Notes = []string{}

	if out.ModelPath != "" && len(reqBody) > 0 {
		if isJSONContentType(in.RequestContentType) || (in.RequestContentType == "" && gjson.ValidBytes(reqBody)) {
			if v := gjson.GetBytes(reqBody, out.ModelPath); v.Type == gjson.String {
				out.Model = strings.TrimSpace(v.Str)
			} else {
				out.Notes = append(out.Notes, "no string at model path "+out.ModelPath+" in the request body")
			}
		} else {
			out.Notes = append(out.Notes, "the request body is not JSON, so no model is read")
		}
	}
	if !out.RouteAllowed {
		out.Notes = append(out.Notes, "strict_routes is on and no route matches: the call is refused with 404")
		return out, nil
	}
	if !billable {
		out.Notes = append(out.Notes, "the route is not billable: the call is relayed but neither counted nor charged")
		return out, nil
	}
	if status < 200 || status >= 300 {
		out.Notes = append(out.Notes, "a non-2xx response reports nothing: not counted, not charged")
		return out, nil
	}
	out.Counted = true

	fields := usage.requestUnits(in.RequestContentType, reqBody)
	if usage.billsUnits() && usage.unitsSource == schemas.GenericUnitsFromRequest && !fields.hasUnits {
		out.Notes = append(out.Notes, "no quantity could be read from the request body with measure "+string(usage.unitsMeasure)+noPath(usage))
	}
	if out.Streams && !isEventStream(in.ResponseContentType) {
		out.Notes = append(out.Notes, "a non-SSE stream is forwarded unread: nothing is taken from the response body")
	} else {
		parsed := usage.parse(in.ResponseContentType, respBody)
		fields.merge(parsed)
		if usage.billsUnits() && usage.unitsSource == schemas.GenericUnitsFromResponse && !parsed.hasUnits {
			out.Notes = append(out.Notes, "no quantity could be read from the response body with measure "+string(usage.unitsMeasure)+noPath(usage))
		}
		if len(respBody) > 0 && !(isJSONContentType(in.ResponseContentType) || (in.ResponseContentType == "" && gjson.ValidBytes(respBody))) && usage.unitsMeasure != schemas.GenericUnitsBodyBytes && usage.unitsMeasure != schemas.GenericUnitsBodyChars {
			out.Notes = append(out.Notes, "the response body is not JSON, so token and cost paths are not read")
		}
		if !parsed.hasInput && !parsed.hasOutput && !parsed.hasTotal && len(respBody) > 0 && (usage.tokensConfigured() || usage.optimistic) {
			out.Notes = append(out.Notes, "no token counts found in the response body")
		}
		if !parsed.hasCost && usage.costPath != "" {
			out.Notes = append(out.Notes, "nothing at cost path "+usage.costPath+" in the response body")
		}
	}
	if fields.hasUnits {
		out.Units = schemas.Ptr(fields.units)
	}
	if fields.hasCost {
		out.ExtractedCost = schemas.Ptr(fields.cost)
	}
	built := usage.build(fields)
	out.Tokens = built.LLMUsage
	if built.Cost != nil {
		out.Cost = built.Cost.TotalCost
	}
	if out.Cost == 0 {
		out.Notes = append(out.Notes, "cost is 0: the call counts one request against rate limits but charges no budget")
	}
	return out, nil
}

func noPath(u resolvedUsage) string {
	if u.unitsPath == "" {
		return " and no units path"
	}
	return " at path " + u.unitsPath
}
