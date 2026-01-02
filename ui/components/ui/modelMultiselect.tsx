"use client";

import { cn } from "@/components/ui/utils";
import { useLazyGetModelsQuery } from "@/lib/store/apis/providersApi";
import { X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { components, MultiValueProps, OptionProps } from "react-select";
import { AsyncMultiSelect } from "./asyncMultiselect";
import { Option } from "./multiselectUtils";

export interface ModelMultiselectProps {
	provider?: string;
	keys?: string[];
	value: string[];
	onChange: (models: string[]) => void;
	placeholder?: string;
	disabled?: boolean;
	className?: string;
}

interface ModelOption {
	label: string;
	value: string;
	provider?: string;
}

export function ModelMultiselect({
	provider,
	keys,
	value,
	onChange,
	placeholder = "Select models...",
	disabled = false,
	className,
}: ModelMultiselectProps) {
	const [getModels, { data: modelsData, isLoading }] = useLazyGetModelsQuery();
	const [inputValue, setInputValue] = useState("");
	const inputValueRef = useRef("");

	// Convert value array to options
	const selectedOptions: ModelOption[] = value.map((model) => ({
		label: model,
		value: model,
	}));

	// Fetch initial models on mount or when provider/keys change
	useEffect(() => {
		if (provider) {
			getModels({
				provider: provider,
				keys: keys && keys.length > 0 ? keys : undefined,
				limit: 5,
			});
		}
	}, [provider, keys, getModels]);

	// Load options function for AsyncMultiSelect - now properly async
	const loadOptions = useCallback(
		(query: string, callback: (options: ModelOption[]) => void) => {
			if (!provider) {
				callback([]);
				return;
			}

			getModels({
				query: query || undefined,
				provider: provider,
				keys: keys && keys.length > 0 ? keys : undefined,
				limit: query ? 20 : 5,
			})
				.unwrap()
				.then((response) => {
					const options = response.models.map((model) => ({
						label: model.name,
						value: model.name,
						provider: model.provider,
					}));
					callback(options);
				})
				.catch(() => {
					callback([]);
				});
		},
		[getModels, provider, keys],
	);

	// Handle selection change
	const handleChange = useCallback(
		(options: Option<ModelOption>[]) => {
			const modelNames = options.map((opt) => opt.value);
			onChange(modelNames);

			// Refresh the list with current query to update available options
			const currentQuery = inputValueRef.current;
			if (provider) {
				getModels({
					query: currentQuery || undefined,
					provider: provider,
					keys: keys && keys.length > 0 ? keys : undefined,
					limit: currentQuery ? 20 : 5,
				});
			}
		},
		[onChange, provider, keys, getModels],
	);

	// Handle input change - track in both state and ref
	// Per react-select docs: ignore input clear on blur, menu close, and set-value (selection)
	const handleInputChange = useCallback((newValue: string, actionMeta: { action: string }) => {
		// Don't clear input when selecting an option, blurring, or closing menu
		if (actionMeta.action === "set-value" || actionMeta.action === "input-blur" || actionMeta.action === "menu-close") {
			return;
		}
		setInputValue(newValue);
		inputValueRef.current = newValue;
	}, []);

	// Convert API data to options for default display
	const defaultOptions: ModelOption[] = useMemo(
		() =>
			modelsData?.models?.map((model) => ({
				label: model.name,
				value: model.name,
				provider: model.provider,
			})) || [],
		[modelsData],
	);

	return (
		<AsyncMultiSelect<ModelOption>
			hideSelectedOptions
			value={selectedOptions}
			onChange={handleChange}
			reload={loadOptions}
			debounce={300}
			isCreatable={true}
			dynamicOptionCreation={true}
			createOptionText="Press enter to add new model"
			defaultOptions={defaultOptions}
			isLoading={isLoading}
			placeholder={placeholder}
			disabled={disabled || !provider}
			className={cn("!min-h-10 w-full", className)}
			triggerClassName="!shadow-none !border-border !min-h-10 px-1"
			menuClassName="!z-[100] max-h-[300px] overflow-y-auto w-full cursor-pointer custom-scrollbar"
			isClearable={false}
			closeMenuOnSelect={false}
			menuPlacement="auto"
			menuListClassName="mx-1"
			inputValue={inputValue}
			onInputChange={handleInputChange}
			noResultsFoundPlaceholder="No models found"
			emptyResultPlaceholder={provider ? "Start typing to search models..." : "Please select a provider first"}
			views={{
				dropdownIndicator: () => <></>,				
				multiValue: (multiValueProps: MultiValueProps<ModelOption>) => {
					return (
						<div
							{...multiValueProps.innerProps}
							className="bg-accent dark:!bg-card flex cursor-pointer items-center gap-1 rounded-sm px-1 py-0.5 text-sm"
						>
							{multiValueProps.data.label}{" "}
							<X
								className="hover:text-foreground text-muted-foreground h-4 w-4 cursor-pointer"
								onClick={(e) => {
									e.stopPropagation();
									multiValueProps.removeProps.onClick?.(e as any);
								}}
							/>
						</div>
					);
				},
				option: (optionProps: OptionProps<ModelOption>) => {
					const { Option } = components;
					return (
						<Option
							{...optionProps}
							className={cn(
								"flex w-full cursor-pointer items-center gap-2 rounded-sm px-2 py-2 text-sm",
								optionProps.isFocused && "bg-accent dark:!bg-card",
								"hover:bg-accent",
								optionProps.isSelected && "bg-accent dark:!bg-card",
							)}
						>
							<span className="text-content-primary grow truncate text-sm">{optionProps.data.label}</span>
						</Option>
					);
				},
			}}
		/>
	);
}
