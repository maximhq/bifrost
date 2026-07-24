package schemas

// EnrichmentDim describes one identity/context dimension that connectors attach
// to the telemetry they emit for a request (which team/customer/business unit/
// virtual key/etc. the request belongs to).
//
// It is the single source of truth that keeps the CURATED emitters from drifting
// apart — the ones that hand-pick a dimension list:
//   - Prometheus labels   (OSS plugins/telemetry)
//   - Datadog metric tags (enterprise plugins/datadog, buildMetricTags)
//   - BigQuery columns    (enterprise plugins/bigquery, traceColumns)
//
// Each of those derives its list from this registry, and a per-connector
// conformance test asserts the derived list matches — so adding a dimension in
// one place can't silently leave the others behind.
//
// The GENERIC emitters (otel, kafka, pubsub) project the entire span attribute
// map and therefore already carry every dimension; they need no derivation and
// no conformance test here.
//
// `alias` and `routing_engine_used` are in the metric tier but derived only
// post-response (once model resolution/routing has run). They are attached to
// the span in framework/tracing and carry a normal (non-empty) SpanAttr, so a
// record/trace-tier connector can read them like any other dimension.
type EnrichmentDim struct {
	// Name is the canonical short identifier, used verbatim as the Prometheus
	// label and the Datadog metric tag key. The BigQuery column name also equals
	// it unless Column overrides (see below).
	Name string
	// Column is the BigQuery column name when it differs from Name. BigQuery
	// predates the "method" naming and stores it as "request_type"; empty means
	// the column name equals Name.
	Column string
	// SpanAttr is the canonical bifrost.* span-attribute key the dimension is
	// stored under. It is what the record/trace-tier emitters read and what a
	// connector derives its projection from.
	SpanAttr string
	// MetricSafe marks a LOW-cardinality dimension eligible to become a Prometheus
	// label / Datadog metric tag. High-cardinality dims (per-user, arrays) are
	// false and live only on records/traces (BigQuery columns, span attributes).
	MetricSafe bool
	// Multi marks an array-valued dimension — governance can attach several teams/
	// customers/business units to a single request. Array dims are never
	// MetricSafe (they would explode metric series cardinality).
	Multi bool
}

// EnrichmentDims is the canonical, ordered registry of identity/context
// dimensions. Order is stable so derived lists (labels/tags/columns) are
// deterministic. Add a dimension here once and every curated connector picks it
// up via its derivation + conformance test.
var EnrichmentDims = []EnrichmentDim{
	// --- Metric tier: low-cardinality, safe as Prometheus labels / Datadog tags,
	//     and also present on records/traces. ---
	{Name: "provider", SpanAttr: AttrBifrostProviderName, MetricSafe: true},
	{Name: "model", SpanAttr: AttrRequestModel, MetricSafe: true},
	{Name: "method", Column: "request_type", SpanAttr: AttrLegacyRequestType, MetricSafe: true},
	// alias and routing_engine_used are derived post-response and attached to the
	// span in framework/tracing (they have no meaning until the model is resolved
	// and routing has run), so connectors read them like any other dimension.
	{Name: "alias", SpanAttr: AttrBifrostAlias, MetricSafe: true},
	{Name: "routing_engine_used", SpanAttr: AttrBifrostRoutingEngineUsed, MetricSafe: true},
	{Name: "virtual_key_id", SpanAttr: AttrBifrostVirtualKeyID, MetricSafe: true},
	{Name: "virtual_key_name", SpanAttr: AttrBifrostVirtualKeyName, MetricSafe: true},
	{Name: "selected_key_id", SpanAttr: AttrBifrostSelectedKeyID, MetricSafe: true},
	{Name: "selected_key_name", SpanAttr: AttrBifrostSelectedKeyName, MetricSafe: true},
	{Name: "routing_rule_id", SpanAttr: AttrBifrostRoutingRuleID, MetricSafe: true},
	{Name: "routing_rule_name", SpanAttr: AttrBifrostRoutingRuleName, MetricSafe: true},
	// complexity_tier and routing_mechanism are set by the governance plugin only
	// when a routing rule references complexity_tier. Both are closed value sets
	// (tiers: SIMPLE/MEDIUM/COMPLEX; mechanisms: lexical/skipped, later
	// semantic/llm), so they are metric-safe. The raw complexity score is
	// deliberately NOT a dimension — unbounded cardinality; it lives only in the
	// logstore columns.
	{Name: "complexity_tier", SpanAttr: AttrBifrostComplexityTier, MetricSafe: true},
	{Name: "routing_mechanism", SpanAttr: AttrBifrostRoutingMechanism, MetricSafe: true},
	{Name: "team_id", SpanAttr: AttrBifrostTeamID, MetricSafe: true},
	{Name: "team_name", SpanAttr: AttrBifrostTeamName, MetricSafe: true},
	{Name: "customer_id", SpanAttr: AttrBifrostCustomerID, MetricSafe: true},
	{Name: "customer_name", SpanAttr: AttrBifrostCustomerName, MetricSafe: true},
	{Name: "business_unit_id", SpanAttr: AttrBifrostBusinessUnitID, MetricSafe: true},
	{Name: "business_unit_name", SpanAttr: AttrBifrostBusinessUnitName, MetricSafe: true},
	{Name: "fallback_index", SpanAttr: AttrBifrostFallbackIndex, MetricSafe: true},

	// --- Record/trace tier only: high cardinality, NOT metric-safe. Present on
	//     BigQuery columns and span attributes, never as metric labels/tags. ---
	{Name: "user_id", SpanAttr: AttrBifrostUserID},
	{Name: "user_name", SpanAttr: AttrBifrostUserName},
	{Name: "user_email", SpanAttr: AttrBifrostUserEmail},
	{Name: "team_ids", SpanAttr: AttrBifrostTeamIDs, Multi: true},
	{Name: "team_names", SpanAttr: AttrBifrostTeamNames, Multi: true},
	{Name: "customer_ids", SpanAttr: AttrBifrostCustomerIDs, Multi: true},
	{Name: "customer_names", SpanAttr: AttrBifrostCustomerNames, Multi: true},
	{Name: "business_unit_ids", SpanAttr: AttrBifrostBusinessUnitIDs, Multi: true},
	{Name: "business_unit_names", SpanAttr: AttrBifrostBusinessUnitNames, Multi: true},
}

// ColumnName returns the BigQuery column name for the dimension — Column when
// set, otherwise Name.
func (d EnrichmentDim) ColumnName() string {
	if d.Column != "" {
		return d.Column
	}
	return d.Name
}

// EnrichmentDimColumnNames returns the BigQuery column name for every dimension,
// in registry order (the record/trace-tier set).
func EnrichmentDimColumnNames() []string {
	out := make([]string, len(EnrichmentDims))
	for i, d := range EnrichmentDims {
		out[i] = d.ColumnName()
	}
	return out
}

// MetricSafeEnrichmentDims returns the low-cardinality dimensions eligible to be
// Prometheus labels / Datadog metric tags, in registry order.
func MetricSafeEnrichmentDims() []EnrichmentDim {
	out := make([]EnrichmentDim, 0, len(EnrichmentDims))
	for _, d := range EnrichmentDims {
		if d.MetricSafe {
			out = append(out, d)
		}
	}
	return out
}

// EnrichmentDimNames returns every dimension name, in registry order (the
// record/trace-tier set).
func EnrichmentDimNames() []string {
	out := make([]string, len(EnrichmentDims))
	for i, d := range EnrichmentDims {
		out[i] = d.Name
	}
	return out
}

// MetricSafeEnrichmentDimNames returns the names of the metric-tier dimensions,
// in registry order.
func MetricSafeEnrichmentDimNames() []string {
	out := make([]string, 0, len(EnrichmentDims))
	for _, d := range EnrichmentDims {
		if d.MetricSafe {
			out = append(out, d.Name)
		}
	}
	return out
}
