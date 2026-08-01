import { ColumnConfigDropdown, type ColumnConfigEntry } from "@/components/table";
import { Button } from "@/components/ui/button";
import { Command, CommandItem, CommandList } from "@/components/ui/command";
import { DateTimePickerWithRange } from "@/components/ui/datePickerWithRange";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { useTimezonePreference } from "@/lib/hooks/useTimezonePreference";
import { getErrorMessage } from "@/lib/store";
import { useGetRecalculateCostStatusQuery } from "@/lib/store/apis/logsApi";
import { getActiveTempToken } from "@/lib/store/apis/tempToken";
import type { LogFilters as LogFiltersType, RecalcJobStatus } from "@/lib/types/logs";
import { getApiBaseUrl } from "@/lib/utils/port";
import { getRangeForPeriod, TIME_PERIODS } from "@/lib/utils/timeRange";
import { Calculator, MoreVertical, Radio, RefreshCw, Search } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { RecalculateCostDialog, type RecalculateCostMode } from "./recalculateCostDialog";

interface LogsHeaderViewProps {
	filters: LogFiltersType;
	onFiltersChange: (filters: LogFiltersType) => void;
	fetchLogs: () => Promise<void>;
	fetchStats: () => Promise<void>;
	fetchHistogram: () => Promise<void>;
	loading?: boolean;
	polling: boolean;
	onPollToggle: (enabled: boolean) => void;
	period: string;
	onPeriodChange: (period?: string, from?: Date, to?: Date) => void;
	/** Total logs matching the current filters/time window (stats.total_requests) */
	totalLogs: number;
	/** Column config for the ColumnConfigDropdown */
	columnEntries: ColumnConfigEntry[];
	columnLabels: Record<string, string>;
	onToggleColumnVisibility: (id: string) => void;
	onResetColumns: () => void;
}

export function LogsHeaderView({
	filters,
	onFiltersChange,
	fetchLogs,
	fetchStats,
	fetchHistogram,
	loading = false,
	polling,
	onPollToggle,
	period,
	onPeriodChange,
	totalLogs,
	columnEntries,
	columnLabels,
	onToggleColumnVisibility,
	onResetColumns,
}: LogsHeaderViewProps) {
	const [openMoreActionsPopover, setOpenMoreActionsPopover] = useState(false);
	const [recalcDialogOpen, setRecalcDialogOpen] = useState(false);
	// Id of the recalculation job to track. Setting it starts polling via the query
	// hook below; clearing it (on a terminal status) stops the polling.
	const [activeRecalcJobId, setActiveRecalcJobId] = useState<string | null>(null);
	const activeRecalcJobIdRef = useRef<string | null>(null);
	useEffect(() => {
		activeRecalcJobIdRef.current = activeRecalcJobId;
	}, [activeRecalcJobId]);
	const { data: recalcJobStatus, isError: recalcJobStatusError } = useGetRecalculateCostStatusQuery(
		activeRecalcJobId ? { id: activeRecalcJobId } : undefined,
		{
			pollingInterval: 2000,
			skip: !activeRecalcJobId,
		},
	);
	const isRecalcRunning = !!activeRecalcJobId;
	const [localSearch, setLocalSearch] = useState(filters.content_search || "");
	const searchTimeoutRef = useRef<NodeJS.Timeout | undefined>(undefined);
	const filtersRef = useRef<LogFiltersType>(filters);

	const [timezone, setTimezone] = useTimezonePreference();

	const [startTime, setStartTime] = useState<Date | undefined>(filters.start_time ? new Date(filters.start_time) : undefined);
	const [endTime, setEndTime] = useState<Date | undefined>(filters.end_time ? new Date(filters.end_time) : undefined);

	useEffect(() => {
		setStartTime(filters.start_time ? new Date(filters.start_time) : undefined);
		setEndTime(filters.end_time ? new Date(filters.end_time) : undefined);
	}, [filters.start_time, filters.end_time]);

	useEffect(() => {
		filtersRef.current = filters;
	}, [filters]);

	useEffect(() => {
		setLocalSearch(filters.content_search || "");
	}, [filters.content_search]);

	useEffect(() => {
		return () => {
			if (searchTimeoutRef.current) clearTimeout(searchTimeoutRef.current);
		};
	}, []);

	const handleRecalculateCosts = useCallback(
		async (mode: RecalculateCostMode) => {
			setRecalcDialogOpen(false);
			const missingCostOnly = mode === "missing";
			const toastId = "logs-recalculate-costs";
			toast.loading("Starting cost recalculation...", { id: toastId });

			try {
				// Recalculation runs as a background job. Enqueue it (or attach to the one
				// already running); the status query below polls it to a terminal state.
				const { status, alreadyRunning } = await startRecalculateCostJob(filters, missingCostOnly);
				if (!status.id) {
					throw new Error("Recalculation job did not start");
				}
				if (alreadyRunning) {
					toast.loading("A cost recalculation is already running...", { id: toastId });
				}
				setActiveRecalcJobId(status.id);
			} catch (err) {
				toast.error("Cost recalculation failed", { id: toastId, description: getErrorMessage(err) });
			}
		},
		[filters],
	);

	// If the status endpoint keeps failing, stop polling and surface the error so the
	// user isn't left with a loading toast that never resolves.
	useEffect(() => {
		if (!activeRecalcJobId || !recalcJobStatusError) return;
		toast.error("Cost recalculation failed", {
			id: "logs-recalculate-costs",
			description: "Lost track of the recalculation job status. Please refresh and try again.",
		});
		setActiveRecalcJobId(null);
	}, [activeRecalcJobId, recalcJobStatusError]);

	// If we unmount while a job is still being tracked, polling stops but the global
	// loading toast would otherwise linger — dismiss it on the way out.
	useEffect(() => {
		return () => {
			if (activeRecalcJobIdRef.current) toast.dismiss("logs-recalculate-costs");
		};
	}, []);

	// React to each polled status snapshot: update the progress toast while running,
	// and on a terminal status show the result, refresh the view, and stop polling.
	useEffect(() => {
		if (!activeRecalcJobId || !recalcJobStatus) return;
		const toastId = "logs-recalculate-costs";

		if (recalcJobStatus.status === "completed" || recalcJobStatus.status === "failed") {
			if (recalcJobStatus.status === "failed") {
				toast.error("Cost recalculation failed", {
					id: toastId,
					description: recalcJobStatus.last_error || recalcJobStatus.message || "The job did not complete",
				});
			} else {
				toast.success("Cost recalculation complete", {
					id: toastId,
					description: recalcJobStatus.message || `${recalcJobStatus.updated} updated, ${recalcJobStatus.skipped} skipped`,
					duration: 5000,
				});
			}
			setActiveRecalcJobId(null);
			void fetchLogs();
			void fetchStats();
			return;
		}

		const total = recalcJobStatus.total || 0;
		const processed = total > 0 ? Math.min(recalcJobStatus.processed, total) : recalcJobStatus.processed;
		toast.loading("Recalculating log costs...", {
			id: toastId,
			description:
				total > 0
					? `${processed}/${total} checked, ${recalcJobStatus.updated} updated, ${recalcJobStatus.skipped} skipped`
					: `${recalcJobStatus.processed} checked, ${recalcJobStatus.updated} updated, ${recalcJobStatus.skipped} skipped`,
		});
	}, [activeRecalcJobId, recalcJobStatus, fetchLogs, fetchStats]);

	const handleSearchChange = useCallback(
		(value: string) => {
			setLocalSearch(value);
			if (searchTimeoutRef.current) clearTimeout(searchTimeoutRef.current);
			searchTimeoutRef.current = setTimeout(() => {
				onFiltersChange({ ...filtersRef.current, content_search: value });
			}, 500);
		},
		[onFiltersChange],
	);

	return (
		<div className="flex grow items-center justify-between space-x-2">
			<Button
				data-testid="logs-refresh-btn"
				variant="outline"
				size="sm"
				className="h-7.5 disabled:opacity-100"
				onClick={() => {
					fetchLogs();
					fetchStats();
					fetchHistogram();
				}}
				disabled={loading}
			>
				<RefreshCw className={`h-4 w-4 ${loading ? "animate-spin" : ""}`} />
				Refresh
			</Button>
			<Button
				data-testid="logs-live-btn"
				variant={polling ? "default" : "outline"}
				size="sm"
				className="h-7.5"
				onClick={() => onPollToggle(!polling)}
			>
				{polling ? <Radio className="h-4 w-4 animate-pulse" /> : <Radio className="h-4 w-4" />}
				Live
			</Button>
			<div className="border-input flex h-7.5 flex-1 items-center gap-2 rounded-sm border">
				<Search className="mr-0.5 ml-2 size-4" />
				<Input
					type="text"
					className="!h-7 rounded-tl-none rounded-tr-sm rounded-br-sm rounded-bl-none border-none bg-slate-50 shadow-none outline-none focus-visible:ring-0"
					placeholder="Search logs"
					value={localSearch}
					onChange={(e) => handleSearchChange(e.target.value)}
				/>
			</div>

			<DateTimePickerWithRange
				triggerTestId="filter-date-range"
				dateTime={{ from: startTime, to: endTime }}
				predefinedPeriod={period || undefined}
				showTimezone
				timezone={timezone}
				onTimezoneChange={setTimezone}
				onDateTimeUpdate={(p) => {
					setStartTime(p.from);
					setEndTime(p.to);
					onPeriodChange(undefined, p.from, p.to);
				}}
				preDefinedPeriods={TIME_PERIODS}
				onPredefinedPeriodChange={(periodValue) => {
					if (!periodValue) return;
					const { from, to } = getRangeForPeriod(periodValue);
					setStartTime(from);
					setEndTime(to);
					// Relative period: store it in URL and update timestamps via parent
					onPeriodChange(periodValue, from, to);
				}}
			/>
			<Popover open={openMoreActionsPopover} onOpenChange={setOpenMoreActionsPopover}>
				<PopoverTrigger asChild>
					<Button variant="outline" size="sm" className="h-7.5 w-7.5">
						<MoreVertical className="h-4 w-4" />
					</Button>
				</PopoverTrigger>
				<PopoverContent className="bg-accent w-[250px] p-2" align="end">
					<Command>
						<CommandList>
							<CommandItem
								className="hover:bg-accent/50 cursor-pointer"
								disabled={isRecalcRunning}
								onSelect={() => {
									if (isRecalcRunning) return;
									setOpenMoreActionsPopover(false);
									setRecalcDialogOpen(true);
								}}
							>
								{isRecalcRunning ? (
									<RefreshCw className="text-muted-foreground size-4 animate-spin" />
								) : (
									<Calculator className="text-muted-foreground size-4" />
								)}
								<div className="flex flex-col">
									<span className="text-sm">{isRecalcRunning ? "Recalculating costs…" : "Recalculate costs"}</span>
									<span className="text-muted-foreground text-xs">
										{isRecalcRunning ? "A recalculation is already running" : "Recompute cost for logs in this view"}
									</span>
								</div>
							</CommandItem>
						</CommandList>
					</Command>
				</PopoverContent>
			</Popover>
			<ColumnConfigDropdown
				entries={columnEntries}
				labels={columnLabels}
				onToggleVisibility={onToggleColumnVisibility}
				onReset={onResetColumns}
			/>

			<RecalculateCostDialog
				open={recalcDialogOpen}
				onOpenChange={setRecalcDialogOpen}
				filters={filters}
				totalLogs={totalLogs}
				onConfirm={handleRecalculateCosts}
			/>
		</div>
	);
}

// startRecalculateCostJob enqueues a background recalculation (or attaches to the
// one already running) and returns its status. A 202 means a new job started; a
// 409 means one was already in flight and its status is returned instead.
async function startRecalculateCostJob(
	filters: LogFiltersType,
	missingCostOnly: boolean,
): Promise<{ status: RecalcJobStatus; alreadyRunning: boolean }> {
	const headers: Record<string, string> = {
		"Content-Type": "application/json",
	};
	const tempToken = getActiveTempToken();
	if (tempToken) {
		headers["X-Bifrost-Temp-Token"] = tempToken;
	}

	const response = await fetch(`${getApiBaseUrl()}/logs/recalculate-cost`, {
		method: "POST",
		credentials: "include",
		headers,
		// Override the page's own missing_cost_only filter with the mode chosen in the dialog.
		body: JSON.stringify({ filters: { ...filters, missing_cost_only: missingCostOnly } }),
	});

	// 202 Accepted (new job) and 409 Conflict (already running) both carry a status.
	if (response.status === 202 || response.status === 409) {
		const status = (await response.json()) as RecalcJobStatus;
		return { status, alreadyRunning: response.status === 409 };
	}
	throw await readRecalculateCostError(response);
}

async function readRecalculateCostError(response: Response): Promise<Error> {
	try {
		return parseRecalculateCostStreamError(await response.text());
	} catch {
		return new Error(`Failed to recalculate costs (${response.status})`);
	}
}

function parseRecalculateCostStreamError(data: string): Error {
	try {
		const parsed = JSON.parse(data) as { error?: { message?: string }; message?: string };
		return new Error(parsed.error?.message || parsed.message || "Failed to recalculate costs");
	} catch {
		return new Error(data || "Failed to recalculate costs");
	}
}