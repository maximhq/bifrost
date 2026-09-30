import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import type { KeyboardEvent, MouseEvent, ReactNode } from "react";

// The pieces of the summary row that sits above the logs table and the adaptive routing
// timeline: one surface split by hairline dividers, each segment a title, a figure and a
// footer that says which way the figure leans. The logs strip and the routing dashboard
// both build on these, so the two rows read as the same component.

/** Whether a segment has its figure yet. */
export type MetricSegmentState = "ready" | "pending" | "unavailable";

// What stands in for a figure that is not there. Both are deliberately not "0": a
// rendered zero is indistinguishable from a real zero-traffic window.
const placeholder: Record<Exclude<MetricSegmentState, "ready">, string> = { pending: "–", unavailable: "N/A" };

/**
 * The strip's surface. gap-px over a divider-coloured background renders the hairlines,
 * so they stay correct at every breakpoint instead of relying on nth-child math. The
 * caller sets the column count; the strip dims while any of its figures refetch.
 */
export function MetricStripCard({
	className,
	loading,
	children,
	testId,
}: {
	className?: string;
	loading?: boolean;
	children: ReactNode;
	testId?: string;
}) {
	return (
		<Card
			className={cn(
				"bg-border grid shrink-0 gap-px overflow-hidden rounded-sm py-0 shadow-none transition-opacity duration-200",
				loading ? "opacity-50" : "opacity-100",
				className,
			)}
			data-testid={testId}
		>
			{children}
		</Card>
	);
}

/**
 * One segment of the strip. A segment with onClick is a button onto a detail view; hover
 * callbacks let a caller show a readout for the whole segment.
 */
export function MetricSegment({
	title,
	children,
	footer,
	state = "ready",
	onClick,
	onMouseEnter,
	onMouseLeave,
	testId,
}: {
	title: string;
	children: ReactNode;
	footer: ReactNode;
	state?: MetricSegmentState;
	onClick?: () => void;
	onMouseEnter?: (event: MouseEvent<HTMLDivElement>) => void;
	onMouseLeave?: (event: MouseEvent<HTMLDivElement>) => void;
	testId?: string;
}) {
	// The footer is derived from the same figures as the value, so it goes with them
	// rather than rendering a flat line beside an "N/A".
	const ready = state === "ready";
	const interactive = onClick !== undefined;
	return (
		<div
			className={cn(
				"bg-card flex min-w-0 flex-col gap-2 px-[18px] py-4",
				interactive && "hover:bg-muted/50 focus-visible:ring-ring/50 cursor-pointer outline-none focus-visible:ring-[3px]",
			)}
			role={interactive ? "button" : undefined}
			tabIndex={interactive ? 0 : undefined}
			onClick={onClick}
			onKeyDown={
				interactive
					? (event: KeyboardEvent<HTMLDivElement>) => {
							if (event.key === "Enter" || event.key === " ") {
								event.preventDefault();
								onClick();
							}
						}
					: undefined
			}
			onMouseEnter={onMouseEnter}
			onMouseLeave={onMouseLeave}
			data-testid={testId}
		>
			<div className="text-muted-foreground truncate text-[11.5px] tracking-[0.06em] uppercase">{title}</div>
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
					ready ? "text-foreground" : "text-muted-foreground",
				)}
				title={state === "unavailable" ? "These statistics could not be loaded" : undefined}
			>
				{ready ? children : placeholder[state]}
			</div>
			{/* Fixed height so the strip does not grow when the footer shapes arrive; min-w-0
			    lets the trailing figure shrink rather than push past the segment's padding. */}
			<div className="flex h-4 min-w-0 items-center gap-2">{ready ? footer : null}</div>
		</div>
	);
}

/** The small unit that trails a value, e.g. the % in "68.27%". */
export function MetricUnit({ children }: { children: ReactNode }) {
	return <span className="text-muted-foreground text-base">{children}</span>;
}

/** The footer's figure, e.g. a change against the previous period. */
export function MetricTrailing({ children, className, testId }: { children: ReactNode; className: string; testId?: string }) {
	return (
		<span className={cn("min-w-0 truncate font-mono text-xs", className)} data-testid={testId}>
			{children}
		</span>
	);
}

/** One name and value line of a segment's hover readout. */
export function MetricTooltipRow({ name, children, className }: { name: string; children: ReactNode; className?: string }) {
	return (
		<div className="flex items-center justify-between gap-6">
			<span className="text-muted-foreground">{name}</span>
			<span className={cn("font-mono", className)}>{children}</span>
		</div>
	);
}

/** A segment's hover readout: a muted heading over its rows. */
export function MetricTooltipBody({ heading, children }: { heading: string; children: ReactNode }) {
	return (
		<div className="space-y-1">
			<div className="text-muted-foreground text-[11px]">{heading}</div>
			<div className="space-y-0.5">{children}</div>
		</div>
	);
}