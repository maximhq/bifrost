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
	onMetadataFilterChange?: (metadataKey: string, value: string | undefined) => void;
	showMissingCost?: boolean;
}

export function FilterPopover({ filters, onFilterChange, onMetadataFilterChange, showMissingCost }: FilterPopoverProps) {
	const [open, setOpen] = useState(false);
	const [customMetadataInputs, setCustomMetadataInputs] = useState<Record<string, string>>({});

	const { data: providersData, isLoading: providersLoading } = useGetProvidersQuery();
	const { data: filterData, isLoading: filterDataLoading } = useGetAvailableFilterDataQuery();

	const availableProviders = providersData || [];
	const availableModels = filterData?.models || [];
	const availableAliases = filterData?.aliases || [];
	const availableSelectedKeys = filterData?.selected_keys || [];
	const availableVirtualKeys = filterData?.virtual_keys || [];
	const availableRoutingRules = filterData?.routing_rules || [];
	const availableRoutingEngines = filterData?.routing_engines || [];
	const availableMetadataKeys = filterData?.metadata_keys || {};

	// Create mappings from name to ALL matching IDs (handles duplicate names from deleted keys)
	const groupByName = (items: { name: string; id: string }[]) => {
		const map = new Map<string, string[]>();
		for (const item of items) {
			const ids = map.get(item.name) || [];
			ids.push(item.id);
			map.set(item.name, ids);
		}
		return map;
	};
	const selectedKeyNameToIds = groupByName(availableSelectedKeys);
	const virtualKeyNameToIds = groupByName(availableVirtualKeys);
	const routingRuleNameToIds = groupByName(availableRoutingRules);

	// Deduplicate by name to avoid React key collisions (e.g. multiple deleted keys with the same name)
	const dedup = (items: { name: string }[]) => [...new Map(items.map((i) => [i.name, i])).values()].map((i) => i.name);

	const FILTER_OPTIONS: Record<string, string[]> = {
		Status: [...Statuses],
		Providers: providersLoading ? [] : availableProviders.map((provider) => provider.name),
		Type: [...RequestTypes],
		Models: filterDataLoading ? [] : availableModels,
		Aliases: filterDataLoading ? [] : availableAliases,
		"Selected Keys": filterDataLoading ? [] : dedup(availableSelectedKeys),
		"Virtual Keys": filterDataLoading ? [] : dedup(availableVirtualKeys),
		"Routing Engines": filterDataLoading ? [] : availableRoutingEngines,
		"Routing Rules": filterDataLoading ? [] : dedup(availableRoutingRules),
	};

	// Add dynamic metadata categories
	for (const [metadataKey, values] of Object.entries(availableMetadataKeys)) {
		FILTER_OPTIONS[`Metadata: ${metadataKey}`] = values;
	}

	const isCategoryLoading = (category: string) =>
		(category === "Providers" && providersLoading) ||
		(category !== "Status" && category !== "Type" && category !== "Providers" && !category.startsWith("Metadata: ") && filterDataLoading);


	const filterKeyMap: Record<string, keyof LogFiltersType> = {
		Status: "status",
		Providers: "providers",
		Type: "objects",
		Models: "models",
		Aliases: "aliases",
		"Selected Keys": "selected_key_ids",
		"Virtual Keys": "virtual_key_ids",
		"Routing Rules": "routing_rule_ids",
		"Routing Engines": "routing_engine_used",
	};

	// Resolves a display name to all matching IDs for key/rule categories
	const resolveValuesForCategory = (category: string, value: string): string[] => {
		if (category === "Selected Keys") return selectedKeyNameToIds.get(value) || [value];
		if (category === "Virtual Keys") return virtualKeyNameToIds.get(value) || [value];
		if (category === "Routing Rules") return routingRuleNameToIds.get(value) || [value];
		return [value];
	};

	const handleFilterSelect = (category: string, value: string) => {
		// Handle metadata categories
		if (category.startsWith("Metadata: ")) {
			const metadataKey = category.replace("Metadata: ", "");
			const currentValue = filters.metadata_filters?.[metadataKey];
			const predefinedValues = FILTER_OPTIONS[category] || [];

			if (currentValue === value) {
				// Deselect - clear the filter and the draft
				onMetadataFilterChange?.(metadataKey, undefined);
				setCustomMetadataInputs((prev) => {
					const updated = { ...prev };
					delete updated[category];
					return updated;
				});
			} else {
				// Select
				onMetadataFilterChange?.(metadataKey, value);
				// Only clear draft if selecting a predefined value (not custom input submission)
				if (predefinedValues.includes(value)) {
					setCustomMetadataInputs((prev) => {
						const updated = { ...prev };
						delete updated[category];
						return updated;
					});
				}
			}
			return;
		}

		const filterKey = filterKeyMap[category];
		const resolvedIds = resolveValuesForCategory(category, value);

		const currentValues = (filters[filterKey] as string[]) || [];
		// Check if ALL resolved IDs are already selected (toggle all together)
		const allSelected = resolvedIds.every((id) => currentValues.includes(id));
		const newValues = allSelected
			? currentValues.filter((v) => !resolvedIds.includes(v))
			: [...currentValues, ...resolvedIds.filter((id) => !currentValues.includes(id))];

		onFilterChange(filterKey, newValues);
	};

	const isSelected = (category: string, value: string) => {
		// Handle metadata categories
		if (category.startsWith("Metadata: ")) {
			const metadataKey = category.replace("Metadata: ", "");
			return filters.metadata_filters?.[metadataKey] === value;
		}

		const filterKey = filterKeyMap[category];
		const currentValues = filters[filterKey];
		const resolvedIds = resolveValuesForCategory(category, value);

		return Array.isArray(currentValues) && resolvedIds.every((id) => currentValues.includes(id));
	};

	// Count unique visible names for ID-based categories (avoids inflated badge when
	// multiple backing IDs share the same display name due to deduplication).
	const countUniqueNames = (ids: string[], nameToIds: Map<string, string[]>): number => {
		const seen = new Set<string>();
		for (const [name, mappedIds] of nameToIds) {
			if (mappedIds.some((id) => ids.includes(id))) {
				seen.add(name);
			}
		}
		return seen.size;
	};
	const dedupedCountKeys: Record<string, Map<string, string[]>> = {
		selected_key_ids: selectedKeyNameToIds,
		virtual_key_ids: virtualKeyNameToIds,
		routing_rule_ids: routingRuleNameToIds,
	};

	const excludedKeys = ["start_time", "end_time", "content_search", "metadata_filters"];
	const selectedCount = Object.entries(filters).reduce((count, [key, value]) => {
		if (excludedKeys.includes(key)) {
			return count;
		}
		if (Array.isArray(value)) {
			const nameMap = dedupedCountKeys[key];
			return count + (nameMap ? countUniqueNames(value, nameMap) : value.length);
		}
		return count + (value ? 1 : 0);
	}, 0) + (filters.metadata_filters ? Object.keys(filters.metadata_filters).length : 0);

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
											const selected = isSelected(category, value);
											return (
												<CommandItem
													key={value}
													data-testid={`filter-item-${category.toLowerCase().replace(/[^a-z0-9]+/g, "-")}-${value.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`}
													onSelect={() => handleFilterSelect(category, value)}
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
									{category.startsWith("Metadata: ") && (() => {
									const metadataKey = category.replace("Metadata: ", "");
									const activeValue = filters.metadata_filters?.[metadataKey];
									const isCustom = activeValue && !values.includes(activeValue);
									const displayValue = customMetadataInputs[category] ?? (isCustom ? activeValue : "");
									return (
										<div className="flex items-center gap-1 px-2 py-1">
											<input
												className="h-7 w-full rounded border bg-transparent px-2 text-sm placeholder:text-muted-foreground"
												placeholder="Custom value..."
												data-testid={`filter-custom-${category.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`}
												value={displayValue}
												onChange={(e) => {
													const newVal = e.target.value;
													setCustomMetadataInputs((prev) => ({ ...prev, [category]: newVal }));
													if (newVal === "" && isCustom) {
														onMetadataFilterChange?.(metadataKey, undefined);
													}
												}}
												onKeyDown={(e) => {
													if (e.key === "Enter" && customMetadataInputs[category]?.trim()) {
														handleFilterSelect(category, customMetadataInputs[category].trim());
													}
													e.stopPropagation();
												}}
												onClick={(e) => e.stopPropagation()}
											/>
										</div>
									);
								})()}
								</CommandGroup>
							))}
					</CommandList>
				</Command>
			</PopoverContent>
		</Popover>
	);
}
