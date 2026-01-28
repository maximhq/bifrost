import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { DateTimePickerWithRange } from "@/components/ui/datePickerWithRange";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { RequestTypeLabels, RequestTypes, Statuses } from "@/lib/constants/logs";
import { getErrorMessage, useGetAvailableFilterDataQuery, useGetProvidersQuery, useRecalculateLogCostsMutation } from "@/lib/store";
import type { LogFilters as LogFiltersType } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { Calculator, Check, FilterIcon, MoreVertical, Pause, Play, Search } from "lucide-react";
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

interface LogFiltersProps {
	filters: LogFiltersType;
	onFiltersChange: (filters: LogFiltersType) => void;
	liveEnabled: boolean;
	onLiveToggle: (enabled: boolean) => void;
	fetchLogs: () => Promise<void>;
	fetchStats: () => Promise<void>;
}

export function LogFilters({ filters, onFiltersChange, liveEnabled, onLiveToggle, fetchLogs, fetchStats }: LogFiltersProps) {
	const [openFiltersPopover, setOpenFiltersPopover] = useState(false);
	const [openMoreActionsPopover, setOpenMoreActionsPopover] = useState(false);
	const [localSearch, setLocalSearch] = useState(filters.content_search || "");
	const searchTimeoutRef = useRef<NodeJS.Timeout | undefined>(undefined);
	const [recalculateCosts, { isLoading: recalculating }] = useRecalculateLogCostsMutation();

	// Convert ISO strings from filters to Date objects for the DateTimePicker
	const [startTime, setStartTime] = useState<Date | undefined>(filters.start_time ? new Date(filters.start_time) : undefined);
	const [endTime, setEndTime] = useState<Date | undefined>(filters.end_time ? new Date(filters.end_time) : undefined);

	// Use RTK Query to fetch available models
	const { data: providersData, isLoading: providersLoading } = useGetProvidersQuery();
	const { data: filterData, isLoading: filterDataLoading } = useGetAvailableFilterDataQuery();

	const availableProviders = providersData || [];
	const availableModels = filterData?.models || [];
	const availableSelectedKeys = filterData?.selected_keys || [];
	const availableVirtualKeys = filterData?.virtual_keys || [];
	const availableRoutingRules = filterData?.routing_rules || [];

	// Create mappings from name to ID for keys and virtual keys
	const selectedKeyNameToId = new Map(availableSelectedKeys.map((key) => [key.name, key.id]));
	const virtualKeyNameToId = new Map(availableVirtualKeys.map((key) => [key.name, key.id]));
	const routingRuleNameToId = new Map(availableRoutingRules.map((rule) => [rule.name, rule.id]));

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

			// Set new timeout
			searchTimeoutRef.current = setTimeout(() => {
				onFiltersChange({ ...filters, content_search: value });
			}, 500); // 500ms debounce
		},
		[filters, onFiltersChange],
	);

	const handleFilterSelect = (category: keyof typeof FILTER_OPTIONS, value: string) => {
		const filterKeyMap: Record<keyof typeof FILTER_OPTIONS, keyof LogFiltersType> = {
			Status: "status",
			Providers: "providers",
			Type: "objects",
			Models: "models",
			"Selected Keys": "selected_key_ids",
			"Virtual Keys": "virtual_key_ids",
			"Routing Rules": "routing_rule_ids",
		};

		const filterKey = filterKeyMap[category];
		let valueToStore = value;

		// Convert name to ID for keys and virtual keys
		if (category === "Selected Keys") {
			valueToStore = selectedKeyNameToId.get(value) || value;
		} else if (category === "Virtual Keys") {
			valueToStore = virtualKeyNameToId.get(value) || value;
		} else if (category === "Routing Rules") {
			valueToStore = routingRuleNameToId.get(value) || value;
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
		const filterKeyMap: Record<keyof typeof FILTER_OPTIONS, keyof LogFiltersType> = {
			Status: "status",
			Providers: "providers",
			Type: "objects",
			Models: "models",
			"Selected Keys": "selected_key_ids",
			"Virtual Keys": "virtual_key_ids",
			"Routing Rules": "routing_rule_ids",
		};

		const filterKey = filterKeyMap[category];
		const currentValues = filters[filterKey];

		// For keys and virtual keys, convert name to ID before checking
		let valueToCheck = value;
		if (category === "Selected Keys") {
			valueToCheck = selectedKeyNameToId.get(value) || value;
		} else if (category === "Virtual Keys") {
			valueToCheck = virtualKeyNameToId.get(value) || value;
		} else if (category === "Routing Rules") {
			valueToCheck = routingRuleNameToId.get(value) || value;
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
		Providers: providersLoading ? ["Loading providers..."] : availableProviders.map((provider) => provider.name),
		Type: RequestTypes,
		Models: filterDataLoading ? ["Loading models..."] : availableModels,
		"Selected Keys": filterDataLoading ? ["Loading selected keys..."] : availableSelectedKeys.map((key) => key.name),
		"Virtual Keys": filterDataLoading ? ["Loading virtual keys..."] : availableVirtualKeys.map((key) => key.name),
		"Routing Rules": filterDataLoading ? ["Loading routing rules..."] : availableRoutingRules.map((rule) => rule.name),
	} as const;

	return (
		<div className="flex items-center justify-between space-x-2">
			<Button variant={"outline"} size="sm" className="h-9" onClick={() => onLiveToggle(!liveEnabled)}>
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
			<div className="border-input flex flex-1 items-center gap-2 rounded-sm border">
				<Search className="mr-0.5 ml-2 size-4" />
				<Input
					type="text"
					className="rounded-tl-none rounded-tr-sm rounded-br-sm rounded-bl-none border-none bg-slate-50 shadow-none outline-none focus-visible:ring-0"
					placeholder="Search logs"
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
					<Button variant="outline" size="sm" className="h-9 w-[120px]">
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
							<CommandGroup>
								<CommandItem className="cursor-pointer">
									<Checkbox
										className={cn(
											"border-primary opacity-50",
											filters.missing_cost_only && "bg-primary text-primary-foreground opacity-100",
										)}
										id="missing-cost-toggle"
										checked={!!filters.missing_cost_only}
										onCheckedChange={(checked: boolean) => onFiltersChange({ ...filters, missing_cost_only: checked })}
									/>
									<span className="text-sm">Show missing cost</span>
								</CommandItem>
							</CommandGroup>
							{Object.entries(FILTER_OPTIONS)
								.filter(([_, values]) => values.length > 0)
								.map(([category, values]) => (
									<CommandGroup key={category} heading={category}>
										{values.map((value: string) => {
											const selected = isSelected(category as keyof typeof FILTER_OPTIONS, value);
											const isLoading =
												(category === "Providers" && providersLoading) ||
												(category === "Models" && filterDataLoading) ||
												(category === "Selected Keys" && filterDataLoading) ||
												(category === "Virtual Keys" && filterDataLoading) ||
												(category === "Routing Rules" && filterDataLoading);
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
													<span className={cn("lowercase", isLoading && "text-muted-foreground")}>
														{category === "Type" ? RequestTypeLabels[value as keyof typeof RequestTypeLabels] : value}
													</span>
												</CommandItem>
											);
										})}
									</CommandGroup>
								))}
						</CommandList>
					</Command>
				</PopoverContent>
			</Popover>
			<Popover open={openMoreActionsPopover} onOpenChange={setOpenMoreActionsPopover}>
				<PopoverTrigger asChild>
					<Button variant="outline" size="sm" className="h-9">
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
