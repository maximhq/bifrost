import { Button } from "@/components/ui/button";
import { Command, CommandItem, CommandList } from "@/components/ui/command";
import { DateTimePickerWithRange } from "@/components/ui/datePickerWithRange";
import { FilterPopover } from "@/components/filters/filterPopover";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { getErrorMessage, useRecalculateLogCostsMutation } from "@/lib/store";
import type { LogFilters as LogFiltersType } from "@/lib/types/logs";
import { Calculator, MoreVertical, Pause, Play, Search } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { toast } from "sonner";

/**
 * Converts a Date object to an RFC 3339 string with the local time zone offset.
 *
 * Example: 2025-11-19T12:23:19.421+05:30
 *
 * @param dateObj The Date object to convert (defaults to new Date() if null/undefined).
 * @returns The RFC 3339 formatted string with local offset.
 */
export function dateToRfc3339Local(dateObj?: Date): string {
	const now = dateObj instanceof Date ? dateObj : new Date();

	// Helper function to pad single digits with a leading zero
	const pad = (num: number): string => (num < 10 ? "0" + num : String(num));

	const Y = now.getFullYear();
	const M = pad(now.getMonth() + 1); // Month is 0-indexed (Jan=0)
	const D = pad(now.getDate());
	const H = pad(now.getHours());
	const m = pad(now.getMinutes());
	const S = pad(now.getSeconds());
	const ms = String(now.getMilliseconds()).padStart(3, "0");

	// getTimezoneOffset() returns the difference in minutes from UTC for the local time.
	// The result is positive for time zones west of Greenwich and negative for those east.
	// We negate it to get the standard ISO/RFC sign convention (+ for East, - for West).
	const timezoneOffsetMinutes = -now.getTimezoneOffset();
	const sign = timezoneOffsetMinutes >= 0 ? "+" : "-";
	const absoluteOffset = Math.abs(timezoneOffsetMinutes);
	const offsetHours = pad(Math.floor(absoluteOffset / 60));
	const offsetMinutes = pad(absoluteOffset % 60);
	const rfc3339Local = `${Y}-${M}-${D}T${H}:${m}:${S}.${ms}${sign}${offsetHours}:${offsetMinutes}`;
	return rfc3339Local;
}

/** Predefined time periods for the logs date range picker (matches E2E test labels) */
const LOG_TIME_PERIODS = [
	{ label: "Last hour", value: "1h" },
	{ label: "Last 6 hours", value: "6h" },
	{ label: "Last 24 hours", value: "24h" },
	{ label: "Last 7 days", value: "7d" },
	{ label: "Last 30 days", value: "30d" },
];

function getRangeForPeriod(period: string): { from: Date; to: Date } {
	const to = new Date();
	const from = new Date(to.getTime());
	switch (period) {
		case "1h":
			from.setHours(from.getHours() - 1);
			break;
		case "6h":
			from.setHours(from.getHours() - 6);
			break;
		case "24h":
			from.setHours(from.getHours() - 24);
			break;
		case "7d":
			from.setDate(from.getDate() - 7);
			break;
		case "30d":
			from.setDate(from.getDate() - 30);
			break;
		default:
			from.setHours(from.getHours() - 24);
	}
	return { from, to };
}

interface LogFiltersProps {
	filters: LogFiltersType;
	onFiltersChange: (filters: LogFiltersType) => void;
	liveEnabled: boolean;
	onLiveToggle: (enabled: boolean) => void;
	fetchLogs: () => Promise<void>;
	fetchStats: () => Promise<void>;
}

export function LogFilters({ filters, onFiltersChange, liveEnabled, onLiveToggle, fetchLogs, fetchStats }: LogFiltersProps) {
	const [openMoreActionsPopover, setOpenMoreActionsPopover] = useState(false);
	const [localSearch, setLocalSearch] = useState(filters.content_search || "");
	const searchTimeoutRef = useRef<NodeJS.Timeout | undefined>(undefined);
	const filtersRef = useRef<LogFiltersType>(filters);
	const [recalculateCosts, { isLoading: recalculating }] = useRecalculateLogCostsMutation();

	// Keep filtersRef in sync so debounced search always merges with latest filters (search within filtered results)
	useEffect(() => {
		filtersRef.current = filters;
	}, [filters]);

	// Sync localSearch when filters.content_search changes externally (e.g. URL restore)
	useEffect(() => {
		setLocalSearch(filters.content_search || "");
	}, [filters.content_search]);

	// Convert ISO strings from filters to Date objects for the DateTimePicker
	const [startTime, setStartTime] = useState<Date | undefined>(filters.start_time ? new Date(filters.start_time) : undefined);
	const [endTime, setEndTime] = useState<Date | undefined>(filters.end_time ? new Date(filters.end_time) : undefined);

	// Sync local date state when filters change from URL
	useEffect(() => {
		setStartTime(filters.start_time ? new Date(filters.start_time) : undefined);
		setEndTime(filters.end_time ? new Date(filters.end_time) : undefined);
	}, [filters.start_time, filters.end_time]);

	// Cleanup timeout on unmount
	useEffect(() => {
		return () => {
			if (searchTimeoutRef.current) {
				clearTimeout(searchTimeoutRef.current);
			}
		};
	}, []);

	const handleRecalculateCosts = useCallback(async () => {
		try {
			const response = await recalculateCosts({ filters }).unwrap();
			await fetchLogs();
			await fetchStats();
			setOpenMoreActionsPopover(false);
			toast.success(`Recalculated costs for ${response.updated} logs`, {
				description: `${response.updated} logs updated, ${response.skipped} logs skipped, ${response.remaining} logs remaining`,
				duration: 5000,
			});
		} catch (err) {
			toast.error(getErrorMessage(err));
		}
	}, [filters, recalculateCosts, fetchLogs, fetchStats]);

	const handleSearchChange = useCallback(
		(value: string) => {
			setLocalSearch(value);

			// Clear existing timeout
			if (searchTimeoutRef.current) {
				clearTimeout(searchTimeoutRef.current);
			}

			// Use filtersRef.current so search is applied on top of current filters (search within filtered results)
			searchTimeoutRef.current = setTimeout(() => {
				onFiltersChange({ ...filtersRef.current, content_search: value });
			}, 500); // 500ms debounce
		},
		[onFiltersChange],
	);

	const handleFilterChange = useCallback(
		(key: keyof LogFiltersType, values: string[] | boolean) => {
			onFiltersChange({ ...filters, [key]: values });
		},
		[filters, onFiltersChange],
	);

	return (
		<div className="flex items-center justify-between space-x-2">
			<Button variant={"outline"} size="sm" className="h-7.5" onClick={() => onLiveToggle(!liveEnabled)}>
				{liveEnabled ? (
					<>
						<Pause className="h-4 w-4" />
						Live updates
					</>
				) : (
					<>
						<Play className="h-4 w-4" />
						Live updates
					</>
				)}
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
				dateTime={{
					from: startTime,
					to: endTime,
				}}
				onDateTimeUpdate={(p) => {
					setStartTime(p.from);
					setEndTime(p.to);
					onFiltersChange({
						...filters,
						start_time: p.from?.toISOString(),
						end_time: p.to?.toISOString(),
					});
				}}
				preDefinedPeriods={LOG_TIME_PERIODS}
				onPredefinedPeriodChange={(periodValue) => {
					if (!periodValue) return;
					const { from, to } = getRangeForPeriod(periodValue);
					setStartTime(from);
					setEndTime(to);
					onFiltersChange({
						...filters,
						start_time: from.toISOString(),
						end_time: to.toISOString(),
					});
				}}
			/>
			<FilterPopover filters={filters} onFilterChange={handleFilterChange} showMissingCost />
			<Popover open={openMoreActionsPopover} onOpenChange={setOpenMoreActionsPopover}>
				<PopoverTrigger asChild>
					<Button variant="outline" size="sm" className="h-7.5">
						<MoreVertical className="h-4 w-4" />
					</Button>
				</PopoverTrigger>
				<PopoverContent className="bg-accent w-[250px] p-2" align="end">
					<Command>
						<CommandList>
							<CommandItem className="hover:bg-accent/50 cursor-pointer" onSelect={handleRecalculateCosts}>
								<Calculator className="text-muted-foreground size-4" />
								<div className="flex flex-col">
									<span className="text-sm">Recalculate costs</span>
									<span className="text-muted-foreground text-xs">For all logs that don't have a cost</span>
								</div>
							</CommandItem>
						</CommandList>
					</Command>
				</PopoverContent>
			</Popover>
		</div>
	);
}
