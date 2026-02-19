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
		Status: Statuses,
		Providers: providersLoading ? ["Loading providers..."] : availableProviders.map((provider) => provider.name),
		Type: RequestTypes,
		Models: filterDataLoading ? ["Loading models..."] : availableModels,
		"Selected Keys": filterDataLoading ? ["Loading selected keys..."] : availableSelectedKeys.map((key) => key.name),
		"Virtual Keys": filterDataLoading ? ["Loading virtual keys..."] : availableVirtualKeys.map((key) => key.name),
		"Routing Engines": filterDataLoading ? ["Loading routing engines..."] : availableRoutingEngines,
		"Routing Rules": filterDataLoading ? ["Loading routing rules..."] : availableRoutingRules.map((rule) => rule.name),
	} as const;

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

	const handleFilterSelect = (category: FilterCategory, value: string) => {
		const filterKey = filterKeyMap[category];
		let valueToStore = value;

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

		onFilterChange(filterKey, newValues);
	};

	const isSelected = (category: FilterCategory, value: string) => {
		const filterKey = filterKeyMap[category];
		const currentValues = filters[filterKey];

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

	return (
		<Popover open={open} onOpenChange={setOpen}>
			<PopoverTrigger asChild>
				<Button variant="outline" size="sm" className="h-7.5 w-[120px]" data-testid="filters-trigger-button">
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
							.filter(([_, values]) => values.length > 0)
							.map(([category, values]) => (
								<CommandGroup key={category} heading={category}>
									{values.map((value: string) => {
										const selected = isSelected(category as FilterCategory, value);
										const isLoading =
											(category === "Providers" && providersLoading) ||
											(category === "Models" && filterDataLoading) ||
											(category === "Selected Keys" && filterDataLoading) ||
											(category === "Virtual Keys" && filterDataLoading) ||
											(category === "Routing Rules" && filterDataLoading) ||
											(category === "Routing Engines" && filterDataLoading);
										return (
											<CommandItem
												key={value}
												data-testid={`filter-item-${category.toLowerCase().replace(/\s+/g, "-")}-${value}`}
												onSelect={() => !isLoading && handleFilterSelect(category as FilterCategory, value)}
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
													{category === "Type" ? RequestTypeLabels[value as keyof typeof RequestTypeLabels] :
														category === "Routing Engines" ? (RoutingEngineUsedLabels[value as keyof typeof RoutingEngineUsedLabels] ?? value) : value}
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
	);
}
