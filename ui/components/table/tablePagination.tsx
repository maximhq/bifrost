import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { ChevronLeft, ChevronRight } from "lucide-react";

interface TablePaginationProps {
	offset: number;
	limit: number;
	totalCount: number;
	onOffsetChange: (offset: number) => void;
	className?: string;
	testId?: string;
	prevTestId?: string;
	nextTestId?: string;
}

export function TablePagination({
	offset,
	limit,
	totalCount,
	onOffsetChange,
	className,
	testId = "pagination",
	prevTestId,
	nextTestId,
}: TablePaginationProps) {
	if (totalCount <= 0) {
		return null;
	}

	return (
		<div className={cn("flex shrink-0 items-center justify-between text-xs", className)} data-testid={testId}>
			<div className="text-muted-foreground flex items-center gap-2">
				{(offset + 1).toLocaleString()}-{Math.min(offset + limit, totalCount).toLocaleString()} of {totalCount.toLocaleString()} entries
			</div>

			<div className="flex items-center gap-2">
				<Button
					variant="ghost"
					size="sm"
					onClick={() => onOffsetChange(Math.max(0, offset - limit))}
					disabled={offset === 0}
					data-testid={prevTestId}
					aria-label="Previous page"
				>
					<ChevronLeft className="size-3" />
				</Button>

				<div className="flex items-center gap-1">
					<span>Page</span>
					<span>{Math.floor(offset / limit) + 1}</span>
					<span>of {Math.ceil(totalCount / limit)}</span>
				</div>

				<Button
					variant="ghost"
					size="sm"
					onClick={() => onOffsetChange(offset + limit)}
					disabled={offset + limit >= totalCount}
					data-testid={nextTestId}
					aria-label="Next page"
				>
					<ChevronRight className="size-3" />
				</Button>
			</div>
		</div>
	);
}
