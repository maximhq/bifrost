import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import type { AgentLogFilters } from "@/lib/types/agentLogs";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

export type FilterTarget = Partial<Omit<AgentLogFilters, "period" | "start_time" | "end_time">>;

/** Values filter on left-click and copy on right-click. */
export function InteractiveValue({
	value,
	label,
	filter,
	testId,
	className,
	onFilter,
}: {
	value: string;
	label: string;
	filter?: FilterTarget;
	onFilter?: (filters: FilterTarget) => void;
	testId?: string;
	className?: string;
}) {
	const { t } = useTranslation("observability");
	const canFilter = Boolean(filter && onFilter);
	const { copy } = useCopyToClipboard({ successMessage: t("agentLogs.interactive.copied") });
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<button
					type="button"
					onClick={(event) => {
						event.stopPropagation();
						if (filter && onFilter) onFilter(filter);
					}}
					onContextMenu={(event) => {
						event.preventDefault();
						event.stopPropagation();
						copy(value);
					}}
					className={cn(
						"focus-visible:ring-ring min-w-0 max-w-full truncate text-left underline-offset-2 transition hover:underline focus-visible:ring-2 focus-visible:outline-none",
						canFilter ? "cursor-pointer" : "cursor-context-menu",
						className,
					)}
					aria-label={canFilter ? t("agentLogs.interactive.filterBy", { label }) : label}
					data-testid={testId}
				>
					{value}
				</button>
			</TooltipTrigger>
			<TooltipContent>{canFilter ? t("agentLogs.interactive.clickToFilter") : t("agentLogs.interactive.rightClickToCopy")}</TooltipContent>
		</Tooltip>
	);
}