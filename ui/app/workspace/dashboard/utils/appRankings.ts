import { mapUserAgentToApp } from "@/lib/constants/logs";
import type { DimensionRankingEntry, DimensionRankingsResponse } from "@/lib/types/logs";

// aggregateRankingsByApp collapses a user_agent-dimension rankings response (one
// row per raw, versioned User-Agent) into one row per client app, using the same
// UI-side UA->app mapping as the logs table and filters.
//
// Totals (requests/tokens/cost) sum cleanly. The per-row trend only carries
// percentage deltas (no absolute previous values), so a rolled-up trend is a
// request-weighted average of its children's request/token trends and a
// cost-weighted average of their cost trends - an approximation, but the
// standard way to combine percentage trends. has_previous_period is true when
// any child had a previous period.
export function aggregateRankingsByApp(data: DimensionRankingsResponse | null): DimensionRankingsResponse | null {
	if (!data) return null;

	type Acc = {
		name: string;
		total_requests: number;
		total_tokens: number;
		total_cost: number;
		requestsTrendWeighted: number;
		tokensTrendWeighted: number;
		costTrendWeighted: number;
		hasPrevious: boolean;
	};

	const byApp = new Map<string, Acc>();
	for (const entry of data.rankings) {
		const name = mapUserAgentToApp(entry.id).name;
		const acc = byApp.get(name) ?? {
			name,
			total_requests: 0,
			total_tokens: 0,
			total_cost: 0,
			requestsTrendWeighted: 0,
			tokensTrendWeighted: 0,
			costTrendWeighted: 0,
			hasPrevious: false,
		};
		acc.total_requests += entry.total_requests;
		acc.total_tokens += entry.total_tokens;
		acc.total_cost += entry.total_cost;
		acc.requestsTrendWeighted += entry.trend.requests_trend * entry.total_requests;
		acc.tokensTrendWeighted += entry.trend.tokens_trend * entry.total_tokens;
		acc.costTrendWeighted += entry.trend.cost_trend * entry.total_cost;
		acc.hasPrevious = acc.hasPrevious || entry.trend.has_previous_period;
		byApp.set(name, acc);
	}

	const rankings: DimensionRankingEntry[] = [...byApp.values()]
		.map((acc) => ({
			id: acc.name,
			name: acc.name,
			total_requests: acc.total_requests,
			total_tokens: acc.total_tokens,
			total_cost: acc.total_cost,
			trend: {
				has_previous_period: acc.hasPrevious,
				requests_trend: acc.total_requests > 0 ? acc.requestsTrendWeighted / acc.total_requests : 0,
				tokens_trend: acc.total_tokens > 0 ? acc.tokensTrendWeighted / acc.total_tokens : 0,
				cost_trend: acc.total_cost > 0 ? acc.costTrendWeighted / acc.total_cost : 0,
			},
		}))
		.sort((a, b) => b.total_requests - a.total_requests);

	return { dimension: data.dimension, rankings };
}