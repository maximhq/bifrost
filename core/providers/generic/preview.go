package generic

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// UsagePreviewInput is one hypothetical relayed call: what the caller would send and what the
// upstream would answer. Bodies are the operator's own samples, never fetched from anywhere.
type UsagePreviewInput struct {
	Method              string            `json:"method"`
	Path                string            `json:"path"`              // request path; a query string may follow, as in /jobs/1?stream=true
	Headers             map[string]string `json:"headers,omitempty"` // request headers, for routes that match on one
	Accept              string            `json:"accept,omitempty"`  // the Accept header; read from headers when empty
	RequestContentType  string            `json:"request_content_type,omitempty"`
	RequestBody         string            `json:"request_body,omitempty"`
	StatusCode          int               `json:"status_code,omitempty"` // default 200
	ResponseContentType string            `json:"response_content_type,omitempty"`
	ResponseBody        string            `json:"response_body,omitempty"`
}

// UsagePreview is what the relay would record for that call: the route it matched, whether it
// counts, what it read from each body, and the cost those numbers add up to.
type UsagePreview struct {
	RouteIndex     *int                        `json:"route_index"`   // index into routes[], null when none matched
	RouteAllowed   bool                        `json:"route_allowed"` // false when no route matches and skip_route_check is off
	Billable       bool                        `json:"billable"`      // the route's or the method default
	Streams        bool                        `json:"streams"`       // served by the streaming relay
	Counted        bool                        `json:"counted"`       // reports usage: billable and a 2xx status
	ModelPath      string                      `json:"model_path"`    // where the model is read from, "" for none
	ModelSource    schemas.GenericModelSource  `json:"model_source"`  // body or query
	Model          string                      `json:"model"`         // what that path yielded from the request body
	UnitsSource    schemas.GenericUnitsSource  `json:"units_source"`
	UnitsMeasure   schemas.GenericUnitsMeasure `json:"units_measure"`
	UnitsPath      string                      `json:"units_path"`
	Units          *float64                    `json:"units"`        // quantity read, null when none
	UnitsBilled    *float64                    `json:"units_billed"` // after block rounding and the minimum, null when none
	UnitsBlockSize float64                     `json:"units_block_size"`
	UnitsMinimum   float64                     `json:"units_minimum"`
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
// extraHeaders is the provider's network_config.extra_headers: the relay matches routes on the
// operator's value of those headers over the caller's, so the preview does too.
// ignoreProviderCost is the provider's ignore_provider_cost: with it set the relay never reads
// the upstream-reported cost path, so the preview must not either.
func PreviewUsage(cfg *schemas.GenericProviderConfig, extraHeaders map[string]schemas.SecretVar, in UsagePreviewInput, ignoreProviderCost bool) (*UsagePreview, error) {
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
	// The query rides along in the path field the way an operator types it; it is split off here
	// so the routes see the path alone and a GET's ?stream=true counts as the relay counts it.
	path, rawQuery, _ := strings.Cut(in.Path, "?")
	// The transport matches the decoded path (a%2Fb is a/b), so the sample path is decoded too.
	path, err := url.PathUnescape(path)
	if err != nil {
		return nil, fmt.Errorf("path: %w", err)
	}
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

	usage := resolveUsage(cfg.Usage, nil)
	usage.ignoreUpstreamCost = ignoreProviderCost
	usage.eventPaths = usage.collectEventPaths()
	routes := compileRoutes(cfg)
	for i := range routes {
		routes[i].usage.ignoreUpstreamCost = ignoreProviderCost
		routes[i].usage.eventPaths = routes[i].usage.collectEventPaths()
	}
	provider := &GenericProvider{cfg: cfg, usage: usage, routes: routes, streamGlobs: compileStreamGlobs(cfg), operatorHeaders: lowerHeaderValues(extraHeaders)}
	headers := make(map[string]string, len(in.Headers))
	for name, value := range in.Headers {
		headers[strings.ToLower(strings.TrimSpace(name))] = value
	}
	// The sample's own content-type and Accept fields are request headers too, as on the wire.
	if _, set := headers["content-type"]; !set && in.RequestContentType != "" {
		headers["content-type"] = in.RequestContentType
	}
	if _, set := headers["accept"]; !set && in.Accept != "" {
		headers["accept"] = in.Accept
	}
	// One content type for the whole preview, the header the relay reads.
	contentType := headers["content-type"]
	route := provider.matchRoute(method, path, headers)
	usage, billable := provider.usage, billableByDefault(method, nil)
	out := &UsagePreview{RouteAllowed: cfg.SkipRouteCheck || route != nil}
	if route != nil {
		usage, billable = route.usage, route.billable
		for i := range provider.routes {
			if &provider.routes[i] == route {
				out.RouteIndex = schemas.Ptr(i)
			}
		}
	}
	reqBody, respBody := []byte(in.RequestBody), []byte(in.ResponseBody)
	// The transport sniffs a body with no declared content type, so the preview does too.
	bodyStream := (len(reqBody) > 0 && looksJSON(contentType, reqBody) && gjson.GetBytes(reqBody, "stream").Type == gjson.True) || queryStreams(method, rawQuery)
	out.Billable = billable
	// The merged header is the one Accept the relay would see, for routes and streaming alike.
	accept := headers["accept"]
	out.Streams = provider.streams(route, path, accept, bodyStream)
	out.ModelPath = usage.effectiveModelPath()
	out.ModelSource = usage.modelSource
	if out.ModelSource == "" {
		out.ModelSource = schemas.GenericModelFromBody
	}
	out.UnitsSource, out.UnitsMeasure, out.UnitsPath = usage.unitsSource, usage.unitsMeasure, usage.unitsPath
	out.CostPerUnit, out.CostPerRequest = usage.costPerUnit, usage.costPerRequest
	out.UnitsBlockSize, out.UnitsMinimum = usage.unitsBlockSize, usage.unitsMinimum
	out.Notes = []string{}

	if !out.RouteAllowed {
		out.Notes = append(out.Notes, "no route matches and skip_route_check is off: the call is refused with 404")
		return out, nil
	}
	// The model is read the way the transport reads it, so a body the relay refuses is reported.
	if out.ModelPath != "" && out.ModelSource == schemas.GenericModelFromQuery {
		model, err := ReadRequestModel(out.ModelSource, reqBody, contentType, rawQuery, out.ModelPath)
		switch {
		case err != nil:
			out.Notes = append(out.Notes, "the relay refuses this request with 400: "+err.Error())
			return out, nil
		case model != "":
			out.Model = model
		default:
			out.Notes = append(out.Notes, "no "+out.ModelPath+" parameter in the request query")
		}
	} else if out.ModelPath != "" && len(reqBody) > 0 {
		model, err := ReadModel(reqBody, contentType, out.ModelPath)
		switch {
		case err != nil:
			out.Notes = append(out.Notes, "the relay refuses this request with 400: "+err.Error())
			return out, nil
		case model != "":
			out.Model = model
		case looksJSON(contentType, reqBody):
			out.Notes = append(out.Notes, "no string at model path "+out.ModelPath+" in the request body")
		default:
			out.Notes = append(out.Notes, "the request body is not JSON, so no model is read")
		}
	}
	if !billable {
		out.Notes = append(out.Notes, "the route is not billable: the call is relayed but neither counted nor charged")
		return out, nil
	}
	// The relay refuses an unmeasurable body before contacting the upstream, so whatever the
	// sample's status, that refusal is what happens.
	if err := usage.requestBodyError(reqBody); err != nil {
		out.Notes = append(out.Notes, "the relay refuses this request with 400: "+err.Error())
		return out, nil
	}
	if status < 200 || status >= 300 {
		out.Notes = append(out.Notes, "a non-2xx response reports nothing: not counted, not charged")
		return out, nil
	}
	out.Counted = true

	fields := usage.requestUnits(reqBody)
	if usage.billsUnits() && usage.unitsSource == schemas.GenericUnitsFromRequest && !fields.hasUnits {
		out.Notes = append(out.Notes, "no quantity could be read from the request body with measure "+string(usage.unitsMeasure)+noPath(usage))
	}
	// A reply priced by its size is counted as the relay forwards it, so a stream bills what the
	// buffered relay would bill for the same bytes; the preview measures the whole sample body.
	streamSize := func() {
		if !usage.wholeBodyResponse() {
			return
		}
		units, _ := usage.measureUnits(in.ResponseContentType, respBody)
		fields.merge(usageFields{units: units, hasUnits: true})
		out.Notes = append(out.Notes, fmt.Sprintf("the streamed reply is billed by its size: %s counted as it is forwarded", string(usage.unitsMeasure)))
	}
	if out.Streams && !isEventStream(in.ResponseContentType) {
		out.Notes = append(out.Notes, "a non-SSE stream is forwarded unread: nothing is taken from the response body but its size")
		streamSize()
	} else if out.Streams && isEventStream(in.ResponseContentType) {
		// The relay frames an SSE reply and reads only the events that carry a mapped field, each
		// field keeping the last value reported; the preview reads the sample the same way.
		events := providerUtils.SplitSSEPayloads(respBody)
		carried := 0
		for _, event := range events {
			if !usage.hasUsageEvent(event) {
				continue
			}
			if f := usage.eventFields(event); f.any() {
				carried++
				fields.merge(f)
			}
		}
		out.Notes = append(out.Notes, fmt.Sprintf("SSE response: %d events, %d carried a mapped usage field; each field keeps the last value reported", len(events), carried))
		streamSize()
		if usage.ignoreUpstreamCost && usage.costPath != "" {
			out.Notes = append(out.Notes, "the upstream-reported cost is ignored: ignore_provider_cost is on for this provider")
		}
	} else {
		parsed := usage.parse(in.ResponseContentType, respBody)
		fields.merge(parsed)
		if usage.billsUnits() && usage.unitsSource == schemas.GenericUnitsFromResponse && !parsed.hasUnits {
			out.Notes = append(out.Notes, "no quantity could be read from the response body with measure "+string(usage.unitsMeasure)+noPath(usage))
		}
		if len(respBody) > 0 && !looksJSON(in.ResponseContentType, respBody) && usage.unitsMeasure != schemas.GenericUnitsBodyBytes && usage.unitsMeasure != schemas.GenericUnitsBodyChars {
			out.Notes = append(out.Notes, "the response body is not JSON, so token and cost paths are not read")
		}
		if !parsed.hasInput && !parsed.hasOutput && !parsed.hasTotal && len(respBody) > 0 && usage.tokensConfigured() {
			out.Notes = append(out.Notes, "no token counts found in the response body")
		}
		switch {
		case usage.ignoreUpstreamCost && (usage.costPath != "" || providerUtils.GetJSONField(respBody, "usage.cost").Exists()):
			out.Notes = append(out.Notes, "the upstream-reported cost is ignored: ignore_provider_cost is on for this provider")
		case !parsed.hasCost && usage.costPath != "":
			out.Notes = append(out.Notes, "nothing at cost path "+usage.costPath+" in the response body")
		}
	}
	if fields.hasUnits {
		out.Units = schemas.Ptr(fields.units)
		out.UnitsBilled = schemas.Ptr(usage.billedUnits(fields.units))
	}
	if fields.hasCost {
		out.ExtractedCost = schemas.Ptr(fields.cost)
	}
	built := usage.build(fields)
	out.Tokens = built.LLMUsage
	if built.Cost != nil {
		out.Cost = built.Cost.TotalCost
	}
	switch {
	case built.LLMUsage != nil:
		// Token counts are logged on the row, never priced: a generic call costs its mapping alone.
		out.Notes = append(out.Notes, "token counts are logged, not priced; the cost is the mapping's (fee, units, reported cost)")
	case out.Cost == 0:
		out.Notes = append(out.Notes, "cost is 0: the call counts one request against rate limits but charges no budget")
	}
	return out, nil
}

// queryStreams mirrors the transport's reading of a GET that asks for SSE with ?stream=true; it is
// a signal only under streaming.detect_from_body, which streams decides.
func queryStreams(method, rawQuery string) bool {
	if method != http.MethodGet || rawQuery == "" {
		return false
	}
	q, err := url.ParseQuery(rawQuery)
	return err == nil && strings.EqualFold(q.Get("stream"), "true")
}

func noPath(u resolvedUsage) string {
	if u.unitsPath == "" {
		return " and no units path"
	}
	return " at path " + u.unitsPath
}
