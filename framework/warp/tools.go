package warp

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// Warp's tools are the only way it can see the deployment's data. Three rules
// hold for every one of them:
//
//  1. Read-only. No executor calls a write method, and the dependency struct
//     below exposes nothing that could.
//  2. Bounded. Every result passes through boundToolResult before it reaches
//     the model. One unbounded log query would otherwise put megabytes of prompt
//     bodies into the context window.
//  3. Scope-carrying. Executors take the caller's context and hand it straight to
//     the store, which applies the queryscope row filter. Losing that context
//     means every query silently returns every row in the deployment, so it is
//     never replaced with context.Background().
const (
	// MaxToolResultBytes caps a serialized tool result. Beyond this the
	// result is replaced wholesale with an instruction to narrow the query.
	// Truncating the JSON instead would hand the model a document it cannot tell
	// is incomplete, and it will answer from the fragment without hedging.
	MaxToolResultBytes = 16384

	// MaxLogRows caps query_logs regardless of what the model asks for.
	MaxLogRows = 25
	// MaxRankingRows caps every ranking tool.
	MaxRankingRows = 20
	// MaxHistogramBuckets rejects a range/bucket combination that would
	// produce more series points than are useful to reason over.
	MaxHistogramBuckets = 200
	// LargeResultThreshold is where "list the rows" stops being a sensible
	// answer and aggregates take over. Set well under what would overflow a tool
	// result even at the row cap, so the model is redirected before it wastes a
	// call rather than after.
	LargeResultThreshold = 500
	// CoarseBuckets is what a per-provider series is reduced to. A dozen
	// points carry the shape of a trend; the rest is detail nobody reads out of
	// a JSON blob.
	CoarseBuckets = 12

	// MaxFilterValues bounds one filter array, and MaxContentSearchChars one
	// substring. Both become query predicates, so an unbounded list from the
	// model turns straight into an unbounded query - and a question needing more
	// than this many names is one that should have been asked by dimension.
	MaxFilterValues       = 50
	MaxContentSearchChars = 500

	// LogContentChars bounds prompt/response text when a question genuinely
	// needs it. Long enough to judge what a request was doing, short enough that
	// 25 of them cannot dominate the context.
	LogContentChars = 400
	// DetailContentChars is the larger budget for a single-row drill-down.
	DetailContentChars = 2000

	// DefaultLookback is the window used when the model names no time range.
	DefaultLookback = 24 * time.Hour
)

// Now is a package-level seam so tests can pin "now" and assert on the
// windows relative offsets resolve to. Relative times are the common case in
// Warp's traffic ("last week"), so they need to be testable without sleeping.
var Now = func() time.Time { return time.Now().UTC() }

// ToolDeps is the entire surface Warp's tools can reach. It is deliberately
// narrow: read methods on the log manager, and nothing else. Widening this type
// is the decision point for whether Warp can see something new.
type ToolDeps struct {
	logManager LogReader
	semantic   *SemanticSearcher
	// scope is the caller's default slice of traffic. It narrows a question that
	// named no scope of its own; it is not an access control, which queryscope
	// already applies inside the store.
	scope Scope
}

// Tool pairs a model-facing declaration with its executor.
type Tool struct {
	name string
	// schemaJSON is raw JSON rather than a hand-built OrderedMap.
	// ToolFunctionParameters implements UnmarshalJSON and preserves key order,
	// and models are sensitive to property order, so writing the schema as the
	// literal document the model will see is both clearer and more faithful.
	schemaJSON  string
	description string
	execute     func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error)
}

// FilterSchema is shared by every flow. It maps one-to-one onto
// logstore.SearchFilters, which is what lets one parser and one scope-injection
// point serve all of them.
const FilterSchema = `{
  "type": "object",
  "description": "Narrows which requests are considered. Omit a field to leave that dimension unfiltered. If start_time is omitted the last 24 hours are used.",
  "properties": {
    "start_time": {"type": "string", "description": "RFC3339 timestamp, or a relative offset like -7d, -24h, -30m."},
    "end_time": {"type": "string", "description": "RFC3339 timestamp. Defaults to now."},
    "providers": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "e.g. openai, anthropic, bedrock."},
    "models": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "status": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "success or error."},
    "virtual_key_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "team_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "customer_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "user_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "business_unit_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "apps": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "min_latency": {"type": "number", "description": "Milliseconds."},
    "max_latency": {"type": "number", "description": "Milliseconds."},
    "min_cost": {"type": "number"},
    "max_cost": {"type": "number"},
    "content_search": {"type": "string", "minLength": 1, "maxLength": 500, "description": "Substring match against request and response content. Omit the field rather than sending an empty string."},
    "scope": {"type": "string", "enum": ["caller", "all"], "description": "Whose traffic. Omitting it defaults to the caller's own traffic only when the caller is identified; when nobody is identified there is no default and the query is bounded only by that deployment's access rules, so ask whose traffic is meant first. Use \"all\" when the question is explicitly about everyone's - it widens the question, not the permission, so results are still limited to what the caller may see."}
  }
}`

// parseFilters converts the model's filter object into SearchFilters.
//
// Unknown keys are rejected rather than ignored. A silently dropped filter
// produces a plausible answer to a different question than the one asked, which
// is the worst failure mode available here: the model cannot tell, and neither
// can the reader.
func parseFilters(raw map[string]any, now time.Time) (*logstore.SearchFilters, error) {
	filters := &logstore.SearchFilters{}
	if raw == nil {
		raw = map[string]any{}
	}

	known := map[string]bool{
		"start_time": true, "end_time": true, "providers": true, "models": true,
		"status": true, "virtual_key_ids": true, "team_ids": true, "customer_ids": true,
		"user_ids": true, "business_unit_ids": true, "apps": true, "min_latency": true,
		"max_latency": true, "min_cost": true, "max_cost": true, "content_search": true,
		"scope": true,
	}
	unknown := []string{}
	for key := range raw {
		if !known[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown filter fields: %s. Supported fields are: start_time, end_time, providers, models, status, virtual_key_ids, team_ids, customer_ids, user_ids, business_unit_ids, apps, min_latency, max_latency, min_cost, max_cost, content_search, scope", strings.Join(unknown, ", "))
	}

	// Presence is checked here, not left to parseTime: indexing a map gives nil
	// for an absent key and for an explicit JSON null alike, so `"start_time":
	// null` took the default window instead of being rejected - and FilterSchema
	// declares both as strings, so null was never in the contract.
	// Presence is checked here, not left to parseTime: indexing a map gives nil
	// for an absent key and for an explicit JSON null alike, so `"start_time":
	// null` took the default window instead of being rejected - and FilterSchema
	// declares both as strings, so null was never in the contract.
	for _, key := range [...]string{"start_time", "end_time"} {
		if value, present := raw[key]; present && value == nil {
			return nil, fmt.Errorf("%s must be a string, got null; omit the field to use the default window", key)
		}
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
		"virtual_key_ids": &filters.VirtualKeyIDs, "team_ids": &filters.TeamIDs,
		"customer_ids": &filters.CustomerIDs, "user_ids": &filters.UserIDs,
		"business_unit_ids": &filters.BusinessUnitIDs, "apps": &filters.Apps,
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
	if value, present := raw["content_search"]; present {
		// Present-and-null is not the same as absent, for the same reason the time
		// fields check presence above: the schema types this as a string.
		if value == nil {
			return nil, fmt.Errorf("content_search must be a string, got null; omit the field to search without a content filter")
		}
		search, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("content_search must be a string")
		}
		// An empty or whitespace-only value reached the logstore as if the field
		// had been omitted - the query applies the content predicate only when
		// ContentSearch is non-empty - so a filtered question came back with
		// unfiltered rows and nothing said the filter had been dropped.
		if strings.TrimSpace(search) == "" {
			return nil, fmt.Errorf("content_search must not be empty; omit the field to search without a content filter")
		}
		// Runes, not bytes: the schema's maxLength counts characters, and
		// len() counting UTF-8 bytes rejected a 500-character non-ASCII search
		// the schema had just accepted - with an error reporting the byte
		// count as a character count.
		if count := utf8.RuneCountInString(search); count > MaxContentSearchChars {
			return nil, fmt.Errorf("content_search is %d characters; at most %d are accepted", count, MaxContentSearchChars)
		}
		filters.ContentSearch = search
	}
	return filters, nil
}

// parseTime accepts RFC3339 or a relative offset like "-7d". Models reach
// for relative offsets constantly ("last week"), and making them compute an
// absolute timestamp from a date they only half-know is a reliable source of
// wrong answers.
func parseTime(value any, now time.Time) (*time.Time, error) {
	// Absent means "use the default window", which is documented. A value that
	// is present but not a usable timestamp does not: returning nil for it
	// turned a malformed argument into a valid query over different dates, and
	// the answer came back looking exactly like the one that was asked for.
	if value == nil {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("must be an RFC3339 timestamp or a relative offset like -7d, got %T", value)
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("must be an RFC3339 timestamp or a relative offset like -7d, got a blank string")
	}
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "-") {
		// time.ParseDuration has no day unit, which is the one people actually use.
		if strings.HasSuffix(text, "d") {
			// The digits between the sign and the final "d" are the whole value.
			// Sscanf stopped at the first match and ignored the rest, so "-7daysd"
			// satisfied the suffix guard, parsed as 7, and ran a seven-day query
			// for a filter that should have been rejected.
			days, err := strconv.ParseFloat(strings.TrimSuffix(text[1:], "d"), 64)
			if err != nil || math.IsNaN(days) || math.IsInf(days, 0) || days <= 0 {
				return nil, fmt.Errorf("could not parse relative offset %q", text)
			}
			result := now.Add(-time.Duration(days * float64(24*time.Hour)))
			return &result, nil
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
		return nil, fmt.Errorf("could not parse %q, expected RFC3339 or a relative offset like -7d", text)
	}
	return &parsed, nil
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

// floatPtr reads an optional JSON number into the pointer the filter expects.
func floatPtr(value any) *float64 {
	number, ok := value.(float64)
	if !ok {
		return nil
	}
	return &number
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
	result := int(value)
	if result < 1 {
		return 0, fmt.Errorf("%s must be at least 1, got %d", key, result)
	}
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

// filterArg parses the shared filter object every flow accepts and applies
// the caller's default scope.
//
// Every flow goes through here, which is what makes the default impossible to
// forget: a tool added later gets the scoping by construction rather than by
// its author remembering to ask for it.
func filterArg(args map[string]any, now time.Time, scope Scope) (*logstore.SearchFilters, error) {
	raw, err := filtersObject(args)
	if err != nil {
		return nil, err
	}
	filters, parseErr := parseFilters(raw, now)
	if parseErr != nil {
		return nil, parseErr
	}
	// Read before the filters are handed on: an explicit "all" is a different
	// question from one that simply named no scope, and only the marker can tell
	// them apart.
	mode, err := ParseScopeMode(raw["scope"])
	if err != nil {
		return nil, err
	}
	// "caller" needs a caller. Without an identity applyScope adds no user
	// filter, so the query would run with no traffic dimension at all and - on a
	// deployment with no queryscope - answer about everyone while claiming to be
	// scoped to the person asking.
	if mode == ScopeModeCaller && !scope.HasIdentity {
		return nil, fmt.Errorf("cannot scope to the caller: this deployment has no user identity. Name a team, customer or business unit, or use scope \"all\"")
	}
	applyScope(filters, scope, mode)
	return filters, nil
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

// boundToolResult serializes a result and enforces the byte budget.
//
// Over budget, the payload is discarded entirely and replaced with an
// instruction. This is deliberate: a tail-truncated JSON document reads to the
// model as a complete one, and it will summarize the fragment as though it were
// the whole answer. An explicit refusal makes the model narrow its filters,
// which produces a correct answer one round trip later.
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

// truncateText caps a string and marks it, so the model can tell it is reading
// a fragment rather than the whole value.
//
// The cut is on rune boundaries, not byte offsets. Log content is arbitrary
// user text and often non-ASCII, so a byte slice would split a multi-byte rune
// and leave invalid UTF-8 in the tool result - the model reads a replacement
// character where the original was. The budgets above are documented as
// character counts, so counting runes is also what they mean.
func truncateText(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	count := 0
	for i := range text {
		if count == limit {
			return text[:i] + "... [truncated]"
		}
		count++
	}
	return text + "... [truncated]"
}

// logRow is the projection query_logs returns. The full logstore.Log carries
// raw request and response bodies; returning even a handful of those would
// exhaust the context window, so the row is reduced to the fields that answer
// operational questions and content is opt-in.
type logRow struct {
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
	Content        string  `json:"content,omitempty"`
}

// projectLog reduces a log row to the fields that answer operational
// questions. The full row carries raw request and response bodies; returning
// even a handful of those would exhaust the context window.
func projectLog(entry *logstore.Log, includeContent bool, contentLimit int) logRow {
	row := logRow{
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
	}
	if entry.ErrorDetailsParsed != nil && entry.ErrorDetailsParsed.Error != nil {
		row.ErrorMessage = truncateText(entry.ErrorDetailsParsed.Error.Message, 300)
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

// logContent renders a compact text view of a request.
//
// ContentHidden is the hard gate. It means content logging was disabled for that
// request, so the payload must never be served back through any API — a promise
// the deployment made to whoever's data this is. Warp is an API like any other,
// and a model is the last place a hidden payload should resurface.
//
// Otherwise it prefers ContentSummary, the stored last-user-message preview,
// which is already bounded. Reconstructing the full message history would
// reintroduce exactly the size problem this projection exists to solve.
func logContent(entry *logstore.Log) string {
	if entry.ContentHidden {
		return ""
	}
	if entry.ContentSummary != "" {
		return entry.ContentSummary
	}
	// ChatMessage.Content is a pointer and is routinely nil - a tool-call turn
	// carries none, and an offloaded payload leaves the parsed history empty.
	// Every access below is guarded because this walks logged traffic, which is
	// the least predictable data in the system.
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
// uses so Warp's numbers line up with the charts a user is looking at. It then
// widens further if the range would still produce too many buckets.
func bucketSize(filters *logstore.SearchFilters) (int64, error) {
	bucket := logstore.DefaultBucketSize(filters.StartTime, filters.EndTime)
	if filters.StartTime == nil || filters.EndTime == nil {
		return bucket, nil
	}
	span := filters.EndTime.Sub(*filters.StartTime).Seconds()
	if bucket > 0 && span/float64(bucket) > MaxHistogramBuckets {
		return 0, fmt.Errorf("the requested time range produces more than %d buckets; use a shorter range", MaxHistogramBuckets)
	}
	return bucket, nil
}

// coarseBucketSize widens the bucket so a per-provider series stays small.
//
// The dashboard's bucket size is sized for a chart with hundreds of pixels. The
// same series serialized as JSON, repeated once per provider, comfortably
// exceeds the tool-result budget - and an over-budget result is discarded, so
// the model retries, which is how one question turned into four identical
// queries in the logs.
func coarseBucketSize(filters *logstore.SearchFilters) (int64, error) {
	if filters.StartTime == nil || filters.EndTime == nil {
		return logstore.DefaultBucketSize(filters.StartTime, filters.EndTime), nil
	}
	span := filters.EndTime.Sub(*filters.StartTime).Seconds()
	if span <= 0 {
		return 0, fmt.Errorf("the time range is empty")
	}
	// Aim for CoarseBuckets points, never finer than the dashboard would use.
	coarse := int64(span / CoarseBuckets)
	return max(coarse, logstore.DefaultBucketSize(filters.StartTime, filters.EndTime)), nil
}

// buildTools returns the tools available for a request.
//
// It takes the deps rather than closing over a handler so the set can be built
// per request, which is what will let a future change withhold content-bearing
// tools from callers who may not read log bodies.
func buildTools() []Tool {
	return buildToolsFor(nil)
}

// buildToolsFor returns the tools a request can actually run.
//
// semantic_search_logs needs an embedding executor, which a deployment may not
// have configured. Declaring it anyway tells the model a capability exists,
// costs it a step to discover otherwise, and on a deployment with no embedding
// provider does that on every single attempt.
func buildToolsFor(searcher *SemanticSearcher) []Tool {
	tools := []Tool{}
	if searcher != nil {
		tools = append(tools, semanticSearchLogsTool())
	}
	tools = append(tools,
		queryLogsTool(),
		countLogsTool(),
		getLogDetailTool(),
		queryMetricsTool(),
		queryUsersTool(),
		queryVirtualKeysTool(),
		queryModelsTool(),
		describeFilterSpaceTool(),
		describeScopeTool(),
		askUserToolDef(),
	)
	return tools
}

// ChatTools converts the tool set into provider-facing declarations.
func responsesTools(tools []Tool) ([]schemas.ResponsesTool, error) {
	declared := make([]schemas.ResponsesTool, 0, len(tools))
	for _, tool := range tools {
		var parameters schemas.ToolFunctionParameters
		if err := sonic.UnmarshalString(tool.schemaJSON, &parameters); err != nil {
			return nil, fmt.Errorf("warp tool %s has an invalid schema: %w", tool.name, err)
		}
		declared = append(declared, schemas.ResponsesTool{
			Type:        schemas.ResponsesToolTypeFunction,
			Name:        new(tool.name),
			Description: new(tool.description),
			ResponsesToolFunction: &schemas.ResponsesToolFunction{
				Parameters: &parameters,
			},
		})
	}
	return declared, nil
}

// toolByName looks up a tool by the name the model used.
func toolByName(tools []Tool, name string) (*Tool, bool) {
	for i := range tools {
		if tools[i].name == name {
			return &tools[i], true
		}
	}
	return nil, false
}

// filtersObject reads the top-level filters argument, rejecting a value of the
// wrong shape rather than discarding it.
//
// A discarded type assertion turned a malformed argument into "no filters",
// which parseFilters answers with the default unfiltered 24-hour window - so a
// bad filter widened the query instead of failing it, and nothing told the
// model its filter had been ignored. This checks only the top-level type; the
// fields inside a valid object are validated by parseFilters.
func filtersObject(args map[string]any) (map[string]any, error) {
	value, present := args["filters"]
	if !present || value == nil {
		return nil, nil
	}
	raw, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("filters must be an object mapping dimension names to values, got %T", value)
	}
	return raw, nil
}
