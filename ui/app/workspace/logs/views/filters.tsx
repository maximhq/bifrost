import { Button } from "@/components/ui/button";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { RequestTypeLabels, RequestTypes, Statuses } from "@/lib/constants/logs";
import { useGetAvailableFilterDataQuery, useGetProvidersQuery } from "@/lib/store";
import type { LogFilters } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { Check, FilterIcon, Search } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

interface LogFiltersProps {
	filters: LogFilters;
	onFiltersChange: (filters: LogFilters) => void;
}

export function LogFilters({ filters, onFiltersChange }: LogFiltersProps) {
	const [open, setOpen] = useState(false);
	const [localSearch, setLocalSearch] = useState(filters.content_search || "");
	const searchTimeoutRef = useRef<NodeJS.Timeout | undefined>(undefined);

	// Use RTK Query to fetch available models
	const { data: providersData, isLoading: providersLoading } = useGetProvidersQuery();
	const { data: filterData, isLoading: filterDataLoading } = useGetAvailableFilterDataQuery();

	const availableProviders = providersData || [];
	const availableModels = filterData?.models || [];
	const availableSelectedKeys = filterData?.selected_keys || [];
	const availableVirtualKeys = filterData?.virtual_keys || [];

	// Create mappings from name to ID for keys and virtual keys
	const selectedKeyNameToId = new Map(availableSelectedKeys.map((key) => [key.name, key.id]));
	const virtualKeyNameToId = new Map(availableVirtualKeys.map((key) => [key.name, key.id]));

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

			// Set new timeout
			searchTimeoutRef.current = setTimeout(() => {
				onFiltersChange({ ...filters, content_search: value });
			}, 500); // 500ms debounce
		},
		[filters, onFiltersChange],
	);

	const handleFilterSelect = (category: keyof typeof FILTER_OPTIONS, value: string) => {
		const filterKeyMap: Record<keyof typeof FILTER_OPTIONS, keyof LogFilters> = {
			Status: "status",
			Providers: "providers",
			Type: "objects",
			Models: "models",
			"Selected Keys": "selected_key_ids",
			"Virtual Keys": "virtual_key_ids",
		};

		const filterKey = filterKeyMap[category];
		let valueToStore = value;

		// Convert name to ID for keys and virtual keys
		if (category === "Selected Keys") {
			valueToStore = selectedKeyNameToId.get(value) || value;
		} else if (category === "Virtual Keys") {
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
		const filterKeyMap: Record<keyof typeof FILTER_OPTIONS, keyof LogFilters> = {
			Status: "status",
			Providers: "providers",
			Type: "objects",
			Models: "models",
			"Selected Keys": "selected_key_ids",
			"Virtual Keys": "virtual_key_ids",
		};

		const filterKey = filterKeyMap[category];
		const currentValues = filters[filterKey];

		// For keys and virtual keys, convert name to ID before checking
		let valueToCheck = value;
		if (category === "Selected Keys") {
			valueToCheck = selectedKeyNameToId.get(value) || value;
		} else if (category === "Virtual Keys") {
			valueToCheck = virtualKeyNameToId.get(value) || value;
		}

		return Array.isArray(currentValues) && currentValues.includes(valueToCheck);
	};

	const getSelectedCount = () => {
		return Object.entries(filters).reduce((count, [key, value]) => {
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
	} as const;

	return (
		<div className="flex items-center justify-between space-x-4">
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

			<Popover open={open} onOpenChange={setOpen}>
				<PopoverTrigger asChild>
					<Button variant="outline" size="sm" className="h-9">
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
												(category === "Providers" && providersLoading) ||
												(category === "Models" && filterDataLoading) ||
												(category === "Selected Keys" && filterDataLoading) ||
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
		</div>
	);
}
