import { Button } from "@/components/ui/button";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { DateTimePickerWithRange } from "@/components/ui/datePickerWithRange";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Statuses } from "@/lib/constants/logs";
import { useGetMCPLogsFilterDataQuery } from "@/lib/store";
import type { MCPToolLogFilters } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { Check, FilterIcon, Pause, Play, Search } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

interface MCPLogFiltersProps {
	filters: MCPToolLogFilters;
	onFiltersChange: (filters: MCPToolLogFilters) => void;
	liveEnabled: boolean;
	onLiveToggle: (enabled: boolean) => void;
}

export function MCPLogFilters({ filters, onFiltersChange, liveEnabled, onLiveToggle }: MCPLogFiltersProps) {
	const [openFiltersPopover, setOpenFiltersPopover] = useState(false);
	const [localSearch, setLocalSearch] = useState(filters.content_search || "");
	const searchTimeoutRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
	const filtersRef = useRef<MCPToolLogFilters>(filters);

	// Convert ISO strings from filters to Date objects for the DateTimePicker
	const [startTime, setStartTime] = useState<Date | undefined>(filters.start_time ? new Date(filters.start_time) : undefined);
	const [endTime, setEndTime] = useState<Date | undefined>(filters.end_time ? new Date(filters.end_time) : undefined);

	// Use RTK Query to fetch available filter data
	const { data: filterData, isLoading: filterDataLoading } = useGetMCPLogsFilterDataQuery();

	const availableToolNames = filterData?.tool_names || [];
	const availableServerLabels = filterData?.server_labels || [];
	const availableVirtualKeys = filterData?.virtual_keys || [];

	// Create mapping from name to ID for virtual keys
	const virtualKeyNameToId = new Map(availableVirtualKeys.map((key) => [key.name, key.id]));

	// Keep filtersRef in sync with filters prop
	useEffect(() => {
		filtersRef.current = filters;
	}, [filters]);

	// Sync localSearch when filters.content_search changes externally
	useEffect(() => {
		setLocalSearch(filters.content_search || "");
	}, [filters.content_search]);

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

	const handleSearchChange = useCallback(
		(value: string) => {
			setLocalSearch(value);

			// Clear existing timeout
			if (searchTimeoutRef.current) {
				clearTimeout(searchTimeoutRef.current);
			}

			// Set new timeout - use filtersRef.current to avoid stale closure
			searchTimeoutRef.current = setTimeout(() => {
				onFiltersChange({ ...filtersRef.current, content_search: value });
			}, 500); // 500ms debounce
		},
		[onFiltersChange],
	);

	const handleFilterSelect = (category: keyof typeof FILTER_OPTIONS, value: string) => {
		const filterKeyMap: Record<keyof typeof FILTER_OPTIONS, keyof MCPToolLogFilters> = {
			Status: "status",
			"Tool Names": "tool_names",
			Servers: "server_labels",
			"Virtual Keys": "virtual_key_ids",
		};

		const filterKey = filterKeyMap[category];
		let valueToStore = value;

		// Convert name to ID for virtual keys
		if (category === "Virtual Keys") {
			valueToStore = virtualKeyNameToId.get(value) || value;
		}

		const currentValues = (filters[filterKey] as string[]) || [];
		const newValues = currentValues.includes(valueToStore)
			? currentValues.filter((v) => v !== valueToStore)
			: [...currentValues, valueToStore];

		onFiltersChange({
			...filters,
			[filterKey]: newValues,
		});
	};

	const isSelected = (category: keyof typeof FILTER_OPTIONS, value: string) => {
		const filterKeyMap: Record<keyof typeof FILTER_OPTIONS, keyof MCPToolLogFilters> = {
			Status: "status",
			"Tool Names": "tool_names",
			Servers: "server_labels",
			"Virtual Keys": "virtual_key_ids",
		};

		const filterKey = filterKeyMap[category];
		const currentValues = filters[filterKey];

		// For virtual keys, convert name to ID before checking
		let valueToCheck = value;
		if (category === "Virtual Keys") {
			valueToCheck = virtualKeyNameToId.get(value) || value;
		}

		return Array.isArray(currentValues) && currentValues.includes(valueToCheck);
	};

	const getSelectedCount = () => {
		// Exclude timestamp filters and content_search from the count
		const excludedKeys = ["start_time", "end_time", "content_search"];

		return Object.entries(filters).reduce((count, [key, value]) => {
			if (excludedKeys.includes(key)) {
				return count;
			}
			if (Array.isArray(value)) {
				return count + value.length;
			}
			return count + (value ? 1 : 0);
		}, 0);
	};

	const FILTER_OPTIONS = {
		Status: Statuses,
		"Tool Names": filterDataLoading ? ["Loading..."] : availableToolNames,
		Servers: filterDataLoading ? ["Loading..."] : availableServerLabels,
		"Virtual Keys": filterDataLoading ? ["Loading virtual keys..."] : availableVirtualKeys.map((key) => key.name),
	} as const;

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
					className="!h-7 rounded-tl-none rounded-tr-sm rounded-br-sm rounded-bl-none border-none bg-slate-50 shadow-none outline-none focus-visible:ring-0 dark:bg-zinc-900"
					placeholder="Search MCP logs"
					value={localSearch}
					onChange={(e) => handleSearchChange(e.target.value)}
				/>
			</div>

			<DateTimePickerWithRange
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
			/>
			<Popover open={openFiltersPopover} onOpenChange={setOpenFiltersPopover}>
				<PopoverTrigger asChild>
					<Button variant="outline" size="sm" className="h-7.5 w-[120px]">
						<FilterIcon className="h-4 w-4" />
						Filters
						{getSelectedCount() > 0 && (
							<span className="bg-primary/10 flex h-6 w-6 items-center justify-center rounded-full text-xs font-normal">
								{getSelectedCount()}
							</span>
						)}
					</Button>
				</PopoverTrigger>
				<PopoverContent className="w-[200px] p-0" align="end">
					<Command>
						<CommandInput placeholder="Search filters..." />
						<CommandList>
							<CommandEmpty>No filters found.</CommandEmpty>
							{Object.entries(FILTER_OPTIONS)
								.filter(([_, values]) => values.length > 0)
								.map(([category, values]) => (
									<CommandGroup key={category} heading={category}>
										{values.map((value) => {
											const selected = isSelected(category as keyof typeof FILTER_OPTIONS, value);
											const isLoading =
												(category === "Tool Names" && filterDataLoading) ||
												(category === "Servers" && filterDataLoading) ||
												(category === "Virtual Keys" && filterDataLoading);
											return (
												<CommandItem
													key={value}
													onSelect={() => !isLoading && handleFilterSelect(category as keyof typeof FILTER_OPTIONS, value)}
													disabled={isLoading}
												>
													<div
														className={cn(
															"border-primary mr-2 flex h-4 w-4 items-center justify-center rounded-sm border",
															selected ? "bg-primary text-primary-foreground" : "opacity-50 [&_svg]:invisible",
														)}
													>
														{isLoading ? (
															<div className="border-primary h-3 w-3 animate-spin rounded-full border border-t-transparent" />
														) : (
															<Check className="text-primary-foreground size-3" />
														)}
													</div>
													<span className={cn("lowercase", isLoading && "text-muted-foreground")}>{value}</span>
												</CommandItem>
											);
										})}
									</CommandGroup>
								))}
						</CommandList>
					</Command>
				</PopoverContent>
			</Popover>
		</div>
	);
}
