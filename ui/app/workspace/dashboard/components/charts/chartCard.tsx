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
	height?: string;
}

export function ChartCard({ title, children, headerActions, loading, testId, height = "200px" }: ChartCardProps) {
	if (loading) {
		return (
			<Card className="min-w-0 rounded-sm px-2 py-1 shadow-none" data-testid={testId}>
				<div className="mb-3 space-y-2">
					<span className="text-primary pl-2 text-sm font-medium">{title}</span>
					{headerActions && (
						<div className="w-full min-w-0" data-testid={testId ? `${testId}-actions` : undefined}>
							{headerActions}
						</div>
					)}
				</div>
				<div style={{ height, marginBottom: 6 }} data-testid={testId ? `${testId}-chart-skeleton` : undefined}>
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
					<div className="w-full min-w-0" data-testid={testId ? `${testId}-actions` : undefined}>
						{headerActions}
					</div>
				)}
			</div>
			<div style={{ height }}>{children}</div>
		</Card>
	);
}
