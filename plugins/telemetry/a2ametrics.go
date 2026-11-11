package telemetry

import "github.com/maximhq/bifrost/core/schemas"

var a2aMetricLabelNames = []string{
	"operation", "method", "upstream_transport", "error_type",
	"virtual_key_id", "virtual_key_name", "team_id", "team_name",
	"customer_id", "customer_name", "business_unit_id", "business_unit_name",
	"project_id", "project_name",
}

// recordA2AMetrics observes completed operation spans once, matching OTel's wire
// latency preference and keeping task, message and session IDs out of labels.
func (p *PrometheusPlugin) recordA2AMetrics(trace *schemas.Trace) {
	for _, span := range trace.Spans {
		if span == nil || span.Kind != schemas.SpanKindA2AOperation {
			continue
		}
		operation := schemas.GetStringAttr(span.Attributes, schemas.AttrBifrostA2AOperationName)
		if operation == "" {
			continue
		}
		values := make([]string, len(a2aMetricLabelNames))
		values[0] = operation
		values[1] = schemas.GetStringAttr(span.Attributes, schemas.AttrA2AMethodName)
		values[2] = schemas.GetStringAttr(span.Attributes, schemas.AttrBifrostA2AUpstreamTransport)
		if span.Status == schemas.SpanStatusError {
			values[3] = schemas.GetStringAttr(span.Attributes, schemas.AttrErrorTypeSpec)
			if values[3] == "" {
				values[3] = "_OTHER"
			}
		}
		for i, key := range []string{
			schemas.AttrBifrostVirtualKeyID, schemas.AttrBifrostVirtualKeyName,
			schemas.AttrBifrostTeamID, schemas.AttrBifrostTeamName,
			schemas.AttrBifrostCustomerID, schemas.AttrBifrostCustomerName,
			schemas.AttrBifrostBusinessUnitID, schemas.AttrBifrostBusinessUnitName,
			schemas.AttrBifrostProjectID, schemas.AttrBifrostProjectName,
		} {
			values[i+4] = schemas.GetStringAttr(span.Attributes, key)
		}
		var seconds float64
		if ms := schemas.GetIntAttr(span.Attributes, schemas.AttrBifrostA2AOperationDurationMs); ms > 0 {
			seconds = float64(ms) / 1000
		} else if !span.StartTime.IsZero() && !span.EndTime.IsZero() {
			seconds = span.EndTime.Sub(span.StartTime).Seconds()
		}
		p.A2AOperationDuration.WithLabelValues(values...).Observe(seconds)
	}
}
