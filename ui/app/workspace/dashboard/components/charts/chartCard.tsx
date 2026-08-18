import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import type { ReactNode } from "react";

interface ChartCardProps {
	title: string;
	children: ReactNode;
	controls?: ReactNode;
	legend?: ReactNode;
	loading?: boolean;
	testId?: string;
	className?: string;
	// Let the card grow to its content instead of the fixed chart height. Needed
	// by cards that render a ranked legend under the plot. This has to be a prop
	// rather than an `h-full` in `className`: cn() merges per variant, so a
	// caller class only cancels the unprefixed height and leaves `sm:h-[330px]`
	// in place, which clips the legend at the sm breakpoint and up.
	autoHeight?: boolean;
	total?: ReactNode;
	totalLabel?: string;
	totalTooltip?: ReactNode;
	// Optional second labeled total rendered beside the first (e.g. actual vs
	// attributed request counts).
	secondaryTotal?: ReactNode;
	secondaryTotalLabel?: string;
	secondaryTotalTooltip?: ReactNode;
}

function TotalChip({
	total,
	totalLabel,
	totalTooltip,
	testId,
}: {
	total: ReactNode;
	totalLabel?: string;
	totalTooltip?: ReactNode;
	testId?: string;
}) {
	const chip = (
		<span
			className="text-muted-foreground flex shrink-0 items-baseline gap-1 pl-2 text-xs"
			data-testid={testId ? `${testId}-total` : undefined}
		>
			{totalLabel && <span>{totalLabel}</span>}
			<span className="text-primary text-sm font-semibold tabular-nums">{total}</span>
		</span>
	);

	if (totalTooltip === undefined || totalTooltip === null) {
		return chip;
	}

	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<span tabIndex={0} data-testid={testId ? `${testId}-total-trigger` : undefined}>
					{chip}
				</span>
			</TooltipTrigger>
			<TooltipContent data-testid={testId ? `${testId}-total-tooltip` : undefined}>{totalTooltip}</TooltipContent>
		</Tooltip>
	);
}

function Header({
	title,
	controls,
	legend,
	total,
	totalLabel,
	totalTooltip,
	secondaryTotal,
	secondaryTotalLabel,
	secondaryTotalTooltip,
	testId,
}: {
	title: string;
	controls?: ReactNode;
	legend?: ReactNode;
	total?: ReactNode;
	totalLabel?: string;
	totalTooltip?: ReactNode;
	secondaryTotal?: ReactNode;
	secondaryTotalLabel?: string;
	secondaryTotalTooltip?: ReactNode;
	testId?: string;
}) {
	const hasTotal = total !== undefined && total !== null;
	const hasSecondaryTotal = secondaryTotal !== undefined && secondaryTotal !== null;
	const hasActionRow = hasTotal || controls;
	return (
		<div className="shrink-0 space-y-2">
			<div className="pr-1 pl-2">
				<span className="text-primary text-sm font-medium">{title}</span>
			</div>
			{hasActionRow && (
				<div
					className="flex min-h-7 w-full min-w-0 flex-wrap items-center justify-between gap-2 sm:gap-3"
					data-testid={testId ? `${testId}-actions` : undefined}
				>
					{hasTotal ? (
						<div className="flex min-w-0 items-center gap-5">
							<TotalChip total={total} totalLabel={totalLabel} totalTooltip={totalTooltip} testId={testId} />
							{hasSecondaryTotal && (
								<TotalChip
									total={secondaryTotal}
									totalLabel={secondaryTotalLabel}
									totalTooltip={secondaryTotalTooltip}
									testId={testId ? `${testId}-secondary` : undefined}
								/>
							)}
						</div>
					) : (
						<span className="shrink-0" />
					)}
					{controls && <div className="flex shrink-0 items-center gap-2">{controls}</div>}
				</div>
			)}
			{legend && <div className="w-full min-w-0">{legend}</div>}
		</div>
	);
}

export function ChartCard({
	title,
	children,
	controls,
	legend,
	loading,
	testId,
	className,
	total,
	totalLabel,
	totalTooltip,
	secondaryTotal,
	secondaryTotalLabel,
	secondaryTotalTooltip,
	autoHeight,
}: ChartCardProps) {
	const heightClass = autoHeight ? "h-auto" : "h-[260px] sm:h-[330px]";
	// An auto-height card has no height for the skeleton's `h-full` to resolve
	// against, so the loading state keeps the fixed height as a floor.
	const loadingHeightClass = autoHeight ? "h-auto min-h-[260px] sm:min-h-[330px]" : heightClass;

	if (loading) {
		return (
			<Card className={cn("min-w-0 rounded-sm p-2 shadow-none", loadingHeightClass, className)} data-testid={testId}>
				<Header
					title={title}
					controls={controls}
					legend={legend}
					total={total}
					totalLabel={totalLabel}
					totalTooltip={totalTooltip}
					secondaryTotal={secondaryTotal}
					secondaryTotalLabel={secondaryTotalLabel}
					secondaryTotalTooltip={secondaryTotalTooltip}
					testId={testId}
				/>
				<div className="grow" data-testid={testId ? `${testId}-chart-skeleton` : undefined}>
					<Skeleton className="h-full w-full" />
				</div>
			</Card>
		);
	}

	return (
		<Card className={cn("min-w-0 rounded-sm p-2 shadow-none", heightClass, className)} data-testid={testId}>
			<Header
				title={title}
				controls={controls}
				legend={legend}
				total={total}
				totalLabel={totalLabel}
				totalTooltip={totalTooltip}
				secondaryTotal={secondaryTotal}
				secondaryTotalLabel={secondaryTotalLabel}
				secondaryTotalTooltip={secondaryTotalTooltip}
				testId={testId}
			/>
			<div className="grow">{children}</div>
		</Card>
	);
}