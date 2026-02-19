import { Button } from "@/components/ui/button";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { RequestTypeLabels, RequestTypes } from "@/lib/constants/logs";
import { useGetAvailableFilterDataQuery, useGetProvidersQuery } from "@/lib/store";
import { cn } from "@/lib/utils";
import { Check, FilterIcon } from "lucide-react";
import { useState } from "react";

interface DashboardFiltersProps {
	selectedProviders: string[];
	selectedModels: string[];
	selectedKeyIds: string[];
	selectedVirtualKeyIds: string[];
	selectedTypes: string[];
	onProvidersChange: (values: string[]) => void;
	onModelsChange: (values: string[]) => void;
	onSelectedKeysChange: (values: string[]) => void;
	onVirtualKeysChange: (values: string[]) => void;
	onTypesChange: (values: string[]) => void;
}

export function DashboardFilters({
	selectedProviders,
	selectedModels,
	selectedKeyIds,
	selectedVirtualKeyIds,
	selectedTypes,
	onProvidersChange,
	onModelsChange,
	onSelectedKeysChange,
	onVirtualKeysChange,
	onTypesChange,
}: DashboardFiltersProps) {
	const [open, setOpen] = useState(false);

	const { data: providersData, isLoading: providersLoading } = useGetProvidersQuery();
	const { data: filterData, isLoading: filterDataLoading } = useGetAvailableFilterDataQuery();

	const availableProviders = providersData || [];
	const availableModels = filterData?.models || [];
	const availableSelectedKeys = filterData?.selected_keys || [];
	const availableVirtualKeys = filterData?.virtual_keys || [];

	// Name-to-ID mappings for keys
	const selectedKeyNameToId = new Map(availableSelectedKeys.map((key) => [key.name, key.id]));
	const virtualKeyNameToId = new Map(availableVirtualKeys.map((key) => [key.name, key.id]));

	const totalSelected = selectedProviders.length + selectedModels.length + selectedKeyIds.length + selectedVirtualKeyIds.length + selectedTypes.length;

	const handleSelect = (category: string, value: string) => {
		if (category === "Providers") {
			const next = selectedProviders.includes(value)
				? selectedProviders.filter((v) => v !== value)
				: [...selectedProviders, value];
			onProvidersChange(next);
		} else if (category === "Models") {
			const next = selectedModels.includes(value)
				? selectedModels.filter((v) => v !== value)
				: [...selectedModels, value];
			onModelsChange(next);
		} else if (category === "Selected Keys") {
			const id = selectedKeyNameToId.get(value) || value;
			const next = selectedKeyIds.includes(id)
				? selectedKeyIds.filter((v) => v !== id)
				: [...selectedKeyIds, id];
			onSelectedKeysChange(next);
		} else if (category === "Virtual Keys") {
			const id = virtualKeyNameToId.get(value) || value;
			const next = selectedVirtualKeyIds.includes(id)
				? selectedVirtualKeyIds.filter((v) => v !== id)
				: [...selectedVirtualKeyIds, id];
			onVirtualKeysChange(next);
		} else if (category === "Type") {
			const next = selectedTypes.includes(value)
				? selectedTypes.filter((v) => v !== value)
				: [...selectedTypes, value];
			onTypesChange(next);
		}
	};

	const isSelected = (category: string, value: string) => {
		if (category === "Providers") return selectedProviders.includes(value);
		if (category === "Models") return selectedModels.includes(value);
		if (category === "Selected Keys") {
			const id = selectedKeyNameToId.get(value) || value;
			return selectedKeyIds.includes(id);
		}
		if (category === "Virtual Keys") {
			const id = virtualKeyNameToId.get(value) || value;
			return selectedVirtualKeyIds.includes(id);
		}
		if (category === "Type") return selectedTypes.includes(value);
		return false;
	};

	const FILTER_OPTIONS: Record<string, string[]> = {
		Providers: providersLoading ? [] : availableProviders.map((p) => p.name),
		Type: [...RequestTypes],
		Models: filterDataLoading ? [] : availableModels,
		"Selected Keys": filterDataLoading ? [] : availableSelectedKeys.map((key) => key.name),
		"Virtual Keys": filterDataLoading ? [] : availableVirtualKeys.map((key) => key.name),
	};

	return (
		<Popover open={open} onOpenChange={setOpen}>
			<PopoverTrigger asChild>
				<Button variant="outline" size="sm" className="h-7.5 w-[120px]">
					<FilterIcon className="h-4 w-4" />
					Filters
					{totalSelected > 0 && (
						<span className="bg-primary/10 flex h-6 w-6 items-center justify-center rounded-full text-xs font-normal">
							{totalSelected}
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
										const selected = isSelected(category, value);
										return (
											<CommandItem
												key={value}
												onSelect={() => handleSelect(category, value)}
											>
												<div
													className={cn(
														"border-primary mr-2 flex h-4 w-4 items-center justify-center rounded-sm border",
														selected ? "bg-primary text-primary-foreground" : "opacity-50 [&_svg]:invisible",
													)}
												>
													<Check className="text-primary-foreground size-3" />
												</div>
												<span className="lowercase">
												{category === "Type" ? RequestTypeLabels[value as keyof typeof RequestTypeLabels] ?? value : value}
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
