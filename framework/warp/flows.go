package warp

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/bytedance/sonic"

	"github.com/maximhq/bifrost/framework/logstore"
)

// maxQueryMetrics caps how many metrics one query_metrics call may request.
// The schema's maxItems and enumSliceArg's runtime check both read this
// constant, so the advertised contract and the enforced one cannot drift:
// the vocabulary has six values, so a purely element-wise check would let a
// call overrun the published limit by two queries.
const maxQueryMetrics = 4

// The five named query flows Warp exposes, plus a drill-down and a discovery
// tool. Each flow is one tool over the logstore read surface; they all take the
// same filter object, which is what lets one parser and one scope path serve
// every one of them.

// ---------------------------------------------------------------- flow 1: logs

// queryLogsTool is flow 1: individual request logs, projected and row-capped.
func queryLogsTool() Tool {
	return Tool{
		name: "query_logs",
		description: "List individual LLM request logs matching a filter. Returns compact rows (timestamp, provider, model, status, latency, tokens, cost, virtual key, user), not full message bodies. " +
			"Use this to find specific requests - which ones failed, which were slowest, what a given user actually sent. For totals and trends use query_metrics instead, which is far cheaper.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "filters": ` + FilterSchema + `,
    "limit": {"type": "integer", "minimum": 1, "maximum": 25, "description": "Rows to return. Capped at 25."},
    "sort_by": {"type": "string", "enum": ["timestamp", "latency", "tokens", "cost"]},
    "order": {"type": "string", "enum": ["asc", "desc"]},
    "include_content": {"type": "boolean", "description": "Include a truncated preview of the request content. Expensive - only set this when the question is about what was actually said."}
  },
  "required": ["filters"]
}`,
		execute: func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error) {
			now := Now()
			filters, err := filterArg(args, now, deps.scope)
			if err != nil {
				return nil, err
			}
			limit, err := intArg(args, "limit", 10, MaxLogRows)
			if err != nil {
				return nil, err
			}
			sortBy, err := enumArg(args, "sort_by", "timestamp", []string{"timestamp", "latency", "tokens", "cost"})
			if err != nil {
				return nil, err
			}
			order, err := enumArg(args, "order", "desc", []string{"asc", "desc"})
			if err != nil {
				return nil, err
			}
			result, err := deps.logManager.Search(ctx, filters, &logstore.PaginationOptions{
				Limit: limit, Offset: 0, SortBy: sortBy, Order: order,
			})
			if err != nil {
				return nil, fmt.Errorf("log search failed: %w", err)
			}
			includeContent, err := boolArg(args, "include_content")
			if err != nil {
				return nil, err
			}
			rows := make([]logRow, 0, len(result.Logs))
			for i := range result.Logs {
				rows = append(rows, projectLog(&result.Logs[i], includeContent, LogContentChars))
			}
			// total_matching is reported separately from the returned rows so the
			// model can say "12,400 matched, here are the 10 slowest" instead of
			// implying it saw everything.
			return map[string]any{
				"rows":           rows,
				"returned":       len(rows),
				"total_matching": result.Pagination.TotalCount,
				"scope":          scopeNote(filters, deps.scope),
			}, nil
		},
	}
}

// getLogDetailTool is the single-row drill-down behind flow 1, with a larger content budget than a list row can afford.
func getLogDetailTool() Tool {
	return Tool{
		name:        "get_log_detail",
		description: "Fetch one log by id with a larger content preview. Use after query_logs to investigate a specific request, for example to explain why it failed.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "log_id": {"type": "string"}
  },
  "required": ["log_id"]
}`,
		execute: func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error) {
			// Type-checked like every other argument here. A discarded assertion
			// turned a present non-string into "", so the tool answered "log_id is
			// required" - and the model, believing it had omitted the field,
			// retried with the same wrong shape.
			id, err := stringArg(args, "log_id")
			if err != nil {
				return nil, err
			}
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
			return projectLog(entry, true, DetailContentChars), nil
		},
	}
}

// isHistogramMetric reports whether a metric returns a bucketed series.
//
// Only "summary" does not: every other metric calls one of the histogram
// readers, which is why the bucket is computed for them and skipped for it.
func isHistogramMetric(metric string) bool {
	return metric != "summary"
}

// ------------------------------------------------------------- flow 2: metrics

// queryMetricsTool is flow 2: aggregates and time series. This is the cheap path and the one most questions should take.
func queryMetricsTool() Tool {
	return Tool{
		name: "query_metrics",
		description: "Aggregate statistics and time series over requests: totals, cost, tokens, latency percentiles, throughput. " +
			"This is the cheapest way to answer 'how much', 'how many' and 'is it getting worse'. Series come back bucket by bucket, with the bucket width chosen from the window so the number of buckets stays small - bounded, not summarized, so read the buckets rather than expecting a precomputed total. " +
			"group_by supports 'none' and 'provider' only.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "filters": ` + FilterSchema + `,
    "metrics": {
      "type": "array",
      "minItems": 1,
      "maxItems": ` + strconv.Itoa(maxQueryMetrics) + `,
      "items": {"type": "string", "enum": ["summary", "requests", "tokens", "cost", "latency", "throughput"]},
      "description": "'summary' returns overall totals and is usually the right starting point."
    },
    "group_by": {"type": "string", "enum": ["none", "provider"]}
  },
  "required": ["filters", "metrics"]
}`,
		execute: func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error) {
			now := Now()
			filters, err := filterArg(args, now, deps.scope)
			if err != nil {
				return nil, err
			}
			metrics, err := enumSliceArg(args, "metrics", "metric", []string{"summary", "requests", "tokens", "cost", "latency", "throughput"}, maxQueryMetrics)
			if err != nil {
				return nil, err
			}
			byProvider, err := groupByProvider(args)
			if err != nil {
				return nil, err
			}

			// Only when something actually wants buckets. bucketSize enforces a
			// bucket-count limit, so running it unconditionally failed a perfectly
			// valid long-range `metrics: ["summary"]` request before GetStats was
			// ever called - a limit on a series the answer does not contain.
			var bucket int64
			if slices.ContainsFunc(metrics, isHistogramMetric) {
				computed, bucketErr := bucketSize(filters)
				if bucketErr != nil {
					return nil, bucketErr
				}
				bucket = computed
			}

			out := map[string]any{
				"scope": scopeNote(filters, deps.scope),
				"window": map[string]string{
					"start": filters.StartTime.UTC().Format("2006-01-02T15:04:05Z"),
					"end":   filters.EndTime.UTC().Format("2006-01-02T15:04:05Z"),
				},
			}
			for _, metric := range metrics {
				switch metric {
				case "summary":
					stats, err := deps.logManager.GetStats(ctx, filters)
					if err != nil {
						return nil, fmt.Errorf("stats query failed: %w", err)
					}
					out["summary"] = stats
				case "requests":
					// GetHistogram has no provider breakdown, so grouping it by
					// provider returned deployment-wide totals under a heading that
					// said per-provider - a wrong answer the model cannot detect.
					if byProvider {
						return nil, fmt.Errorf("group_by \"provider\" is not supported for the requests metric; ask for cost, tokens or latency by provider, or requests without grouping")
					}
					result, err := deps.logManager.GetHistogram(ctx, filters, bucket)
					if err != nil {
						return nil, fmt.Errorf("request histogram failed: %w", err)
					}
					out["requests"] = result
				case "tokens":
					if byProvider {
						result, err := deps.logManager.GetProviderTokenHistogram(ctx, filters, bucket)
						if err != nil {
							return nil, fmt.Errorf("token histogram failed: %w", err)
						}
						out["tokens"] = result
						continue
					}
					result, err := deps.logManager.GetTokenHistogram(ctx, filters, bucket)
					if err != nil {
						return nil, fmt.Errorf("token histogram failed: %w", err)
					}
					out["tokens"] = result
				case "cost":
					if byProvider {
						result, err := deps.logManager.GetProviderCostHistogram(ctx, filters, bucket)
						if err != nil {
							return nil, fmt.Errorf("cost histogram failed: %w", err)
						}
						out["cost"] = result
						continue
					}
					result, err := deps.logManager.GetCostHistogram(ctx, filters, bucket)
					if err != nil {
						return nil, fmt.Errorf("cost histogram failed: %w", err)
					}
					out["cost"] = result
				case "latency":
					if byProvider {
						result, err := deps.logManager.GetProviderLatencyHistogram(ctx, filters, bucket)
						if err != nil {
							return nil, fmt.Errorf("latency histogram failed: %w", err)
						}
						out["latency"] = result
						continue
					}
					result, err := deps.logManager.GetLatencyHistogram(ctx, filters, bucket)
					if err != nil {
						return nil, fmt.Errorf("latency histogram failed: %w", err)
					}
					out["latency"] = result
				case "throughput":
					if byProvider {
						result, err := deps.logManager.GetProviderThroughputHistogram(ctx, filters, bucket)
						if err != nil {
							return nil, fmt.Errorf("throughput histogram failed: %w", err)
						}
						out["throughput"] = result
						continue
					}
					result, err := deps.logManager.GetThroughputHistogram(ctx, filters, bucket)
					if err != nil {
						return nil, fmt.Errorf("throughput histogram failed: %w", err)
					}
					out["throughput"] = result
				default:
					// Unreachable through enumSliceArg, which validates the same
					// vocabulary before dispatch. Kept as a drift guard: a metric
					// added to that list but not to this switch would otherwise be
					// accepted and then silently produce nothing.
					return nil, fmt.Errorf("unknown metric %q; supported: summary, requests, tokens, cost, latency, throughput", metric)
				}
			}
			return out, nil
		},
	}
}

// --------------------------------------------------------------- flow 3: users

// queryUsersTool is flow 3: users ranked by usage.
func queryUsersTool() Tool {
	return Tool{
		name: "query_user_usage",
		description: "Rank users by usage - cost, requests and tokens - over a window. Answers 'who is spending the most', 'who drove the spike'. " +
			"Users come from the user dimension recorded on each request, not from a directory, so anyone who has not made a request will not appear.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "filters": ` + FilterSchema + `,
    "limit": {"type": "integer", "minimum": 1, "maximum": 20}
  },
  "required": ["filters"]
}`,
		execute: func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error) {
			return rankByDimension(ctx, deps, args, logstore.RankingDimensionUser)
		},
	}
}

// -------------------------------------------------------- flow 4: virtual keys

// queryVirtualKeysTool is flow 4: virtual keys ranked by usage.
func queryVirtualKeysTool() Tool {
	return Tool{
		name: "query_virtual_key_usage",
		description: "Rank virtual keys by usage - cost, requests and tokens - over a window. Answers 'which key is burning the budget'. " +
			"Note: a per-key time series is not available; to see a trend, filter by virtual_key_ids and call query_metrics, which returns one combined series for those keys.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "filters": ` + FilterSchema + `,
    "limit": {"type": "integer", "minimum": 1, "maximum": 20}
  },
  "required": ["filters"]
}`,
		execute: func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error) {
			return rankByDimension(ctx, deps, args, logstore.RankingDimensionVirtualKey)
		},
	}
}

// rankByDimension backs the user and virtual-key flows. RankingLimit pushes
// the cap into SQL, so a large deployment never materializes more rows than the
// answer needs.
func rankByDimension(ctx context.Context, deps *ToolDeps, args map[string]any, dimension logstore.RankingDimension) (any, error) {
	now := Now()
	filters, err := filterArg(args, now, deps.scope)
	if err != nil {
		return nil, err
	}
	limit, err := intArg(args, "limit", 10, MaxRankingRows)
	if err != nil {
		return nil, err
	}
	filters.RankingLimit = &limit
	result, err := deps.logManager.GetDimensionRankings(ctx, filters, dimension)
	if err != nil {
		return nil, fmt.Errorf("%s rankings failed: %w", dimension, err)
	}
	// Flattened, not wrapped. DimensionRankingResult already serializes as
	// {"rankings": [...], "dimension": ..., totals}, so nesting it under another
	// "rankings" key produced rankings.rankings and pushed the dimension and the
	// totals a level down. The model consumes this JSON directly, and a shape it
	// does not expect does not fail - it answers from whatever it can find.
	return withScopeNote(result, scopeNote(filters, deps.scope))
}

// withScopeNote returns a result's own fields with the scope note alongside
// them, rather than nested beneath a key.
//
// It round-trips through JSON deliberately: the tool results are typed structs
// whose wire shape is what the model reads, so composing on the encoded form is
// what keeps the note additive instead of restructuring the answer around it.
func withScopeNote(result any, note string) (any, error) {
	encoded, err := sonic.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("could not encode rankings: %w", err)
	}
	out := map[string]any{}
	if err := sonic.Unmarshal(encoded, &out); err != nil {
		return nil, fmt.Errorf("could not read back rankings: %w", err)
	}
	out["scope"] = note
	return out, nil
}

// ------------------------------------------------ flow 5: providers and models

// queryModelsTool is flow 5: model rankings and provider performance.
func queryModelsTool() Tool {
	return Tool{
		name: "query_model_performance",
		description: "Rank models by usage and, optionally, compare provider performance (latency percentiles and throughput). " +
			"Answers 'which model do we use most', 'which provider is slowest', 'did p99 regress'.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "filters": ` + FilterSchema + `,
    "limit": {"type": "integer", "minimum": 1, "maximum": 20},
    "include_performance": {"type": "boolean", "description": "Adds per-provider latency and throughput series."}
  },
  "required": ["filters"]
}`,
		execute: func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error) {
			now := Now()
			filters, err := filterArg(args, now, deps.scope)
			if err != nil {
				return nil, err
			}
			limit, err := intArg(args, "limit", 10, MaxRankingRows)
			if err != nil {
				return nil, err
			}
			filters.RankingLimit = &limit

			rankings, err := deps.logManager.GetModelRankings(ctx, filters)
			if err != nil {
				return nil, fmt.Errorf("model rankings failed: %w", err)
			}
			out := map[string]any{"models": rankings, "scope": scopeNote(filters, deps.scope)}

			includePerformance, err := boolArg(args, "include_performance")
			if err != nil {
				return nil, err
			}
			if includePerformance {
				bucket, err := bucketSize(filters)
				if err != nil {
					return nil, err
				}
				latency, err := deps.logManager.GetProviderLatencyHistogram(ctx, filters, bucket)
				if err != nil {
					return nil, fmt.Errorf("provider latency failed: %w", err)
				}
				throughput, err := deps.logManager.GetProviderThroughputHistogram(ctx, filters, bucket)
				if err != nil {
					return nil, fmt.Errorf("provider throughput failed: %w", err)
				}
				out["provider_latency"] = latency
				out["provider_throughput"] = throughput
			}
			return out, nil
		},
	}
}

// --------------------------------------------------------------- discovery

// describeFilterSpaceTool lists the values that actually exist in this
// deployment. It is the highest-leverage tool for answer quality: a guessed
// model or key name returns an empty result that reads exactly like a real
// finding of zero.
func describeFilterSpaceTool() Tool {
	return Tool{
		name: "describe_filter_space",
		// Names exactly what the result map carries, no more. Advertising a
		// dimension this tool cannot enumerate (providers, which query_logs does
		// accept as a filter but LogReader has no listing method for) would cause
		// the very failure the tool prevents: the model asks, gets nothing, and
		// filters on a guess anyway.
		description: "List the values that actually appear in this deployment's logs - models, virtual keys, apps and stop reasons. " +
			"Call this before filtering by a name you are not certain about. Guessing a model or key name returns an empty result that looks like a real answer.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "search": {"type": "string", "description": "Optional substring to narrow the returned values."}
  }
}`,
		execute: func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error) {
			// A discarded type assertion turned a malformed search into "", and the
			// tool then ran four unfiltered discovery queries and returned every
			// value in the deployment - a broader answer than the one asked for,
			// with nothing to say it had been widened.
			query := ""
			if raw, present := args["search"]; present && raw != nil {
				text, ok := raw.(string)
				if !ok {
					return nil, fmt.Errorf("search must be a string, got %T", raw)
				}
				query = text
			}
			const limit = 50

			models, err := deps.logManager.GetAvailableModels(ctx, limit, query)
			if err != nil {
				return nil, fmt.Errorf("could not list models: %w", err)
			}
			virtualKeys, err := deps.logManager.GetAvailableVirtualKeys(ctx, limit, query)
			if err != nil {
				return nil, fmt.Errorf("could not list virtual keys: %w", err)
			}
			apps, err := deps.logManager.GetAvailableApps(ctx, limit, query)
			if err != nil {
				return nil, fmt.Errorf("could not list apps: %w", err)
			}
			stopReasons, err := deps.logManager.GetAvailableStopReasons(ctx, limit, query)
			if err != nil {
				return nil, fmt.Errorf("could not list stop reasons: %w", err)
			}
			return map[string]any{
				"models":       models,
				"virtual_keys": virtualKeys,
				"apps":         apps,
				"stop_reasons": stopReasons,
			}, nil
		},
	}
}
