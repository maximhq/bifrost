import { ColumnConfigDropdown, type ColumnConfigEntry } from "@/components/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Command, CommandItem, CommandList } from "@/components/ui/command";
import { DateTimePickerWithRange } from "@/components/ui/datePickerWithRange";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuRadioGroup,
	DropdownMenuRadioItem,
	DropdownMenuTrigger,
} from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useTimezonePreference } from "@/lib/hooks/useTimezonePreference";
import type { RelayLog, RelayLogFilters, RelayLogPagination } from "@/lib/types/relays";
import { formatLogSearchInput, isLogIdSearch, type LogSearchMode, parseLogSearchInput } from "@/lib/utils/logSearch";
import { getRangeForPeriod, TIME_PERIODS } from "@/lib/utils/timeRange";
import { ChevronDown, Download, MoreVertical, Radio, RefreshCw, Search } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { RelayLogsExportDialog } from "./relayLogsExportDialog";

/**
 * The relay search box has two modes: `auto` (a UUID looks up request_id, anything else
 * runs the metadata search) and `request_id` (the input verbatim as an id). The LLM page's
 * third mode, content, has no relay equivalent: the relay `search` filter is the only text
 * search there is, so it is what `auto` falls through to.
 */
export type RelayLogSearchMode = Extract<LogSearchMode, "auto" | "request_id">;

interface RelayHeaderViewProps {
	filters: RelayLogFilters;
	onFiltersChange: (filters: RelayLogFilters) => void;
	period: string;
	onPeriodChange: (period?: string, from?: Date, to?: Date) => void;
	polling: boolean;
	onPollToggle: (enabled: boolean) => void;
	onRefresh: () => void;
	loading?: boolean;
	/** Total relay logs matching the current filters/time window (stats.total_requests) */
	totalLogs: number;
	/** Column config for the ColumnConfigDropdown */
	columnEntries: ColumnConfigEntry[];
	columnLabels: Record<string, string>;
	onToggleColumnVisibility: (id: string) => void;
	onResetColumns: () => void;
	currentPageLogs?: RelayLog[];
	sortBy?: RelayLogPagination["sort_by"];
	order?: RelayLogPagination["order"];
}

export function RelayHeaderView({
	filters,
	onFiltersChange,
	period,
	onPeriodChange,
	polling,
	onPollToggle,
	onRefresh,
	loading = false,
	totalLogs,
	columnEntries,
	columnLabels,
	onToggleColumnVisibility,
	onResetColumns,
	currentPageLogs = [],
	sortBy = "timestamp",
	order = "desc",
}: RelayHeaderViewProps) {
	const { t } = useTranslation("observability");
	const searchPlaceholders: Record<RelayLogSearchMode, string> = {
	auto: t("relayLogs.header.searchAuto"),
	request_id: t("logs.searchPlaceholders.request_id"),
	};
	const searchModeLabels: Record<RelayLogSearchMode, string> = {
	auto: t("logs.searchModes.auto"),
	request_id: t("logs.searchModes.request_id"),
	};
	const [openMoreActionsPopover, setOpenMoreActionsPopover] = useState(false);
	const [exportDialogOpen, setExportDialogOpen] = useState(false);
	const [searchMode, setSearchMode] = useState<RelayLogSearchMode>("auto");
	const searchModeRef = useRef<RelayLogSearchMode>(searchMode);
	useEffect(() => {
		searchModeRef.current = searchMode;
	}, [searchMode]);
	const [localSearch, setLocalSearch] = useState(() =>
		formatLogSearchInput({ request_id: filters.request_id, content_search: filters.search }),
	);
	const [timezone, setTimezone] = useTimezonePreference();
	const [startTime, setStartTime] = useState<Date | undefined>(filters.start_time ? new Date(filters.start_time) : undefined);
	const [endTime, setEndTime] = useState<Date | undefined>(filters.end_time ? new Date(filters.end_time) : undefined);
	const searchTimeoutRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
	const filtersRef = useRef<RelayLogFilters>(filters);

	useEffect(() => {
		filtersRef.current = filters;
	}, [filters]);
	const { search: searchFilter, request_id: requestIDFilter } = filters;
	useEffect(() => {
		// Not keyed on the mode: switching it re-runs the search with what is already typed,
		// and re-syncing here would blank the box against the pre-switch filters first.
		setLocalSearch(formatLogSearchInput({ request_id: requestIDFilter, content_search: searchFilter }, searchModeRef.current));
	}, [searchFilter, requestIDFilter]);
	useEffect(() => {
		setStartTime(filters.start_time ? new Date(filters.start_time) : undefined);
		setEndTime(filters.end_time ? new Date(filters.end_time) : undefined);
	}, [filters.start_time, filters.end_time]);
	useEffect(() => {
		return () => {
			if (searchTimeoutRef.current) clearTimeout(searchTimeoutRef.current);
		};
	}, []);

	const handleSearchChange = useCallback(
		(value: string, mode: RelayLogSearchMode) => {
			setLocalSearch(value);
			if (searchTimeoutRef.current) clearTimeout(searchTimeoutRef.current);
			// A request id is an exact lookup with nothing to debounce; free text waits for the typist.
			const { request_id = "", content_search = "" } = parseLogSearchInput(value, mode);
			searchTimeoutRef.current = setTimeout(
				() => {
					onFiltersChange({ ...filtersRef.current, request_id: request_id || undefined, search: content_search || undefined });
				},
				request_id ? 0 : 500,
			);
		},
		[onFiltersChange],
	);

	// Switching the mode re-runs whatever is already typed under the new mode.
	const handleSearchModeChange = useCallback(
		(mode: RelayLogSearchMode) => {
			setSearchMode(mode);
			handleSearchChange(localSearch, mode);
		},
		[handleSearchChange, localSearch],
	);

	// From the live input, so the badge appears as soon as an id is pasted. Only auto needs
	// it: the pinned mode already says what it is on the trigger.
	const isIdSearch = searchMode === "auto" && isLogIdSearch(localSearch);

	return (
		// justify-between only once the row can hold everything: while the controls wrap,
		// spreading them pushes the last row's icon buttons to opposite edges of the card.
		<div className="flex grow flex-wrap items-center justify-start gap-2 @4xl/relay-logs-header:flex-nowrap @4xl/relay-logs-header:justify-between">
			<Button
				variant="outline"
				size="sm"
				className="h-7.5 disabled:opacity-100"
				onClick={onRefresh}
				disabled={loading}
				data-testid="relay-logs-header-refresh-btn"
			>
				<RefreshCw className={`h-4 w-4 ${loading ? "animate-spin" : ""}`} />
				{t("labels.refresh")}
			</Button>
			<Button
				variant={polling ? "default" : "outline"}
				size="sm"
				className="h-7.5"
				onClick={() => onPollToggle(!polling)}
				data-testid="relay-logs-header-live-btn"
			>
				{polling ? <Radio className="h-4 w-4 animate-pulse" /> : <Radio className="h-4 w-4" />}
				{t("labels.live")}
			</Button>
			{/* Full width while the row wraps, so the search field owns its own line instead of
			    squeezing to its 12rem minimum beside the date picker. */}
			<div className="border-input flex h-7.5 min-w-[12rem] flex-1 basis-full items-center overflow-hidden rounded-sm border @4xl/relay-logs-header:basis-auto">
				<Search className="mr-2 ml-2 size-4 shrink-0" />
				<Input
					type="text"
					data-testid="relay-logs-search-input"
					className="!h-7 rounded-tl-none rounded-tr-sm rounded-br-sm rounded-bl-none border-none bg-slate-50 shadow-none outline-none focus-visible:ring-0 dark:bg-zinc-900"
					placeholder={searchPlaceholders[searchMode]}
					value={localSearch}
					onChange={(e) => handleSearchChange(e.target.value, searchMode)}
				/>
				{isIdSearch && (
					<Tooltip>
						<TooltipTrigger asChild>
							<Badge variant="secondary" className="mr-2 shrink-0 px-1.5 py-0 text-[10px]" data-testid="relay-logs-search-id-badge">
								ID
							</Badge>
						</TooltipTrigger>
						<TooltipContent sideOffset={6} className="max-w-64">
							{t("relayLogs.header.idLookupHint")}
						</TooltipContent>
					</Tooltip>
				)}
				<DropdownMenu>
					<DropdownMenuTrigger asChild>
						<Button
							variant="ghost"
							size="sm"
							className="text-muted-foreground h-7 shrink-0 rounded-none text-xs"
							title={t("relayLogs.header.searchModeHint")}
							data-testid="relay-logs-search-mode-trigger"
						>
							{searchModeLabels[searchMode]}
							<ChevronDown className="size-3" />
						</Button>
					</DropdownMenuTrigger>
					<DropdownMenuContent align="end" className="w-44">
						<DropdownMenuRadioGroup value={searchMode} onValueChange={(value) => handleSearchModeChange(value as RelayLogSearchMode)}>
							<DropdownMenuRadioItem value="auto" className="text-xs" data-testid="relay-logs-search-mode-auto">
								{t("logs.searchModeOptions.auto")}
							</DropdownMenuRadioItem>
							<DropdownMenuRadioItem value="request_id" className="text-xs" data-testid="relay-logs-search-mode-request-id">
								{t("logs.searchModeOptions.request_id")}
							</DropdownMenuRadioItem>
						</DropdownMenuRadioGroup>
					</DropdownMenuContent>
				</DropdownMenu>
			</div>
			<DateTimePickerWithRange
				buttonClassName="w-auto"
				triggerTestId="relay-logs-filter-date-range"
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
			<Tooltip>
				<TooltipTrigger asChild>
					<Button
						data-testid="relay-logs-export-btn"
						variant="outline"
						size="sm"
						className="h-7.5"
						onClick={() => setExportDialogOpen(true)}
						aria-label={t("logs.export.exportCsv")}
					>
						<Download className="h-4 w-4" />
						<span className="hidden @5xl/relay-logs-header:inline">{t("logs.export.exportCsv")}</span>
					</Button>
				</TooltipTrigger>
				<TooltipContent sideOffset={6}>{t("logs.export.exportCsv")}</TooltipContent>
			</Tooltip>

			<Popover open={openMoreActionsPopover} onOpenChange={setOpenMoreActionsPopover}>
				<PopoverTrigger asChild>
					<Button variant="outline" size="sm" className="h-7.5 w-7.5" aria-label={t("logs.moreActions")} data-testid="relay-logs-more-menu">
						<MoreVertical className="h-4 w-4" aria-hidden="true" />
					</Button>
				</PopoverTrigger>
				<PopoverContent className="bg-accent w-[250px] p-2" align="end">
					<Command>
						<CommandList>
							<CommandItem
								className="hover:bg-accent/50 cursor-pointer"
								data-testid="relay-logs-more-menu-export"
								onSelect={() => {
									setOpenMoreActionsPopover(false);
									setExportDialogOpen(true);
								}}
							>
								<Download className="text-muted-foreground size-4" />
								<div className="flex flex-col">
									<span className="text-sm">{t("logs.export.exportCsv")}</span>
									<span className="text-muted-foreground text-xs">{t("relayLogs.header.exportDescription")}</span>
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

			<RelayLogsExportDialog
				open={exportDialogOpen}
				onOpenChange={setExportDialogOpen}
				filters={filters}
				currentPageLogs={currentPageLogs}
				totalLogs={totalLogs}
				sortBy={sortBy}
				order={order}
			/>
		</div>
	);
}