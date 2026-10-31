package telemetry

import (
	"strconv"

	"github.com/maximhq/bifrost/core/schemas"
)

// relayMetricLabelNames is the label set of the bifrost_relay_* metrics: the relay, the HTTP
// method and matched route, the status code and failure class, and the governance identity.
// The request path is not a label: an unmatched path is caller input of unbounded cardinality.
var relayMetricLabelNames = []string{
	"relay_name", "method", "route_path", "status_code", "error_type",
	"virtual_key_id", "virtual_key_name", "team_id", "team_name",
	"customer_id", "customer_name", "business_unit_id", "business_unit_name",
	"project_id", "project_name",
}

// recordRelayMetrics observes completed relay spans once, as recordA2AMetrics does for Agent
// operations: one duration sample per call, and the USD charged for a counted one.
func (p *PrometheusPlugin) recordRelayMetrics(trace *schemas.Trace) {
	for _, span := range trace.Spans {
		if span == nil || span.Kind != schemas.SpanKindRelay {
			continue
		}
		relayName := schemas.GetStringAttr(span.Attributes, schemas.AttrBifrostRelayName)
		if relayName == "" {
			continue
		}
		values := make([]string, len(relayMetricLabelNames))
		values[0] = relayName
		values[1] = schemas.RelayMethodLabel(schemas.GetStringAttr(span.Attributes, schemas.AttrHTTPRequestMethod))
		values[2] = schemas.GetStringAttr(span.Attributes, schemas.AttrBifrostRelayRoutePath)
		if code := schemas.GetIntAttr(span.Attributes, schemas.AttrHTTPResponseStatusCode); code > 0 {
			values[3] = strconv.Itoa(code)
		}
		if span.Status == schemas.SpanStatusError {
			values[4] = schemas.GetStringAttr(span.Attributes, schemas.AttrErrorTypeSpec)
			if values[4] == "" {
				values[4] = "_OTHER"
			}
		}
		for i, key := range []string{
			schemas.AttrBifrostVirtualKeyID, schemas.AttrBifrostVirtualKeyName,
			schemas.AttrBifrostTeamID, schemas.AttrBifrostTeamName,
			schemas.AttrBifrostCustomerID, schemas.AttrBifrostCustomerName,
			schemas.AttrBifrostBusinessUnitID, schemas.AttrBifrostBusinessUnitName,
			schemas.AttrBifrostProjectID, schemas.AttrBifrostProjectName,
		} {
			values[i+5] = schemas.GetStringAttr(span.Attributes, key)
		}
		var seconds float64
		if !span.StartTime.IsZero() && !span.EndTime.IsZero() {
			seconds = span.EndTime.Sub(span.StartTime).Seconds()
		}
		p.RelayOperationDuration.WithLabelValues(values...).Observe(seconds)
		if cost, ok := schemas.GetFloat64AttrOK(span.Attributes, schemas.AttrBifrostRelayCost); ok && cost > 0 {
			p.RelayCostTotal.WithLabelValues(values...).Add(cost)
		}
	}
}
