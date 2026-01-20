"use client";

import { Button } from "@/components/ui/button";
import { BarChart3, LineChart } from "lucide-react";

export type ChartType = "bar" | "line";

interface ChartTypeToggleProps {
	chartType: ChartType;
	onToggle: (type: ChartType) => void;
}

export function ChartTypeToggle({ chartType, onToggle }: ChartTypeToggleProps) {
	return (
		<div className="flex items-center gap-1">
			<Button
				variant={chartType === "bar" ? "secondary" : "ghost"}
				size="sm"
				className="h-7 w-7 p-0"
				onClick={() => onToggle("bar")}
			>
				<BarChart3 className="h-3.5 w-3.5" />
			</Button>
			<Button
				variant={chartType === "line" ? "secondary" : "ghost"}
				size="sm"
				className="h-7 w-7 p-0"
				onClick={() => onToggle("line")}
			>
				<LineChart className="h-3.5 w-3.5" />
			</Button>
		</div>
	);
}
