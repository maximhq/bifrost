"use client";

import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import type { ReactNode } from "react";

interface ChartCardProps {
	title: string;
	children: ReactNode;
	headerActions?: ReactNode;
	loading?: boolean;
}

export function ChartCard({ title, children, headerActions, loading }: ChartCardProps) {
	if (loading) {
		return (
			<Card className="rounded-sm px-2 py-1 shadow-none">
				<div className="mb-3 flex items-center justify-between">
					<span className="text-primary pl-2 text-sm font-medium">{title}</span>
					{headerActions && <div className="flex items-center gap-2">{headerActions}</div>}
				</div>
				<div style={{ height: "200px" }}>
					<Skeleton className="h-full w-full" />
				</div>
			</Card>
		);
	}

	return (
		<Card className="rounded-sm px-2 py-1 shadow-none">
			<div className="mb-3 flex items-center justify-between">
				<span className="text-primary pl-2 text-sm font-medium">{title}</span>
				{headerActions && <div className="flex items-center gap-2">{headerActions}</div>}
			</div>
			<div style={{ height: "200px" }}>{children}</div>
		</Card>
	);
}
