package logstore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// Request traces: the causal chain behind one request.
//
// A trace walks a request's retry attempts and its full fallback chain (every
// provider and model tried, in order), and returns each hop's error, guardrail
// and cache decisions, and latency breakdown. It explains why one request did
// what it did; it cannot explain why an aggregate changed. GET
// /api/logs/{id}/trace serves it, and Warp's get_request_trace tool reads the
// same function, so both report the same chain.

const (
	// MaxTraceChainLength caps fallback hops returned beyond the root. A real
	// chain runs a handful of providers deep at most; this exists so a
	// misconfigured deployment with many fallbacks cannot blow the tool-result
	// budget on one request.
	MaxTraceChainLength = 10
	// MaxTraceLogLines caps plugin and routing-engine log lines per node.
	// These are free-text diagnostic lines, not a bounded row set like
	// query_logs returns - unbounded, one noisy node could dominate the result.
	MaxTraceLogLines = 20
	// MaxTraceJudgeCalls caps guardrail judge calls returned per node.
	MaxTraceJudgeCalls = 20
	// MaxTraceAttempts caps retry-attempt records returned per node.
	MaxTraceAttempts = 20
	// MaxTraceDiagnosticBytes bounds how much of PluginLogs' and
	// RoutingEngineLogs' raw persisted text traceFromLog will parse or split,
	// per node. Both are free-text columns with no size cap enforced where
	// they are written (see plugins/logging's serializePluginLogs and
	// formatRoutingEngineLogs), and a chain can carry up to
	// MaxTraceChainLength+1 nodes - so without this, one chatty plugin or a
	// busy routing engine on any hop forces a full sonic.UnmarshalString or
	// strings.Split over an arbitrarily large blob, long before
	// MaxTraceLogLines gets a chance to bound the output.
	MaxTraceDiagnosticBytes = 64 * 1024
)

// TraceAttempt projects one retry attempt from AttemptTrailParsed.
type TraceAttempt struct {
	Attempt           int    `json:"attempt"`
	KeyName           string `json:"key_name,omitempty"`
	FailReason        string `json:"fail_reason,omitempty"`
	TriggeredRotation bool   `json:"triggered_rotation,omitempty"`
}

// TraceError projects the fields of a BifrostError useful for root-causing a
// failure, out of the full struct's cost/streaming/redaction plumbing.
type TraceError struct {
	StatusCode int    `json:"status_code,omitempty"`
	Type       string `json:"type,omitempty"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message,omitempty"`
}

// TraceCache projects the semantic/direct cache decision for a request.
type TraceCache struct {
	Hit        bool     `json:"hit"`
	HitType    string   `json:"hit_type,omitempty"`
	Similarity *float64 `json:"similarity,omitempty"`
	Threshold  *float64 `json:"threshold,omitempty"`
}

// TraceGuardrailDecision projects one judge call from GuardrailDebugParsed.
type TraceGuardrailDecision struct {
	Phase         string `json:"phase,omitempty"`
	RuleName      string `json:"rule_name,omitempty"`
	GuardrailName string `json:"guardrail_name,omitempty"`
	Action        string `json:"action,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// TraceOverhead projects one self-time span from OverheadBreakdownParsed.
type TraceOverhead struct {
	Name       string  `json:"name"`
	DurationMs float64 `json:"duration_ms"`
}

// TracePluginLogLine is one plugin log entry, flattened out of the per-plugin
// grouping the row persists it under. A map keeps every plugin's own lines in
// order but not the interleaving between plugins, and json.Marshal sorts map
// keys alphabetically on top of that - both erase the cross-plugin sequence
// this trace is meant to explain. A flat, timestamp-sorted slice keeps it.
type TracePluginLogLine struct {
	Plugin    string `json:"plugin"`
	Timestamp string `json:"timestamp"`
	Level     string `json:"level,omitempty"`
	Message   string `json:"message"`
}

// TraceNode is one request in a causal chain: the primary attempt (fallback
// index 0) or one fallback hop.
type TraceNode struct {
	ID                string                   `json:"id"`
	FallbackIndex     int                      `json:"fallback_index"`
	Timestamp         string                   `json:"timestamp"`
	Provider          string                   `json:"provider"`
	Model             string                   `json:"model"`
	Status            string                   `json:"status"`
	StopReason        string                   `json:"stop_reason,omitempty"`
	LatencyMs         float64                  `json:"latency_ms,omitempty"`
	UpstreamLatencyMs float64                  `json:"upstream_latency_ms,omitempty"`
	OverheadLatencyMs float64                  `json:"overhead_latency_ms,omitempty"`
	NumberOfRetries   int                      `json:"number_of_retries,omitempty"`
	Attempts          []TraceAttempt           `json:"attempts,omitempty"`
	Error             *TraceError              `json:"error,omitempty"`
	Cache             *TraceCache              `json:"cache,omitempty"`
	Guardrails        []TraceGuardrailDecision `json:"guardrails,omitempty"`
	// PluginLogs and RoutingEngineLogs are ordered by when they actually
	// happened, not re-grouped or deduplicated: their sequence is itself part
	// of the causal story (a plugin logging "rejected" before governance logs
	// "fallback triggered" reads differently than the reverse).
	PluginLogs        []TracePluginLogLine `json:"plugin_logs,omitempty"`
	RoutingEngineLogs []string             `json:"routing_engine_logs,omitempty"`
	OverheadBreakdown []TraceOverhead      `json:"overhead_breakdown,omitempty"`
	// Link is a dashboard path to the row. The API leaves it empty; Warp fills it.
	Link string `json:"link,omitempty"`
}

// TraceReader is what BuildRequestTrace reads logs through. Both calls must
// apply the caller's access scope, as the log manager's do.
type TraceReader interface {
	GetLog(ctx context.Context, id string) (*Log, error)
	Search(ctx context.Context, filters *SearchFilters, pagination *PaginationOptions) (*SearchResult, error)
}

// RequestTrace is the causal chain behind one request: its root and every
// fallback hop, ordered by fallback index.
type RequestTrace struct {
	RootID      string      `json:"root_id"`
	ChainLength int         `json:"chain_length"`
	Nodes       []TraceNode `json:"nodes"`
	// TruncatedChain is true when the chain has more fallback hops than
	// MaxTraceChainLength. The last node is then not the terminal hop, so
	// FinalStatus is left empty rather than inferred from it.
	TruncatedChain bool   `json:"truncated_chain"`
	FinalStatus    string `json:"final_status,omitempty"`
}

// BuildRequestTrace resolves the full chain of the request with this id, which
// can be the root or any fallback hop. A row the reader cannot load, because it
// does not exist or is outside the caller's scope, returns a TraceNotFoundError.
func BuildRequestTrace(ctx context.Context, reader TraceReader, id string) (*RequestTrace, error) {
	if id == "" {
		return nil, fmt.Errorf("log id is required")
	}
	entry, err := loadTraceLog(ctx, reader, id)
	if err != nil {
		return nil, err
	}
	root, rootID := entry, entry.ID
	// A row's own id can equal its parent_request_id on a corrupted or
	// deliberately self-referencing row; treating that as "has a parent" would
	// recurse into fetching itself as its own root.
	if entry.ParentRequestID != nil && *entry.ParentRequestID != "" && *entry.ParentRequestID != entry.ID {
		rootID = *entry.ParentRequestID
		if root, err = loadTraceLog(ctx, reader, rootID); err != nil {
			return nil, err
		}
	}
	children, err := reader.Search(ctx, &SearchFilters{ParentRequestID: rootID}, &PaginationOptions{
		Limit: MaxTraceChainLength, SortBy: "timestamp", Order: "asc",
	})
	if err != nil {
		return nil, fmt.Errorf("could not load fallback chain for %s: %w", rootID, err)
	}
	nodes := make([]TraceNode, 0, 1+len(children.Logs))
	nodes = append(nodes, TraceFromLog(root))
	for i := range children.Logs {
		nodes = append(nodes, TraceFromLog(&children.Logs[i]))
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].FallbackIndex < nodes[j].FallbackIndex })
	trace := &RequestTrace{
		RootID:         rootID,
		ChainLength:    len(nodes),
		Nodes:          nodes,
		TruncatedChain: children.Pagination.TotalCount > int64(len(children.Logs)),
	}
	if !trace.TruncatedChain {
		trace.FinalStatus = nodes[len(nodes)-1].Status
	}
	return trace, nil
}

// TraceNotFoundError names the row a trace could not load: the one asked for,
// or the root of its chain. It matches ErrNotFound.
type TraceNotFoundError struct{ ID string }

func (e *TraceNotFoundError) Error() string { return "log not found: " + e.ID }

// Is makes errors.Is(err, ErrNotFound) hold.
func (e *TraceNotFoundError) Is(target error) bool { return target == ErrNotFound }

// loadTraceLog loads one row, reporting a missing one as a TraceNotFoundError.
func loadTraceLog(ctx context.Context, reader TraceReader, id string) (*Log, error) {
	entry, err := reader.GetLog(ctx, id)
	if errors.Is(err, ErrNotFound) || (err == nil && entry == nil) {
		return nil, &TraceNotFoundError{ID: id}
	}
	if err != nil {
		return nil, fmt.Errorf("could not load log %s: %w", id, err)
	}
	return entry, nil
}

// TraceFromLog projects one log row into a TraceNode, bounding every
// unbounded-length field it carries (attempts, plugin/routing log lines,
// guardrail judge calls) so a single noisy request cannot dominate the result.
func TraceFromLog(entry *Log) TraceNode {
	node := TraceNode{
		ID:                entry.ID,
		FallbackIndex:     entry.FallbackIndex,
		Timestamp:         entry.Timestamp.UTC().Format(time.RFC3339),
		Provider:          entry.Provider,
		Model:             entry.Model,
		Status:            entry.Status,
		LatencyMs:         derefTraceFloat(entry.Latency),
		UpstreamLatencyMs: derefTraceFloat(entry.UpstreamLatency),
		OverheadLatencyMs: derefTraceFloat(entry.OverheadLatency),
		NumberOfRetries:   entry.NumberOfRetries,
	}
	if entry.StopReason != nil {
		node.StopReason = *entry.StopReason
	}

	for i, attempt := range entry.AttemptTrailParsed {
		if i >= MaxTraceAttempts {
			break
		}
		rec := TraceAttempt{Attempt: attempt.Attempt, KeyName: attempt.KeyName, TriggeredRotation: attempt.TriggeredRotation}
		if attempt.FailReason != nil {
			rec.FailReason = *attempt.FailReason
		}
		node.Attempts = append(node.Attempts, rec)
	}

	if be := entry.ErrorDetailsParsed; be != nil {
		summary := &TraceError{Message: truncateTraceText(be.GetErrorString(), 300)}
		if be.StatusCode != nil {
			summary.StatusCode = *be.StatusCode
		}
		if be.Error != nil {
			if be.Error.Type != nil {
				summary.Type = *be.Error.Type
			}
			if be.Error.Code != nil {
				summary.Code = *be.Error.Code
			}
		}
		node.Error = summary
	}

	if cd := entry.CacheDebugParsed; cd != nil {
		cache := &TraceCache{Hit: cd.CacheHit, Similarity: cd.Similarity, Threshold: cd.Threshold}
		if cd.HitType != nil {
			cache.HitType = *cd.HitType
		}
		node.Cache = cache
	}

	if gd := entry.GuardrailDebugParsed; gd != nil {
		for i, call := range gd.JudgeCalls {
			if i >= MaxTraceJudgeCalls {
				break
			}
			node.Guardrails = append(node.Guardrails, TraceGuardrailDecision{
				Phase: call.Phase, RuleName: call.RuleName, GuardrailName: call.GuardrailName,
				Action: call.Action, Reason: truncateTraceText(call.Reason, 300),
			})
		}
	}

	// PluginLogs is stored as JSON grouped by plugin name (see
	// plugins/logging.serializePluginLogs); RoutingEngineLogs is stored as
	// pre-formatted "[ts] [engine] [level] - message" lines (see
	// plugins/logging.formatRoutingEngineLogs), not JSON, so it is only split
	// into lines rather than unmarshaled.
	// Over MaxTraceDiagnosticBytes the field is skipped rather than unmarshaled
	// - the same silent omission already applied to a malformed PluginLogs
	// value below, since a byte-truncated JSON document would fail to parse
	// anyway and isn't worth the cost of trying.
	if entry.PluginLogs != "" && len(entry.PluginLogs) <= MaxTraceDiagnosticBytes {
		var grouped map[string][]schemas.PluginLogEntry
		if err := sonic.UnmarshalString(entry.PluginLogs, &grouped); err == nil {
			flat := make([]schemas.PluginLogEntry, 0, len(grouped))
			for _, entries := range grouped {
				flat = append(flat, entries...)
			}
			// The grouping above is per plugin; the node-wide cap belongs here,
			// after flattening, or a request with several chatty plugins could
			// still return MaxTraceLogLines lines from each of them.
			sort.SliceStable(flat, func(i, j int) bool { return flat[i].Timestamp < flat[j].Timestamp })
			if len(flat) > MaxTraceLogLines {
				flat = flat[:MaxTraceLogLines]
			}
			if len(flat) > 0 {
				lines := make([]TracePluginLogLine, len(flat))
				for i, e := range flat {
					lines[i] = TracePluginLogLine{
						Plugin:    e.PluginName,
						Timestamp: time.UnixMilli(e.Timestamp).UTC().Format(time.RFC3339Nano),
						Level:     string(e.Level),
						Message:   e.Message,
					}
				}
				node.PluginLogs = lines
			}
		}
	}
	if entry.RoutingEngineLogs != "" {
		raw := entry.RoutingEngineLogs
		if len(raw) > MaxTraceDiagnosticBytes {
			// Keep the tail before ever calling strings.Split, so the cost of
			// splitting scales with MaxTraceDiagnosticBytes rather than
			// however large the persisted field actually is. Drop the first
			// (likely partial) line left by the byte cut, if there's a whole
			// one after it.
			raw = raw[len(raw)-MaxTraceDiagnosticBytes:]
			if idx := strings.IndexByte(raw, '\n'); idx >= 0 {
				raw = raw[idx+1:]
			}
		}
		lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
		if len(lines) > MaxTraceLogLines {
			// Keep the tail: the most recent decisions are the ones most likely
			// to explain a terminal outcome.
			lines = lines[len(lines)-MaxTraceLogLines:]
		}
		node.RoutingEngineLogs = lines
	}

	for i, bucket := range entry.OverheadBreakdownParsed {
		if i >= MaxTraceLogLines {
			break
		}
		node.OverheadBreakdown = append(node.OverheadBreakdown, TraceOverhead{Name: bucket.Name, DurationMs: bucket.DurationUs / 1000})
	}

	return node
}

// truncateTraceText caps a string on a rune boundary and marks the cut.
func truncateTraceText(text string, limit int) string {
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

// derefTraceFloat reads an optional float, zero when unset.
func derefTraceFloat(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}
