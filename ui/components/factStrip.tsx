import { MetricStripCard } from "@/components/metricStrip";
import { cn } from "@/lib/utils";
import { ChevronRight } from "lucide-react";
import type { ReactNode } from "react";

// A row of small facts about an entity: what it is set up with, at a glance.
// It shares the metric strip's surface, hairline dividers between segments,
// but each segment carries text rather than a large figure.
export function FactStrip({ className, children, testId }: { className?: string; children: ReactNode; testId?: string }) {
	return (
		<MetricStripCard className={cn("grid-cols-2 sm:grid-cols-3 lg:grid-cols-5", className)} testId={testId}>
			{children}
		</MetricStripCard>
	);
}

// Fact is one segment: a label, a value and a line under it. A fact with a tab
// of its own takes onClick, and the segment becomes a button that opens it.
export function Fact({
	label,
	value,
	sub,
	dim,
	onClick,
	testId,
}: {
	label: string;
	value: ReactNode;
	sub?: ReactNode;
	// A dim fact is one with nothing behind it yet.
	dim?: boolean;
	onClick?: () => void;
	testId?: string;
}) {
	const body = (
		<>
			<span className="text-muted-foreground flex items-center justify-between gap-2 text-[11px] font-medium tracking-wide uppercase">
				<span className="truncate">{label}</span>
				{onClick && <ChevronRight className="h-3 w-3 shrink-0 opacity-0 transition-opacity group-hover:opacity-100" aria-hidden="true" />}
			</span>
			<span className={cn("truncate text-sm font-medium", dim && "text-muted-foreground")}>{value}</span>
			<div className="text-muted-foreground min-h-4 min-w-0 truncate text-xs">{sub}</div>
		</>
	);
	const className = "bg-card flex min-w-0 flex-col gap-1 px-4 py-3 text-left";
	if (!onClick) {
		return (
			<div className={className} data-testid={testId}>
				{body}
			</div>
		);
	}
	return (
		<button
			type="button"
			className={cn(className, "group hover:bg-muted/40 cursor-pointer transition-colors")}
			onClick={onClick}
			data-testid={testId}
		>
			{body}
		</button>
	);
}