"use client";

import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import type { ReactNode } from "react";

interface ChartCardProps {
	title: string;
	children: ReactNode;
	headerActions?: ReactNode;
	loading?: boolean;
	testId?: string;
}

export function ChartCard({ title, children, headerActions, loading, testId }: ChartCardProps) {
	// loading = true;
	if (loading) {
		return (
			<Card className="min-w-0 rounded-sm px-2 py-1 shadow-none" data-testid={testId}>
				<div className="mb-3 space-y-2">
					<span className="text-primary pl-2 text-sm font-medium">{title}</span>
					{headerActions && (
						<div className="min-w-0 w-full" data-testid={testId ? `${testId}-actions` : undefined}>
							{headerActions}
						</div>
					)}
				</div>
				<div style={{ height: "200px", marginBottom: 6 }} data-testid={testId ? `${testId}-chart-skeleton` : undefined}>
					<Skeleton className="h-full w-full" />
				</div>
			</Card>
		);
	}

	return (
		<Card className="min-w-0 rounded-sm px-2 py-1 shadow-none" data-testid={testId}>
			<div className="mb-2 space-y-2">
				<span className="text-primary pl-2 text-sm font-medium">{title}</span>
				{headerActions && (
					<div className="min-w-0 w-full" data-testid={testId ? `${testId}-actions` : undefined}>
						{headerActions}
					</div>
				)}
			</div>
			<div style={{ height: "200px" }}>{children}</div>
		</Card>
	);
}
