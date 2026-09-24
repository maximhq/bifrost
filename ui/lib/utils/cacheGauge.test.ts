import { describe, expect, test } from "vitest";
import type { LogStats } from "@/lib/types/logs";
import { clampPercentage, resolveLocalCacheGauge } from "./cacheGauge";

function stats(overrides: Partial<LogStats> = {}): LogStats {
	return {
		total_requests: 0,
		success_rate: 100,
		user_facing_success_rate: 100,
		user_facing_total_requests: 0,
		average_latency: 0,
		total_tokens: 0,
		prompt_tokens: 0,
		completion_tokens: 0,
		total_cost: 0,
		...overrides,
	};
}

describe("resolveLocalCacheGauge", () => {
	test("reports no-data when there is no stats payload", () => {
		expect(resolveLocalCacheGauge(null).state).toBe("no-data");
		expect(resolveLocalCacheGauge(undefined).state).toBe("no-data");
	});

	test("reports no-data when the window holds no requests", () => {
		const result = resolveLocalCacheGauge(stats({ total_requests: 0, direct_cache_hits: 0, semantic_cache_hits: 0 }));
		expect(result.state).toBe("no-data");
		expect(result.percentage).toBe(0);
	});

	// The regression this guards: a full window whose counters are omitted because
	// no request ever reached the cache. Previously indistinguishable from no-data.
	test("reports not-engaged when requests exist but the counters are omitted", () => {
		const result = resolveLocalCacheGauge(stats({ total_requests: 5170, cache_hit_rate_total_requests: 5170 }));
		expect(result.state).toBe("not-engaged");
		expect(result.totalRequests).toBe(5170);
		expect(result.percentage).toBe(0);
	});

	test("reports not-engaged when either counter alone is missing", () => {
		expect(resolveLocalCacheGauge(stats({ total_requests: 10, direct_cache_hits: 3 })).state).toBe("not-engaged");
		expect(resolveLocalCacheGauge(stats({ total_requests: 10, semantic_cache_hits: 3 })).state).toBe("not-engaged");
	});

	test("reports not-engaged when the counters are explicitly null", () => {
		const result = resolveLocalCacheGauge(stats({ total_requests: 10, direct_cache_hits: null, semantic_cache_hits: null }));
		expect(result.state).toBe("not-engaged");
	});

	// Distinct from not-engaged: the cache ran and simply did not hit.
	test("reports ready at 0% when the counters are present and zero", () => {
		const result = resolveLocalCacheGauge(stats({ total_requests: 200, direct_cache_hits: 0, semantic_cache_hits: 0 }));
		expect(result.state).toBe("ready");
		expect(result.percentage).toBe(0);
	});

	test("combines direct and semantic hits into the percentage", () => {
		const result = resolveLocalCacheGauge(stats({ total_requests: 200, direct_cache_hits: 30, semantic_cache_hits: 10 }));
		expect(result.state).toBe("ready");
		expect(result.percentage).toBe(20);
		expect(result.directHits).toBe(30);
		expect(result.semanticHits).toBe(10);
	});

	test("prefers cache_hit_rate_total_requests over total_requests as the denominator", () => {
		const result = resolveLocalCacheGauge(
			stats({ total_requests: 999, cache_hit_rate_total_requests: 100, direct_cache_hits: 25, semantic_cache_hits: 0 }),
		);
		expect(result.totalRequests).toBe(100);
		expect(result.percentage).toBe(25);
	});

	test("clamps a percentage that would exceed 100", () => {
		const result = resolveLocalCacheGauge(stats({ total_requests: 10, direct_cache_hits: 40, semantic_cache_hits: 0 }));
		expect(result.percentage).toBe(100);
	});
});

describe("clampPercentage", () => {
	test("passes through values inside the range", () => {
		expect(clampPercentage(42.5)).toBe(42.5);
	});

	test("clamps outside the range", () => {
		expect(clampPercentage(-5)).toBe(0);
		expect(clampPercentage(150)).toBe(100);
	});

	test("honours a custom maximum", () => {
		expect(clampPercentage(80, 60)).toBe(60);
	});

	test("treats non-finite input as zero", () => {
		expect(clampPercentage(Number.NaN)).toBe(0);
		expect(clampPercentage(Number.POSITIVE_INFINITY)).toBe(0);
	});
});