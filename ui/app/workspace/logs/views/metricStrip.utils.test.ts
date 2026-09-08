import { describe, expect, it } from "vitest";
import { buildSparkPoints, formatMs, formatPctChange, formatPointDelta, metricsState, SPARK_POINTS, sparkIndexAt, tokenSplit, weightedP95, windowTrend } from "./metricStrip.utils";

describe("formatPctChange", () => {
	it("reads a rise as positive by default", () => {
		expect(formatPctChange(1084, 1000)).toEqual({ text: "↗ 8.4%", tone: "positive" });
	});

	it("reads a fall as negative by default", () => {
		expect(formatPctChange(875, 1000)).toEqual({ text: "↘ 12.5%", tone: "negative" });
	});

	it("has nothing to say when the previous period was empty", () => {
		expect(formatPctChange(1200, 0)).toEqual({ text: "", tone: "muted" });
	});

	it("reads a change too small to round as neutral noise", () => {
		expect(formatPctChange(1000.0004, 1000)).toEqual({ text: "0.0%", tone: "muted" });
	});

	// Cost is the metric this exists for: spending more is not good news, even
	// though the number went up.
	it("inverts the tone for a lower-is-better metric without changing the text", () => {
		expect(formatPctChange(12.5, 10, "lower-is-better")).toEqual({ text: "↗ 25.0%", tone: "negative" });
		expect(formatPctChange(7.5, 10, "lower-is-better")).toEqual({ text: "↘ 25.0%", tone: "positive" });
	});

	it("leaves noise neutral for a lower-is-better metric too", () => {
		expect(formatPctChange(10.0000004, 10, "lower-is-better")).toEqual({ text: "0.0%", tone: "muted" });
	});
});

describe("formatPointDelta", () => {
	it("reports a rate move in percentage points", () => {
		expect(formatPointDelta(99.2, 98)).toEqual({ text: "↗ 1.2 pt", tone: "positive" });
		expect(formatPointDelta(98, 99.2)).toEqual({ text: "↘ 1.2 pt", tone: "negative" });
	});

	it("reads a sub-tenth-point move as neutral noise", () => {
		expect(formatPointDelta(99, 99.02)).toEqual({ text: "0.0 pt", tone: "muted" });
	});

	it("inverts the tone for a lower-is-better metric", () => {
		expect(formatPointDelta(2.5, 1.5, "lower-is-better")).toEqual({ text: "↗ 1.0 pt", tone: "negative" });
	});
});

describe("formatMs", () => {
	it("keeps sub-second durations in whole milliseconds", () => {
		expect(formatMs(842.4)).toBe("842ms");
	});

	it("switches to seconds at one thousand", () => {
		expect(formatMs(1000)).toBe("1.0s");
		expect(formatMs(1500)).toBe("1.5s");
	});
});
describe("buildSparkPoints", () => {
	const bucket = (hour: number, value: number) => ({ timestamp: `2026-08-28T${String(hour).padStart(2, "0")}:00:00Z`, value });

	it("plots a short series bucket for bucket, with no span to hedge about", () => {
		expect(buildSparkPoints([bucket(0, 10), bucket(1, 20)])).toEqual([
			{ timestamp: "2026-08-28T00:00:00Z", endTimestamp: "2026-08-28T00:00:00Z", buckets: 1, value: 10 },
			{ timestamp: "2026-08-28T01:00:00Z", endTimestamp: "2026-08-28T01:00:00Z", buckets: 1, value: 20 },
		]);
	});

	it("averages a long series down to SPARK_POINTS points", () => {
		const series = Array.from({ length: 48 }, (_, i) => bucket(i % 24, i));
		const points = buildSparkPoints(series);
		expect(points).toHaveLength(SPARK_POINTS);
		// 48 buckets over 16 points is an even three per point: 0,1,2 averages 1.
		expect(points[0].value).toBe(1);
		expect(points[0].buckets).toBe(3);
	});

	it("carries the span each averaged point covers", () => {
		const series = Array.from({ length: 32 }, (_, i) => bucket(i % 24, 1));
		const [first] = buildSparkPoints(series);
		expect(first.buckets).toBe(2);
		expect(first.timestamp).toBe(series[0].timestamp);
		expect(first.endTimestamp).toBe(series[1].timestamp);
	});

	// Every bucket has to land in exactly one point: dropping the tail would make
	// the line disagree with the total shown above it.
	it("covers the whole series, first bucket to last", () => {
		const series = Array.from({ length: 100 }, (_, i) => bucket(i % 24, i));
		const points = buildSparkPoints(series);
		expect(points[0].timestamp).toBe(series[0].timestamp);
		expect(points[points.length - 1].endTimestamp).toBe(series[99].timestamp);
		expect(points.reduce((sum, p) => sum + p.buckets, 0)).toBe(100);
	});
});
describe("sparkIndexAt", () => {
	// A 56px sparkline with a 2px inset each side: 52px of plot for the points,
	// first point on the left edge of the plot, last on the right edge.
	const width = 56;

	it("picks the point nearest the pointer", () => {
		expect(sparkIndexAt(2, width, 5)).toBe(0);
		expect(sparkIndexAt(28, width, 5)).toBe(2);
		expect(sparkIndexAt(54, width, 5)).toBe(4);
	});

	it("snaps to the nearer of two points rather than the one before", () => {
		// Five points sit 13px apart: 15px is nearest the second, 20px the third.
		expect(sparkIndexAt(15, width, 5)).toBe(1);
		expect(sparkIndexAt(20, width, 5)).toBe(1);
		expect(sparkIndexAt(23, width, 5)).toBe(2);
	});

	// The pointer reaches the 2px inset at either end, where no point is drawn.
	it("clamps inside the series at both edges", () => {
		expect(sparkIndexAt(0, width, 5)).toBe(0);
		expect(sparkIndexAt(-30, width, 5)).toBe(0);
		expect(sparkIndexAt(56, width, 5)).toBe(4);
		expect(sparkIndexAt(999, width, 5)).toBe(4);
	});

	it("has only one point to pick from a single-point series", () => {
		expect(sparkIndexAt(40, width, 1)).toBe(0);
	});
});
describe("windowTrend", () => {
	it("reads a series that ends higher than it started as rising", () => {
		expect(windowTrend([10, 12, 40, 44])).toEqual({ text: "↗ rising", tone: "positive" });
	});

	it("reads a series that ends lower as falling", () => {
		expect(windowTrend([44, 40, 12, 10])).toEqual({ text: "↘ falling", tone: "negative" });
	});

	// A metric wandering inside a few percent has no direction worth colouring.
	it("reads a small drift as steady", () => {
		expect(windowTrend([100, 101, 102, 103])).toEqual({ text: "→ steady", tone: "muted" });
	});

	// The window opening at zero is the case this exists for: the period-over-period
	// percentage is undefined there, but the direction still is not.
	it("still reads a direction when the series starts at zero", () => {
		expect(windowTrend([0, 0, 30, 50])).toEqual({ text: "↗ rising", tone: "positive" });
		expect(windowTrend([0, 0, 0, 0])).toEqual({ text: "→ steady", tone: "muted" });
	});

	it("has nothing to say about a series too short to have a direction", () => {
		expect(windowTrend([5])).toEqual({ text: "", tone: "muted" });
		expect(windowTrend([])).toEqual({ text: "", tone: "muted" });
	});

	// An odd count drops the middle point rather than letting it weigh both halves.
	it("compares equal halves of an odd-length series", () => {
		expect(windowTrend([10, 10, 999, 40, 40])).toEqual({ text: "↗ rising", tone: "positive" });
	});
});

describe("weightedP95", () => {
	const bucket = (p95: number, requests: number) => ({ p95_latency: p95, total_requests: requests });

	it("weights each bucket's p95 by the requests behind it", () => {
		expect(weightedP95([bucket(100, 1), bucket(200, 3)])).toBe(175);
	});

	it("has nothing to report for a window with no requests", () => {
		expect(weightedP95([])).toBe(0);
		expect(weightedP95([bucket(500, 0), bucket(900, 0)])).toBe(0);
	});

	// This is the whole reason the strip labels the number approximate rather than
	// as the window's p95: a mean of tail values is not itself a tail value, and a
	// quiet bucket with a slow tail is averaged away by the busy buckets around it.
	it("is a mean, so it lands far below the window's real tail", () => {
		const spikyTail = [bucket(10_000, 1), bucket(10, 99)];
		expect(weightedP95(spikyTail)).toBeCloseTo(109.9, 1);
		// A true p95 over 100 requests would sit at the slow end, not near 110ms.
		expect(weightedP95(spikyTail)).toBeLessThan(1000);
	});
});

describe("metricsState", () => {
	const stats = { total_requests: 1200, total_cost: 4.5 };

	it("reads a loaded response as ready", () => {
		expect(metricsState(stats, false, undefined)).toBe("ready");
	});

	// The bug this exists for: a failed /logs/stats leaves stats undefined, and
	// every readout falls back to 0, so the strip reports zero requests and zero
	// cost as though the window were genuinely empty.
	it("reads a failure with nothing to show as unavailable", () => {
		expect(metricsState(undefined, false, { status: 500 })).toBe("unavailable");
	});

	it("reads a first load still in flight as pending, not unavailable", () => {
		expect(metricsState(undefined, true, undefined)).toBe("pending");
	});

	it("reads an unstarted query as pending rather than a failure", () => {
		expect(metricsState(undefined, false, undefined)).toBe("pending");
	});

	// The queries poll and RTK Query keeps the last good data, so a transient
	// poll failure still has real figures behind it.
	it("keeps showing retained figures when a later poll fails", () => {
		expect(metricsState(stats, false, { status: 500 })).toBe("ready");
	});

	// A genuinely empty window must not be mistaken for a failure: these are the
	// real zeros the fallback was hiding.
	it("reads a real zero-traffic window as ready", () => {
		expect(metricsState({ total_requests: 0, total_cost: 0 }, false, undefined)).toBe("ready");
	});
});

describe("tokenSplit", () => {
	it("splits a mixed window by share", () => {
		expect(tokenSplit(750, 250)).toEqual({ first: 75, second: 25 });
	});

	// The bug this exists for: with no tokens the first share is 0, so a bar that
	// derives its second width as 100 - first draws a full output-coloured bar
	// for a window that has no tokens at all.
	it("has nothing to split when the window held no tokens", () => {
		expect(tokenSplit(0, 0)).toBeNull();
	});

	it("gives the whole bar to input when there is no output", () => {
		expect(tokenSplit(400, 0)).toEqual({ first: 100, second: 0 });
	});

	it("gives the whole bar to output when there is no input", () => {
		expect(tokenSplit(0, 400)).toEqual({ first: 0, second: 100 });
	});
});
