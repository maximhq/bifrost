import { Button } from "@/components/ui/button";
import { BarChart3, LineChart } from "lucide-react";

export type ChartType = "bar" | "line";

interface ChartTypeToggleProps {
	chartType: ChartType;
	onToggle: (type: ChartType) => void;
	"data-testid"?: string;
}

export function ChartTypeToggle({ chartType, onToggle, "data-testid": testId }: ChartTypeToggleProps) {
	return (
		<div className="flex items-center gap-1" role="group" aria-label="Chart type" data-testid={testId}>
			<Button
				variant={chartType === "bar" ? "secondary" : "ghost"}
				size="sm"
				className="h-7 w-7 p-0"
				onClick={() => onToggle("bar")}
				aria-label="Bar chart"
				aria-pressed={chartType === "bar"}
				data-testid={testId ? `${testId}-bar-btn` : undefined}
			>
				<BarChart3 className="h-3.5 w-3.5" aria-hidden="true" />
			</Button>
			<Button
				variant={chartType === "line" ? "secondary" : "ghost"}
				size="sm"
				className="h-7 w-7 p-0"
				onClick={() => onToggle("line")}
				aria-label="Line chart"
				aria-pressed={chartType === "line"}
				data-testid={testId ? `${testId}-line-btn` : undefined}
			>
				<LineChart className="h-3.5 w-3.5" aria-hidden="true" />
			</Button>
		</div>
	);
}