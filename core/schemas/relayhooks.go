package schemas

// BifrostRelayRequest is the envelope for one relay call as it flows through the
// PreRelayHook/PostRelayHook pipeline. The route has already been matched, so plugins see the
// billing decision the relay will act on; RelayName is the resource key access is decided on.
type BifrostRelayRequest struct {
	RelayName  string
	Method     string
	Path       string            // decoded path below the endpoint, rooted ("/v1/search")
	RawQuery   string            // caller query string, without the ?
	Headers    map[string]string // lower-cased caller headers, credentials and gateway headers stripped
	Body       []byte            // caller body; small handle, the relay sends it as is
	Model      string            // read where the usage mapping says, "" for none
	RoutePath  string            // the matched route's path glob, "" when none matched
	RouteIndex int               // index into routes[], -1 when none matched
	// RouteHeaders holds, by lower-cased name, the value of each header the matched route matches
	// on: the one that selected the route (the relay's own extra header when it sets one, else the
	// caller's). A value the relay reads from a secret is already redacted. The logging plugin keeps
	// these on the row, as it keeps the configured logging headers.
	RouteHeaders map[string]string
	Billable     bool // a 2xx reply on this call is counted and charged
	Streams      bool // relayed as a stream
}

// BifrostRelayResponse is what PostRelayHook receives, once the reply (or the stream) has ended.
// Body's content depends on how the reply arrived: a buffered (non-streamed) reply always carries
// its full body here, regardless of content-capture policy, since it was already read into memory
// to be forwarded. A streamed reply's Body is nil unless content capture was enabled for the call
// (see BifrostContextKeyRelayContentLoggingEnabledByDefault), in which case it is a bounded prefix
// of what was buffered as the reply was forwarded — not the whole reply the way a buffered one is.
// Usage is nil when the call is not counted: a route that is not billable, or a non-2xx reply.
type BifrostRelayResponse struct {
	RelayName       string
	StatusCode      int
	Headers         map[string]string
	Body            []byte
	Streamed        bool
	BytesOut        int64 // body bytes forwarded to the caller
	Latency         int64 // milliseconds the call spent in Bifrost, pre-hooks to the end of the reply (or the stream)
	UpstreamLatency int64 // milliseconds blocked on the upstream socket, 0 when the upstream was never called
	Usage           *RelayUsage
}

// RelayUsage is what one counted call costs and what the mapping read to get there. Cost is the
// figure governance charges to budgets; the other fields explain it on the log row.
type RelayUsage struct {
	Cost           float64  `json:"cost"`                     // cost_per_request + extracted cost + billed units * cost_per_unit
	CostPerRequest float64  `json:"cost_per_request"`         // the flat fee in effect
	CostPerUnit    float64  `json:"cost_per_unit"`            // the unit rate in effect
	ExtractedCost  *float64 `json:"extracted_cost,omitempty"` // read at cost_path, nil when none
	Units          *float64 `json:"units,omitempty"`          // raw quantity read, nil when none
	BilledUnits    *float64 `json:"billed_units,omitempty"`   // after block rounding and the minimum
	InputTokens    int      `json:"input_tokens,omitempty"`   // logged, never priced
	OutputTokens   int      `json:"output_tokens,omitempty"`
	TotalTokens    int      `json:"total_tokens,omitempty"`

	Job *RelayJobHandle `json:"job,omitempty"` // set when the call started a job billed on completion
}

// RelayJobHandle is a job a route started: the upstream id, the seconds until the first poll and
// the settlement snapshot the sweeper prices it with.
type RelayJobHandle struct {
	ID              string `json:"id"`
	IntervalSeconds int    `json:"interval_seconds"`
	Params          string `json:"params"`
}

// RelayPluginShortCircuit is a pre-hook's decision to answer without calling the upstream.
type RelayPluginShortCircuit struct {
	Response *BifrostRelayResponse // If set, short-circuit with this response
	Error    *BifrostError         // If set, short-circuit with this error (e.g. an access denial)
}
