import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { RequestTypeLabels, RequestTypes, RoutingEngineUsedLabels, Statuses } from "@/lib/constants/logs";
import { useGetAvailableFilterDataQuery, useGetProvidersQuery } from "@/lib/store";
import type { LogFilters as LogFiltersType } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { Check, FilterIcon } from "lucide-react";
import { useState } from "react";

interface FilterPopoverProps {
	filters: LogFiltersType;
	onFilterChange: (key: keyof LogFiltersType, values: string[] | boolean) => void;
	showMissingCost?: boolean;
}

export function FilterPopover({ filters, onFilterChange, showMissingCost }: FilterPopoverProps) {
	const [open, setOpen] = useState(false);

	const { data: providersData, isLoading: providersLoading } = useGetProvidersQuery();
	const { data: filterData, isLoading: filterDataLoading } = useGetAvailableFilterDataQuery();

	const availableProviders = providersData || [];
	const availableModels = filterData?.models || [];
	const availableSelectedKeys = filterData?.selected_keys || [];
	const availableVirtualKeys = filterData?.virtual_keys || [];
	const availableRoutingRules = filterData?.routing_rules || [];
	const availableRoutingEngines = filterData?.routing_engines || [];

	// Create mappings from name to ID for keys, virtual keys, and routing rules
	const selectedKeyNameToId = new Map(availableSelectedKeys.map((key) => [key.name, key.id]));
	const virtualKeyNameToId = new Map(availableVirtualKeys.map((key) => [key.name, key.id]));
	const routingRuleNameToId = new Map(availableRoutingRules.map((rule) => [rule.name, rule.id]));

	const FILTER_OPTIONS = {
		Status: [...Statuses],
		Providers: providersLoading ? [] : availableProviders.map((provider) => provider.name),
		Type: [...RequestTypes],
		Models: filterDataLoading ? [] : availableModels,
		"Selected Keys": filterDataLoading ? [] : availableSelectedKeys.map((key) => key.name),
		"Virtual Keys": filterDataLoading ? [] : availableVirtualKeys.map((key) => key.name),
		"Routing Engines": filterDataLoading ? [] : availableRoutingEngines,
		"Routing Rules": filterDataLoading ? [] : availableRoutingRules.map((rule) => rule.name),
	};

	const isCategoryLoading = (category: string) =>
		(category === "Providers" && providersLoading) ||
		(category !== "Status" && category !== "Type" && category !== "Providers" && filterDataLoading);

	type FilterCategory = keyof typeof FILTER_OPTIONS;

	const filterKeyMap: Record<FilterCategory, keyof LogFiltersType> = {
		Status: "status",
		Providers: "providers",
		Type: "objects",
		Models: "models",
		"Selected Keys": "selected_key_ids",
		"Virtual Keys": "virtual_key_ids",
		"Routing Rules": "routing_rule_ids",
		"Routing Engines": "routing_engine_used",
	};

	const resolveValueForCategory = (category: FilterCategory, value: string): string => {
		if (category === "Selected Keys") return selectedKeyNameToId.get(value) || value;
		if (category === "Virtual Keys") return virtualKeyNameToId.get(value) || value;
		if (category === "Routing Rules") return routingRuleNameToId.get(value) || value;
		return value;
	};

	const handleFilterSelect = (category: FilterCategory, value: string) => {
		const filterKey = filterKeyMap[category];
		const resolved = resolveValueForCategory(category, value);

		const currentValues = (filters[filterKey] as string[]) || [];
		const newValues = currentValues.includes(resolved)
			? currentValues.filter((v) => v !== resolved)
			: [...currentValues, resolved];

		onFilterChange(filterKey, newValues);
	};

	const isSelected = (category: FilterCategory, value: string) => {
		const filterKey = filterKeyMap[category];
		const currentValues = filters[filterKey];
		const resolved = resolveValueForCategory(category, value);

		return Array.isArray(currentValues) && currentValues.includes(resolved);
	};

	const excludedKeys = ["start_time", "end_time", "content_search"];
	const selectedCount = Object.entries(filters).reduce((count, [key, value]) => {
		if (excludedKeys.includes(key)) {
			return count;
		}
		if (Array.isArray(value)) {
			return count + value.length;
		}
		return count + (value ? 1 : 0);
	}, 0);

	return (
		<Popover open={open} onOpenChange={setOpen}>
			<PopoverTrigger asChild>
				<Button variant="outline" size="sm" className="h-7.5 w-[120px]" data-testid="filters-trigger-button">
					<FilterIcon className="h-4 w-4" />
					Filters
					{selectedCount > 0 && (
						<span className="bg-primary/10 flex h-6 w-6 items-center justify-center rounded-full text-xs font-normal">
							{selectedCount}
						</span>
					)}
				</Button>
			</PopoverTrigger>
			<PopoverContent className="w-[200px] p-0" align="end">
				<Command>
					<CommandInput placeholder="Search filters..." data-testid="filters-search-input" />
					<CommandList>
						<CommandEmpty>No filters found.</CommandEmpty>
						{showMissingCost && (
							<CommandGroup>
								<CommandItem className="cursor-pointer">
									<Checkbox
										className={cn(
											"border-primary opacity-50",
											filters.missing_cost_only && "bg-primary text-primary-foreground opacity-100",
										)}
										id="missing-cost-toggle"
										checked={!!filters.missing_cost_only}
										onCheckedChange={(checked: boolean) => onFilterChange("missing_cost_only", checked)}
									/>
									<span className="text-sm">Show missing cost</span>
								</CommandItem>
							</CommandGroup>
						)}
						{Object.entries(FILTER_OPTIONS)
							.filter(([category, values]) => values.length > 0 || isCategoryLoading(category))
							.map(([category, values]) => (
								<CommandGroup key={category} heading={category}>
									{isCategoryLoading(category) && values.length === 0 ? (
										<CommandItem disabled>
											<div className="border-primary mr-2 flex h-4 w-4 items-center justify-center">
												<div className="border-primary h-3 w-3 animate-spin rounded-full border border-t-transparent" />
											</div>
											<span className="text-muted-foreground text-sm">Loading...</span>
										</CommandItem>
									) : (
										values.map((value: string) => {
											const selected = isSelected(category as FilterCategory, value);
											return (
												<CommandItem
													key={value}
													data-testid={`filter-item-${category.toLowerCase().replace(/\s+/g, "-")}-${value}`}
													onSelect={() => handleFilterSelect(category as FilterCategory, value)}
												>
													<div
														className={cn(
															"border-primary mr-2 flex h-4 w-4 items-center justify-center rounded-sm border",
															selected ? "bg-primary text-primary-foreground" : "opacity-50 [&_svg]:invisible",
														)}
													>
														<Check className="text-primary-foreground size-3" />
													</div>
													<span className={cn(category === "Status" && "lowercase")}>
														{category === "Type" ? RequestTypeLabels[value as keyof typeof RequestTypeLabels] :
															category === "Routing Engines" ? (RoutingEngineUsedLabels[value as keyof typeof RoutingEngineUsedLabels] ?? value) : value}
													</span>
												</CommandItem>
											);
										})
									)}
								</CommandGroup>
							))}
					</CommandList>
				</Command>
			</PopoverContent>
		</Popover>
	);
}
