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

// get_request_trace: the causal drill-down behind one request.
//
// Every other tool answers "what happened" - counts, totals, rows. This is the
// only one that answers "why": it walks a request's retry attempts and its full
// fallback chain (every provider/model tried, in order), and returns each hop's
// error, guardrail and cache decisions, and latency breakdown. get_log_detail
// returns one row's content; this returns the causal chain around it.
//
// It cannot explain why an *aggregate* changed - a spike, a trend - only why one
// specific request did what it did. The system prompt says so, because a model
// asked "why did errors spike" has every incentive to point at one bad request
// it found and call that the cause.

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

// attemptRecord projects one retry attempt from AttemptTrailParsed.
type attemptRecord struct {
	Attempt           int    `json:"attempt"`
	KeyName           string `json:"key_name,omitempty"`
	FailReason        string `json:"fail_reason,omitempty"`
	TriggeredRotation bool   `json:"triggered_rotation,omitempty"`
}

// errorSummary projects the fields of a BifrostError useful for root-causing a
// failure, out of the full struct's cost/streaming/redaction plumbing.
type errorSummary struct {
	StatusCode int    `json:"status_code,omitempty"`
	Type       string `json:"type,omitempty"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message,omitempty"`
}

// cacheSummary projects the semantic/direct cache decision for a request.
type cacheSummary struct {
	Hit        bool     `json:"hit"`
	HitType    string   `json:"hit_type,omitempty"`
	Similarity *float64 `json:"similarity,omitempty"`
	Threshold  *float64 `json:"threshold,omitempty"`
}

// guardrailDecision projects one judge call from GuardrailDebugParsed.
type guardrailDecision struct {
	Phase         string `json:"phase,omitempty"`
	RuleName      string `json:"rule_name,omitempty"`
	GuardrailName string `json:"guardrail_name,omitempty"`
	Action        string `json:"action,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// overheadEntry projects one self-time span from OverheadBreakdownParsed.
type overheadEntry struct {
	Name       string  `json:"name"`
	DurationMs float64 `json:"duration_ms"`
}

// pluginLogLine is one plugin log entry, flattened out of the per-plugin
// grouping the row persists it under. A map keeps every plugin's own lines in
// order but not the interleaving between plugins, and json.Marshal sorts map
// keys alphabetically on top of that - both erase the cross-plugin sequence
// this trace is meant to explain. A flat, timestamp-sorted slice keeps it.
type pluginLogLine struct {
	Plugin    string `json:"plugin"`
	Timestamp string `json:"timestamp"`
	Level     string `json:"level,omitempty"`
	Message   string `json:"message"`
}

// traceNode is one request in a causal chain: the primary attempt (fallback
// index 0) or one fallback hop.
type traceNode struct {
	ID                string              `json:"id"`
	FallbackIndex     int                 `json:"fallback_index"`
	Timestamp         string              `json:"timestamp"`
	Provider          string              `json:"provider"`
	Model             string              `json:"model"`
	Status            string              `json:"status"`
	StopReason        string              `json:"stop_reason,omitempty"`
	LatencyMs         float64             `json:"latency_ms,omitempty"`
	UpstreamLatencyMs float64             `json:"upstream_latency_ms,omitempty"`
	OverheadLatencyMs float64             `json:"overhead_latency_ms,omitempty"`
	NumberOfRetries   int                 `json:"number_of_retries,omitempty"`
	Attempts          []attemptRecord     `json:"attempts,omitempty"`
	Error             *errorSummary       `json:"error,omitempty"`
	Cache             *cacheSummary       `json:"cache,omitempty"`
	Guardrails        []guardrailDecision `json:"guardrails,omitempty"`
	// PluginLogs and RoutingEngineLogs are ordered by when they actually
	// happened, not re-grouped or deduplicated: their sequence is itself part
	// of the causal story (a plugin logging "rejected" before governance logs
	// "fallback triggered" reads differently than the reverse).
	PluginLogs        []pluginLogLine `json:"plugin_logs,omitempty"`
	RoutingEngineLogs []string        `json:"routing_engine_logs,omitempty"`
	OverheadBreakdown []overheadEntry `json:"overhead_breakdown,omitempty"`
	Link              string          `json:"link,omitempty"`
}

// getRequestTraceTool is the causal drill-down: retries, fallback chain,
// guardrail/cache decisions and latency breakdown for one request.
func getRequestTraceTool() Tool {
	return Tool{
		name: "get_request_trace",
		description: "Fetch the causal trace behind one request: its retry attempts, its complete fallback chain (every provider/model tried, in order, whether each hop succeeded or failed and why), guardrail and cache decisions, and a latency breakdown. " +
			"Use this to answer why a specific request failed or behaved unexpectedly. get_log_detail returns one row's content; this returns the causal chain around it - use get_log_detail for content, this for causation. " +
			"log_id can be any request in a chain (the root or a fallback hop); the full chain is resolved and returned regardless of which one you pass. " +
			"This explains one request. It cannot explain why an aggregate changed - a spike in errors, a shift in latency - only why one specific request did what it did.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "log_id": {"type": "string"}
  },
  "required": ["log_id"]
}`,
		execute: func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error) {
			id, _ := args["log_id"].(string)
			if id == "" {
				return nil, fmt.Errorf("log_id is required")
			}
			entry, err := deps.logManager.GetLog(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("could not load log %s: %w", id, err)
			}
			if entry == nil {
				return nil, fmt.Errorf("no log found with id %s", id)
			}

			root := entry
			rootID := entry.ID
			// A row's own id can equal its parent_request_id on a corrupted or
			// deliberately self-referencing row; treating that as "has a parent"
			// would recurse into fetching itself as its own root.
			if entry.ParentRequestID != nil && *entry.ParentRequestID != "" && *entry.ParentRequestID != entry.ID {
				rootID = *entry.ParentRequestID
				root, err = deps.logManager.GetLog(ctx, rootID)
				if err != nil {
					return nil, fmt.Errorf("could not load root request %s: %w", rootID, err)
				}
				if root == nil {
					return nil, fmt.Errorf("no log found with id %s", rootID)
				}
			}

			children, err := deps.logManager.Search(ctx, &logstore.SearchFilters{ParentRequestID: rootID}, &logstore.PaginationOptions{
				Limit: MaxTraceChainLength, SortBy: "timestamp", Order: "asc",
			})
			if err != nil {
				return nil, fmt.Errorf("could not load fallback chain for %s: %w", rootID, err)
			}

			nodes := make([]traceNode, 0, 1+len(children.Logs))
			nodes = append(nodes, traceFromLog(root))
			for i := range children.Logs {
				nodes = append(nodes, traceFromLog(&children.Logs[i]))
			}
			sort.Slice(nodes, func(i, j int) bool { return nodes[i].FallbackIndex < nodes[j].FallbackIndex })

			return map[string]any{
				"root_id":         rootID,
				"chain_length":    len(nodes),
				"final_status":    nodes[len(nodes)-1].Status,
				"nodes":           nodes,
				"truncated_chain": children.Pagination.TotalCount > int64(len(children.Logs)),
				"link":            logDetailLink(rootID),
			}, nil
		},
	}
}

// traceFromLog projects one log row into a traceNode, bounding every
// unbounded-length field it carries (attempts, plugin/routing log lines,
// guardrail judge calls) so a single noisy request cannot dominate the result.
func traceFromLog(entry *logstore.Log) traceNode {
	node := traceNode{
		ID:                entry.ID,
		FallbackIndex:     entry.FallbackIndex,
		Timestamp:         entry.Timestamp.UTC().Format(time.RFC3339),
		Provider:          entry.Provider,
		Model:             entry.Model,
		Status:            entry.Status,
		LatencyMs:         derefFloat(entry.Latency),
		UpstreamLatencyMs: derefFloat(entry.UpstreamLatency),
		OverheadLatencyMs: derefFloat(entry.OverheadLatency),
		NumberOfRetries:   entry.NumberOfRetries,
		Link:              logDetailLink(entry.ID),
	}
	if entry.StopReason != nil {
		node.StopReason = *entry.StopReason
	}

	for i, attempt := range entry.AttemptTrailParsed {
		if i >= MaxTraceAttempts {
			break
		}
		rec := attemptRecord{Attempt: attempt.Attempt, KeyName: attempt.KeyName, TriggeredRotation: attempt.TriggeredRotation}
		if attempt.FailReason != nil {
			rec.FailReason = *attempt.FailReason
		}
		node.Attempts = append(node.Attempts, rec)
	}

	if be := entry.ErrorDetailsParsed; be != nil {
		summary := &errorSummary{Message: truncateText(be.GetErrorString(), 300)}
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
		cache := &cacheSummary{Hit: cd.CacheHit, Similarity: cd.Similarity, Threshold: cd.Threshold}
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
			node.Guardrails = append(node.Guardrails, guardrailDecision{
				Phase: call.Phase, RuleName: call.RuleName, GuardrailName: call.GuardrailName,
				Action: call.Action, Reason: truncateText(call.Reason, 300),
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
				lines := make([]pluginLogLine, len(flat))
				for i, e := range flat {
					lines[i] = pluginLogLine{
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
		node.OverheadBreakdown = append(node.OverheadBreakdown, overheadEntry{Name: bucket.Name, DurationMs: bucket.DurationUs / 1000})
	}

	return node
}
