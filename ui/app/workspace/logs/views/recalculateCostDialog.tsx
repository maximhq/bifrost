import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useLazyGetLogsStatsQuery } from "@/lib/store/apis/logsApi";
import type { LogFilters as LogFiltersType } from "@/lib/types/logs";
import { formatCompactNumber } from "@/lib/utils/numbers";
import { Check, Info } from "lucide-react";
import { useCallback, useState } from "react";

export type RecalculateCostMode = "missing" | "all";

interface RecalculateCostDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	filters: LogFiltersType;
	/** Total logs matching the current filters/time window (stats.total_requests). */
	totalLogs: number;
	/** Invoked with the chosen mode when the user confirms. */
	onConfirm: (mode: RecalculateCostMode) => void;
}

export function RecalculateCostDialog({ open, onOpenChange, filters, totalLogs, onConfirm }: RecalculateCostDialogProps) {
	const [mode, setMode] = useState<RecalculateCostMode>("missing");
	// Lazy query for the missing-cost count: triggered imperatively on open and on
	// selecting "Missing cost only", so there is no data-fetching effect to manage.
	const [triggerStats, { data, isFetching, isError }] = useLazyGetLogsStatsQuery();

	const loadMissingCount = useCallback(() => {
		// preferCacheValue=false: always refetch so the count reflects the current
		// filters even if they changed since the last time the dialog was opened.
		triggerStats({ filters: { ...filters, missing_cost_only: true } }, /* preferCacheValue */ false);
	}, [triggerStats, filters]);

	const selectMode = (next: RecalculateCostMode) => {
		setMode(next);
		if (next === "missing") loadMissingCount();
	};

	const missingCount = data?.total_requests ?? null;
	const confirmDisabled = mode === "missing" ? isFetching || missingCount === 0 : totalLogs === 0;

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent
				className="sm:max-w-[500px]"
				// Fires whenever the dialog opens (including programmatic open): reset to
				// the default mode and fetch its count without a useEffect.
				onOpenAutoFocus={() => {
					setMode("missing");
					loadMissingCount();
				}}
			>
				<DialogHeader className="pb-2">
					<DialogTitle>Recalculate costs</DialogTitle>
					<DialogDescription>
						The current time window and filters will be applied. Choose which logs to recompute cost for.
					</DialogDescription>
				</DialogHeader>

				<div className="flex flex-col gap-2">
					<RecalculateModeOption
						selected={mode === "missing"}
						onSelect={() => selectMode("missing")}
						title="Missing cost only"
						description="Only recompute logs that don't have a cost yet."
					/>
					<RecalculateModeOption
						selected={mode === "all"}
						onSelect={() => selectMode("all")}
						title="All selected logs"
						description="Recompute cost for every log matching the current filters."
					/>
				</div>

				<p className="text-muted-foreground text-xs">
					{mode === "all" ? (
						<>
							<span className="text-foreground font-medium">{formatCompactNumber(totalLogs)}</span> logs match the current filters and will be
							recalculated.
						</>
					) : isFetching ? (
						"Checking how many logs are missing a cost…"
					) : isError || missingCount === null ? (
						"Logs in the current window that don't have a cost yet will be recalculated."
					) : missingCount === 0 ? (
						"All logs in the current window already have a cost."
					) : (
						<>
							<span className="text-foreground font-medium">{formatCompactNumber(missingCount)}</span> {missingCount === 1 ? "log" : "logs"} in the
							current window {missingCount === 1 ? "doesn't have" : "don't have"} a cost yet and will be recalculated.
						</>
					)}
				</p>

				<div className="flex items-start gap-2 rounded-md border border-amber-500/30 bg-amber-500/10 p-3 text-xs text-muted-foreground">
					<Info className="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-500" />
					<span>Affects the logs dashboard only. Governance budgets and usage tracking remain unchanged.</span>
				</div>

				<DialogFooter className="pt-0">
					<Button variant="outline" size="sm" onClick={() => onOpenChange(false)}>
						Cancel
					</Button>
					<Button size="sm" onClick={() => onConfirm(mode)} disabled={confirmDisabled}>
						Recalculate
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}

function RecalculateModeOption({
	selected,
	onSelect,
	title,
	description,
}: {
	selected: boolean;
	onSelect: () => void;
	title: string;
	description: string;
}) {
	return (
		<button
			type="button"
			onClick={onSelect}
			aria-pressed={selected}
			className={`flex items-start cursor-pointer gap-3 rounded-md border p-3 text-left transition-colors ${selected ? "border-primary bg-primary/5" : "border-input hover:bg-accent/50"
				}`}
		>
			<span
				className={`mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border ${selected ? "border-primary bg-primary text-primary-foreground" : "border-muted-foreground/40"
					}`}
			>
				{selected && <Check className="size-3" />}
			</span>
			<span className="flex flex-col gap-0.5">
				<span className="text-sm font-medium">{title}</span>
				<span className="text-muted-foreground text-xs">{description}</span>
			</span>
		</button>
	);
}
