import { COMPACT_NUMBER_FORMAT, formatCompactNumber as formatNumber } from "@/lib/utils/numbers";
import { ArrowDown, ArrowUp, ArrowUpDown, Minus } from "lucide-react";

export { formatNumber, COMPACT_NUMBER_FORMAT };

export function formatCost(value: number): string {
	if (value >= 1) return `$${value.toFixed(2)}`;
	if (value >= 0.01) return `$${value.toFixed(3)}`;
	if (value > 0) return `$${value.toFixed(4)}`;
	return "$0.00";
}

export function TrendBadge({ value, positiveIsGood = true, isNew = false }: { value: number; positiveIsGood?: boolean; isNew?: boolean }) {
	if (isNew) {
		return <span className="inline-flex items-center gap-0.5 text-xs font-medium text-blue-600 dark:text-blue-400">new</span>;
	}

	if (value === 0) {
		return (
			<span className="text-muted-foreground inline-flex items-center gap-0.5 text-xs">
				<Minus className="h-3 w-3" />
			</span>
		);
	}

	const isPositive = value > 0;
	const isGood = positiveIsGood ? isPositive : !isPositive;
	return (
		<span
			className={`inline-flex items-center gap-0.5 text-xs font-medium ${isGood ? "text-emerald-600 dark:text-emerald-400" : "text-red-600 dark:text-red-400"}`}
		>
			{isPositive ? <ArrowUp className="h-3 w-3" /> : <ArrowDown className="h-3 w-3" />}
			{Math.abs(value).toFixed(1)}%
		</span>
	);
}

export function SortableHeader<T extends string>({
	label,
	field,
	currentSort,
	currentOrder,
	onSort,
}: {
	label: string;
	field: T;
	currentSort: T;
	currentOrder: "asc" | "desc";
	onSort: (field: T) => void;
}) {
	const isActive = currentSort === field;
	return (
		<button
			type="button"
			data-testid={`sort-${field}-btn`}
			className="hover:text-foreground inline-flex items-center gap-1 transition-colors"
			onClick={() => onSort(field)}
		>
			{label}
			{isActive ? (
				currentOrder === "desc" ? (
					<ArrowDown className="h-3 w-3" />
				) : (
					<ArrowUp className="h-3 w-3" />
				)
			) : (
				<ArrowUpDown className="text-muted-foreground h-3 w-3" />
			)}
		</button>
	);
}