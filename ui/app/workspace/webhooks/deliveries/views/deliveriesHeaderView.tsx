import { ColumnConfigDropdown, type ColumnConfigEntry } from "@/components/table";
import { Button } from "@/components/ui/button";
import { DateTimePickerWithRange } from "@/components/ui/datePickerWithRange";
import { Input } from "@/components/ui/input";
import { useTimezonePreference } from "@/lib/hooks/useTimezonePreference";
import type { WebhookDeliveryFilters } from "@/lib/types/webhooks";
import { getRangeForPeriod, TIME_PERIODS } from "@/lib/utils/timeRange";
import { Radio, RefreshCw, Search } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { initialIdSearchField } from "../deliveries.page.utils";

interface DeliveriesHeaderViewProps {
	filters: WebhookDeliveryFilters;
	onFiltersChange: (filters: WebhookDeliveryFilters) => void;
	period: string;
	onPeriodChange: (period?: string, from?: Date, to?: Date) => void;
	polling: boolean;
	onPollToggle: (enabled: boolean) => void;
	onRefresh: () => void;
	loading?: boolean;
	columnEntries: ColumnConfigEntry[];
	columnLabels: Record<string, string>;
	onToggleColumnVisibility: (id: string) => void;
	onResetColumns: () => void;
}

// A pasted id is looked up as either a request id or a delivery id — the two
// are indistinguishable by shape, and the user rarely knows which they hold.
// Both go on the wire; the store ANDs them, so one blank means "ignore".
const idSearchValue = (filters: WebhookDeliveryFilters) => filters.request_id || filters.delivery_id || "";

export function DeliveriesHeaderView({
	filters,
	onFiltersChange,
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
}: DeliveriesHeaderViewProps) {
	const [localSearch, setLocalSearch] = useState(idSearchValue(filters));
	const [timezone, setTimezone] = useTimezonePreference();
	const [startTime, setStartTime] = useState<Date | undefined>(filters.start_time ? new Date(filters.start_time) : undefined);
	const [endTime, setEndTime] = useState<Date | undefined>(filters.end_time ? new Date(filters.end_time) : undefined);
	const searchTimeoutRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
	const filtersRef = useRef<WebhookDeliveryFilters>(filters);

	useEffect(() => {
		filtersRef.current = filters;
	}, [filters]);
	useEffect(() => {
		setLocalSearch(idSearchValue(filters));
	}, [filters]);
	useEffect(() => {
		setStartTime(filters.start_time ? new Date(filters.start_time) : undefined);
		setEndTime(filters.end_time ? new Date(filters.end_time) : undefined);
	}, [filters.start_time, filters.end_time]);
	useEffect(() => {
		return () => {
			if (searchTimeoutRef.current) clearTimeout(searchTimeoutRef.current);
		};
	}, []);

	const [searchField, setSearchField] = useState<"request_id" | "delivery_id">(() => initialIdSearchField(filters));

	const handleSearchChange = useCallback(
		(value: string, field: "request_id" | "delivery_id") => {
			setLocalSearch(value);
			if (searchTimeoutRef.current) clearTimeout(searchTimeoutRef.current);
			searchTimeoutRef.current = setTimeout(() => {
				// Only the selected field is populated; the other is cleared so a
				// stale value from the previous mode can't silently narrow results.
				onFiltersChange({
					...filtersRef.current,
					request_id: field === "request_id" ? value || undefined : undefined,
					delivery_id: field === "delivery_id" ? value || undefined : undefined,
				});
			}, 500);
		},
		[onFiltersChange],
	);

	return (
		<div className="flex grow flex-wrap items-center justify-between gap-2">
			<Button
				variant="outline"
				size="sm"
				className="h-7.5 disabled:opacity-100"
				onClick={onRefresh}
				disabled={loading}
				data-testid="webhook-deliveries-header-refresh-btn"
			>
				<RefreshCw className={`h-4 w-4 ${loading ? "animate-spin" : ""}`} />
				Refresh
			</Button>
			<Button
				variant={polling ? "default" : "outline"}
				size="sm"
				className="h-7.5"
				onClick={() => onPollToggle(!polling)}
				data-testid="webhook-deliveries-header-live-btn"
			>
				<Radio className={`h-4 w-4 ${polling ? "animate-pulse" : ""}`} />
				Live
			</Button>
			<div className="border-input flex h-7.5 min-w-[16rem] flex-1 items-center gap-2 rounded-sm border">
				<Search className="mr-0.5 ml-2 size-4" />
				<Input
					type="text"
					className="!h-7 rounded-none border-none bg-slate-50 shadow-none outline-none focus-visible:ring-0 dark:bg-zinc-900"
					placeholder={searchField === "request_id" ? "Search by request ID" : "Search by delivery ID"}
					value={localSearch}
					onChange={(e) => handleSearchChange(e.target.value, searchField)}
					data-testid="webhook-deliveries-search-input"
				/>
				<Button
					variant="ghost"
					size="sm"
					className="text-muted-foreground h-7 shrink-0 rounded-l-none text-xs"
					onClick={() => {
						const next = searchField === "request_id" ? "delivery_id" : "request_id";
						setSearchField(next);
						handleSearchChange(localSearch, next);
					}}
					title="Switch between request ID and delivery ID lookup"
					data-testid="webhook-deliveries-search-field-toggle"
				>
					{searchField === "request_id" ? "Request ID" : "Delivery ID"}
				</Button>
			</div>
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
	);
}