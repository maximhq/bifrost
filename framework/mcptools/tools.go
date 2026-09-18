// Package mcptools hosts the read-only log/metrics/governance query tools on
// Bifrost's own MCP server, so any virtual-key-authenticated MCP client - not
// just Warp's dashboard agent - can reach them.
//
// Three rules hold for every tool here:
//
//  1. Read-only. No executor calls a write method, and Deps exposes nothing
//     that could.
//  2. Bounded. Every result passes through boundToolResult before it reaches
//     a caller. One unbounded log query would otherwise put megabytes of
//     prompt bodies into a model's context window.
//  3. Scope-carrying. Executors take the caller's context and hand it straight
//     to the store, which applies the queryscope row filter. Losing that
//     context means every query silently returns every row in the
//     deployment, so it is never replaced with context.Background().
//
// One Deps value is shared across every concurrent caller, so nothing
// caller-specific lives on it: each handler resolves the caller's default
// scope fresh, per call, from ctx (see scope.go).
package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/framework/logstore"
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
	// GovernanceReader. describe_virtual_key is the only tool that reaches it,
	// and reports itself unavailable rather than panicking on a nil pointer.
	Governance GovernanceReader
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
}

// FilterSchema is shared by every flow. It maps one-to-one onto
// logstore.SearchFilters.
const FilterSchema = `{
  "type": "object",
  "description": "Narrows which requests are considered. Omit a field to leave that dimension unfiltered. If start_time is omitted the last 24 hours are used.",
  "properties": {
    "start_time": {"type": "string", "description": "A relative offset like -7d (this week), -24h (today), -30m, or an RFC3339 timestamp. Use this for any time window the question names; do not ask which window to use."},
    "end_time": {"type": "string", "description": "RFC3339 timestamp. Defaults to now."},
    "providers": {"type": "array", "items": {"type": "string"}, "description": "e.g. openai, anthropic, bedrock."},
    "models": {"type": "array", "items": {"type": "string"}},
    "status": {"type": "array", "items": {"type": "string"}, "description": "success, error, or cancelled."},
    "stop_reasons": {"type": "array", "items": {"type": "string"}, "description": "e.g. stop, length, content_filter, tool_calls. Call describe_filter_space to see which actually occur."},
    "objects": {"type": "array", "items": {"type": "string"}, "description": "Request type, e.g. chat_completion, embedding, speech, transcription, image_generation, video_generation. Use this to exclude non-chat traffic - embeddings, speech, and image/video generation have their own cost and latency shape and otherwise get averaged in with chat requests."},
    "virtual_key_ids": {"type": "array", "items": {"type": "string"}},
    "team_ids": {"type": "array", "items": {"type": "string"}},
    "customer_ids": {"type": "array", "items": {"type": "string"}},
    "user_ids": {"type": "array", "items": {"type": "string"}},
    "business_unit_ids": {"type": "array", "items": {"type": "string"}},
    "project_ids": {"type": "array", "items": {"type": "string"}},
    "apps": {"type": "array", "items": {"type": "string"}},
    "min_latency": {"type": "number", "description": "Milliseconds."},
    "max_latency": {"type": "number", "description": "Milliseconds."},
    "min_tokens": {"type": "integer", "description": "Total tokens on the request."},
    "max_tokens": {"type": "integer", "description": "Total tokens on the request."},
    "min_cost": {"type": "number"},
    "max_cost": {"type": "number"},
    "cache_hit_types": {"type": "array", "items": {"type": "string", "enum": ["direct", "semantic"]}, "description": "Local-cache hit type: direct (exact match) or semantic (fuzzy match)."},
    "content_search": {"type": "string", "description": "Substring match against request and response content."}
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
		"cache_hit_types": true, "content_search": true,
	}
	unknown := []string{}
	for key := range raw {
		if !known[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown filter fields: %s. Supported fields are: start_time, end_time, providers, models, status, stop_reasons, objects, virtual_key_ids, team_ids, customer_ids, user_ids, business_unit_ids, project_ids, apps, min_latency, max_latency, min_tokens, max_tokens, min_cost, max_cost, cache_hit_types, content_search", strings.Join(unknown, ", "))
	}

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
		"stop_reasons": &filters.StopReasons, "objects": &filters.Objects,
		"virtual_key_ids": &filters.VirtualKeyIDs, "team_ids": &filters.TeamIDs,
		"customer_ids": &filters.CustomerIDs, "user_ids": &filters.UserIDs,
		"business_unit_ids": &filters.BusinessUnitIDs, "project_ids": &filters.ProjectIDs,
		"apps": &filters.Apps, "cache_hit_types": &filters.CacheHitTypes,
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
	if value, present := raw["content_search"]; present {
		if value == nil {
			return nil, fmt.Errorf("content_search must be a string, got null; omit the field to search without a content filter")
		}
		search, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("content_search must be a string")
		}
		if strings.TrimSpace(search) == "" {
			return nil, fmt.Errorf("content_search must not be empty; omit the field to search without a content filter")
		}
		if len(search) > MaxContentSearchChars {
			return nil, fmt.Errorf("content_search is %d characters; at most %d are accepted", len(search), MaxContentSearchChars)
		}
		filters.ContentSearch = search
	}
	return filters, nil
}

// parseTime accepts RFC3339 or a relative offset like "-7d".
func parseTime(value any, now time.Time) (*time.Time, error) {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return nil, nil
	}
	text = strings.TrimSpace(text)
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
		return nil, fmt.Errorf("could not parse %q, expected RFC3339 or a relative offset like -7d", text)
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
	raw, _ := args["filters"].(map[string]any)
	filters, err := parseFilters(raw, now)
	if err != nil {
		return nil, scope, err
	}
	applyScope(filters, scope)
	return filters, scope, nil
}

// enumSliceArg reads a list argument whose values must come from a fixed set.
//
// The schema is advertised to the provider, not enforced on what comes back, so
// dropping the elements that do not fit turned ["cost", 42] into ["cost"] and
// answered a narrower question than the one asked - successfully, which is the
// part that makes it hard to notice.
func enumSliceArg(args map[string]any, key, noun string, allowed []string) ([]string, error) {
	value, present := args[key]
	if !present || value == nil {
		return nil, fmt.Errorf("%s must list at least one of: %s", key, strings.Join(allowed, ", "))
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
	// The schema's maxItems is advertised to the provider, never enforced on what
	// comes back. Each metric here is a separate database query, so a list of a
	// hundred repeated valid values was a hundred sequential queries - bounded by
	// the vocabulary is not bounded by anything a caller cannot repeat.
	if len(items) > len(allowed) {
		return nil, fmt.Errorf("%s lists %d values; at most %d are accepted, and each one is a separate query", key, len(items), len(allowed))
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
	if !present || value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of strings", key)
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
	return text[:limit] + "... [truncated]"
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
	ErrorType      string  `json:"error_type,omitempty"`
	ErrorCode      string  `json:"error_code,omitempty"`
	StatusCode     int     `json:"status_code,omitempty"`
	Content        string  `json:"content,omitempty"`
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
