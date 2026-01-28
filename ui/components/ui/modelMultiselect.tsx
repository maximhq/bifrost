"use client";

import { cn } from "@/components/ui/utils";
import { useLazyGetModelsQuery } from "@/lib/store/apis/providersApi";
import { X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { components, MultiValueProps, OptionProps, SingleValueProps } from "react-select";
import { AsyncMultiSelect } from "./asyncMultiselect";
import { Option } from "./multiselectUtils";

interface ModelMultiselectPropsBase {
	provider?: string;
	keys?: string[];
	placeholder?: string;
	disabled?: boolean;
	className?: string;
	/** Load models even when no provider is selected */
	loadModelsOnEmptyProvider?: boolean;
}

interface ModelMultiselectPropsSingle extends ModelMultiselectPropsBase {
	/** Single select mode - value and onChange will be string instead of string[] */
	isSingleSelect: true;
	value: string;
	onChange: (model: string) => void;
}

interface ModelMultiselectPropsMulti extends ModelMultiselectPropsBase {
	/** Multi select mode (default) - value and onChange will be string[] */
	isSingleSelect?: false;
	value: string[];
	onChange: (models: string[]) => void;
}

export type ModelMultiselectProps = ModelMultiselectPropsSingle | ModelMultiselectPropsMulti;

interface ModelOption {
	label: string;
	value: string;
	provider?: string;
}

export function ModelMultiselect(props: ModelMultiselectProps) {
	const {
		provider,
		keys,
		value,
		onChange,
		placeholder = "Select models...",
		disabled = false,
		className,
		loadModelsOnEmptyProvider = false,
	} = props;
	const isSingleSelect = props.isSingleSelect === true;

	const [getModels, { data: modelsData, isLoading }] = useLazyGetModelsQuery();
	const [inputValue, setInputValue] = useState("");
	const inputValueRef = useRef("");

	// Convert value to options (handle both single and multi select)
	const stringValue = value as string;
	const arrayValue = value as string[];
	const selectedOptions: ModelOption[] = isSingleSelect
		? stringValue
			? [{ label: stringValue, value: stringValue }]
			: []
		: arrayValue.map((model) => ({
			label: model,
			value: model,
		}));

	// Fetch initial models on mount or when provider/keys change
	useEffect(() => {
		if (provider || loadModelsOnEmptyProvider) {
			getModels({
				provider: provider || undefined,
				keys: keys && keys.length > 0 ? keys : undefined,
				limit: loadModelsOnEmptyProvider && !provider ? 20 : 5,
			});
		}
	}, [provider, keys, getModels, loadModelsOnEmptyProvider]);

	// Load options function for AsyncMultiSelect
	const loadOptions = useCallback(
		(query: string, callback: (options: ModelOption[]) => void) => {
			if (!provider && !loadModelsOnEmptyProvider) {
				callback([]);
				return;
			}

			getModels({
				query: query || undefined,
				provider: provider || undefined,
				keys: keys && keys.length > 0 ? keys : undefined,
				limit: query ? 50 : loadModelsOnEmptyProvider && !provider ? 20 : 5,
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
		[getModels, provider, keys, loadModelsOnEmptyProvider],
	);

	// Handle selection change
	const handleChange = useCallback(
		(options: Option<ModelOption>[]) => {
			if (isSingleSelect) {
				const selected = options[0];
				(onChange as (model: string) => void)(selected?.value || "");
			} else {
				const modelNames = options.map((opt) => opt.value);
				(onChange as (models: string[]) => void)(modelNames);
			}

			// Refresh the list with current query to update available options
			const currentQuery = inputValueRef.current;
			if (provider || loadModelsOnEmptyProvider) {
				getModels({
					query: currentQuery || undefined,
					provider: provider || undefined,
					keys: keys && keys.length > 0 ? keys : undefined,
					limit: currentQuery ? 20 : 5,
				});
			}
		},
		[onChange, provider, keys, getModels, isSingleSelect, loadModelsOnEmptyProvider],
	);

	// Handle input change - track in both state and ref
	// Per react-select docs: ignore input clear on blur, menu close, and set-value (selection)
	const handleInputChange = useCallback((newValue: string, actionMeta: { action: string }) => {
		// Don't clear input when selecting an option, blurring, or closing menu
		if (actionMeta.action === "set-value") {
			return;
		}
		if (!isSingleSelect && (actionMeta.action === "input-blur" || actionMeta.action === "menu-close")) {
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

	const shouldBeDisabled = disabled || (!provider && !loadModelsOnEmptyProvider);

	return (
		<AsyncMultiSelect<ModelOption>
			isSingleSelect={isSingleSelect}
			hideSelectedOptions
			value={selectedOptions}
			onChange={handleChange}
			reload={loadOptions}
			debounce={300}
			isCreatable={!isSingleSelect}
			dynamicOptionCreation={!isSingleSelect}
			createOptionText={isSingleSelect ? undefined : "Press enter to add new model"}
			defaultOptions={defaultOptions.length > 0 ? defaultOptions : [] as Option<ModelOption>[]}
			isLoading={isLoading}
			placeholder={placeholder}
			disabled={shouldBeDisabled}
			className={cn("!min-h-9 w-full", className)}
			triggerClassName="!shadow-none !border-border !min-h-9 px-1"
			menuClassName="!z-[100] max-h-[300px] overflow-y-auto w-full cursor-pointer custom-scrollbar"
			isClearable={false}
			closeMenuOnSelect={isSingleSelect}
			menuPlacement="auto"
			menuListClassName="mx-1"
			inputValue={inputValue}
			onInputChange={handleInputChange}
			noResultsFoundPlaceholder="No models found"
			emptyResultPlaceholder={provider || loadModelsOnEmptyProvider ? "Start typing to search models..." : "Please select a provider first"}
			views={{
				dropdownIndicator: isSingleSelect ? undefined : () => <></>,
				singleValue: isSingleSelect ? (singleValueProps: SingleValueProps<ModelOption>) => (
					<span className="absolute left-1.5 text-sm">{singleValueProps.data.label}</span>
				) : undefined,
				multiValue: isSingleSelect ? undefined : (multiValueProps: MultiValueProps<ModelOption>) => {
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
							<span className="grow truncate text-sm">{optionProps.data.label}</span>
						</Option>
					);
				},
			}}
		/>
	);
}
