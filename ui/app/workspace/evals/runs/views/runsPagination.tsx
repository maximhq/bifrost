import { Button } from "@/components/ui/button";
import { getLocale } from "@/lib/i18n";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { useTranslation } from "react-i18next";

interface RunsPaginationProps {
	offset: number;
	limit: number;
	total: number;
	onOffsetChange: (offset: number) => void;
	// The start of the test IDs: "<testId>-pagination", "-prev" and "-next".
	testId: string;
}

// RunsPagination is the pager under a table, the same as on the other pages of the
// app: the range shown on the left, and the page with its arrows on the right.
export function RunsPagination({ offset, limit, total, onOffsetChange, testId }: RunsPaginationProps) {
	const { t } = useTranslation("mcp");
	if (total <= 0) return null;
	return (
		<div className="flex shrink-0 items-center justify-between text-xs" data-testid={`${testId}-pagination`}>
			<div className="text-muted-foreground flex items-center gap-2">
				{t("common.ofEntries", {
					from: (offset + 1).toLocaleString(getLocale()),
					to: Math.min(offset + limit, total).toLocaleString(getLocale()),
					total: total.toLocaleString(getLocale()),
				})}
			</div>
			<div className="flex items-center gap-2">
				<Button
					variant="ghost"
					size="sm"
					onClick={() => onOffsetChange(Math.max(0, offset - limit))}
					disabled={offset === 0}
					data-testid={`${testId}-pagination-prev`}
					aria-label={t("common.previousPage")}
				>
					<ChevronLeft className="size-3" />
				</Button>
				<div className="flex items-center gap-1">
					<span>{t("common.page")}</span>
					<span>{Math.floor(offset / limit) + 1}</span>
					<span>{t("common.ofPages", { total: Math.ceil(total / limit) })}</span>
				</div>
				<Button
					variant="ghost"
					size="sm"
					onClick={() => onOffsetChange(offset + limit)}
					disabled={offset + limit >= total}
					data-testid={`${testId}-pagination-next`}
					aria-label={t("common.nextPage")}
				>
					<ChevronRight className="size-3" />
				</Button>
			</div>
		</div>
	);
}