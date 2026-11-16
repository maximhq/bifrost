package warp

import (
	"context"
	"errors"
	"fmt"

	"github.com/maximhq/bifrost/framework/logstore"
)

// get_request_trace: the causal drill-down behind one request.
//
// Every other tool answers "what happened" - counts, totals, rows. This is the
// only one that answers "why". The chain itself is built by
// logstore.BuildRequestTrace, which GET /api/logs/{id}/trace also serves; the
// tool adds dashboard links and Warp's not-found wording.
//
// It cannot explain why an *aggregate* changed - a spike, a trend - only why one
// specific request did what it did. The system prompt says so, because a model
// asked "why did errors spike" has every incentive to point at one bad request
// it found and call that the cause.

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
			trace, err := logstore.BuildRequestTrace(ctx, deps.logManager, id)
			var missing *logstore.TraceNotFoundError
			if errors.As(err, &missing) {
				return nil, logNotFound(missing.ID, deps.scope)
			}
			if err != nil {
				return nil, err
			}
			for i := range trace.Nodes {
				trace.Nodes[i].Link = logDetailLink(trace.Nodes[i].ID)
			}
			result := map[string]any{
				"root_id":         trace.RootID,
				"chain_length":    trace.ChainLength,
				"nodes":           trace.Nodes,
				"truncated_chain": trace.TruncatedChain,
				"link":            logDetailLink(trace.RootID),
			}
			// Left out when the chain is truncated: the last node returned is not
			// necessarily the terminal hop.
			if !trace.TruncatedChain {
				result["final_status"] = trace.FinalStatus
			}
			return result, nil
		},
	}
}
