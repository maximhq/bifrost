// Package mcptools hosts the log, metrics, governance and catalog tools on
// Bifrost's own MCP server, so any virtual-key-authenticated MCP client - not
// just Warp's dashboard agent - can reach them.
//
// Three rules hold for every tool here:
//
//  1. Bounded. Every result passes through boundToolResult before it reaches
//     a caller. One unbounded log query would otherwise put megabytes of
//     prompt bodies into a model's context window.
//  2. Scope-carrying. Executors take the caller's context and hand it straight
//     to the store, which applies the queryscope row filter. Losing that
//     context means every query silently returns every row in the
//     deployment, so it is never replaced with context.Background().
//  3. No key material on get/list. Secrets are returned only from
//     create_virtual_key and rotate_virtual_key, once. Provider-key values
//     go in and never come out.
//
// Writes persist through GovernanceReader and then reload the in-memory
// cache (GovernanceReloader / MCPRuntime / ProviderRuntime). Warp's
// allowlist stays the original read-only subset; mutating tools live on
// this server only. A write tool runs when governance granted it to the
// caller's virtual key (or user) - what a key may do on Bifrost it may do
// here - or, for a request governance does not govern, under the admin API's
// rule (GrantWriteAccess: open when dashboard auth is disabled, otherwise an
// admin credential). See writeAllowed.
//
// One Deps value is shared across every concurrent caller, so nothing
// caller-specific lives on it: each handler resolves the caller's default
// scope fresh, per call, from ctx (see scope.go).
package mcptools

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/featureflags"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

// Bounding constants. The context-window and result-size concerns they protect
// against are properties of the model reading the result, not of who called.
const (
	// MaxToolResultBytes caps a serialized tool result. Beyond this the result
	// is replaced wholesale with an instruction to narrow the query, rather than
	// tail-truncating a JSON document into something that reads as complete but
	// isn't.
	MaxToolResultBytes = 16384

	// MaxLogRows caps query_logs and semantic_search_logs regardless of what the
	// caller asks for.
	MaxLogRows = 25
	// MaxRankingRows caps every ranking tool.
	MaxRankingRows = 20
	// MaxHistogramBuckets rejects a range/bucket combination that would produce
	// more series points than are useful to reason over.
	MaxHistogramBuckets = 200
	// LargeResultThreshold is where "list the rows" stops being a sensible
	// answer and aggregates take over.
	LargeResultThreshold = 500
	// CoarseBuckets is what a per-provider series is reduced to.
	CoarseBuckets = 12

	// LogContentChars bounds prompt/response text on a list row.
	LogContentChars = 400
	// DetailContentChars is the larger budget for a single-row drill-down.
	DetailContentChars = 2000

	// MaxFilterValues bounds one filter array, and MaxContentSearchChars one
	// substring. Both become query predicates, so an unbounded list from the
	// model turns straight into an unbounded query - and a question needing more
	// than this many names is one that should have been asked by dimension.
	MaxFilterValues       = 50
	MaxContentSearchChars = 500

	// DefaultLookback is the window used when the caller names no time range.
	DefaultLookback = 24 * time.Hour

	// MaxGovernanceRows caps list_virtual_keys / list_teams / list_customers
	// and the other catalog lists.
	MaxGovernanceRows = 20

	// VirtualKeyPrefix is the public prefix of every generated virtual-key
	// secret. Duplicated from plugins/governance because this package cannot
	// import it.
	VirtualKeyPrefix = "sk-bf-"
)

// Now is a package-level seam so tests can pin "now" and assert on the windows
// relative offsets resolve to.
var Now = func() time.Time { return time.Now().UTC() }

// Deps is the entire surface a tool call can reach. It carries no caller
// identity: scope is resolved per call (see scope.go), because one Deps value
// is shared across every concurrent caller this server serves, not rebuilt
// per turn for a single one.
type Deps struct {
	LogManager LogReader
	// Semantic is nil when no vector store, embedding path or Warp config is
	// available. semantic_search_logs is the only tool that reaches it.
	Semantic SemanticSearcher
	// Governance is nil on a deployment whose config store does not implement
	// GovernanceReader. Catalog and write tools report themselves unavailable
	// rather than panicking on a nil pointer.
	Governance GovernanceReader
	// Reloader refreshes the in-memory governance cache after a write. Nil
	// means the row is stored but inference may not see it until restart.
	Reloader GovernanceReloader
	// MCPRuntime dials or drops an upstream MCP client in this process.
	MCPRuntime MCPRuntime
	// MCPClients applies an edited MCP client to the live process.
	MCPClients MCPClientEditor
	// WarpConfig validates and saves Warp configuration writes.
	WarpConfig WarpConfigSaver
	// RoutingCEL validates routing rule expressions before they are stored.
	RoutingCEL CELValidator
	// ProviderKeys validates provider-key writes and refreshes the catalog.
	ProviderKeys ProviderKeyChecks
	// ProviderRuntime adds providers and keys to the live process.
	ProviderRuntime ProviderRuntime
	// PluginRuntime reloads a plugin after persist. Nil means stored but not loaded.
	PluginRuntime PluginRuntime
	// ModelsRuntime re-runs list-models discovery. Nil means refresh is unavailable.
	ModelsRuntime ModelsRuntime
	// ModelCatalog backs describe_model. Nil means the catalog is not loaded.
	ModelCatalog *modelcatalog.ModelCatalog
	// FeatureFlags is the in-memory flag store. Nil falls back to persisted overrides.
	FeatureFlags *featureflags.Store
	// Warp is the singleton Warp config row. Nil means Warp has never been configured
	// and get/update report themselves unavailable.
	Warp configstore.WarpStore

	// Version is the gateway build string get_version returns.
	Version string
	// DisableDBPings skips store pings from get_health, matching the HTTP
	// /health flag.
	DisableDBPings bool
	ConfigPing     Pinger
	LogsPing       Pinger
	VectorPing     Pinger
}

// Tool pairs a model-facing declaration with its executor. execute takes ctx
// so it can resolve the caller's scope itself (via scopeFromContext) rather
// than reading it off Deps.
type Tool struct {
	name string
	// schemaJSON is raw JSON rather than a hand-built structure: models are
	// sensitive to declared property order, and this is the literal document a
	// caller will see.
	schemaJSON  string
	description string
	execute     func(ctx context.Context, deps *Deps, args map[string]any) (any, error)
	// noLogs is true when the tool never touches Deps.LogManager, so it stays
	// available on a deployment with logging disabled.
	noLogs bool
	// readsLogRows is true on a noLogs tool that still reads log rows through
	// another dependency (semantic_search_logs hydrates its hits from the log
	// store), so a scoped caller's row filter must reach it.
	readsLogRows bool
	// mutating is true when the tool writes. The MCP declaration then drops
	// ReadOnlyHint so a client can confirm side effects.
	mutating bool
	// tenantScoped is true on a write tool that checks the caller's tenant
	// itself (tenant.go), so a team- or customer-scoped key may use it inside
	// its own tenant. Every other write tool changes deployment-wide state and
	// is refused for a scoped caller, which is also the safe default for a new
	// tool that has not thought about tenants.
	tenantScoped bool
}

// argumentNames lists the arguments a tool takes, read off the schema the model
// is shown, sorted. The schema is the one place they are declared, so reading it
// here means a refusal can never disagree with what was advertised.
func (t Tool) argumentNames() []string {
	var schema struct {
		Properties map[string]sonic.NoCopyRawMessage `json:"properties"`
	}
	if err := sonic.UnmarshalString(t.schemaJSON, &schema); err != nil {
		return nil
	}
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// FilterSchema is shared by every flow. It maps one-to-one onto
// logstore.SearchFilters.
const FilterSchema = `{
  "type": "object",
  "description": "Narrows which requests are considered. Omit a field to leave that dimension unfiltered. If start_time is omitted the last 24 hours are used.",
  "properties": {
    "start_time": {"type": "string", "description": "A relative offset like -7d (this week), -24h (today), -30m, or an RFC3339 timestamp. Use this for any time window the question names; do not ask which window to use."},
    "end_time": {"type": "string", "description": "RFC3339 timestamp, a relative offset like -1d, or \"now\". Defaults to now; omit it for a window that ends now."},
    "providers": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "e.g. openai, anthropic, bedrock."},
    "models": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "status": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "success, error, or cancelled."},
    "stop_reasons": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "e.g. stop, length, content_filter, tool_calls. Call describe_filter_space to see which actually occur."},
    "objects": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "Request type, e.g. chat_completion, embedding, speech, transcription, image_generation, video_generation. Use this to exclude non-chat traffic - embeddings, speech, and image/video generation have their own cost and latency shape and otherwise get averaged in with chat requests."},
    "virtual_key_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "team_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "customer_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "user_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "business_unit_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "project_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "apps": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "Include only these apps. \"Warp\" is refused: it is this assistant, not the person's traffic."},
    "min_latency": {"type": "number", "description": "Milliseconds."},
    "max_latency": {"type": "number", "description": "Milliseconds."},
    "min_tokens": {"type": "integer", "description": "Total tokens on the request."},
    "max_tokens": {"type": "integer", "description": "Total tokens on the request."},
    "min_cost": {"type": "number"},
    "max_cost": {"type": "number"},
    "cache_hit_types": {"type": "array", "items": {"type": "string", "enum": ["direct", "semantic"]}, "minItems": 1, "description": "Local-cache hit type: direct (exact match) or semantic (fuzzy match)."},
    "routing_rule_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "Routing rules that handled the request. Ids come from describe_filter_space or a query_usage_by routing_rule ranking - never guess one."},
    "routing_engine_used": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "Routing engines the request passed through, e.g. routing-rule, governance, loadbalancing. A request can pass through several."},
    "selected_key_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "Provider API keys Bifrost picked for the request - not virtual keys. Ids come from describe_filter_space (provider_keys)."},
    "aliases": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "Model aliases the request was addressed to before Bifrost resolved them to a model."},
    "complexity_tiers": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "Tier the complexity router assigned: SIMPLE, MEDIUM or COMPLEX. Only requests the complexity router saw have one."},
    "complexity_mechanisms": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "How the complexity tier was decided, e.g. semantic, llm, session."},
    "tool_call_names": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "Requests whose response called any of these function names."},
    "user_agents": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "Raw User-Agent strings. Prefer apps, which groups them."},
    "metadata_filters": {"type": "object", "additionalProperties": {"type": "string"}, "minProperties": 1, "description": "Request metadata, as key to value, e.g. {\"env\": \"prod\"}. Every pair must match. Keys and values that occur are listed by describe_filter_space (metadata)."},
    "session_id": {"type": "string", "minLength": 1, "description": "Exact Bifrost session id."},
    "request_id": {"type": "string", "minLength": 1, "description": "Exact request id."},
    "parent_request_id": {"type": "string", "minLength": 1, "description": "Requests spawned by this request, e.g. the fallback attempts of one call."},
    "missing_cost_only": {"type": "boolean", "description": "Only successful requests whose cost could not be computed."},
    "error_types": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "The provider's error classification on failed requests, e.g. invalid_request_error, overloaded_error - the ids a query_usage_by error_type ranking returns. This is how to fetch the rows behind one row of that ranking."},
    "error_codes": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "The provider's finer-grained error code, e.g. context_length_exceeded. Many providers leave it empty - prefer error_types or status_codes unless an error_code ranking shows values."},
    "status_codes": {"type": "array", "items": {"type": "integer", "minimum": 100, "maximum": 599}, "minItems": 1, "maxItems": 50, "description": "HTTP status the failure came back with, e.g. 400, 429, 529. Populated on every failed request."},
    "content_search": {"type": "string", "minLength": 1, "maxLength": 500, "description": "Substring match against request and response content. Omit the field rather than sending an empty string."},
    "scope": {"type": "string", "enum": ["all"], "description": "Set to \"all\" when the question is explicitly about everyone's traffic. Without it, a caller with a default user scope and no team, customer, business unit, project, user or virtual key named is scoped to their own traffic. It widens the question, not the permission: results are still limited to what the caller may see."}
  }
}`

// parseFilters converts the caller's filter object into SearchFilters.
//
// Unknown keys are rejected rather than ignored: a silently dropped filter
// produces a plausible answer to a different question than the one asked, and
// neither the caller nor its model can tell.
func parseFilters(raw map[string]any, now time.Time) (*logstore.SearchFilters, error) {
	filters := &logstore.SearchFilters{}
	if raw == nil {
		raw = map[string]any{}
	}

	known := map[string]bool{
		"start_time": true, "end_time": true, "providers": true, "models": true,
		"status": true, "stop_reasons": true, "objects": true, "virtual_key_ids": true,
		"team_ids": true, "customer_ids": true, "user_ids": true, "business_unit_ids": true,
		"project_ids": true, "apps": true, "min_latency": true, "max_latency": true,
		"min_tokens": true, "max_tokens": true, "min_cost": true, "max_cost": true,
		"cache_hit_types": true, "content_search": true, "scope": true,
		"error_types": true, "error_codes": true, "status_codes": true,
		"routing_rule_ids": true, "routing_engine_used": true, "selected_key_ids": true, "aliases": true,
		"complexity_tiers": true, "complexity_mechanisms": true, "tool_call_names": true, "user_agents": true,
		"metadata_filters": true, "session_id": true, "request_id": true, "parent_request_id": true,
		"missing_cost_only": true,
	}
	unknown := []string{}
	for key := range raw {
		if !known[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		// Listed from the set the check itself uses. Written out by hand, the
		// list fell behind the schema the first time a filter was added.
		supported := slices.Sorted(maps.Keys(known))
		return nil, fmt.Errorf("unknown filter fields: %s. Supported fields are: %s", strings.Join(unknown, ", "), strings.Join(supported, ", "))
	}

	if err := rejectNullTimeBounds(raw); err != nil {
		return nil, err
	}
	start, err := parseTime(raw["start_time"], now)
	if err != nil {
		return nil, fmt.Errorf("start_time: %w", err)
	}
	end, err := parseTime(raw["end_time"], now)
	if err != nil {
		return nil, fmt.Errorf("end_time: %w", err)
	}
	if end == nil {
		end = &now
	}
	if start == nil {
		defaulted := end.Add(-DefaultLookback)
		start = &defaulted
	}
	if start.After(*end) {
		return nil, fmt.Errorf("start_time must be before end_time")
	}
	filters.StartTime, filters.EndTime = start, end

	for key, target := range map[string]*[]string{
		"providers": &filters.Providers, "models": &filters.Models, "status": &filters.Status,
		"stop_reasons": &filters.StopReasons, "objects": &filters.Objects,
		"virtual_key_ids": &filters.VirtualKeyIDs, "team_ids": &filters.TeamIDs,
		"customer_ids": &filters.CustomerIDs, "user_ids": &filters.UserIDs,
		"business_unit_ids": &filters.BusinessUnitIDs, "project_ids": &filters.ProjectIDs,
		"apps": &filters.Apps, "cache_hit_types": &filters.CacheHitTypes,
		"error_types": &filters.ErrorTypes, "error_codes": &filters.ErrorCodes,
		"routing_rule_ids": &filters.RoutingRuleIDs, "routing_engine_used": &filters.RoutingEngineUsed,
		"selected_key_ids": &filters.SelectedKeyIDs, "aliases": &filters.Aliases,
		"complexity_tiers": &filters.ComplexityTiers, "complexity_mechanisms": &filters.ComplexityMechanisms,
		"tool_call_names": &filters.ToolCallNames, "user_agents": &filters.UserAgents,
	} {
		values, err := stringSliceField(raw, key)
		if err != nil {
			return nil, err
		}
		*target = values
	}
	for key, target := range map[string]**float64{
		"min_latency": &filters.MinLatency, "max_latency": &filters.MaxLatency,
		"min_cost": &filters.MinCost, "max_cost": &filters.MaxCost,
	} {
		value, err := floatField(raw, key)
		if err != nil {
			return nil, err
		}
		*target = value
	}
	for key, target := range map[string]**int{
		"min_tokens": &filters.MinTokens, "max_tokens": &filters.MaxTokens,
	} {
		value, err := intPtrChecked(raw[key], key)
		if err != nil {
			return nil, err
		}
		*target = value
	}
	for key, target := range map[string]*string{
		"session_id": &filters.SessionID, "request_id": &filters.RequestID, "parent_request_id": &filters.ParentRequestID,
	} {
		value, err := exactStringField(raw, key)
		if err != nil {
			return nil, err
		}
		*target = value
	}
	if value, present := raw["missing_cost_only"]; present {
		flag, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("missing_cost_only must be true or false")
		}
		filters.MissingCostOnly = flag
	}
	metadata, err := metadataFiltersField(raw)
	if err != nil {
		return nil, err
	}
	filters.MetadataFilters = metadata
	statusCodes, err := statusCodesField(raw)
	if err != nil {
		return nil, err
	}
	filters.StatusCodes = statusCodes
	search, err := contentSearchField(raw)
	if err != nil {
		return nil, err
	}
	filters.ContentSearch = search
	return filters, nil
}

// rejectNullTimeBounds refuses an explicit null start_time or end_time, which
// would otherwise read as omitted and silently fall back to the default window.
func rejectNullTimeBounds(raw map[string]any) error {
	for _, key := range [...]string{"start_time", "end_time"} {
		if value, present := raw[key]; present && value == nil {
			return fmt.Errorf("%s must be a string, got null; omit the field to use the default window", key)
		}
	}
	return nil
}

// contentSearchField reads the optional content_search filter shared by the log
// and MCP log flows. Null, non-string and blank values are refused rather than
// read as "no filter", which would answer a wider question than the one asked.
func contentSearchField(raw map[string]any) (string, error) {
	value, present := raw["content_search"]
	if !present {
		return "", nil
	}
	if value == nil {
		return "", fmt.Errorf("content_search must be a string, got null; omit the field to search without a content filter")
	}
	search, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("content_search must be a string, got %T", value)
	}
	if strings.TrimSpace(search) == "" {
		return "", fmt.Errorf("content_search must not be empty; omit the field to search without a content filter")
	}
	// Runes, not bytes: the schema's maxLength counts characters, and
	// len() counting UTF-8 bytes rejected a 500-character non-ASCII search
	// the schema had just accepted - with an error reporting the byte
	// count as a character count.
	if count := utf8.RuneCountInString(search); count > MaxContentSearchChars {
		return "", fmt.Errorf("content_search is %d characters; at most %d are accepted", count, MaxContentSearchChars)
	}
	return search, nil
}

// parseTime accepts RFC3339 or a relative offset like "-7d".
func parseTime(value any, now time.Time) (*time.Time, error) {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return nil, nil
	}
	text = strings.TrimSpace(text)
	// end_time is documented as defaulting to now, and models spell that out.
	if strings.EqualFold(text, "now") {
		return &now, nil
	}
	if strings.HasPrefix(text, "-") {
		// time.ParseDuration has no day unit, which is the one people actually use.
		if strings.HasSuffix(text, "d") {
			var days float64
			if _, err := fmt.Sscanf(text, "-%fd", &days); err == nil && days > 0 {
				result := now.Add(-time.Duration(days * float64(24*time.Hour)))
				return &result, nil
			}
			return nil, fmt.Errorf("could not parse relative offset %q", text)
		}
		duration, err := time.ParseDuration(text)
		if err != nil {
			return nil, fmt.Errorf("could not parse relative offset %q, expected forms like -24h, -30m or -7d", text)
		}
		result := now.Add(duration)
		return &result, nil
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return nil, fmt.Errorf("could not parse %q, expected RFC3339, a relative offset like -7d, or \"now\"", text)
	}
	return &parsed, nil
}

// intPtr reads an optional JSON number into an int pointer. JSON numbers
// decode as float64 regardless of the schema's declared type.
func intPtr(value any) *int {
	number, ok := value.(float64)
	if !ok {
		return nil
	}
	result := int(number)
	return &result
}

// intPtrChecked reads an optional JSON number into an int pointer, rejecting
// values that don't round-trip cleanly into a platform int, unlike intPtr
// which silently truncates.
func intPtrChecked(value any, field string) (*int, error) {
	if value == nil {
		return nil, nil
	}
	number, ok := value.(float64)
	if !ok {
		return nil, fmt.Errorf("%s must be an integer", field)
	}
	if number != math.Trunc(number) {
		return nil, fmt.Errorf("%s must be an integer, got %v", field, number)
	}
	// float64(math.MaxInt) is not actually math.MaxInt: the 53-bit mantissa
	// cannot hold 2^63-1, so it rounds up to 2^63 - one past the largest int64.
	// A `>` check against it would let 2^63 through, and int(2^63) is
	// implementation-defined. -float64(math.MinInt) names the same boundary
	// from the side that is exact (a power of two), so >= against it excludes
	// the first value int cannot represent.
	if number < float64(math.MinInt) || number >= -float64(math.MinInt) {
		return nil, fmt.Errorf("%s is out of range: %v", field, number)
	}
	return intPtr(value), nil
}

// filterArg parses the shared filter object every flow accepts and applies
// the caller's default scope, resolved fresh from ctx.
func filterArg(ctx context.Context, args map[string]any, now time.Time) (*logstore.SearchFilters, Scope, error) {
	scope := scopeFromContext(ctx)
	// Checked rather than asserted away: a filters value of the wrong type
	// (a string, an array, null) would otherwise parse as "no filters" and
	// answer over the default window and scope - a plausible answer to a
	// question nobody asked. Every flow's schema requires the object.
	value, present := args["filters"]
	if !present || value == nil {
		return nil, scope, fmt.Errorf("filters is required; pass an object, {} for the defaults")
	}
	raw, ok := value.(map[string]any)
	if !ok {
		return nil, scope, fmt.Errorf("filters must be an object, got %T", value)
	}
	filters, err := parseFilters(raw, now)
	if err != nil {
		return nil, scope, err
	}
	all, err := scopeAllArg(raw)
	if err != nil {
		return nil, scope, err
	}
	if err := refuseWarpApp(filters); err != nil {
		return nil, scope, err
	}
	applyScope(filters, scope, all)
	return filters, scope, nil
}

// scopeAllArg reads the filter object's scope marker. "all" is the only value;
// anything else is rejected rather than read as the default, because guessing
// would answer a different question than the one asked and look right doing it.
func scopeAllArg(raw map[string]any) (bool, error) {
	value, present := raw["scope"]
	if !present {
		return false, nil
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) != "all" {
		return false, fmt.Errorf(`scope must be "all" when given; omit it for the default`)
	}
	return true, nil
}

// enumSliceArg reads a list argument whose values must come from a fixed set.
//
// The schema is advertised to the provider, not enforced on what comes back, so
// dropping the elements that do not fit turned ["cost", 42] into ["cost"] and
// answered a narrower question than the one asked - successfully, which is the
// part that makes it hard to notice.
func enumSliceArg(args map[string]any, key, noun string, allowed []string, maxItems int) ([]string, error) {
	value, present := args[key]
	if !present || value == nil {
		return nil, fmt.Errorf("%s must list at least one of: %s", key, strings.Join(allowed, ", "))
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
	// The schema's maxItems is advertised to the provider, never enforced on
	// what comes back, so the caller passes the same limit here. Each metric is
	// a separate database query, and bounding by the vocabulary instead let a
	// call overrun the published maxItems whenever the vocabulary was larger.
	if len(items) > maxItems {
		return nil, fmt.Errorf("%s lists %d values; at most %d are accepted, and each one is a separate query", key, len(items), maxItems)
	}
	result := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for index, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be a string, got %T", key, index, item)
		}
		if seen[text] {
			return nil, fmt.Errorf("%s[%d] repeats %q; each value runs its own query, so asking twice only costs twice", key, index, text)
		}
		seen[text] = true
		if !slices.Contains(allowed, text) {
			return nil, fmt.Errorf("unknown %s at %s[%d]: %q; supported: %s", noun, key, index, text, strings.Join(allowed, ", "))
		}
		result = append(result, text)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%s must list at least one of: %s", key, strings.Join(allowed, ", "))
	}
	return result, nil
}

// stringSliceField is the checked form of stringSlice.
//
// Reporting rather than coercing, for the same reason unknown field names are
// rejected above: a filter that silently turns into nothing runs a broader
// query than the one asked for, and the answer looks right. Bounded too - each
// value becomes a query predicate.
func stringSliceField(raw map[string]any, key string) ([]string, error) {
	value, present := raw[key]
	if !present {
		return nil, nil
	}
	// Present-and-null is not absent. The schema types these as arrays, and a
	// nil returned here becomes a filter applyFilters skips entirely - so the
	// query ran without the filter that was asked for and the answer looked like
	// a narrow one.
	if value == nil {
		return nil, fmt.Errorf("%s must be an array of strings, got null; omit the field to search without that filter", key)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
	// Same reasoning for an explicitly empty array: it asks for a filter and
	// then names nothing to filter on, which silently widens the query.
	if len(items) == 0 {
		return nil, fmt.Errorf("%s must list at least one value; omit the field to search without that filter", key)
	}
	if len(items) > MaxFilterValues {
		return nil, fmt.Errorf("%s lists %d values; at most %d are accepted. Narrow by dimension instead", key, len(items), MaxFilterValues)
	}
	result := make([]string, 0, len(items))
	for index, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be a string", key, index)
		}
		// Skipping an empty element dropped the whole filter when every element
		// was empty, so models: [""] ran an unfiltered query and returned a
		// broader result that reads as an answer to the narrow question.
		if strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("%s[%d] must not be empty", key, index)
		}
		result = append(result, text)
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

// floatField is the checked form of floatPtr, for the same reason.
func floatField(raw map[string]any, key string) (*float64, error) {
	value, present := raw[key]
	if !present || value == nil {
		return nil, nil
	}
	number, ok := value.(float64)
	if !ok {
		return nil, fmt.Errorf("%s must be a number", key)
	}
	return &number, nil
}

// exactStringField reads an exact-match id filter. Blank is refused rather than
// read as absent: the store skips an empty id, so the question would be answered
// unfiltered with nothing to say the filter had been dropped.
func exactStringField(raw map[string]any, key string) (string, error) {
	value, present := raw[key]
	if !present {
		return "", nil
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s must be a non-empty string; omit the field to leave it unfiltered", key)
	}
	return strings.TrimSpace(text), nil
}

// metadataFiltersField reads metadata_filters: a non-empty object of string
// values, every pair of which must match.
func metadataFiltersField(raw map[string]any) (map[string]string, error) {
	value, present := raw["metadata_filters"]
	if !present {
		return nil, nil
	}
	pairs, ok := value.(map[string]any)
	if !ok || len(pairs) == 0 {
		return nil, fmt.Errorf(`metadata_filters must be a non-empty object of key to value, like {"env": "prod"}; omit the field to leave it unfiltered`)
	}
	out := make(map[string]string, len(pairs))
	for key, item := range pairs {
		text, ok := item.(string)
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("metadata_filters values must be strings, got %v for %q", item, key)
		}
		out[key] = text
	}
	return out, nil
}

// statusCodesField reads status_codes: a non-empty array of whole numbers in
// the HTTP status range. Anything else is refused rather than dropped, since a
// dropped filter answers a wider question than the one asked and says nothing.
func statusCodesField(raw map[string]any) ([]int, error) {
	value, present := raw["status_codes"]
	if !present {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("status_codes must be a non-empty array of HTTP status codes like [400, 429]; omit the field to leave it unfiltered")
	}
	codes := make([]int, 0, len(items))
	for _, item := range items {
		number, ok := item.(float64)
		if !ok || number != math.Trunc(number) || number < 100 || number > 599 {
			return nil, fmt.Errorf("status_codes must hold whole HTTP status codes between 100 and 599, got %v", item)
		}
		codes = append(codes, int(number))
	}
	return codes, nil
}

// intArg reads an optional bounded integer.
//
// Absent means the default. Present-but-unusable does not: silently falling back
// turned "limit": -5 or "limit": "ten" into a successful query with a different
// limit than the one asked for, and the answer looked like the requested one.
// A fractional value is rejected rather than truncated for the same reason.
func intArg(args map[string]any, key string, fallback, max int) (int, error) {
	raw, present := args[key]
	if !present || raw == nil {
		return fallback, nil
	}
	value, ok := raw.(float64)
	if !ok {
		return 0, fmt.Errorf("%s must be a number, got %T", key, raw)
	}
	if value != math.Trunc(value) {
		return 0, fmt.Errorf("%s must be a whole number, got %v", key, value)
	}
	if value < 1 {
		return 0, fmt.Errorf("%s must be at least 1, got %v", key, value)
	}
	// Compared as a float first: converting a value past the int range is
	// implementation-defined and could wrap to a negative.
	if value >= float64(max) {
		return max, nil
	}
	result := int(value)
	// Clamp rather than reject. The cap exists to protect the context window,
	// not to police the model, and failing the call would just cost another
	// round trip to arrive at the number we would have used anyway.
	return min(result, max), nil
}

// stringArg reads a required string argument.
//
// A discarded type assertion turns a present non-string into "", which the
// caller then reports as a missing field - telling the model it forgot
// something it actually sent, so it retries with the same wrong shape.
func stringArg(args map[string]any, key string) (string, error) {
	value, present := args[key]
	if !present || value == nil {
		return "", fmt.Errorf("%s is required", key)
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string, got %T", key, value)
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s must not be empty", key)
	}
	return text, nil
}

// optionalStringArg reads a string that may be omitted.
func optionalStringArg(args map[string]any, key string) (string, bool, error) {
	value, present := args[key]
	if !present || value == nil {
		return "", false, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", false, fmt.Errorf("%s must be a string, got %T", key, value)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false, fmt.Errorf("%s must not be empty", key)
	}
	return text, true, nil
}

// optionalStringArgAllowEmpty is optionalStringArg for fields where an empty
// string is a meaningful value (clearing a match expression) rather than a
// mistake. A non-string is still a type error.
func optionalStringArgAllowEmpty(args map[string]any, key string) (string, bool, error) {
	value, present := args[key]
	if !present || value == nil {
		return "", false, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", false, fmt.Errorf("%s must be a string, got %T", key, value)
	}
	return strings.TrimSpace(text), true, nil
}

// optionalBoolArg reads a boolean only when the caller sent it.
func optionalBoolArg(args map[string]any, key string) (*bool, error) {
	value, present := args[key]
	if !present || value == nil {
		return nil, nil
	}
	flag, ok := value.(bool)
	if !ok {
		return nil, fmt.Errorf("%s must be a boolean, got %T", key, value)
	}
	return &flag, nil
}

// optionalInt64Arg reads a whole number only when the caller sent it.
func optionalInt64Arg(args map[string]any, key string) (*int64, error) {
	value, present := args[key]
	if !present || value == nil {
		return nil, nil
	}
	number, ok := value.(float64)
	if !ok {
		return nil, fmt.Errorf("%s must be a number, got %T", key, value)
	}
	if number != math.Trunc(number) {
		return nil, fmt.Errorf("%s must be a whole number, got %v", key, value)
	}
	// -float64(math.MinInt64) is 2^63, exactly the first value int64 cannot
	// hold (see intPtrChecked for why MaxInt64 is not the bound to use).
	if number < float64(math.MinInt64) || number >= -float64(math.MinInt64) {
		return nil, fmt.Errorf("%s is out of range: %v", key, value)
	}
	n := int64(number)
	return &n, nil
}

// optionalIntArg reads a whole number only when the caller sent it.
func optionalIntArg(args map[string]any, key string) (*int, error) {
	value, present := args[key]
	if !present || value == nil {
		return nil, nil
	}
	number, ok := value.(float64)
	if !ok {
		return nil, fmt.Errorf("%s must be a number, got %T", key, value)
	}
	if number != math.Trunc(number) {
		return nil, fmt.Errorf("%s must be a whole number, got %v", key, value)
	}
	if number < float64(math.MinInt) || number >= -float64(math.MinInt) {
		return nil, fmt.Errorf("%s is out of range: %v", key, value)
	}
	n := int(number)
	return &n, nil
}

// optionalObjectArg reads a JSON object only when the caller sent it.
func optionalObjectArg(args map[string]any, key string) (map[string]any, bool, error) {
	value, present := args[key]
	if !present || value == nil {
		return nil, false, nil
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("%s must be an object, got %T", key, value)
	}
	return obj, true, nil
}

// floatArg reads a required number.
func floatArg(args map[string]any, key string) (float64, error) {
	value, present := args[key]
	if !present || value == nil {
		return 0, fmt.Errorf("%s is required", key)
	}
	number, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("%s must be a number, got %T", key, value)
	}
	return number, nil
}

// uintArg reads a required unsigned integer (JSON numbers arrive as float64).
func uintArg(args map[string]any, key string) (uint, error) {
	value, present := args[key]
	if !present || value == nil {
		return 0, fmt.Errorf("%s is required", key)
	}
	number, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("%s must be a number, got %T", key, value)
	}
	if number != math.Trunc(number) || number < 1 {
		return 0, fmt.Errorf("%s must be a positive whole number, got %v", key, value)
	}
	// -2*float64(math.MinInt) is 2^bits, the first value a platform uint
	// cannot hold, and exact as a power of two.
	if number >= -2*float64(math.MinInt) {
		return 0, fmt.Errorf("%s is out of range: %v", key, value)
	}
	return uint(number), nil
}

func requireGovernance(deps *Deps) (GovernanceReader, error) {
	if deps == nil || deps.Governance == nil {
		return nil, fmt.Errorf("governance is not available on this deployment")
	}
	return deps.Governance, nil
}

func requireReloader(deps *Deps) GovernanceReloader {
	if deps == nil {
		return nil
	}
	return deps.Reloader
}

// boolArg reads an optional boolean flag.
//
// A present non-boolean used to read as false, so a malformed include_content
// produced a successful answer with the content quietly left out - the caller
// cannot tell that from a deployment that has no content to give.
func boolArg(args map[string]any, key string) (bool, error) {
	value, present := args[key]
	if !present || value == nil {
		return false, nil
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be a boolean, got %T", key, value)
	}
	return flag, nil
}

// WarpAppName is the app label Warp's own requests are logged under. Exported
// because Warp's indexer skips those rows by the same name.
const WarpAppName = "Warp"

// refuseWarpApp keeps any filter from narrowing to Warp's own queries.
//
// The model is Warp, so "what did I spend" reads to it as "what was spent in
// Warp". It sent apps: ["Warp"] under a prompt saying never to, and when that
// was refused with a scope "warp" escape hatch for questions about Warp itself,
// it sent that instead for the same question - both times reporting this
// assistant's own spend as the person's. No filter narrows to Warp now. Warp's
// own cost is still one row of query_usage_by by app, beside every other app,
// where it cannot be mistaken for the whole.
func refuseWarpApp(filters *logstore.SearchFilters) error {
	for _, app := range filters.Apps {
		if strings.EqualFold(strings.TrimSpace(app), WarpAppName) {
			return fmt.Errorf(`apps must not name "Warp": it is this assistant, and filtering to it counts only the questions asked here, not the person's traffic. Drop it from apps. If the person asked what Warp itself costs, use query_usage_by with dimension app, where Warp is one row among the others`)
		}
	}
	return nil
}

// enumArg reads a string argument that the schema declares as an enum.
//
// The schema is advertised to the model and never enforced on the reply, so an
// unlisted value used to reach the store - where searchLogs quietly maps an
// unknown sort to timestamp and anything but "asc" to DESC. The result is a
// different query answered as though it were the one asked, which is the same
// silent substitution an unchecked group_by produced.
func enumArg(args map[string]any, name, fallback string, allowed []string) (string, error) {
	value, present := args[name]
	if !present || value == nil {
		return fallback, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string, got %T", name, value)
	}
	if text == "" {
		return fallback, nil
	}
	if slices.Contains(allowed, text) {
		return text, nil
	}
	return "", fmt.Errorf("%s %q is not supported; use one of %s", name, text, strings.Join(allowed, ", "))
}

// groupByProvider reads the group_by argument, accepting only what the schema
// offers.
//
// The schema is advertised to the model, never enforced on the arguments that
// come back: chatTools forwards it to the provider and nothing checks the
// reply. Comparing against "provider" alone meant any other value fell through
// to the ungrouped branch, so a request for one breakdown returned aggregates
// for another and looked like a real answer.
func groupByProvider(args map[string]any) (bool, error) {
	value, present := args["group_by"]
	if !present || value == nil {
		return false, nil
	}
	groupBy, ok := value.(string)
	if !ok {
		return false, fmt.Errorf("group_by must be a string, got %T", value)
	}
	switch groupBy {
	case "", "none":
		return false, nil
	case "provider":
		return true, nil
	default:
		return false, fmt.Errorf("group_by %q is not supported; use \"none\" or \"provider\"", groupBy)
	}
}

// boundToolResult serializes a result and enforces the byte budget. Over
// budget, the payload is discarded and replaced with an instruction, rather
// than tail-truncated - a truncated JSON document reads to a model as
// complete, and the model summarizes the fragment as though it were the whole
// answer.
func boundToolResult(result any) string {
	encoded, err := sonic.MarshalString(result)
	if err != nil {
		return fmt.Sprintf(`{"error":"could not serialize result: %s"}`, err.Error())
	}
	if len(encoded) <= MaxToolResultBytes {
		return encoded
	}
	return fmt.Sprintf(
		`{"error":"result too large (%d bytes, limit %d). Narrow the time range, add filters, or lower the limit, then try again.","truncated":true}`,
		len(encoded), MaxToolResultBytes,
	)
}

// truncateText caps a string and marks it, so the reader can tell it is
// reading a fragment rather than the whole value.
func truncateText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	// limit counts bytes; back off to a rune boundary so the cut never splits
	// a multibyte character into invalid UTF-8.
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "... [truncated]"
}

// LogRow is the projection query_logs returns. Exported so a SemanticSearcher
// implementation can build its rows through the same projection and inherit
// the same hidden-content gate.
type LogRow struct {
	ID             string  `json:"id"`
	Timestamp      string  `json:"timestamp"`
	Provider       string  `json:"provider"`
	Model          string  `json:"model"`
	Status         string  `json:"status"`
	LatencyMs      float64 `json:"latency_ms,omitempty"`
	InputTokens    int     `json:"input_tokens,omitempty"`
	OutputTokens   int     `json:"output_tokens,omitempty"`
	Cost           float64 `json:"cost,omitempty"`
	VirtualKeyName string  `json:"virtual_key_name,omitempty"`
	UserID         string  `json:"user_id,omitempty"`
	ErrorMessage   string  `json:"error_message,omitempty"`
	// ErrorType, ErrorCode and StatusCode are the structured classification
	// behind ErrorMessage - the same fields get_request_trace reads. Exposing
	// them here is what lets a "what kinds of errors" question be answered by
	// tallying a field query_logs already returns, rather than reading 25
	// free-text messages and guessing at how many distinct failures they
	// represent.
	ErrorType  string `json:"error_type,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	// What routed the request: the rule that handled it, the provider key it was
	// sent with, the alias it was addressed to, the complexity tier it was given
	// and the functions its response called. All absent on a request none of
	// them touched, so a plain row costs nothing extra.
	RoutingRule    string   `json:"routing_rule,omitempty"`
	ProviderKey    string   `json:"provider_key,omitempty"`
	Alias          string   `json:"alias,omitempty"`
	ComplexityTier string   `json:"complexity_tier,omitempty"`
	ToolCalls      []string `json:"tool_calls,omitempty"`
	Content        string   `json:"content,omitempty"`
	// Link opens this request in the Logs view. Built server-side so the caller
	// repeats it rather than guessing the dashboard's URL scheme.
	Link string `json:"link,omitempty"`
}

// ProjectLog reduces a log row to the fields that answer operational
// questions. The full row carries raw request and response bodies; returning
// even a handful of those would exhaust a caller's context budget. Content is
// opt-in and never served for a ContentHidden row - see logContent.
func ProjectLog(entry *logstore.Log, includeContent bool, contentLimit int) LogRow {
	row := LogRow{
		ID:        entry.ID,
		Timestamp: entry.Timestamp.UTC().Format(time.RFC3339),
		Provider:  entry.Provider,
		Model:     entry.Model,
		Status:    entry.Status,
		LatencyMs: derefFloat(entry.Latency),
		// The denormalized columns rather than TokenUsageParsed: they survive
		// object-storage offload and content-hidden rows, both of which blank the
		// token_usage payload, so they are the only counts that are always right.
		InputTokens:    entry.PromptTokens,
		OutputTokens:   entry.CompletionTokens,
		Cost:           derefFloat(entry.Cost),
		VirtualKeyName: derefString(entry.VirtualKeyName),
		UserID:         derefString(entry.UserID),
		RoutingRule:    cmp.Or(derefString(entry.RoutingRuleName), derefString(entry.RoutingRuleID)),
		ProviderKey:    cmp.Or(entry.SelectedKeyName, entry.SelectedKeyID),
		Alias:          derefString(entry.Alias),
		ComplexityTier: derefString(entry.ComplexityTier),
		ToolCalls:      entry.ToolCallNames,
		Link:           logDetailLink(entry.ID),
	}
	if be := entry.ErrorDetailsParsed; be != nil {
		row.ErrorMessage = truncateText(be.GetErrorString(), 300)
		if be.StatusCode != nil {
			row.StatusCode = *be.StatusCode
		}
		if be.Error != nil {
			if be.Error.Type != nil {
				row.ErrorType = *be.Error.Type
			}
			if be.Error.Code != nil {
				row.ErrorCode = *be.Error.Code
			}
		}
	}
	if includeContent {
		row.Content = truncateText(logContent(entry), contentLimit)
	}
	return row
}

// derefFloat reads a *float64, treating nil as zero.
func derefFloat(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

// derefString reads a *string, treating nil as empty.
func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// logContent renders a compact text view of a request.
//
// ContentHidden is the hard gate: it means content logging was disabled for
// that request, so the payload must never be served back through any API - a
// promise the deployment made to whoever's data this is, and an external MCP
// caller is the last place a hidden payload should resurface.
func logContent(entry *logstore.Log) string {
	if entry.ContentHidden {
		return ""
	}
	if entry.ContentSummary != "" {
		return entry.ContentSummary
	}
	var builder strings.Builder
	for _, message := range entry.InputHistoryParsed {
		if message.Content != nil && message.Content.ContentStr != nil {
			builder.WriteString(string(message.Role))
			builder.WriteString(": ")
			builder.WriteString(*message.Content.ContentStr)
			builder.WriteString("\n")
		}
	}
	if entry.OutputMessageParsed != nil && entry.OutputMessageParsed.Content != nil && entry.OutputMessageParsed.Content.ContentStr != nil {
		builder.WriteString("assistant: ")
		builder.WriteString(*entry.OutputMessageParsed.Content.ContentStr)
	}
	return strings.TrimSpace(builder.String())
}

// bucketSize picks the bucket width, reusing the same helper the dashboard
// uses. It then widens further if the range would still produce too many
// buckets.
func bucketSize(filters *logstore.SearchFilters) (int64, error) {
	// Every caller reaches this through filterArg -> parseFilters, which always
	// defaults both bounds before returning, so StartTime/EndTime are never nil
	// here.
	bucket := logstore.DefaultBucketSize(filters.StartTime, filters.EndTime)
	span := filters.EndTime.Sub(*filters.StartTime).Seconds()
	if bucket > 0 && span/float64(bucket) > MaxHistogramBuckets {
		return 0, fmt.Errorf("the requested time range produces more than %d buckets; use a shorter range", MaxHistogramBuckets)
	}
	return bucket, nil
}

// coarseBucketSize widens the bucket so a per-provider series stays small.
func coarseBucketSize(filters *logstore.SearchFilters) (int64, error) {
	span := filters.EndTime.Sub(*filters.StartTime).Seconds()
	if span <= 0 {
		return 0, fmt.Errorf("the time range is empty")
	}
	coarse := int64(span / CoarseBuckets)
	return max(coarse, logstore.DefaultBucketSize(filters.StartTime, filters.EndTime)), nil
}

// buildTools returns every tool this server hosts.
func buildTools() []Tool {
	return []Tool{
		semanticSearchLogsTool(),
		queryLogsTool(),
		countLogsTool(),
		getLogDetailTool(),
		getRequestTraceTool(),
		queryMetricsTool(),
		queryUsageByTool(),
		queryModelsTool(),
		describeFilterSpaceTool(),
		describeVirtualKeyTool(),

		queryMCPLogsTool(),
		countMCPLogsTool(),
		getMCPLogDetailTool(),
		queryMCPMetricsTool(),
		queryMCPUsageByTool(),
		getSessionTool(),
		getSessionSummaryTool(),
		getDroppedRequestsTool(),

		listVirtualKeysTool(),
		listTeamsTool(),
		describeTeamTool(),
		listCustomersTool(),
		describeCustomerTool(),
		listBudgetsTool(),
		describeBudgetTool(),
		createVirtualKeyTool(),
		updateVirtualKeyTool(),
		deactivateVirtualKeyTool(),
		rotateVirtualKeyTool(),
		createTeamTool(),
		createCustomerTool(),
		createBudgetTool(),
		updateBudgetTool(),

		getHealthTool(),
		getVersionTool(),

		listProvidersTool(),
		addProviderTool(),
		updateProviderTool(),
		listProviderKeysTool(),
		createProviderKeyTool(),
		updateProviderKeyTool(),

		listMCPClientsTool(),
		addMCPClientTool(),
		updateMCPClientTool(),
		reconnectMCPClientTool(),
		listMCPClientToolsTool(),
		listMCPAuthSessionsTool(),
		listVirtualMCPsTool(),
		describeVirtualMCPTool(),
		createVirtualMCPTool(),
		updateVirtualMCPTool(),
		attachVirtualMCPTool(),
		detachVirtualMCPTool(),

		listRoutingRulesTool(),
		describeRoutingRuleTool(),
		createRoutingRuleTool(),
		updateRoutingRuleTool(),

		listRateLimitsTool(),
		describeRateLimitTool(),
		createRateLimitTool(),
		updateRateLimitTool(),

		listModelConfigsTool(),
		describeModelConfigTool(),
		createModelConfigTool(),
		updateModelConfigTool(),

		describeModelTool(),
		refreshProviderModelsTool(),

		listPluginsTool(),
		describePluginTool(),
		createPluginTool(),
		updatePluginTool(),

		listPricingOverridesTool(),
		describePricingOverrideTool(),
		createPricingOverrideTool(),
		updatePricingOverrideTool(),

		listWebhooksTool(),
		describeWebhookTool(),
		createWebhookTool(),
		updateWebhookTool(),

		listFeatureFlagsTool(),
		updateFeatureFlagTool(),

		getWarpConfigTool(),
		updateWarpConfigTool(),
	}
}

// PropertyOrder is the order a tool's schema declares its top-level
// properties in, and FilterPropertyOrder the order the shared filter object
// declares its own. Both are read straight off the authored JSON.
//
// They exist because the order survives nothing else. A schema handed to
// mcp-go is decoded into a plain Go map before any client sees it, so by the
// time a tool comes back over a tools/list the author's order is gone and only
// a deterministic substitute remains. The order is not cosmetic: these schemas
// are written with the fields that steer a query first - the time window, the
// provider - and a model reading them alphabetically reaches for the wrong
// tool, or asks about a window the schema already told it how to express.
// A caller that declares these tools to a model reimposes the order with these.
func PropertyOrder(toolName string) []string {
	for _, tool := range buildTools() {
		if tool.name == toolName {
			return schemaPropertyOrder(tool.schemaJSON)
		}
	}
	return nil
}

// FilterPropertyOrder is PropertyOrder for the shared filter object every
// query tool embeds under "filters".
func FilterPropertyOrder() []string {
	return schemaPropertyOrder(FilterSchema)
}

// schemaPropertyOrder reads the keys of a schema's "properties" object in the
// order the JSON text lists them, which is the order the author chose.
func schemaPropertyOrder(schemaJSON string) []string {
	var schema struct {
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil || len(schema.Properties) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(schema.Properties))
	// Step over the opening brace so the loop below reads key tokens.
	if _, err := decoder.Token(); err != nil {
		return nil
	}
	var order []string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return order
		}
		key, ok := token.(string)
		if !ok {
			return order
		}
		order = append(order, key)
		// Consume the value so the next token read is the following key.
		var discard json.RawMessage
		if err := decoder.Decode(&discard); err != nil {
			return order
		}
	}
	return order
}
