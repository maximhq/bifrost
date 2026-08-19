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
				<div className="flex h-7 w-full min-w-0 items-center justify-between gap-3" data-testid={testId ? `${testId}-actions` : undefined}>
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
}: ChartCardProps) {
	if (loading) {
		return (
			<Card className={cn("min-w-0 rounded-sm p-2 shadow-none h-[330px]", className)} data-testid={testId}>
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
		<Card className={cn("min-w-0 rounded-sm p-2 shadow-none h-[330px]", className)} data-testid={testId}>
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