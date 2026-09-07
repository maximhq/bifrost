import { formatFullTimestamp, formatTimestamp } from "@/app/workspace/dashboard/utils/chartUtils";
import { Card } from "@/components/ui/card";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { CostHistogramResponse, LatencyHistogramResponse, LogsHistogramResponse, LogStatsResponse } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { COMPACT_NUMBER_FORMAT, formatCurrencyNumber } from "@/lib/utils/numbers";
import NumberFlow from "@number-flow/react";
import { useMemo, useState } from "react";
import { Line, LineChart, XAxis, YAxis } from "recharts";
import {
	buildSparkPoints,
	type ChangeTone,
	formatMs,
	formatPctChange,
	formatPointDelta,
	metricsState,
	type MetricsState,
	type SeriesPoint,
	SPARK_INSET,
	sparkIndexAt,
	type SparkPoint,
	tokenSplit,
	weightedP95,
	windowTrend,
} from "./metricStrip.utils";

// Palette is taken verbatim from the approved design direction. Each entry pairs
// the mock's own value with a lighter counterpart of the same hue for dark mode,
// since the mock only specifies a light surface.
const positive = "text-[#16794f] dark:text-[#3fbd85]";
const negative = "text-[#c0392b] dark:text-[#e8705f]";
const warning = "text-[#d98324] dark:text-[#eaa14f]";
// The neutrals come from the theme tokens rather than the mock's own greys: the
// strip sits inside a `bg-card` panel, and a hard-coded warm near-black surface
// read as a foreign block against the app's cooler card colour in dark mode.
const muted = "text-muted-foreground";
const label = "text-muted-foreground";
const value = "text-foreground";
const track = "bg-muted";
const divider = "bg-border";
const surface = "bg-card";
const fill = "bg-[#16794f] dark:bg-[#3fbd85]";
const tokensIn = "bg-[#4c6fdc] dark:bg-[#7f9bf0]";
const tokensOut = "bg-[#a8bcf2] dark:bg-[#c3d2f7]";

// The palette stays here rather than in metricStrip.utils so the formatters hold
// no Tailwind classes and can be tested on their own.
const toneClass: Record<ChangeTone, string> = { positive, negative, muted };

// The sparkline is decorative shape, not a readable chart: it answers "which way
// is this going" beside a number that already gives the magnitude. Fixed 56x16 to
// match the design and to stay legible inside a 200px-wide segment.
const sparkWidth = 56;
const sparkHeight = 16;
// Keeps the stroke and the active dot off the edges of the 16px box. The side
// inset is shared with the hover maths so the two cannot drift apart.
const sparkMargin = { top: 3, right: SPARK_INSET, bottom: 3, left: SPARK_INSET };

/**
 * Labels the span a point covers. One bucket reads as a moment; an averaged
 * point reads as the range it was averaged over, so the number beside it is
 * never read as a single bucket's value.
 */
function formatSpan(point: SparkPoint, bucketSizeSeconds?: number): string {
	// Daily buckets carry no meaningful time of day, so they drop it.
	const daily = (bucketSizeSeconds ?? 0) >= 86400;
	const at = (timestamp: string) => (daily ? formatTimestamp(timestamp, bucketSizeSeconds ?? 0) : formatFullTimestamp(timestamp));
	return point.buckets === 1 ? at(point.timestamp) : `${at(point.timestamp)} - ${at(point.endTimestamp)}`;
}

function TooltipRow({ name, children, className }: { name: string; children: React.ReactNode; className?: string }) {
	return (
		<div className="flex items-center justify-between gap-6">
			<span className="text-muted-foreground">{name}</span>
			<span className={cn("font-mono", className)}>{children}</span>
		</div>
	);
}

function TooltipBody({ heading, children }: { heading: string; children: React.ReactNode }) {
	return (
		<div className="space-y-1">
			<div className="text-muted-foreground text-[11px]">{heading}</div>
			<div className="space-y-0.5">{children}</div>
		</div>
	);
}

/**
 * The hover target for a shape in the footer row. The shapes themselves are 5px
 * to 16px tall, which is too thin to aim at, so the trigger is a full 16px row
 * that the shape sits inside - it takes no extra height in the footer.
 */
function ShapeTooltip({ children, content }: { children: React.ReactNode; content: React.ReactNode }) {
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<div className="flex h-4 w-14 shrink-0 cursor-default items-center">{children}</div>
			</TooltipTrigger>
			<TooltipContent side="top" className="px-2.5 py-2 text-xs">
				{content}
			</TooltipContent>
		</Tooltip>
	);
}

interface SparklineProps {
	points: SparkPoint[];
	className: string;
	bucketSizeSeconds?: number;
	/** Rows shown for the hovered point; the index addresses parallel series. */
	rows: (point: SparkPoint, index: number) => React.ReactNode;
}

/** Props recharts passes to a custom dot renderer, narrowed to what is used. */
interface SparkDotProps {
	cx?: number;
	cy?: number;
	index?: number;
}

/**
 * A recharts line at sparkline scale. Recharts owns the scales and the path, but
 * not the hover: its hit-testing never activates on a chart this short (the
 * tooltip position updates while `active` stays false), so the hovered point is
 * resolved from the pointer's own offset instead. That also keeps the readout out
 * of recharts' tooltip, which renders inside the chart wrapper and would be
 * clipped by the strip's rounded overflow-hidden surface - the app's Radix
 * tooltip portals out of the card instead.
 */
function Sparkline({ points, className, bucketSizeSeconds, rows }: SparklineProps) {
	const [active, setActive] = useState<number | null>(null);

	// A flat series has no range to scale against. Widening the domain by one
	// draws it down the middle instead of pinning every point to the top edge.
	const domain = useMemo<[number, number]>(() => {
		const values = points.map((point) => point.value);
		const min = Math.min(...values);
		const max = Math.max(...values);
		return min === max ? [min - 1, max + 1] : [min, max];
	}, [points]);

	if (points.length < 2) return <div className="h-4 w-14 shrink-0" />;

	const point = active !== null ? points[active] : undefined;

	// Entering counts as much as moving: a pointer that comes to rest on the line
	// without moving again would otherwise never resolve a point.
	const trackPointer = (event: React.MouseEvent<HTMLDivElement>) => {
		const { left, width } = event.currentTarget.getBoundingClientRect();
		setActive(sparkIndexAt(event.clientX - left, width, points.length));
	};

	return (
		<Tooltip open={point !== undefined}>
			<TooltipTrigger asChild>
				<div
					className={cn("h-4 w-14 shrink-0 cursor-crosshair", className)}
					onMouseEnter={trackPointer}
					onMouseMove={trackPointer}
					onMouseLeave={() => setActive(null)}
				>
					<LineChart width={sparkWidth} height={sparkHeight} data={points} margin={sparkMargin}>
						<XAxis dataKey="timestamp" hide />
						<YAxis hide domain={domain} />
						<Line
							dataKey="value"
							stroke="currentColor"
							strokeWidth={1.5}
							strokeLinejoin="round"
							isAnimationActive={false}
							// The dot marks the hovered point. Every other point renders a
							// zero-radius circle so recharts keeps its keyed children stable.
							dot={({ cx, cy, index }: SparkDotProps) => <circle cx={cx} cy={cy} r={index === active ? 2 : 0} fill="currentColor" />}
						/>
					</LineChart>
				</div>
			</TooltipTrigger>
			<TooltipContent side="top" className="px-2.5 py-2 text-xs">
				{point && <TooltipBody heading={formatSpan(point, bucketSizeSeconds)}>{rows(point, active ?? 0)}</TooltipBody>}
			</TooltipContent>
		</Tooltip>
	);
}

/** Marks a value the sparkline averaged, so it is not read as an exact total. */
function averaged(point: SparkPoint, text: string): string {
	return point.buckets > 1 ? `avg ${text}` : text;
}

/** A single-value meter, used for the two success rates. */
function RatioBar({ percent, tooltip }: { percent: number; tooltip: React.ReactNode }) {
	return (
		<ShapeTooltip content={tooltip}>
			<div className={cn("h-[5px] w-14 overflow-hidden rounded-[3px]", track)}>
				<div className={cn("h-full", fill)} style={{ width: `${Math.min(100, Math.max(0, percent))}%` }} />
			</div>
		</ShapeTooltip>
	);
}

/** Input/output token split, sized by share rather than absolute magnitude. */
function StackedBar({ first, second, tooltip }: { first: number; second: number; tooltip: React.ReactNode }) {
	// Null means there were no tokens to split, which renders as the bare track -
	// the same empty shape RatioBar shows when its fill collapses to nothing.
	const split = tokenSplit(first, second);
	return (
		<ShapeTooltip content={tooltip}>
			<div className={cn("flex h-[5px] w-14 overflow-hidden rounded-[3px]", track)}>
				{split && (
					<>
						<div className={tokensIn} style={{ width: `${split.first}%` }} />
						<div className={tokensOut} style={{ width: `${split.second}%` }} />
					</>
				)}
			</div>
		</ShapeTooltip>
	);
}

// What stands in for a figure that is not there. Both are deliberately not "0":
// a rendered zero is indistinguishable from a real zero-traffic window, which is
// the whole reason the state is threaded down here.
const placeholder: Record<Exclude<MetricsState, "ready">, string> = { pending: "–", unavailable: "N/A" };

function Segment({
	title,
	children,
	footer,
	state = "ready",
}: {
	title: string;
	children: React.ReactNode;
	footer: React.ReactNode;
	state?: MetricsState;
}) {
	// The footer is derived from the same figures as the value - sparklines,
	// meters and change-vs-previous chips all read the stats that are missing -
	// so it goes with them rather than rendering a flat line beside an "N/A".
	const ready = state === "ready";
	return (
		<div className={cn("flex flex-col gap-2 px-[18px] py-4", surface)}>
			<div className={cn("truncate text-[11.5px] tracking-[0.06em] uppercase", label)}>{title}</div>
			{/* NumberFlow reserves 0.25em above and below its digits for the mask that
			    fades a rolling number in and out, which left 16px of dead space in a
			    row whose line-height is already the type size. A shorter mask still
			    fades the roll and gives the segment back that height.

			    The row is pinned to 1.5em - tall enough for the shortened mask and the
			    trailing unit - so the placeholder dash occupies exactly the height the
			    rendered figure will, and the strip does not resize when data lands. It
			    stays a block (not a flex row) so `truncate` keeps working. */}
			<div
				className={cn(
					"h-[1.5em] truncate font-mono text-xl leading-[1.5em] font-medium tracking-[-0.02em] sm:text-2xl",
					"[--number-flow-mask-height:0.15em]",
					ready ? value : muted,
				)}
				title={state === "unavailable" ? "These statistics could not be loaded" : undefined}
			>
				{ready ? children : placeholder[state]}
			</div>
			{/* min-w-0 lets the trailing figure shrink rather than push past the
			    segment's padding and collide with the divider. */}
			{/* Fixed height so the strip does not grow when the footer shapes arrive -
			    the placeholder state renders nothing here but must reserve the same
			    row the sparklines and meters occupy. */}
			<div className="flex h-4 min-w-0 items-center gap-2">{ready ? footer : null}</div>
		</div>
	);
}

/** The small unit that trails a value, e.g. the % in "68.27%". */
function Unit({ children }: { children: React.ReactNode }) {
	return <span className={cn("text-base", label)}>{children}</span>;
}

function Trailing({ children, className }: { children: React.ReactNode; className: string }) {
	return <span className={cn("min-w-0 truncate font-mono text-xs", className)}>{children}</span>;
}

/**
 * Splits a total by a rate into the two counts the meter is drawing. The rate and
 * the total are what the segment already shows, so the breakdown can never
 * contradict the number above it the way a separately queried count could.
 */
function splitByRate(total: number, rate: number): { passed: number; failed: number } {
	const passed = Math.round((total * rate) / 100);
	return { passed, failed: Math.max(0, total - passed) };
}

// The footer reads as a proportion at a glance, so one decimal is enough there
// and it buys back the width that pushed "129.22M / 8.29M" into the divider. The
// exact figures are one hover away.
const FOOTER_NUMBER_FORMAT = { ...COMPACT_NUMBER_FORMAT, maximumFractionDigits: 1 } as const;

/** Exact figures for the readouts, where the strip itself shows compact ones. */
const formatCount = (n: number) => Math.round(n).toLocaleString();

interface MetricStripProps {
	stats?: LogStatsResponse;
	requestHistogram?: LogsHistogramResponse;
	latencyHistogram?: LatencyHistogramResponse;
	costHistogram?: CostHistogramResponse;
	loading?: boolean;
	/** The /logs/stats failure, if it failed. Kept as unknown so the strip does not
	 * depend on RTK Query's error union just to know that there was one. */
	error?: unknown;
}

/**
 * The summary row above the logs table: one surface split by hairline dividers,
 * with every metric carrying a shape and a change-vs-previous-period so a glance
 * answers "is this good?" and not only "what is it?".
 *
 * Every hover readout is derived from the stats and histograms this row already
 * receives, so the interactivity costs no extra request.
 */
export function MetricStrip({ stats, requestHistogram, latencyHistogram, costHistogram, loading, error }: MetricStripProps) {
	// Every readout below falls back to 0 when stats is undefined, so without this
	// a failed request renders as a genuinely empty window.
	const state = metricsState(stats, loading ?? false, error);
	const requestPoints = useMemo(
		() => buildSparkPoints((requestHistogram?.buckets ?? []).map<SeriesPoint>((b) => ({ timestamp: b.timestamp, value: b.count }))),
		[requestHistogram],
	);
	// Errors ride along on the same chunking so the requests readout can say how
	// many of the hovered span's requests failed.
	const errorPoints = useMemo(
		() =>
			buildSparkPoints(
				(requestHistogram?.buckets ?? []).map<SeriesPoint>((b) => ({ timestamp: b.timestamp, value: b.error + b.cancelled })),
			),
		[requestHistogram],
	);
	const latencyPoints = useMemo(
		() => buildSparkPoints((latencyHistogram?.buckets ?? []).map<SeriesPoint>((b) => ({ timestamp: b.timestamp, value: b.avg_latency }))),
		[latencyHistogram],
	);
	const latencyP95Points = useMemo(
		() => buildSparkPoints((latencyHistogram?.buckets ?? []).map<SeriesPoint>((b) => ({ timestamp: b.timestamp, value: b.p95_latency }))),
		[latencyHistogram],
	);
	const costPoints = useMemo(
		() => buildSparkPoints((costHistogram?.buckets ?? []).map<SeriesPoint>((b) => ({ timestamp: b.timestamp, value: b.total_cost }))),
		[costHistogram],
	);

	// A request-weighted mean of the per-bucket p95s, not a true whole-window
	// percentile - see weightedP95 for why, and note the "~" it is rendered with.
	const p95 = useMemo(() => weightedP95(latencyHistogram?.buckets ?? []), [latencyHistogram]);

	const previous = stats?.has_previous_period ? stats.previous : undefined;
	const totalRequests = stats?.total_requests ?? 0;
	const totalCost = stats?.total_cost ?? 0;
	const costPerRequest = totalRequests > 0 ? totalCost / totalRequests : 0;
	const successRate = stats?.success_rate ?? 0;
	const userSuccessRate = stats?.user_facing_success_rate ?? 0;
	const userRequests = stats?.user_facing_total_requests ?? 0;
	const promptTokens = stats?.prompt_tokens ?? 0;
	const completionTokens = stats?.completion_tokens ?? 0;
	const tokenTotal = promptTokens + completionTokens;

	const success = splitByRate(totalRequests, successRate);
	const userSuccess = splitByRate(userRequests, userSuccessRate);

	const requestsChange = previous ? formatPctChange(totalRequests, previous.total_requests) : undefined;
	// A period-over-period percentage is the better answer when there is one, but
	// it is empty whenever the previous window held no requests. The window's own
	// direction fills that slot so the segment always says which way it is going.
	const requestsSignal = requestsChange?.text ? requestsChange : windowTrend(requestPoints.map((point) => point.value));
	const successChange = previous ? formatPointDelta(successRate, previous.success_rate) : undefined;
	const userSuccessChange = previous ? formatPointDelta(userSuccessRate, previous.user_facing_success_rate) : undefined;
	// Cost is the one lower-is-better metric in the strip: the same "+25.0%" that
	// is good news on requests is a regression here.
	const costChange = previous ? formatPctChange(totalCost, previous.total_cost, "lower-is-better") : undefined;

	return (
		<Card
			className={cn(
				"shrink-0 overflow-hidden rounded-sm py-0 shadow-none",
				// gap-px over a divider-coloured surface renders the hairlines, so they
				// stay correct at every breakpoint instead of relying on nth-child math.
				"grid grid-cols-2 gap-px md:grid-cols-3 lg:grid-cols-6",
				divider,
				"transition-opacity duration-200",
				loading ? "opacity-50" : "opacity-100",
			)}
			data-testid="logs-metric-strip"
		>
			<Segment
				state={state}
				title="Total Requests"
				footer={
					<>
						<Sparkline
							points={requestPoints}
							bucketSizeSeconds={requestHistogram?.bucket_size_seconds}
							className={toneClass[requestsSignal.tone]}
							rows={(point, index) => (
								<>
									<TooltipRow name="Requests">{averaged(point, formatCount(point.value))}</TooltipRow>
									{errorPoints[index] && errorPoints[index].value > 0 && (
										<TooltipRow name="Failed" className={negative}>
											{averaged(point, formatCount(errorPoints[index].value))}
										</TooltipRow>
									)}
								</>
							)}
						/>
						{requestsSignal.text && <Trailing className={toneClass[requestsSignal.tone]}>{requestsSignal.text}</Trailing>}
					</>
				}
			>
				<NumberFlow value={totalRequests} format={COMPACT_NUMBER_FORMAT} />
			</Segment>

			<Segment
				state={state}
				title="Success Rate"
				footer={
					<>
						<RatioBar
							percent={successRate}
							tooltip={
								<TooltipBody heading="Of all requests in this window">
									<TooltipRow name="Succeeded" className={positive}>
										{formatCount(success.passed)}
									</TooltipRow>
									<TooltipRow name="Failed" className={negative}>
										{formatCount(success.failed)}
									</TooltipRow>
									<TooltipRow name="Total">{formatCount(totalRequests)}</TooltipRow>
									{previous && <TooltipRow name="Previous period">{`${previous.success_rate.toFixed(2)}%`}</TooltipRow>}
								</TooltipBody>
							}
						/>
						{successChange && <Trailing className={toneClass[successChange.tone]}>{successChange.text}</Trailing>}
					</>
				}
			>
				<NumberFlow value={successRate} format={{ minimumFractionDigits: 2, maximumFractionDigits: 2 }} />
				<Unit>%</Unit>
			</Segment>

			<Segment
				state={state}
				title="User Success"
				footer={
					<>
						<RatioBar
							percent={userSuccessRate}
							tooltip={
								<TooltipBody heading="Of user-facing requests only">
									<TooltipRow name="Succeeded" className={positive}>
										{formatCount(userSuccess.passed)}
									</TooltipRow>
									<TooltipRow name="Failed" className={negative}>
										{formatCount(userSuccess.failed)}
									</TooltipRow>
									<TooltipRow name="Total">{formatCount(userRequests)}</TooltipRow>
									{previous && <TooltipRow name="Previous period">{`${previous.user_facing_success_rate.toFixed(2)}%`}</TooltipRow>}
								</TooltipBody>
							}
						/>
						{userSuccessChange && <Trailing className={toneClass[userSuccessChange.tone]}>{userSuccessChange.text}</Trailing>}
					</>
				}
			>
				<NumberFlow value={userSuccessRate} format={{ minimumFractionDigits: 2, maximumFractionDigits: 2 }} />
				<Unit>%</Unit>
			</Segment>

			<Segment
				state={state}
				title="Avg Latency"
				footer={
					<>
						<Sparkline
							points={latencyPoints}
							bucketSizeSeconds={latencyHistogram?.bucket_size_seconds}
							className={warning}
							rows={(point, index) => (
								<>
									<TooltipRow name="Average">{averaged(point, formatMs(point.value))}</TooltipRow>
									{latencyP95Points[index] && (
										<TooltipRow name="p95">{averaged(point, formatMs(latencyP95Points[index].value))}</TooltipRow>
									)}
								</>
							)}
						/>
						{/* "~" because this is a weighted mean of bucket p95s, not the
						    window's own 95th percentile. */}
						{p95 > 0 && <Trailing className={warning}>p95 ~{formatMs(p95)}</Trailing>}
					</>
				}
			>
				<NumberFlow value={Math.round(stats?.average_latency ?? 0)} />
				<Unit>ms</Unit>
			</Segment>

			<Segment
				state={state}
				title="Total Tokens"
				footer={
					<>
						<StackedBar
							first={promptTokens}
							second={completionTokens}
							tooltip={
								<TooltipBody heading="Token split">
									<TooltipRow name="Input" className="text-[#4c6fdc] dark:text-[#7f9bf0]">
										{`${formatCount(promptTokens)}${tokenTotal > 0 ? ` (${((promptTokens / tokenTotal) * 100).toFixed(1)}%)` : ""}`}
									</TooltipRow>
									<TooltipRow name="Output" className="text-[#7f93cc] dark:text-[#c3d2f7]">
										{`${formatCount(completionTokens)}${tokenTotal > 0 ? ` (${((completionTokens / tokenTotal) * 100).toFixed(1)}%)` : ""}`}
									</TooltipRow>
									<TooltipRow name="Total">{formatCount(stats?.total_tokens ?? 0)}</TooltipRow>
								</TooltipBody>
							}
						/>
						<Trailing className={muted}>
							<NumberFlow value={promptTokens} format={FOOTER_NUMBER_FORMAT} />
							{/* No spaces around the slash: it reads the same in mono figures and
							    keeps both halves of the split visible one breakpoint lower. */}
							{"/"}
							<NumberFlow value={completionTokens} format={FOOTER_NUMBER_FORMAT} />
						</Trailing>
					</>
				}
			>
				<NumberFlow value={stats?.total_tokens ?? 0} format={COMPACT_NUMBER_FORMAT} />
			</Segment>

			<Segment
				state={state}
				title="Total Cost"
				footer={
					<>
						<Sparkline
							points={costPoints}
							bucketSizeSeconds={costHistogram?.bucket_size_seconds}
							className={costChange ? toneClass[costChange.tone] : muted}
							rows={(point) => <TooltipRow name="Cost">{averaged(point, formatCurrencyNumber(point.value))}</TooltipRow>}
						/>
						{costChange?.text ? (
							<Trailing className={toneClass[costChange.tone]}>{costChange.text}</Trailing>
						) : (
							costPerRequest > 0 && <Trailing className={muted}>{formatCurrencyNumber(costPerRequest)}/req</Trailing>
						)}
					</>
				}
			>
				<NumberFlow value={totalCost} format={{ ...COMPACT_NUMBER_FORMAT, style: "currency", currency: "USD" }} />
			</Segment>
		</Card>
	);
}