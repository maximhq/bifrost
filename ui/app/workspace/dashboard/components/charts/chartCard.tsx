import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
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
}

function TotalChip({ total, totalLabel, testId }: { total: ReactNode; totalLabel?: string; testId?: string }) {
	return (
		<span
			className="text-muted-foreground flex shrink-0 items-baseline gap-1 pl-2 text-xs"
			data-testid={testId ? `${testId}-total` : undefined}
		>
			{totalLabel && <span>{totalLabel}</span>}
			<span className="text-primary text-sm font-semibold tabular-nums">{total}</span>
		</span>
	);
}

function Header({
	title,
	controls,
	legend,
	total,
	totalLabel,
	testId,
}: {
	title: string;
	controls?: ReactNode;
	legend?: ReactNode;
	total?: ReactNode;
	totalLabel?: string;
	testId?: string;
}) {
	const hasTotal = total !== undefined && total !== null;
	const hasActionRow = hasTotal || controls;
	return (
		<div className="shrink-0 space-y-2">
			<div className="pr-1 pl-2">
				<span className="text-primary text-sm font-medium">{title}</span>
			</div>
			{hasActionRow && (
				<div className="flex h-7 w-full min-w-0 items-center justify-between gap-3" data-testid={testId ? `${testId}-actions` : undefined}>
					{hasTotal ? <TotalChip total={total} totalLabel={totalLabel} testId={testId} /> : <span className="shrink-0" />}
					{controls && <div className="flex shrink-0 items-center gap-2">{controls}</div>}
				</div>
			)}
			{legend && <div className="w-full min-w-0">{legend}</div>}
		</div>
	);
}

export function ChartCard({ title, children, controls, legend, loading, testId, className, total, totalLabel }: ChartCardProps) {
	if (loading) {
		return (
			<Card className={cn("min-w-0 rounded-sm p-2 shadow-none h-[330px]", className)} data-testid={testId}>
				<Header title={title} controls={controls} legend={legend} total={total} totalLabel={totalLabel} testId={testId} />
				<div className="grow" data-testid={testId ? `${testId}-chart-skeleton` : undefined}>
					<Skeleton className="h-full w-full" />
				</div>
			</Card>
		);
	}

	return (
		<Card className={cn("min-w-0 rounded-sm p-2 shadow-none h-[330px]", className)} data-testid={testId}>
			<Header title={title} controls={controls} legend={legend} total={total} totalLabel={totalLabel} testId={testId} />
			<div className="grow">{children}</div>
		</Card>
	);
}