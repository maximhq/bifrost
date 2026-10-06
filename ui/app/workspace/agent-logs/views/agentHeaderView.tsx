import { ColumnConfigDropdown, type ColumnConfigEntry } from "@/components/table";
import { Button } from "@/components/ui/button";
import { DateTimePickerWithRange } from "@/components/ui/datePickerWithRange";
import { Input } from "@/components/ui/input";
import { useTimezonePreference } from "@/lib/hooks/useTimezonePreference";
import type { AgentLogFilters } from "@/lib/types/agentLogs";
import { getRangeForPeriod, TIME_PERIODS } from "@/lib/utils/timeRange";
import { Radio, RefreshCw, Search, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

interface AgentHeaderViewProps {
	filters: AgentLogFilters;
	onSearchChange: (search: string) => void;
	period: string;
	onPeriodChange: (period?: string, from?: Date, to?: Date) => void;
	polling: boolean;
	onPollToggle: (enabled: boolean) => void;
	onRefresh: () => void;
	loading?: boolean;
	/** Column config for the ColumnConfigDropdown */
	columnEntries: ColumnConfigEntry[];
	columnLabels: Record<string, string>;
	onToggleColumnVisibility: (id: string) => void;
	onResetColumns: () => void;
}

export function AgentHeaderView({
	filters,
	onSearchChange,
	period,
	onPeriodChange,
	polling,
	onPollToggle,
	onRefresh,
	loading = false,
	columnEntries,
	columnLabels,
	onToggleColumnVisibility,
	onResetColumns,
}: AgentHeaderViewProps) {
	const [timezone, setTimezone] = useTimezonePreference();
	const [startTime, setStartTime] = useState<Date | undefined>(filters.start_time ? new Date(filters.start_time) : undefined);
	const [endTime, setEndTime] = useState<Date | undefined>(filters.end_time ? new Date(filters.end_time) : undefined);
	const [localSearch, setLocalSearch] = useState(filters.search || "");
	const searchTimeoutRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

	useEffect(() => {
		setStartTime(filters.start_time ? new Date(filters.start_time) : undefined);
		setEndTime(filters.end_time ? new Date(filters.end_time) : undefined);
	}, [filters.start_time, filters.end_time]);
	useEffect(() => {
		setLocalSearch(filters.search || "");
	}, [filters.search]);
	useEffect(() => {
		return () => {
			if (searchTimeoutRef.current) clearTimeout(searchTimeoutRef.current);
		};
	}, []);

	// Debounced so typing does not fire a request (and a URL push) per keystroke.
	const handleSearchChange = useCallback(
		(value: string) => {
			setLocalSearch(value);
			if (searchTimeoutRef.current) clearTimeout(searchTimeoutRef.current);
			searchTimeoutRef.current = setTimeout(() => onSearchChange(value), 500);
		},
		[onSearchChange],
	);

	const clearSearch = useCallback(() => {
		if (searchTimeoutRef.current) clearTimeout(searchTimeoutRef.current);
		setLocalSearch("");
		onSearchChange("");
	}, [onSearchChange]);

	return (
		<div className="flex grow flex-wrap items-center justify-between gap-2">
			<Button
				variant="outline"
				size="sm"
				className="h-7.5 disabled:opacity-100"
				onClick={onRefresh}
				disabled={loading}
				data-testid="agent-logs-header-refresh-btn"
			>
				<RefreshCw className={`h-4 w-4 ${loading ? "animate-spin" : ""}`} />
				Refresh
			</Button>
			<Button
				variant={polling ? "default" : "outline"}
				size="sm"
				className="h-7.5"
				onClick={() => onPollToggle(!polling)}
				data-testid="agent-logs-header-live-btn"
			>
				{polling ? <Radio className="h-4 w-4 animate-pulse" /> : <Radio className="h-4 w-4" />}
				Live
			</Button>
			<div className="border-input flex h-7.5 min-w-[12rem] flex-1 items-center gap-2 rounded-sm border">
				<Search className="mr-0.5 ml-2 size-4" />
				<Input
					type="text"
					className="!h-7 rounded-tl-none rounded-tr-sm rounded-br-sm rounded-bl-none border-none bg-slate-50 shadow-none outline-none focus-visible:ring-0 dark:bg-zinc-900"
					placeholder="Search agent, operation, or any identifier"
					value={localSearch}
					onChange={(e) => handleSearchChange(e.target.value)}
					data-testid="agent-logs-header-search-input"
				/>
				{localSearch && (
					<button
						type="button"
						onClick={clearSearch}
						aria-label="Clear search"
						className="text-muted-foreground hover:text-foreground mr-2 transition-colors"
						data-testid="agent-logs-header-search-clear-btn"
					>
						<X className="size-3.5" />
					</button>
				)}
			</div>
			<div className="flex items-center gap-2">
				<DateTimePickerWithRange
					buttonClassName="w-full sm:w-auto"
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
						onPeriodChange(periodValue, from, to);
					}}
				/>
				<ColumnConfigDropdown
					entries={columnEntries}
					labels={columnLabels}
					onToggleVisibility={onToggleColumnVisibility}
					onReset={onResetColumns}
				/>
			</div>
		</div>
	);
}