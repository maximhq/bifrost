package warp

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/framework/logstore"
)

// The five named query flows Warp exposes, plus a drill-down and a discovery
// tool. Each flow is one tool over the logstore read surface; they all take the
// same filter object, which is what lets one parser and one scope path serve
// every one of them.

// ---------------------------------------------------------------- flow 1: logs

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
			filters, err := filterArg(args, now)
			if err != nil {
				return nil, err
			}
			limit := intArg(args, "limit", 10, MaxLogRows)
			sortBy, _ := args["sort_by"].(string)
			if sortBy == "" {
				sortBy = "timestamp"
			}
			order, _ := args["order"].(string)
			if order == "" {
				order = "desc"
			}
			result, err := deps.logManager.Search(ctx, filters, &logstore.PaginationOptions{
				Limit: limit, Offset: 0, SortBy: sortBy, Order: order,
			})
			if err != nil {
				return nil, fmt.Errorf("log search failed: %w", err)
			}
			includeContent := boolArg(args, "include_content")
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
			}, nil
		},
	}
}

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
			return projectLog(entry, true, DetailContentChars), nil
		},
	}
}

// ------------------------------------------------------------- flow 2: metrics

func queryMetricsTool() Tool {
	return Tool{
		name: "query_metrics",
		description: "Aggregate statistics and time series over requests: totals, cost, tokens, latency percentiles, throughput. " +
			"This is the cheapest way to answer 'how much', 'how many' and 'is it getting worse'. Series are returned summarized (total, mean, min, max, first, last) rather than bucket by bucket. " +
			"group_by supports 'none' and 'provider' only.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "filters": ` + FilterSchema + `,
    "metrics": {
      "type": "array",
      "minItems": 1,
      "maxItems": 4,
      "items": {"type": "string", "enum": ["summary", "requests", "tokens", "cost", "latency", "throughput"]},
      "description": "'summary' returns overall totals and is usually the right starting point."
    },
    "group_by": {"type": "string", "enum": ["none", "provider"]}
  },
  "required": ["filters", "metrics"]
}`,
		execute: func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error) {
			now := Now()
			filters, err := filterArg(args, now)
			if err != nil {
				return nil, err
			}
			metrics := stringSlice(args["metrics"])
			if len(metrics) == 0 {
				return nil, fmt.Errorf("metrics must list at least one of: summary, requests, tokens, cost, latency, throughput")
			}
			groupBy, _ := args["group_by"].(string)
			byProvider := groupBy == "provider"

			bucket, err := bucketSize(filters)
			if err != nil {
				return nil, err
			}

			out := map[string]any{
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
					return nil, fmt.Errorf("unknown metric %q; supported: summary, requests, tokens, cost, latency, throughput", metric)
				}
			}
			return out, nil
		},
	}
}

// --------------------------------------------------------------- flow 3: users

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
	filters, err := filterArg(args, now)
	if err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 10, MaxRankingRows)
	filters.RankingLimit = &limit
	result, err := deps.logManager.GetDimensionRankings(ctx, filters, dimension)
	if err != nil {
		return nil, fmt.Errorf("%s rankings failed: %w", dimension, err)
	}
	return result, nil
}

// ------------------------------------------------ flow 5: providers and models

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
			filters, err := filterArg(args, now)
			if err != nil {
				return nil, err
			}
			limit := intArg(args, "limit", 10, MaxRankingRows)
			filters.RankingLimit = &limit

			rankings, err := deps.logManager.GetModelRankings(ctx, filters)
			if err != nil {
				return nil, fmt.Errorf("model rankings failed: %w", err)
			}
			out := map[string]any{"models": rankings}

			if boolArg(args, "include_performance") {
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

func describeFilterSpaceTool() Tool {
	return Tool{
		name: "describe_filter_space",
		description: "List the values that actually appear in this deployment's logs - models, providers, virtual keys, apps. " +
			"Call this before filtering by a name you are not certain about. Guessing a model or key name returns an empty result that looks like a real answer.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "search": {"type": "string", "description": "Optional substring to narrow the returned values."}
  }
}`,
		execute: func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error) {
			query, _ := args["search"].(string)
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
