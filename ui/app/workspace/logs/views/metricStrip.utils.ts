/**
 * Change-vs-previous-period formatting for the logs metric strip.
 *
 * The tone is semantic rather than a Tailwind class so these stay pure and
 * testable: the strip maps the tone onto the design palette.
 */
export type ChangeTone = "positive" | "negative" | "muted";

/**
 * Which direction is good news for a metric. Requests and success rates read
 * better as they rise; cost reads better as it falls, so the same "+25.0%" is
 * green on one and red on the other.
 */
export type Polarity = "higher-is-better" | "lower-is-better";

export interface Change {
	text: string;
	tone: ChangeTone;
}

/**
 * A move smaller than this rounds to "0.0" in the text, so colouring it as an
 * improvement or a regression would contradict the number next to it.
 */
const noiseThreshold = 0.05;

function toneFor(rounded: number, polarity: Polarity): ChangeTone {
	if (rounded === 0) return "muted";
	const good = polarity === "lower-is-better" ? rounded < 0 : rounded > 0;
	return good ? "positive" : "negative";
}

// U+2197 and U+2198. The chips carry a direction of travel rather than an
// arithmetic sign: an arrow reads as "which way did this go" at a glance, where
// a leading + or − has to be read as a number first. A move too small to round
// gets neither - it did not travel anywhere worth drawing.
const up = "↗";
const down = "↘";

function sign(rounded: number): string {
	if (rounded > 0) return `${up} `;
	if (rounded < 0) return `${down} `;
	return "";
}

/** Formats a percentage-point delta between two rates, e.g. "−1.2 pt". */
export function formatPointDelta(current: number, previous: number, polarity: Polarity = "higher-is-better"): Change {
	const delta = current - previous;
	const rounded = Math.abs(delta) < noiseThreshold ? 0 : delta;
	return { text: `${sign(rounded)}${Math.abs(rounded).toFixed(1)} pt`, tone: toneFor(rounded, polarity) };
}

/** Formats a percentage change between two magnitudes, e.g. "+8.4%". */
export function formatPctChange(current: number, previous: number, polarity: Polarity = "higher-is-better"): Change {
	// A previous period of zero has no ratio to report: every rise from it is an
	// infinite increase, which says nothing useful.
	if (previous === 0) return { text: "", tone: "muted" };
	const pct = ((current - previous) / previous) * 100;
	const rounded = Math.abs(pct) < noiseThreshold ? 0 : pct;
	return { text: `${sign(rounded)}${Math.abs(rounded).toFixed(1)}%`, tone: toneFor(rounded, polarity) };
}

export function formatMs(ms: number): string {
	return ms >= 1000 ? `${(ms / 1000).toFixed(1)}s` : `${Math.round(ms)}ms`;
}

/** A histogram bucket reduced to the one number a sparkline plots. */
export interface SeriesPoint {
	timestamp: string;
	value: number;
}

/**
 * One plotted sparkline point. A point covers several buckets once the window
 * holds more of them than the line can show, so it carries the span it was
 * averaged over: the hover readout can then say "14:00 - 16:00, avg 1,204"
 * instead of claiming a single moment it does not have.
 */
export interface SparkPoint extends SeriesPoint {
	/** Timestamp of the last bucket in the span; equal to `timestamp` for one. */
	endTimestamp: string;
	/** How many buckets were averaged into this point. */
	buckets: number;
}

/** More points than this compress into noise at the sparkline's 56px width. */
export const SPARK_POINTS = 16;

/**
 * Downsamples a series to at most SPARK_POINTS points by averaging each chunk,
 * so a 30-day window and a 1-hour window produce a comparably dense line.
 *
 * Averaging rather than summing is deliberate: chunk sizes differ by one when
 * the series does not divide evenly, and a sum would turn that off-by-one into
 * a visible step in the line that says nothing about the data.
 */
export function buildSparkPoints(series: SeriesPoint[]): SparkPoint[] {
	if (series.length <= SPARK_POINTS) {
		return series.map((point) => ({ ...point, endTimestamp: point.timestamp, buckets: 1 }));
	}
	const chunk = series.length / SPARK_POINTS;
	return Array.from({ length: SPARK_POINTS }, (_, i) => {
		const start = Math.floor(i * chunk);
		const end = Math.max(start + 1, Math.floor((i + 1) * chunk));
		const slice = series.slice(start, end);
		return {
			timestamp: slice[0].timestamp,
			endTimestamp: slice[slice.length - 1].timestamp,
			buckets: slice.length,
			value: slice.reduce((sum, point) => sum + point.value, 0) / slice.length,
		};
	});
}
/**
 * Horizontal inset the sparkline reserves on each side, matching the chart's own
 * margin so the pointer maths and the drawn path agree on where a point sits.
 */
export const SPARK_INSET = 2;

/**
 * The index of the point nearest a pointer `x` pixels into a `width`-wide
 * sparkline. The chart draws its first point on the left edge of the plot and
 * its last on the right edge, so the points sit one step apart across the inset
 * span and the nearest one is a rounded division.
 *
 * The strip resolves its own hover this way rather than reading recharts' active
 * index: recharts does not activate a tooltip on a chart 16px tall.
 */
export function sparkIndexAt(x: number, width: number, count: number, inset = SPARK_INSET): number {
	if (count < 2) return 0;
	const step = (width - inset * 2) / (count - 1);
	const index = Math.round((x - inset) / step);
	return Math.min(count - 1, Math.max(0, index));
}
/** A move smaller than this across the window is drift, not a direction. */
const trendThreshold = 5;

const rising = `${up} rising`;
const falling = `${down} falling`;
const steady = "→ steady";

/**
 * The direction a series travels across its own window, comparing the mean of
 * its first half against the mean of its last half.
 *
 * This is the answer when there is no previous period to compare against - a
 * window whose previous period is empty has no meaningful percentage change (any
 * rise from zero is infinite), but it still visibly rises or falls, and a
 * segment that says nothing at all reads as though its metric has no trend.
 */
export function windowTrend(values: number[]): Change {
	if (values.length < 2) return { text: "", tone: "muted" };
	// An odd count drops its middle value rather than letting it weigh both ends.
	const half = Math.floor(values.length / 2);
	const mean = (slice: number[]) => slice.reduce((sum, n) => sum + n, 0) / slice.length;
	const first = mean(values.slice(0, half));
	const last = mean(values.slice(values.length - half));
	// A window that opens at zero has no ratio either, so fall back to whether it
	// ends anywhere above nothing.
	if (first === 0) return last > 0 ? { text: rising, tone: "positive" } : { text: steady, tone: "muted" };
	const pct = ((last - first) / first) * 100;
	if (Math.abs(pct) < trendThreshold) return { text: steady, tone: "muted" };
	return pct > 0 ? { text: rising, tone: "positive" } : { text: falling, tone: "negative" };
}

/** The two fields of a latency bucket the window aggregate reads. */
export interface LatencyBucketWeight {
	p95_latency: number;
	total_requests: number;
}

/**
 * Aggregates per-bucket p95 latencies into one number for the whole window, as a
 * request-weighted mean.
 *
 * This is an approximation, and the strip labels it as one. A mean of tail values
 * is not itself a tail value: a quiet bucket with a very slow tail is averaged
 * away by the busy buckets around it, so the result can sit far below the
 * window's true 95th percentile. An exact whole-window percentile would have to
 * be computed where the rows are, and the latency chart on the same page reads
 * the same per-bucket p95s - so this keeps the two consistent rather than showing
 * a figure that disagrees with the chart directly beneath it.
 */
export function weightedP95(buckets: LatencyBucketWeight[]): number {
	const requests = buckets.reduce((sum, b) => sum + b.total_requests, 0);
	if (requests === 0) return 0;
	return buckets.reduce((sum, b) => sum + b.p95_latency * b.total_requests, 0) / requests;
}

/**
 * What the strip's readouts should show for the current period.
 *
 * "ready" renders the figures, "pending" renders placeholders while the first
 * response is still in flight, and "unavailable" says the figures could not be
 * fetched. The distinction matters because every readout falls back to 0, and a
 * rendered 0 is indistinguishable from a real zero-traffic window.
 */
export type MetricsState = "ready" | "pending" | "unavailable";

/**
 * Decides which of those three the strip is in.
 *
 * Stats win over an error on purpose. The queries poll, and RTK Query keeps the
 * last successful `data` when a later poll fails, so a transient failure still
 * has real figures to show; blanking them would be a worse answer than showing
 * the ones that were true a moment ago. Only a failure with nothing behind it
 * reads as unavailable.
 */
export function metricsState(stats: unknown, loading: boolean, error: unknown): MetricsState {
	if (stats) return "ready";
	if (loading) return "pending";
	if (error) return "unavailable";
	// Nothing has resolved and nothing has failed, so the figures are not yet
	// known rather than not available.
	return "pending";
}

/** The two widths of the token bar, as percentages that sum to 100. */
export interface TokenSplit {
	first: number;
	second: number;
}

/**
 * Splits a token total into the bar's two shares, or null when there are no
 * tokens to split.
 *
 * Null rather than a pair of zeroes because the bar derives its second width by
 * subtraction: a zero first share makes the second one 100, so an empty window
 * would draw a full output-coloured bar that reads as "all output tokens". The
 * absent case has to be its own answer for the bar to be able to render nothing.
 */
export function tokenSplit(first: number, second: number): TokenSplit | null {
	const total = first + second;
	if (total <= 0) return null;
	const share = (first / total) * 100;
	return { first: share, second: 100 - share };
}
