package warp

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
)

// The named query flows Warp exposes, plus a drill-down and a discovery tool.
// Each flow is one tool over the logstore read surface; they all take the same
// filter object, which is what lets one parser and one scope path serve every
// one of them.

// formatWindow renders a window the way every tool result reports it: an
// absolute UTC instant, regardless of whether the caller passed a relative
// offset, an absolute date, or nothing.
func formatWindow(start, end time.Time) map[string]string {
	return map[string]string{
		"start": start.UTC().Format("2006-01-02T15:04:05Z"),
		"end":   end.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// resolvedWindow reports the absolute window filters actually resolved to.
//
// The system prompt requires every answer with numbers to end with an
// absolute-time provenance block, but "-7d" only becomes an absolute instant
// inside parseFilters - the model was never told that instant anywhere else,
// which meant reconstructing it by hand from the current-time reference at
// the bottom of the prompt, the exact kind of arithmetic that produces a
// subtly wrong footer. Every flow that resolves a window reports it back
// here so the model copies rather than recomputes it.
func resolvedWindow(filters *logstore.SearchFilters) map[string]string {
	return formatWindow(*filters.StartTime, *filters.EndTime)
}

// ---------------------------------------------------------------- flow 1: logs

// semanticSearchLogsTool finds requests by conversational meaning. It still
// accepts the shared structured filters, but unlike query_logs the query is
// embedded and compared with the stored user/assistant conversation vectors.
func semanticSearchLogsTool() Tool {
	return Tool{
		name: "semantic_search_logs",
		description: "Find logged conversations by meaning. Use this when the question is about what users discussed, wanted, reported, or what assistants answered, even when the wording differs. " +
			"Use query_logs, count_logs, or query_metrics for exact fields, counts, latency, cost, and trends.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "A natural-language description of the conversations to find."},
    "filters": ` + FilterSchema + `,
    "limit": {"type": "integer", "minimum": 1, "maximum": 25, "description": "Matches to return. Also capped by the configured semantic search limit."}
  },
  "required": ["query", "filters"]
}`,
		execute: func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error) {
			query, _ := args["query"].(string)
			if deps.semantic == nil {
				return nil, fmt.Errorf("semantic log search is not configured")
			}
			filters, err := filterArg(args, Now(), deps.scope)
			if err != nil {
				return nil, err
			}
			result, err := deps.semantic.Search(ctx, query, filters, intArg(args, "limit", 0, MaxLogRows))
			if err != nil {
				return nil, err
			}
			response := map[string]any{
				"rows":      result.Rows,
				"returned":  result.Returned,
				"threshold": result.Threshold,
				"scope":     scopeNote(filters, deps.scope),
				"logs_link": logsViewLink(filters),
				"window":    resolvedWindow(filters),
			}
			if result.Returned == 0 {
				// Four bare fields read as "search is useless here", and the
				// model went off counting and listing logs instead. Say what
				// happened and what the legitimate next moves are.
				response["hint"] = fmt.Sprintf("No stored conversation scored above the similarity threshold of %.2f. "+
					"Do not fall back to count_logs or query_logs to answer a question about meaning. "+
					"Widen the time range once, rephrase the query, or report that no matching conversations were found.", result.Threshold)
			}
			return response, nil
		},
	}
}

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
				"scope":          scopeNote(filters, deps.scope),
				"logs_link":      logsViewLink(filters),
				"window":         resolvedWindow(filters),
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

// countLogsTool answers "how much is there?" before anything answers "what
// is it?".
//
// A log question over a wide window can match hundreds of thousands of rows.
// Fetching a page of them to find that out is the expensive way to learn it, and
// the model cannot tell a genuine "no matches" from "I looked at 25 of 400,000"
// unless it is told. This costs one aggregate query and turns a blind pull into
// a decision: narrow first, or slice the window and ask again.
func countLogsTool() Tool {
	return Tool{
		name: "count_logs",
		description: "Count matching requests and summarise them, without fetching any rows. " +
			"Call this before query_logs whenever the window is wider than a few hours or the filters are loose. " +
			"If the count is large, narrow the filters or split the question into smaller time slices and count again - do not page through the whole set.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "filters": ` + FilterSchema + `
  },
  "required": ["filters"]
}`,
		execute: func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error) {
			now := Now()
			filters, err := filterArg(args, now, deps.scope)
			if err != nil {
				return nil, err
			}
			stats, err := deps.logManager.GetStats(ctx, filters)
			if err != nil {
				return nil, fmt.Errorf("count failed: %w", err)
			}

			out := map[string]any{
				"total_requests":     stats.TotalRequests,
				"total_tokens":       stats.TotalTokens,
				"total_cost":         stats.TotalCost,
				"success_rate":       stats.SuccessRate,
				"average_latency_ms": stats.AverageLatency,
				"scope":              scopeNote(filters, deps.scope),
				"logs_link":          logsViewLink(filters),
				"window":             resolvedWindow(filters),
			}
			// The advice travels with the number rather than living only in the
			// prompt: this is the moment the decision gets made, and the threshold
			// is a property of the tool rather than of the conversation.
			switch {
			case stats.TotalRequests == 0:
				out["guidance"] = "Nothing matched. Widen the time range or check the filter values with describe_filter_space before concluding there is no traffic."
			case stats.TotalRequests > LargeResultThreshold:
				out["too_many_to_list"] = true
				out["guidance"] = fmt.Sprintf(
					"%d requests match - far too many to list in full. For a sorted top-N (\"slowest requests\", \"most expensive calls\"), call query_logs with sort_by and limit directly; that works regardless of this count, it is not the same as listing everything. For a total, answer from aggregates (query_metrics, query_model_performance) where you can. Only narrow by provider, model, status or virtual key, or split the window into smaller slices, if you genuinely need individual rows beyond a top-N.",
					stats.TotalRequests)
			default:
				out["guidance"] = "Small enough to list with query_logs if individual rows are needed."
			}
			return out, nil
		},
	}
}

// ------------------------------------------------------------- flow 2: metrics

// queryMetricsTool is flow 2: aggregates and time series. This is the cheap path and the one most questions should take.
func queryMetricsTool() Tool {
	return Tool{
		name: "query_metrics",
		description: "Aggregate statistics and time series over requests: totals, cost, tokens, latency percentiles, throughput. " +
			"This is the cheapest way to answer 'how much', 'how many' and 'is it getting worse'. Without group_by, each series is reduced to one summary per field (total, mean, min, max, first, last) rather than bucket by bucket - total is omitted for a field summing cannot describe, like a percentile. " +
			"group_by supports 'none' and 'provider' only. With 'provider', series stay as coarse buckets instead of a summary, since collapsing away the per-provider split would defeat the reason to group by it in the first place. " +
			"Set compare_to_previous with metrics including summary to answer 'is it up or down vs last period' in this one call, instead of calling this twice with a shifted window.",
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
    "group_by": {"type": "string", "enum": ["none", "provider"]},
    "compare_to_previous": {"type": "boolean", "description": "Requires 'summary' in metrics. Also fetches the immediately preceding period of equal length and returns a trend block (has_previous_period, requests_trend, tokens_trend, cost_trend as percent change) alongside summary."}
  },
  "required": ["filters", "metrics"]
}`,
		execute: func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error) {
			now := Now()
			filters, err := filterArg(args, now, deps.scope)
			if err != nil {
				return nil, err
			}
			metrics := stringSlice(args["metrics"])
			if len(metrics) == 0 {
				return nil, fmt.Errorf("metrics must list at least one of: summary, requests, tokens, cost, latency, throughput")
			}
			groupBy, _ := args["group_by"].(string)
			byProvider := groupBy == "provider"
			compareToPrevious := boolArg(args, "compare_to_previous")
			if compareToPrevious && !slices.Contains(metrics, "summary") {
				return nil, fmt.Errorf("compare_to_previous requires metrics to include \"summary\"")
			}

			// Every non-grouped series below is reduced to a seriesSummary and the
			// buckets discarded, so a finer bucket only improves the summary's
			// fidelity - it costs nothing in result size. The per-provider path
			// returns its buckets raw (see the byProvider branches), so it keeps
			// the coarse bucket size that path has always needed to stay bounded.
			bucket, err := bucketSize(filters)
			if err != nil {
				return nil, err
			}
			var providerBucket int64
			if byProvider {
				if providerBucket, err = coarseBucketSize(filters); err != nil {
					return nil, err
				}
			}

			out := map[string]any{
				"scope":     scopeNote(filters, deps.scope),
				"logs_link": logsViewLink(filters),
				"window":    resolvedWindow(filters),
			}
			for _, metric := range metrics {
				switch metric {
				case "summary":
					stats, err := deps.logManager.GetStats(ctx, filters)
					if err != nil {
						return nil, fmt.Errorf("stats query failed: %w", err)
					}
					out["summary"] = stats
					if compareToPrevious {
						trend, err := previousPeriodTrend(ctx, deps, filters, stats)
						if err != nil {
							return nil, fmt.Errorf("previous-period comparison failed: %w", err)
						}
						out["previous_period"] = trend
					}
				case "requests":
					result, err := deps.logManager.GetHistogram(ctx, filters, bucket)
					if err != nil {
						return nil, fmt.Errorf("request histogram failed: %w", err)
					}
					out["requests"] = summarizeRequestsHistogram(result)
				case "tokens":
					if byProvider {
						result, err := deps.logManager.GetProviderTokenHistogram(ctx, filters, providerBucket)
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
					out["tokens"] = summarizeTokensHistogram(result)
				case "cost":
					if byProvider {
						result, err := deps.logManager.GetProviderCostHistogram(ctx, filters, providerBucket)
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
					out["cost"] = summarizeCostHistogram(result)
				case "latency":
					if byProvider {
						result, err := deps.logManager.GetProviderLatencyHistogram(ctx, filters, providerBucket)
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
					out["latency"] = summarizeLatencyHistogram(result)
				case "throughput":
					if byProvider {
						result, err := deps.logManager.GetProviderThroughputHistogram(ctx, filters, providerBucket)
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
					out["throughput"] = summarizeThroughputHistogram(result)
				default:
					return nil, fmt.Errorf("unknown metric %q; supported: summary, requests, tokens, cost, latency, throughput", metric)
				}
			}
			return out, nil
		},
	}
}

// previousPeriodTrend fetches the period immediately preceding the filtered
// window, of equal length, and compares it to the stats already fetched for
// that window. It costs one extra store query so query_metrics's caller never
// has to spend a second tool call computing the same comparison rankings get
// for free.
func previousPeriodTrend(ctx context.Context, deps *ToolDeps, filters *logstore.SearchFilters, current *logstore.SearchStats) (map[string]any, error) {
	span := filters.EndTime.Sub(*filters.StartTime)
	prevEnd := *filters.StartTime
	prevStart := prevEnd.Add(-span)

	// A shallow copy: every other filter (providers, scope, status...) carries
	// over unchanged, only the window shifts.
	prevFilters := *filters
	prevFilters.StartTime, prevFilters.EndTime = &prevStart, &prevEnd

	previous, err := deps.logManager.GetStats(ctx, &prevFilters)
	if err != nil {
		return nil, err
	}

	trend := map[string]any{
		"has_previous_period": previous.TotalRequests > 0,
		"requests_trend":      0.0,
		"tokens_trend":        0.0,
		"cost_trend":          0.0,
		"window":              formatWindow(prevStart, prevEnd),
	}
	if previous.TotalRequests > 0 {
		trend["requests_trend"] = pctChange(float64(previous.TotalRequests), float64(current.TotalRequests))
		trend["tokens_trend"] = pctChange(float64(previous.TotalTokens), float64(current.TotalTokens))
		trend["cost_trend"] = pctChange(previous.TotalCost, current.TotalCost)
	}
	return trend, nil
}

// pctChange is the percentage change from old to new. old == 0 reports no
// change rather than a divide-by-zero or an infinite percentage - there is
// nothing to compare against, which has_previous_period already says plainly.
func pctChange(old, new float64) float64 {
	if old == 0 {
		return 0
	}
	return (new - old) / old * 100
}

// seriesSummary reduces a numeric series to its shape rather than its detail:
// the same handful of numbers whether the window was an hour or a month,
// which is what actually makes query_metrics cheap regardless of range - the
// tool's own description has claimed this since it was written, but nothing
// executed it; a full-resolution bucket array was returned instead, and nothing
// stopped that array from being the thing that blew the result-size budget.
//
// Total is a pointer, omitted from JSON when nil, because it is only a real
// number for an additive field (a request count, a token count). Summing a
// percentile or a rate across buckets is not a total, it is nonsense with a
// unit on it, so summarizeSeries is never asked to compute one for those.
type seriesSummary struct {
	Total *float64 `json:"total,omitempty"`
	Mean  float64  `json:"mean"`
	Min   float64  `json:"min"`
	Max   float64  `json:"max"`
	First float64  `json:"first"`
	Last  float64  `json:"last"`
}

func summarizeSeries(values []float64, additive bool) seriesSummary {
	if len(values) == 0 {
		return seriesSummary{}
	}
	sum, min, max := 0.0, values[0], values[0]
	for _, v := range values {
		sum += v
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	summary := seriesSummary{
		Mean:  sum / float64(len(values)),
		Min:   min,
		Max:   max,
		First: values[0],
		Last:  values[len(values)-1],
	}
	if additive {
		summary.Total = &sum
	}
	return summary
}

// summarizeRequestsHistogram, summarizeTokensHistogram, summarizeCostHistogram,
// summarizeLatencyHistogram and summarizeThroughputHistogram each reduce one
// histogram type's buckets to a seriesSummary per field, plus how many raw
// buckets fed it - so the model can still tell a summary built from 3 points
// apart from one built from 200. bucket_size_seconds travels alongside for the
// same reason: the shape is legible without it, but the sampling isn't.
func summarizeRequestsHistogram(result *logstore.HistogramResult) map[string]any {
	n := len(result.Buckets)
	count := make([]float64, n)
	success := make([]float64, n)
	errored := make([]float64, n)
	cancelled := make([]float64, n)
	for i, b := range result.Buckets {
		count[i], success[i], errored[i], cancelled[i] = float64(b.Count), float64(b.Success), float64(b.Error), float64(b.Cancelled)
	}
	return map[string]any{
		"count":               summarizeSeries(count, true),
		"success":             summarizeSeries(success, true),
		"error":               summarizeSeries(errored, true),
		"cancelled":           summarizeSeries(cancelled, true),
		"buckets":             n,
		"bucket_size_seconds": result.BucketSizeSeconds,
	}
}

func summarizeTokensHistogram(result *logstore.TokenHistogramResult) map[string]any {
	n := len(result.Buckets)
	prompt := make([]float64, n)
	completion := make([]float64, n)
	total := make([]float64, n)
	cachedRead := make([]float64, n)
	for i, b := range result.Buckets {
		prompt[i], completion[i], total[i], cachedRead[i] = float64(b.PromptTokens), float64(b.CompletionTokens), float64(b.TotalTokens), float64(b.CachedReadTokens)
	}
	return map[string]any{
		"prompt_tokens":       summarizeSeries(prompt, true),
		"completion_tokens":   summarizeSeries(completion, true),
		"total_tokens":        summarizeSeries(total, true),
		"cached_read_tokens":  summarizeSeries(cachedRead, true),
		"buckets":             n,
		"bucket_size_seconds": result.BucketSizeSeconds,
	}
}

// summarizeCostHistogram drops the per-bucket by_model breakdown rather than
// summarizing it: a per-model series-of-series is exactly the kind of nested
// detail a shape-only summary exists to avoid, and the top-level model list is
// already cheap and already returned separately.
func summarizeCostHistogram(result *logstore.CostHistogramResult) map[string]any {
	n := len(result.Buckets)
	cost := make([]float64, n)
	for i, b := range result.Buckets {
		cost[i] = b.TotalCost
	}
	return map[string]any{
		"total_cost":          summarizeSeries(cost, true),
		"models":              result.Models,
		"buckets":             n,
		"bucket_size_seconds": result.BucketSizeSeconds,
	}
}

func summarizeLatencyHistogram(result *logstore.LatencyHistogramResult) map[string]any {
	n := len(result.Buckets)
	avgLatency := make([]float64, n)
	p90Latency := make([]float64, n)
	p95Latency := make([]float64, n)
	p99Latency := make([]float64, n)
	avgOverhead := make([]float64, n)
	p90Overhead := make([]float64, n)
	p95Overhead := make([]float64, n)
	p99Overhead := make([]float64, n)
	totalRequests := make([]float64, n)
	for i, b := range result.Buckets {
		avgLatency[i], p90Latency[i], p95Latency[i], p99Latency[i] = b.AvgLatency, b.P90Latency, b.P95Latency, b.P99Latency
		avgOverhead[i], p90Overhead[i], p95Overhead[i], p99Overhead[i] = b.AvgOverhead, b.P90Overhead, b.P95Overhead, b.P99Overhead
		totalRequests[i] = float64(b.TotalRequests)
	}
	return map[string]any{
		"avg_latency":         summarizeSeries(avgLatency, false),
		"p90_latency":         summarizeSeries(p90Latency, false),
		"p95_latency":         summarizeSeries(p95Latency, false),
		"p99_latency":         summarizeSeries(p99Latency, false),
		"avg_overhead":        summarizeSeries(avgOverhead, false),
		"p90_overhead":        summarizeSeries(p90Overhead, false),
		"p95_overhead":        summarizeSeries(p95Overhead, false),
		"p99_overhead":        summarizeSeries(p99Overhead, false),
		"total_requests":      summarizeSeries(totalRequests, true),
		"buckets":             n,
		"bucket_size_seconds": result.BucketSizeSeconds,
	}
}

func summarizeThroughputHistogram(result *logstore.ThroughputHistogramResult) map[string]any {
	n := len(result.Buckets)
	tokensPerSecond := make([]float64, n)
	completionTokens := make([]float64, n)
	totalRequests := make([]float64, n)
	for i, b := range result.Buckets {
		tokensPerSecond[i], completionTokens[i], totalRequests[i] = b.TokensPerSecond, float64(b.TotalCompletionTokens), float64(b.TotalRequests)
	}
	return map[string]any{
		"tokens_per_second":       summarizeSeries(tokensPerSecond, false),
		"total_completion_tokens": summarizeSeries(completionTokens, true),
		"total_requests":          summarizeSeries(totalRequests, true),
		"buckets":                 n,
		"bucket_size_seconds":     result.BucketSizeSeconds,
	}
}

// ---------------------------------------------------------- flow 3: rankings

// rankingDimensions is every dimension GetDimensionRankings can group by, with
// the one-line sense of each. Declared once here so the tool's enum and its
// validation error can never drift apart from each other.
var rankingDimensions = []struct {
	value       logstore.RankingDimension
	description string
}{
	{logstore.RankingDimensionUser, "the user recorded on each request"},
	{logstore.RankingDimensionVirtualKey, "the virtual key used"},
	{logstore.RankingDimensionTeam, "the team on the request or its virtual key"},
	{logstore.RankingDimensionCustomer, "the customer on the request or its virtual key"},
	{logstore.RankingDimensionBusinessUnit, "the business unit on the request or its virtual key"},
	{logstore.RankingDimensionProject, "the project on the request or its virtual key"},
	{logstore.RankingDimensionApp, "the client app that sent the request"},
	{logstore.RankingDimensionUserAgent, "the raw User-Agent string"},
}

// queryUsageByTool is flow 3: any dimension ranked by usage, one tool instead
// of one per dimension. team/customer/business_unit/project only have data
// where that field is populated on the request or its virtual key - a
// deployment that never sets it will get back an all-"Unassigned" ranking,
// which is a real answer ("nothing is tagged"), not a tool failure.
func queryUsageByTool() Tool {
	var enumValues, describedValues []string
	for _, dim := range rankingDimensions {
		enumValues = append(enumValues, string(dim.value))
		describedValues = append(describedValues, fmt.Sprintf("%s (%s)", dim.value, dim.description))
	}

	return Tool{
		name: "query_usage_by",
		description: "Rank a dimension by usage - cost, requests and tokens - over a window, with a trend against the immediately preceding period of equal length. " +
			"Answers 'who is spending the most', 'which team spends the most', 'which key is burning the budget', 'is X up or down'. " +
			"Each ranking row already carries a trend block (has_previous_period, requests_trend, tokens_trend, cost_trend); read it rather than calling this twice to check direction. " +
			"Dimensions: " + strings.Join(describedValues, "; ") + ". " +
			"Note: a per-entity time series is not available for any dimension; to see one, filter by the relevant id(s) and call query_metrics, which returns one combined series.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "dimension": {"type": "string", "enum": [` + quotedJoin(enumValues) + `]},
    "filters": ` + FilterSchema + `,
    "limit": {"type": "integer", "minimum": 1, "maximum": 20}
  },
  "required": ["dimension", "filters"]
}`,
		execute: func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error) {
			raw, _ := args["dimension"].(string)
			dimension, ok := validRankingDimension(raw)
			if !ok {
				return nil, fmt.Errorf("unknown dimension %q; supported: %s", raw, strings.Join(enumValues, ", "))
			}

			now := Now()
			filters, err := filterArg(args, now, deps.scope)
			if err != nil {
				return nil, err
			}
			limit := intArg(args, "limit", 10, MaxRankingRows)
			filters.RankingLimit = &limit
			result, err := deps.logManager.GetDimensionRankings(ctx, filters, dimension)
			if err != nil {
				return nil, fmt.Errorf("%s rankings failed: %w", dimension, err)
			}
			return map[string]any{
				"rankings":  result,
				"scope":     scopeNote(filters, deps.scope),
				"logs_link": logsViewLink(filters),
				"window":    resolvedWindow(filters),
			}, nil
		},
	}
}

// validRankingDimension checks the model's dimension string against the
// declared set, rather than casting it blindly: a typo would otherwise reach
// the store as an opaque "invalid ranking dimension" error with no indication
// of what was actually available.
func validRankingDimension(raw string) (logstore.RankingDimension, bool) {
	for _, dim := range rankingDimensions {
		if string(dim.value) == raw {
			return dim.value, true
		}
	}
	return "", false
}

// quotedJoin renders a string slice as comma-separated JSON string literals,
// for splicing into a schema written as a Go string literal above.
func quotedJoin(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = `"` + v + `"`
	}
	return strings.Join(quoted, ", ")
}

// ------------------------------------------------ flow 4: providers and models

// queryModelsTool is flow 4: model rankings and provider performance.
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
			limit := intArg(args, "limit", 10, MaxRankingRows)
			filters.RankingLimit = &limit

			rankings, err := deps.logManager.GetModelRankings(ctx, filters)
			if err != nil {
				return nil, fmt.Errorf("model rankings failed: %w", err)
			}
			out := map[string]any{
				"models":    rankings,
				"scope":     scopeNote(filters, deps.scope),
				"logs_link": logsViewLink(filters),
				"window":    resolvedWindow(filters),
			}

			if boolArg(args, "include_performance") {
				// A coarse bucket on purpose. The dashboard's bucket size is chosen for
				// a chart with hundreds of pixels; the same series as JSON, once per
				// provider, was overflowing the tool-result budget and sending Warp
				// round the retry loop. A dozen buckets carry the shape of a latency
				// trend, which is all the answer needs.
				bucket, err := coarseBucketSize(filters)
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
