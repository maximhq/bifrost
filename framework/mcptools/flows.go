package mcptools

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
)

// maxQueryMetrics caps how many metrics one query_metrics call may request.
// The schema's maxItems and enumSliceArg's runtime check both read this
// constant, so the advertised contract and the enforced one cannot drift:
// the vocabulary has six values, so a purely element-wise check would let a
// call overrun the published limit by two queries.
const maxQueryMetrics = 4

// The named query flows this server exposes, plus a drill-down and a
// discovery tool. Each flow is one tool over the logstore read surface; they
// all take the same filter object, which is what lets one parser and one
// scope path serve every one of them. filterArg resolves the caller's default
// scope fresh from ctx on every call: this server has no per-turn boundary,
// so concurrent callers with different identities can be in flight at once.

// formatWindow renders a window the way every tool result reports it: an
// absolute UTC instant, regardless of whether the caller passed a relative
// offset, an absolute date, or nothing.
func formatWindow(start, end time.Time) map[string]string {
	return map[string]string{
		"start": start.UTC().Format(time.RFC3339Nano),
		"end":   end.UTC().Format(time.RFC3339Nano),
	}
}

// resolvedWindow reports the absolute window filters actually resolved to,
// so the caller copies it rather than recomputing "-7d" by hand.
func resolvedWindow(filters *logstore.SearchFilters) map[string]string {
	return formatWindow(*filters.StartTime, *filters.EndTime)
}

// ---------------------------------------------------------------- flow 1: logs

// semanticSearchLogsTool finds requests by conversational meaning. It still
// accepts the shared structured filters, but unlike query_logs the query is
// embedded and compared with the stored user/assistant conversation vectors.
// SemanticSearchToolName is the one tool that needs an embedding executor, so
// it is the one tool the set can be missing.
const SemanticSearchToolName = "semantic_search_logs"

func semanticSearchLogsTool() Tool {
	return Tool{
		name: SemanticSearchToolName,
		description: "Find logged conversations by meaning. Use this when the question is about what users discussed, wanted, reported, or what assistants answered, even when the wording differs. " +
			"Use query_logs, count_logs, or query_metrics for exact fields, counts, latency, cost, and trends.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "query": {"type": "string", "maxLength": ` + strconv.Itoa(MaxSemanticQueryChars) + `, "description": "A natural-language description of the conversations to find."},
    "filters": ` + FilterSchema + `,
    "limit": {"type": "integer", "minimum": 1, "maximum": 25, "description": "Matches to return. Also capped by the configured semantic search limit."}
  },
  "required": ["query", "filters"]
}`,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			query, _ := args["query"].(string)
			if deps.Semantic == nil {
				return nil, fmt.Errorf("semantic log search is not configured")
			}
			filters, scope, err := filterArg(ctx, args, Now())
			if err != nil {
				return nil, err
			}
			// Fallback 0 means "use the configured semantic limit", which is why
			// absent stays 0 here rather than defaulting to a row count.
			limit, err := intArg(args, "limit", 0, MaxLogRows)
			if err != nil {
				return nil, err
			}
			result, err := deps.Semantic.Search(ctx, query, filters, limit)
			if err != nil {
				return nil, err
			}
			response := map[string]any{
				"rows":      result.Rows,
				"returned":  result.Returned,
				"threshold": result.Threshold,
				"scope":     scopeNote(filters, scope),
				"window":    resolvedWindow(filters),
			}
			setLogsLink(response, filters)
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
		description: "List individual LLM request logs matching a filter. Returns compact rows (timestamp, provider, model, status, latency, tokens, cost, virtual key, user, and on an error row: error_message, error_type, error_code, status_code), not full message bodies. " +
			"Use this to find specific requests - which ones failed, which were slowest, what a given user actually sent. It is also the right tool for 'what kinds of errors are these' - filter to status error and tally error_type/error_code across the returned rows, rather than opening each one individually. For totals and trends use query_metrics instead, which is far cheaper.",
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
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			filters, scope, err := filterArg(ctx, args, Now())
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
			result, err := deps.LogManager.Search(ctx, filters, &logstore.PaginationOptions{
				Limit: limit, Offset: 0, SortBy: sortBy, Order: order,
			})
			if err != nil {
				return nil, fmt.Errorf("log search failed: %w", err)
			}
			includeContent, err := boolArg(args, "include_content")
			if err != nil {
				return nil, err
			}
			rows := make([]LogRow, 0, len(result.Logs))
			for i := range result.Logs {
				rows = append(rows, ProjectLog(&result.Logs[i], includeContent, LogContentChars))
			}
			// total_matching is reported separately from the returned rows so the
			// model can say "12,400 matched, here are the 10 slowest" instead of
			// implying it saw everything.
			return setLogsLink(map[string]any{
				"rows":           rows,
				"returned":       len(rows),
				"total_matching": result.Pagination.TotalCount,
				// Stated rather than left to be inferred from the two counts above.
				// The prompt asks the model to caveat a partial answer, and a caveat
				// it has to derive by comparing numbers is the one it forgets.
				"sampled": int64(len(rows)) < result.Pagination.TotalCount,
				"scope":   scopeNote(filters, scope),
				"window":  resolvedWindow(filters),
			}, filters), nil
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
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
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
			entry, err := deps.LogManager.GetLog(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("could not load log %s: %w", id, err)
			}
			if entry == nil {
				return nil, fmt.Errorf("no log found with id %s", id)
			}
			return ProjectLog(entry, true, DetailContentChars), nil
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
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			filters, scope, err := filterArg(ctx, args, Now())
			if err != nil {
				return nil, err
			}
			stats, err := deps.LogManager.GetStats(ctx, filters)
			if err != nil {
				return nil, fmt.Errorf("count failed: %w", err)
			}

			out := map[string]any{
				"total_requests":     stats.TotalRequests,
				"total_tokens":       stats.TotalTokens,
				"total_cost":         stats.TotalCost,
				"success_rate":       stats.SuccessRate,
				"average_latency_ms": stats.AverageLatency,
				"scope":              scopeNote(filters, scope),
				"window":             resolvedWindow(filters),
			}
			setLogsLink(out, filters)
			// A query the Logs page cannot reproduce gets no logs_link, and the
			// guidance must not then hand over a link the result does not carry.
			_, hasLogsLink := out["logs_link"]
			if link := failuresLink(filters, stats.TotalRequests, stats.SuccessRate); link != "" {
				out["failures_link"] = link
			}
			// The advice travels with the number rather than living only in the
			// prompt: this is the moment the decision gets made, and the threshold
			// is a property of the tool rather than of the conversation.
			switch {
			case stats.TotalRequests == 0:
				out["guidance"] = "Nothing matched. Widen the time range or check the filter values with describe_filter_space before concluding there is no traffic."
			case stats.TotalRequests > LargeResultThreshold && !hasLogsLink:
				out["too_many_to_list"] = true
				out["guidance"] = fmt.Sprintf(
					"%d requests match - far too many to list. For a sorted top-N (\"slowest requests\", \"most expensive calls\"), call query_logs with sort_by and limit directly. Otherwise answer from aggregates (query_metrics, query_model_performance) where you can. If you genuinely need individual rows, narrow by provider, model, status or virtual key, or split the window into smaller slices and handle one at a time.",
					stats.TotalRequests)
			case stats.TotalRequests > LargeResultThreshold:
				out["too_many_to_list"] = true
				out["guidance"] = fmt.Sprintf(
					"%d requests match - far too many to list in full. If the person asked to see or list these requests, that is answerable: give the count and link it to logs_link, which opens this exact filtered set in the Logs view - do not tell them it cannot be listed or ask how to narrow it. For a sorted top-N (\"slowest requests\", \"most expensive calls\"), call query_logs with sort_by and limit directly; that works regardless of this count, it is not the same as listing everything. For a total, answer from aggregates (query_metrics, query_model_performance) where you can. Only narrow by provider, model, status or virtual key, or split the window into smaller slices, if you genuinely need individual rows beyond a top-N.",
					stats.TotalRequests)
			case stats.TotalRequests > MaxLogRows && !hasLogsLink:
				out["guidance"] = fmt.Sprintf(
					"%d requests match, and query_logs returns at most %d - listing them would answer from a sample without saying so. Answer from aggregates (query_metrics, query_model_performance), or narrow by provider, model, status or virtual key, or split the window, until the count is %d or fewer.",
					stats.TotalRequests, MaxLogRows, MaxLogRows)
			case stats.TotalRequests > MaxLogRows:
				// Between MaxLogRows and LargeResultThreshold, "list it" was advice
				// the tool cannot take: query_logs returns at most MaxLogRows, so the
				// model got a sample and had no way to know it was one.
				out["guidance"] = fmt.Sprintf(
					"%d requests match, and query_logs returns at most %d - listing them would answer from a sample without saying so. If the person asked to see or list these requests, give the count and link it to logs_link, which opens this exact filtered set in the Logs view - that is the full list, so do not tell them it cannot be listed or ask how to narrow it. Otherwise answer from aggregates (query_metrics, query_model_performance), or narrow by provider, model, status or virtual key, or split the window, until the count is %d or fewer.",
					stats.TotalRequests, MaxLogRows, MaxLogRows)
			default:
				out["guidance"] = fmt.Sprintf("Small enough to list with query_logs if individual rows are needed - it returns up to %d.", MaxLogRows)
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
			"This is also the only tool that splits usage by provider: group_by 'provider' answers 'spend per provider', 'which provider costs the most', 'cost on each provider'. Provider is not a dimension of query_usage_by. " +
			"This is the cheapest way to answer 'how much', 'how many' and 'is it getting worse'. Without group_by, each series is reduced to one summary per field (total, mean, min, max, first, last) rather than bucket by bucket - total is omitted for a field summing cannot describe, like a percentile. " +
			"group_by supports 'none' and 'provider' only. With 'provider', series stay as coarse buckets instead of a summary, since collapsing away the per-provider split would defeat the reason to group by it in the first place, and provider_totals carries each provider's exact total cost, tokens, requests, success rate and average latency over the window, with a link to that provider's requests. Report per-provider totals from provider_totals, never by adding up buckets: bucket boundaries do not line up with the window, so a sum of buckets does not match the dashboard. 'requests' does not support group_by 'provider' - drop it from metrics or query without group_by. " +
			"Set compare_to_previous with metrics including summary to answer 'is it up or down vs last period' in this one call, instead of calling this twice with a shifted window.",
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
    "group_by": {"type": "string", "enum": ["none", "provider"]},
    "compare_to_previous": {"type": "boolean", "description": "Requires 'summary' in metrics. Also fetches the immediately preceding period of equal length and returns a trend block (has_previous_period, requests_trend, tokens_trend, cost_trend as percent change; tokens_trend or cost_trend is null when that metric was zero in the previous period and nonzero now) alongside summary."}
  },
  "required": ["filters", "metrics"]
}`,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			filters, scope, err := filterArg(ctx, args, Now())
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
			// compare_to_previous only has something to compare when a summary is
			// being returned; the trend is computed against it.
			compareToPrevious, err := boolArg(args, "compare_to_previous")
			if err != nil {
				return nil, err
			}
			if compareToPrevious && !slices.Contains(metrics, "summary") {
				return nil, fmt.Errorf("compare_to_previous requires metrics to include \"summary\"")
			}
			// Every other metric has a GetProvider*Histogram counterpart that keeps
			// the per-provider split; "requests" has none. Silently falling through
			// to the ungrouped histogram would answer a different question than the
			// one asked and look identical to a real per-provider breakdown, so this
			// is rejected up front instead.
			if byProvider && slices.Contains(metrics, "requests") {
				return nil, fmt.Errorf(`group_by "provider" is not supported for the "requests" metric; drop "requests" from metrics or query without group_by`)
			}

			// Every non-grouped series below is reduced to a seriesSummary and the
			// buckets discarded, so a finer bucket only improves the summary's
			// fidelity - it costs nothing in result size. The per-provider path
			// returns its buckets raw (see the byProvider branches), so it keeps
			// the coarse bucket size that path has always needed to stay bounded.
			//
			// Only computed when a histogram metric is asked for: bucketSize
			// rejects a range that would produce too many buckets, and a
			// summary-only request over that range builds no buckets at all.
			var bucket, providerBucket int64
			if slices.ContainsFunc(metrics, func(metric string) bool { return metric != "summary" }) {
				if bucket, err = bucketSize(filters); err != nil {
					return nil, err
				}
			}
			// A summary-only grouped call still needs the coarse bucket: the
			// provider list is discovered through a per-provider histogram below.
			if byProvider {
				if providerBucket, err = coarseBucketSize(filters); err != nil {
					return nil, err
				}
			}

			out := map[string]any{
				"scope":  scopeNote(filters, scope),
				"window": resolvedWindow(filters),
			}
			setLogsLink(out, filters)
			var groupedProviders []string
			for _, metric := range metrics {
				switch metric {
				case "summary":
					stats, err := deps.LogManager.GetStats(ctx, filters)
					if err != nil {
						return nil, fmt.Errorf("stats query failed: %w", err)
					}
					out["summary"] = stats
					if link := failuresLink(filters, stats.TotalRequests, stats.SuccessRate); link != "" {
						out["failures_link"] = link
					}
					if compareToPrevious {
						trend, err := previousPeriodTrend(ctx, deps, filters, stats)
						if err != nil {
							return nil, fmt.Errorf("previous-period comparison failed: %w", err)
						}
						out["previous_period"] = trend
					}
				case "requests":
					// GetHistogram has no provider breakdown, so grouping it by
					// provider returned deployment-wide totals under a heading that
					// said per-provider - a wrong answer the model cannot detect.
					if byProvider {
						return nil, fmt.Errorf("group_by \"provider\" is not supported for the requests metric; ask for cost, tokens or latency by provider, or requests without grouping")
					}
					result, err := deps.LogManager.GetHistogram(ctx, filters, bucket)
					if err != nil {
						return nil, fmt.Errorf("request histogram failed: %w", err)
					}
					out["requests"] = summarizeRequestsHistogram(result)
				case "tokens":
					if byProvider {
						result, err := deps.LogManager.GetProviderTokenHistogram(ctx, filters, providerBucket)
						if err != nil {
							return nil, fmt.Errorf("token histogram failed: %w", err)
						}
						out["tokens"] = result
						groupedProviders = append(groupedProviders, result.Providers...)
						continue
					}
					result, err := deps.LogManager.GetTokenHistogram(ctx, filters, bucket)
					if err != nil {
						return nil, fmt.Errorf("token histogram failed: %w", err)
					}
					out["tokens"] = summarizeTokensHistogram(result)
				case "cost":
					if byProvider {
						result, err := deps.LogManager.GetProviderCostHistogram(ctx, filters, providerBucket)
						if err != nil {
							return nil, fmt.Errorf("cost histogram failed: %w", err)
						}
						out["cost"] = result
						groupedProviders = append(groupedProviders, result.Providers...)
						continue
					}
					result, err := deps.LogManager.GetCostHistogram(ctx, filters, bucket)
					if err != nil {
						return nil, fmt.Errorf("cost histogram failed: %w", err)
					}
					out["cost"] = summarizeCostHistogram(result)
				case "latency":
					if byProvider {
						result, err := deps.LogManager.GetProviderLatencyHistogram(ctx, filters, providerBucket)
						if err != nil {
							return nil, fmt.Errorf("latency histogram failed: %w", err)
						}
						out["latency"] = result
						groupedProviders = append(groupedProviders, result.Providers...)
						continue
					}
					result, err := deps.LogManager.GetLatencyHistogram(ctx, filters, bucket)
					if err != nil {
						return nil, fmt.Errorf("latency histogram failed: %w", err)
					}
					out["latency"] = summarizeLatencyHistogram(result)
				case "throughput":
					if byProvider {
						result, err := deps.LogManager.GetProviderThroughputHistogram(ctx, filters, providerBucket)
						if err != nil {
							return nil, fmt.Errorf("throughput histogram failed: %w", err)
						}
						out["throughput"] = result
						groupedProviders = append(groupedProviders, result.Providers...)
						continue
					}
					result, err := deps.LogManager.GetThroughputHistogram(ctx, filters, bucket)
					if err != nil {
						return nil, fmt.Errorf("throughput histogram failed: %w", err)
					}
					out["throughput"] = summarizeThroughputHistogram(result)
				default:
					// Unreachable through enumSliceArg, which validates the same
					// vocabulary before dispatch. Kept as a drift guard: a metric
					// added to that list but not to this switch would otherwise be
					// accepted and then silently produce nothing.
					return nil, fmt.Errorf("unknown metric %q; supported: summary, requests, tokens, cost, latency, throughput", metric)
				}
			}
			// No per-provider histogram ran - "summary" alone, the natural ask for
			// "which provider fails most". Without this the grouping was dropped
			// silently: one deployment-wide summary came back under a request for
			// a per-provider split. The histogram is read only for its provider
			// list; its buckets are not returned, since nobody asked for cost.
			if byProvider && len(groupedProviders) == 0 {
				discovered, err := deps.LogManager.GetProviderCostHistogram(ctx, filters, providerBucket)
				if err != nil {
					return nil, fmt.Errorf("provider lookup failed: %w", err)
				}
				groupedProviders = discovered.Providers
			}
			if len(groupedProviders) > 0 {
				totals, err := providerTotals(ctx, deps, filters, groupedProviders)
				if err != nil {
					return nil, fmt.Errorf("per-provider totals failed: %w", err)
				}
				out["provider_totals"] = totals
			}
			return out, nil
		},
	}
}

// providerTotal is one provider's exact aggregate over the filtered window.
type providerTotal struct {
	TotalRequests  int64   `json:"total_requests"`
	TotalCost      float64 `json:"total_cost"`
	TotalTokens    int64   `json:"total_tokens"`
	SuccessRate    float64 `json:"success_rate"`
	AverageLatency float64 `json:"average_latency_ms"`
	// Link opens this provider's requests under the tool's own filters.
	Link string `json:"link,omitempty"`
}

// providerTotals reads each provider's exact totals over the filtered window,
// one GetStats per provider, run concurrently.
//
// The grouped buckets cannot stand in for these. They are aligned to the
// bucket size, so the first starts before the window, and on Postgres they are
// read from whole matview hours; GetStats reads the partial hours at each edge
// from the raw table, which is what the dashboard's total does. Left to add
// the buckets up itself, the model dropped the first one as out of range and
// reported a provider's spend short of the dashboard's number for the same
// window.
func providerTotals(ctx context.Context, deps *Deps, filters *logstore.SearchFilters, providers []string) (map[string]providerTotal, error) {
	slices.Sort(providers)
	providers = slices.Compact(providers)
	stats := make([]*logstore.SearchStats, len(providers))
	errs := make([]error, len(providers))
	var wg sync.WaitGroup
	for index, provider := range providers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each provider came out of the same filtered query, so narrowing
			// to it never leaves the scope the buckets covered.
			narrowed := *filters
			narrowed.Providers = []string{provider}
			stats[index], errs[index] = deps.LogManager.GetStats(ctx, &narrowed)
		}()
	}
	wg.Wait()
	totals := make(map[string]providerTotal, len(providers))
	for index, provider := range providers {
		if errs[index] != nil {
			return nil, errs[index]
		}
		narrowed := *filters
		narrowed.Providers = []string{provider}
		totals[provider] = providerTotal{
			TotalRequests:  stats[index].TotalRequests,
			TotalCost:      stats[index].TotalCost,
			TotalTokens:    stats[index].TotalTokens,
			SuccessRate:    stats[index].SuccessRate,
			AverageLatency: stats[index].AverageLatency,
			Link:           logsViewLink(&narrowed),
		}
	}
	return totals, nil
}

// previousPeriodTrend fetches the period immediately preceding the filtered
// window, of equal length, and compares it to the stats already fetched for
// that window. It costs one extra store query so query_metrics's caller never
// has to spend a second tool call computing the same comparison rankings get
// for free.
func previousPeriodTrend(ctx context.Context, deps *Deps, filters *logstore.SearchFilters, current *logstore.SearchStats) (map[string]any, error) {
	span := filters.EndTime.Sub(*filters.StartTime)
	boundary := *filters.StartTime
	prevStart := boundary.Add(-span)
	// GetStats bounds are inclusive at both ends, so ending the previous window
	// at the boundary would count a log stamped exactly there in both periods.
	// Stopping one microsecond short - the finest tick Postgres stores - makes
	// it [prevStart, boundary) without changing the store's shared semantics.
	prevEnd := boundary.Add(-time.Microsecond)

	// A shallow copy: every other filter (providers, scope, status...) carries
	// over unchanged, only the window shifts.
	prevFilters := *filters
	prevFilters.StartTime, prevFilters.EndTime = &prevStart, &prevEnd

	previous, err := deps.LogManager.GetStats(ctx, &prevFilters)
	if err != nil {
		return nil, err
	}
	trend := map[string]any{
		"has_previous_period": previous.TotalRequests > 0,
		"requests_trend":      0.0,
		// Tokens and cost are judged on their own baselines, whether or not the
		// previous period had requests. Against a zero baseline there is no
		// percentage, so a nonzero current value reports null instead of the 0%
		// that reads as "unchanged".
		"tokens_trend": metricTrend(float64(previous.TotalTokens), float64(current.TotalTokens)),
		"cost_trend":   metricTrend(previous.TotalCost, current.TotalCost),
		"window":       formatWindow(prevStart, boundary),
	}
	if previous.TotalRequests > 0 {
		trend["requests_trend"] = pctChange(float64(previous.TotalRequests), float64(current.TotalRequests))
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

// metricTrend is pctChange that tells a zero baseline apart from no change:
// zero to nonzero is nil (null on the wire), zero to zero is 0%.
func metricTrend(old, new float64) any {
	if old == 0 && new != 0 {
		return nil
	}
	return pctChange(old, new)
}

// seriesSummary reduces a numeric series to its shape rather than its detail:
// the same handful of numbers whether the window was an hour or a month.
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

// summarizeWeightedSeries behaves like summarizeSeries but recomputes Mean as
// a weighted average using weights (bucket request counts, typically), so a
// request-heavy bucket counts more toward the mean than a near-empty one.
// Min/Max/First/Last/Total stay as summarizeSeries computed them - only the
// mean's per-bucket averaging changes. Falls back to the unweighted mean when
// the weights sum to zero (e.g. no requests in the window at all).
func summarizeWeightedSeries(values, weights []float64, additive bool) seriesSummary {
	summary := summarizeSeries(values, additive)
	if len(values) == 0 || len(weights) != len(values) {
		return summary
	}
	weightedSum, weightTotal := 0.0, 0.0
	for i, v := range values {
		weightedSum += v * weights[i]
		weightTotal += weights[i]
	}
	if weightTotal > 0 {
		summary.Mean = weightedSum / weightTotal
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
// already cheap and already returned separately. The names come without
// amounts, so per_model_cost says where the amounts are: asked for model-wise
// spend, a model that saw names with no numbers beside them concluded the
// breakdown did not exist.
func summarizeCostHistogram(result *logstore.CostHistogramResult) map[string]any {
	n := len(result.Buckets)
	cost := make([]float64, n)
	for i, b := range result.Buckets {
		cost[i] = b.TotalCost
	}
	return map[string]any{
		"total_cost":          summarizeSeries(cost, true),
		"models":              result.Models,
		"per_model_cost":      "not in this result - call query_model_performance for each model's total, input and output cost",
		"buckets":             n,
		"bucket_size_seconds": result.BucketSizeSeconds,
	}
}

// The store pads idle time slots with zero-valued buckets. Those are gaps, not
// requests that took 0ms, so the latency and overhead series skip them - fed
// in, they read as a 0ms min, a 0ms first/last whenever the window starts or
// ends quiet, and diluted percentile means. Request counts are additive, so a
// gap is a real zero there and total_requests keeps every bucket.
func summarizeLatencyHistogram(result *logstore.LatencyHistogramResult) map[string]any {
	n := len(result.Buckets)
	var avgLatency, p90Latency, p95Latency, p99Latency []float64
	var avgOverhead, p90Overhead, p95Overhead, p99Overhead, weights []float64
	totalRequests := make([]float64, n)
	for i, b := range result.Buckets {
		totalRequests[i] = float64(b.TotalRequests)
		if b.TotalRequests == 0 {
			continue
		}
		avgLatency, p90Latency = append(avgLatency, b.AvgLatency), append(p90Latency, b.P90Latency)
		p95Latency, p99Latency = append(p95Latency, b.P95Latency), append(p99Latency, b.P99Latency)
		avgOverhead, p90Overhead = append(avgOverhead, b.AvgOverhead), append(p90Overhead, b.P90Overhead)
		p95Overhead, p99Overhead = append(p95Overhead, b.P95Overhead), append(p99Overhead, b.P99Overhead)
		weights = append(weights, float64(b.TotalRequests))
	}
	return map[string]any{
		"avg_latency":         summarizeWeightedSeries(avgLatency, weights, false),
		"p90_latency":         summarizeSeries(p90Latency, false),
		"p95_latency":         summarizeSeries(p95Latency, false),
		"p99_latency":         summarizeSeries(p99Latency, false),
		"avg_overhead":        summarizeWeightedSeries(avgOverhead, weights, false),
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
	// Idle buckets are skipped for throughput like latency: 0 tokens/sec there
	// is no traffic, not a stalled stream. The additive counts keep every bucket.
	var tokensPerSecond []float64
	completionTokens := make([]float64, n)
	totalRequests := make([]float64, n)
	for i, b := range result.Buckets {
		completionTokens[i], totalRequests[i] = float64(b.TotalCompletionTokens), float64(b.TotalRequests)
		if b.TotalRequests == 0 {
			continue
		}
		tokensPerSecond = append(tokensPerSecond, b.TokensPerSecond)
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
	{logstore.RankingDimensionRoutingRule, "the routing rule that handled the request, by name - requests no rule handled are left out, so compare against count_logs for the share"},
	{logstore.RankingDimensionRoutingEngine, "the routing engines a request passed through, e.g. routing-rule, governance, loadbalancing - one request can pass through several, so totals can exceed the request count"},
	{logstore.RankingDimensionSelectedKey, "the provider API key Bifrost sent the request with, by name - not a virtual key; pair with status error to find a failing key"},
	{logstore.RankingDimensionAlias, "the model alias the request was addressed to before Bifrost resolved it"},
	{logstore.RankingDimensionComplexityTier, "the tier the complexity router assigned: SIMPLE, MEDIUM or COMPLEX - only requests it saw"},
	{logstore.RankingDimensionComplexityMechanism, "how the complexity tier was decided, e.g. semantic, llm, session"},
	{logstore.RankingDimensionToolCallName, "the function names responses called - one request can call several, so totals can exceed the request count"},
	{logstore.RankingDimensionErrorType, "the provider's error classification on a failed request, e.g. rate_limit_error, invalid_request_error, timeout - filter to status error first, or every non-error request is silently excluded rather than shown as a false zero"},
	{logstore.RankingDimensionErrorCode, "the provider's finer-grained error code on a failed request, e.g. rate_limited, context_length_exceeded - same status-error caveat as error_type"},
	{logstore.RankingDimensionStatusCode, "the HTTP status a failed request came back with, e.g. 400, 429, 529 - populated on every failed request, unlike error_code which many providers leave empty; same status-error caveat as error_type"},
	{logstore.RankingDimensionFailReason, "why a retry attempt failed, e.g. rate_limit_error, authentication_error, billing_error - one request can contribute more than one (one per failed attempt before it succeeded or gave up), so totals here can exceed the request count It counts retry attempts, including attempts on requests that went on to succeed, so it answers a different question from error_type: never compare its counts with error_type's, and never use it to check or correct an error_type count."},
	{logstore.RankingDimensionGuardrailRule, "which named guardrail rule fired - one request can trigger more than one rule, same over-count caveat as fail_reason"},
	{logstore.RankingDimensionGuardrailAction, "what a guardrail did (e.g. block, redact) when it fired - same over-count caveat as fail_reason"},
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
			"It is also the failure breakdown: dimension error_type, status_code or error_code with filters.status [\"error\"] counts every failed request by kind, exactly rather than from a sample of rows - the tool for 'what errors are we seeing', 'why are requests failing', 'what caused the failure spike'. To fetch the requests behind one row, pass that row's id to query_logs as error_types, status_codes or error_codes. " +
			"Each ranking row already carries a trend block (has_previous_period, requests_trend, tokens_trend, cost_trend); read it rather than calling this twice to check direction. " +
			"Dimensions: " + strings.Join(describedValues, "; ") + ". " +
			"Provider is not one of them: for spend or requests split by provider, call query_metrics with group_by 'provider'. " +
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
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			raw, _ := args["dimension"].(string)
			dimension, ok := validRankingDimension(raw)
			if !ok {
				return nil, fmt.Errorf("unknown dimension %q; supported: %s. For a breakdown by provider, call query_metrics with group_by \"provider\"", raw, strings.Join(enumValues, ", "))
			}

			filters, scope, err := filterArg(ctx, args, Now())
			if err != nil {
				return nil, err
			}
			limit, err := intArg(args, "limit", 10, MaxRankingRows)
			if err != nil {
				return nil, err
			}
			filters.RankingLimit = &limit
			result, err := deps.LogManager.GetDimensionRankings(ctx, filters, dimension)
			if err != nil {
				return nil, fmt.Errorf("%s rankings failed: %w", dimension, err)
			}
			return setLogsLink(map[string]any{
				"rankings": linkDimensionRankings(result, filters, dimension),
				"scope":    scopeNote(filters, scope),
				"window":   resolvedWindow(filters),
			}, filters), nil
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
		description: "Rank models by usage and spend and, optionally, compare provider performance (latency percentiles and throughput). " +
			"Each model row carries requests, tokens, success rate, latency, throughput and total cost split into input cost, output cost and additional cost. " +
			"This is the tool for model-wise spend and per-model cost - query_metrics has no per-model split. " +
			"Answers 'model-wise spend', 'which model costs the most', 'which model do we use most', 'which provider is slowest', 'did p99 regress'.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "filters": ` + FilterSchema + `,
    "limit": {"type": "integer", "minimum": 1, "maximum": 20},
    "include_performance": {"type": "boolean", "description": "Adds per-provider latency and throughput series."}
  },
  "required": ["filters"]
}`,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			filters, scope, err := filterArg(ctx, args, Now())
			if err != nil {
				return nil, err
			}
			limit, err := intArg(args, "limit", 10, MaxRankingRows)
			if err != nil {
				return nil, err
			}
			filters.RankingLimit = &limit

			rankings, err := deps.LogManager.GetModelRankings(ctx, filters)
			if err != nil {
				return nil, fmt.Errorf("model rankings failed: %w", err)
			}
			out := map[string]any{
				"models": linkModelRankings(rankings, filters),
				"scope":  scopeNote(filters, scope),
				"window": resolvedWindow(filters),
			}
			setLogsLink(out, filters)

			includePerformance, err := boolArg(args, "include_performance")
			if err != nil {
				return nil, err
			}
			if includePerformance {
				// A coarse bucket on purpose. The dashboard's bucket size is chosen for
				// a chart with hundreds of pixels; the same series as JSON, once per
				// provider, was overflowing the tool-result budget and sending the
				// caller round the retry loop. A dozen buckets carry the shape of a
				// latency trend, which is all the answer needs.
				bucket, err := coarseBucketSize(filters)
				if err != nil {
					return nil, err
				}
				latency, err := deps.LogManager.GetProviderLatencyHistogram(ctx, filters, bucket)
				if err != nil {
					return nil, fmt.Errorf("provider latency failed: %w", err)
				}
				throughput, err := deps.LogManager.GetProviderThroughputHistogram(ctx, filters, bucket)
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

// describeFilterSpaceTool lists everything a question might need to know
// before it can be answered correctly: who is asking and what the default
// scope is, which teams/customers/business units exist to narrow to, and
// every value a filter field actually accepts.
func describeFilterSpaceTool() Tool {
	return Tool{
		name: "describe_filter_space",
		description: "Report who is asking, what teams/customers/business units they could mean, and the real values that appear in this deployment's logs - models, apps, stop reasons, virtual keys, routing rules, provider keys, aliases, routing engines, tool call names and metadata keys - up to 50 of each, not necessarily every one that exists. " +
			"Call this before filtering by a name you are not certain about: guessing a model or key name returns an empty result that looks like a real finding. " +
			"If a deployment has more than 50 of something, this list is a sample, not the full set - pass search to narrow to the specific value you need rather than treating an absence here as proof it does not exist. " +
			"Also call it whenever a question about usage, spend or performance does not say whose traffic it means - with a known caller, their own traffic is the default; without one there is no default.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "search": {"type": "string", "description": "Optional substring to narrow the returned values."}
  }
}`,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
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
			scope := scopeFromContext(ctx)

			out := map[string]any{
				"caller_is_identified": scope.HasIdentity,
				"default_scope": func() string {
					if scope.HasIdentity {
						return "the person asking"
					}
					// Read at the moment the caller decides how to ask. "ask which
					// team..." alone came back as that same sentence in prose, with
					// nothing to click, so it says how. Worded for any MCP client:
					// Warp's own prompt names its question tool.
					return "none - nobody is identified. If you need to ask whose traffic is meant, ask through your question tool if you have one, with options from the teams, customers or business_units below (one dimension at a time) - not as prose the person cannot click - or name it explicitly"
				}(),
			}
			if scope.HasIdentity {
				out["caller_user_id"] = scope.UserID
			}

			models, err := deps.LogManager.GetAvailableModels(ctx, limit, query)
			if err != nil {
				return nil, fmt.Errorf("could not list models: %w", err)
			}
			apps, err := deps.LogManager.GetAvailableApps(ctx, limit, query)
			if err != nil {
				return nil, fmt.Errorf("could not list apps: %w", err)
			}
			stopReasons, err := deps.LogManager.GetAvailableStopReasons(ctx, limit, query)
			if err != nil {
				return nil, fmt.Errorf("could not list stop reasons: %w", err)
			}
			out["models"] = models
			out["apps"] = apps
			out["stop_reasons"] = stopReasons

			// The routing values, from the lookups behind the Logs page's own
			// filter dropdowns. Run together: each is an independent indexed read.
			var (
				routingRules, providerKeys             []KeyPair
				aliases, routingEngines, toolCallNames []string
				metadata                               map[string][]string
				routingErrs                            [6]error
				routingWG                              sync.WaitGroup
			)
			for index, lookup := range []func() error{
				func() (err error) {
					routingRules, err = deps.LogManager.GetAvailableRoutingRules(ctx, limit, query)
					return
				},
				func() (err error) {
					providerKeys, err = deps.LogManager.GetAvailableSelectedKeys(ctx, limit, query)
					return
				},
				func() (err error) { aliases, err = deps.LogManager.GetAvailableAliases(ctx, limit, query); return },
				func() (err error) {
					routingEngines, err = deps.LogManager.GetAvailableRoutingEngines(ctx, limit, query)
					return
				},
				func() (err error) {
					toolCallNames, err = deps.LogManager.GetAvailableToolCallNames(ctx, limit, query)
					return
				},
				func() (err error) {
					metadata, err = deps.LogManager.GetAvailableMetadataKeys(ctx, limit, query)
					return
				},
			} {
				routingWG.Add(1)
				go func() {
					defer routingWG.Done()
					routingErrs[index] = lookup()
				}()
			}
			routingWG.Wait()
			for _, err := range routingErrs {
				if err != nil {
					return nil, fmt.Errorf("could not list routing values: %w", err)
				}
			}
			out["routing_rules"] = routingRules
			out["provider_keys"] = providerKeys
			out["aliases"] = aliases
			out["routing_engines"] = routingEngines
			out["tool_call_names"] = toolCallNames
			out["metadata"] = metadata

			// virtual_keys, teams, customers and business_units are the distinct-value
			// lookups the Logs filter bar uses - one indexed DISTINCT each, run
			// concurrently.
			lookups := []struct {
				key    string
				lookup func(context.Context, int, string) ([]KeyPair, error)
			}{
				{"virtual_keys", deps.LogManager.GetAvailableVirtualKeys},
				{"teams", deps.LogManager.GetAvailableTeams},
				{"customers", deps.LogManager.GetAvailableCustomers},
				{"business_units", deps.LogManager.GetAvailableBusinessUnits},
			}
			results := make([][]KeyPair, len(lookups))
			errs := make([]error, len(lookups))
			var wg sync.WaitGroup
			for index, entry := range lookups {
				wg.Add(1)
				go func() {
					defer wg.Done()
					results[index], errs[index] = entry.lookup(ctx, limit, query)
				}()
			}
			wg.Wait()
			for index, entry := range lookups {
				if errs[index] != nil {
					return nil, errs[index]
				}
				if entry.key == "virtual_keys" {
					out[entry.key] = results[index]
					continue
				}
				out[entry.key] = keyPairLabels(results[index])
			}
			return out, nil
		},
	}
}
