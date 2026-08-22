import type { LogStats } from "@/lib/types/logs";

/**
 * Rendering state for the local (semantic) cache gauge.
 *
 * - `no-data` - nothing to report: no stats payload, or no requests in the window.
 * - `not-engaged` - the window has requests, but none of them reached the cache,
 *   so the API omitted the hit counters entirely. This is distinct from a 0% hit
 *   rate: the cache was never consulted, rather than consulted and missed.
 * - `ready` - the API reported hit counters, so the gauge can be drawn.
 */
export type LocalCacheGaugeState = "no-data" | "not-engaged" | "ready";

export interface LocalCacheGauge {
	state: LocalCacheGaugeState;
	/** Combined hit rate, clamped to 0-100. Zero unless `state` is `ready`. */
	percentage: number;
	directHits: number;
	semanticHits: number;
	totalRequests: number;
}

/** Clamps a raw percentage into the 0-100 range the gauge can render. */
export function clampPercentage(value: number, max = 100): number {
	if (!Number.isFinite(value)) return 0;
	return Math.max(0, Math.min(max, value));
}

function toCount(value: number | null | undefined): number {
	return Number.isFinite(value) ? (value as number) : 0;
}

/**
 * Derives the gauge state from a logs stats payload.
 *
 * `direct_cache_hits` and `semantic_cache_hits` are omitted from the response
 * whenever no log row in the window carried cache debug info: the logstore
 * leaves them nil and `omitempty` drops the keys. Treating that as "no data"
 * hides a meaningful signal, because a window can be full of requests and still
 * report no counters - which means the cache is configured but never invoked
 * (requests carry no `x-bf-cache-key` and no `default_cache_key` is set).
 * That case is reported as `not-engaged` so the UI can say so explicitly
 * instead of showing an empty-state placeholder.
 */
export function resolveLocalCacheGauge(data: LogStats | null | undefined): LocalCacheGauge {
	const directHits = toCount(data?.direct_cache_hits);
	const semanticHits = toCount(data?.semantic_cache_hits);
	const totalRequests = toCount(data?.cache_hit_rate_total_requests ?? data?.total_requests);
	const base = { percentage: 0, directHits, semanticHits, totalRequests };

	if (!data || totalRequests <= 0) {
		return { ...base, state: "no-data" };
	}

	// Either counter being absent means the window carried no cache telemetry at
	// all; the logstore only populates them as a pair.
	if (data.direct_cache_hits == null || data.semantic_cache_hits == null) {
		return { ...base, state: "not-engaged" };
	}

	return {
		...base,
		state: "ready",
		percentage: clampPercentage(((directHits + semanticHits) / totalRequests) * 100),
	};
}