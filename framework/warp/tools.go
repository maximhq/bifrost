package warp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

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
    "providers": {"type": "array", "items": {"type": "string"}, "description": "e.g. openai, anthropic, bedrock."},
    "models": {"type": "array", "items": {"type": "string"}},
    "status": {"type": "array", "items": {"type": "string"}, "description": "success or error."},
    "virtual_key_ids": {"type": "array", "items": {"type": "string"}},
    "team_ids": {"type": "array", "items": {"type": "string"}},
    "customer_ids": {"type": "array", "items": {"type": "string"}},
    "user_ids": {"type": "array", "items": {"type": "string"}},
    "business_unit_ids": {"type": "array", "items": {"type": "string"}},
    "apps": {"type": "array", "items": {"type": "string"}},
    "min_latency": {"type": "number", "description": "Milliseconds."},
    "max_latency": {"type": "number", "description": "Milliseconds."},
    "min_cost": {"type": "number"},
    "max_cost": {"type": "number"},
    "content_search": {"type": "string", "description": "Substring match against request and response content."}
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
	}
	unknown := []string{}
	for key := range raw {
		if !known[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown filter fields: %s. Supported fields are: start_time, end_time, providers, models, status, virtual_key_ids, team_ids, customer_ids, user_ids, business_unit_ids, apps, min_latency, max_latency, min_cost, max_cost, content_search", strings.Join(unknown, ", "))
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

	filters.Providers = stringSlice(raw["providers"])
	filters.Models = stringSlice(raw["models"])
	filters.Status = stringSlice(raw["status"])
	filters.VirtualKeyIDs = stringSlice(raw["virtual_key_ids"])
	filters.TeamIDs = stringSlice(raw["team_ids"])
	filters.CustomerIDs = stringSlice(raw["customer_ids"])
	filters.UserIDs = stringSlice(raw["user_ids"])
	filters.BusinessUnitIDs = stringSlice(raw["business_unit_ids"])
	filters.Apps = stringSlice(raw["apps"])
	filters.MinLatency = floatPtr(raw["min_latency"])
	filters.MaxLatency = floatPtr(raw["max_latency"])
	filters.MinCost = floatPtr(raw["min_cost"])
	filters.MaxCost = floatPtr(raw["max_cost"])
	if search, ok := raw["content_search"].(string); ok {
		filters.ContentSearch = search
	}
	return filters, nil
}

// parseTime accepts RFC3339 or a relative offset like "-7d". Models reach
// for relative offsets constantly ("last week"), and making them compute an
// absolute timestamp from a date they only half-know is a reliable source of
// wrong answers.
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

// stringSlice reads a JSON array of strings, dropping empties. Returns nil when absent, so the filter field stays unset rather than becoming an empty IN clause.
func stringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && text != "" {
			result = append(result, text)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// floatPtr reads an optional JSON number into the pointer the filter expects.
func floatPtr(value any) *float64 {
	number, ok := value.(float64)
	if !ok {
		return nil
	}
	return &number
}

// intArg reads a bounded integer argument.
//
// Values above max are clamped rather than rejected: the cap exists to protect
// the context window, not to police the model, and failing the call would cost
// an extra round trip to arrive at the number we would have used anyway.
func intArg(args map[string]any, key string, fallback, max int) int {
	value, ok := args[key].(float64)
	if !ok {
		return fallback
	}
	result := int(value)
	if result < 1 {
		return fallback
	}
	// Clamp rather than reject. The cap exists to protect the context window,
	// not to police the model, and failing the call would just cost another
	// round trip to arrive at the number we would have used anyway.
	return min(result, max)
}

// boolArg reads an optional boolean argument, defaulting to false.
func boolArg(args map[string]any, key string) bool {
	value, _ := args[key].(bool)
	return value
}

// filterArg parses the shared filter object every flow accepts.
func filterArg(args map[string]any, now time.Time) (*logstore.SearchFilters, error) {
	raw, _ := args["filters"].(map[string]any)
	return parseFilters(raw, now)
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

// truncateText caps a string and marks it, so the model can tell it is reading a fragment rather than the whole value.
func truncateText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "... [truncated]"
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

// buildTools returns the tools available for a request.
//
// It takes the deps rather than closing over a handler so the set can be built
// per request, which is what will let a future change withhold content-bearing
// tools from callers who may not read log bodies.
func buildTools() []Tool {
	return []Tool{
		queryLogsTool(),
		getLogDetailTool(),
		queryMetricsTool(),
		queryUsersTool(),
		queryVirtualKeysTool(),
		queryModelsTool(),
		describeFilterSpaceTool(),
	}
}

// chatTools converts the tool set into provider-facing declarations.
func chatTools(tools []Tool) ([]schemas.ChatTool, error) {
	declared := make([]schemas.ChatTool, 0, len(tools))
	for _, tool := range tools {
		var parameters schemas.ToolFunctionParameters
		if err := sonic.UnmarshalString(tool.schemaJSON, &parameters); err != nil {
			return nil, fmt.Errorf("warp tool %s has an invalid schema: %w", tool.name, err)
		}
		declared = append(declared, schemas.ChatTool{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        tool.name,
				Description: &[]string{tool.description}[0],
				Parameters:  &parameters,
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
