"use client";

import { CheckIcon, ChevronDown, PlusCircle, PlusIcon, XIcon } from "lucide-react";
import React, { KeyboardEventHandler, useCallback, useEffect, useRef, useState } from "react";
import {
	ClearIndicatorProps,
	components,
	ControlProps,
	DropdownIndicatorProps,
	GroupBase,
	GroupHeadingProps,
	GroupProps,
	IndicatorsContainerProps,
	IndicatorSeparatorProps,
	InputProps,
	LoadingIndicatorProps,
	MenuListProps,
	MenuProps,
	MultiValueGenericProps,
	MultiValueProps,
	MultiValueRemoveProps,
	NoticeProps,
	OptionProps,
	PlaceholderProps,
	SingleValueProps,
	ValueContainerProps,
} from "react-select";
import AsyncCreatableSelect from "react-select/async-creatable";
import { useDebouncedFunction } from "../../hooks/useDebounce";
import { Checkbox } from "./checkbox";
import { Icons } from "./icons";
import { Label } from "./label";
import {
	createOption,
	CustomComponentsProps,
	CustomDropdownIndicatorProps,
	CustomOptionProps,
	CustomPlaceholderProps,
	EvaluatorGroup,
	EvaluatorOption,
	Option,
	OptionGroup,
} from "./multiselectUtils";
import { Separator } from "./separator";
import { cn, radixDialogOnBlurWorkaround } from "./utils";

// Create wrapper functions for react-select components to fix TypeScript issues
const OptionWrapper = <T extends unknown = unknown>(props: OptionProps<T, boolean, GroupBase<T>>): React.ReactNode => {
	return components.Option(props) as React.ReactNode;
};

const GroupHeadingWrapper = <T extends unknown = unknown>(props: GroupHeadingProps<T, boolean, GroupBase<T>>): React.ReactNode => {
	return components.GroupHeading(props) as React.ReactNode;
};

const GroupWrapper = <T extends unknown = unknown>(props: GroupProps<T, boolean, GroupBase<T>>): React.ReactNode => {
	return components.Group(props) as React.ReactNode;
};

const MultiValueWrapper = <T extends unknown = unknown>(props: MultiValueProps<T, boolean, GroupBase<T>>): React.ReactNode => {
	return components.MultiValue(props) as React.ReactNode;
};

const ControlWrapper = <T extends unknown = unknown>(props: ControlProps<T, boolean, GroupBase<T>>): React.ReactNode => {
	return components.Control(props) as React.ReactNode;
};

const MultiValueRemoveWrapper = <T extends unknown = unknown>(props: MultiValueRemoveProps<T, boolean, GroupBase<T>>): React.ReactNode => {
	return components.MultiValueRemove(props) as React.ReactNode;
};

const ClearIndicatorWrapper = <T extends unknown = unknown>(props: ClearIndicatorProps<T, boolean, GroupBase<T>>): React.ReactNode => {
	return components.ClearIndicator(props) as React.ReactNode;
};

const IndicatorSeparatorWrapper = <T extends unknown = unknown>(
	props: IndicatorSeparatorProps<T, boolean, GroupBase<T>>,
): React.ReactNode => {
	return components.IndicatorSeparator(props) as React.ReactNode;
};

const InputWrapper = <T extends unknown = unknown>(props: InputProps<T, boolean, GroupBase<T>>): React.ReactNode => {
	return components.Input(props) as React.ReactNode;
};

const LoadingIndicatorWrapper = <T extends unknown = unknown>(props: LoadingIndicatorProps<T, boolean, GroupBase<T>>): React.ReactNode => {
	return components.LoadingIndicator(props) as React.ReactNode;
};

const MenuWrapper = <T extends unknown = unknown>(props: MenuProps<T, boolean, GroupBase<T>>): React.ReactNode => {
	return components.Menu(props) as React.ReactNode;
};

const MenuListWrapper = <T extends unknown = unknown>(props: MenuListProps<T, boolean, GroupBase<T>>): React.ReactNode => {
	return components.MenuList(props) as React.ReactNode;
};

const MultiValueContainerWrapper = <T extends unknown = unknown>(
	props: MultiValueGenericProps<T, boolean, GroupBase<T>>,
): React.ReactNode => {
	return components.MultiValueContainer(props) as React.ReactNode;
};

const NoOptionsMessageWrapper = <T extends unknown = unknown>(props: NoticeProps<T, boolean, GroupBase<T>>): React.ReactNode => {
	return components.NoOptionsMessage(props) as React.ReactNode;
};

const PlaceholderWrapper = <T extends unknown = unknown>(props: PlaceholderProps<T, boolean, GroupBase<T>>): React.ReactNode => {
	return components.Placeholder(props) as React.ReactNode;
};

const SingleValueWrapper = <T extends unknown = unknown>(props: SingleValueProps<T, boolean, GroupBase<T>>): React.ReactNode => {
	return components.SingleValue(props) as React.ReactNode;
};

const ValueContainerWrapper = <T extends unknown = unknown>(props: ValueContainerProps<T, boolean, GroupBase<T>>): React.ReactNode => {
	return components.ValueContainer(props) as React.ReactNode;
};

interface AsyncMultiSelectProps<T> {
	/** disable multiselect */
	isSingleSelect?: boolean;
	/** disable async */
	isNonAsync?: boolean;
	/** enable cross option to clear all selected values (default: false) */
	isClearable?: boolean;
	/** enable create new option functionality (default: false) */
	isCreatable?: boolean;
	/** Force close the menu on selection of option (default: false when isMulti is true) */
	closeMenuOnSelect?: boolean;
	/** hide selected options
	 * false : would add a checkmark in front of the selected option (default)
	 * true: would remove the selected option from the list
	 */
	hideSelectedOptions?: boolean;
	/** style of the check icon if hideSelectedOption is false */
	checkIconStyling?: string;

	/** Controls whether the selected value is rendered in the control element.
	 * Set to false to hide the selected value in the control when rendering custom selection UI
	 * or to reduce visual clutter when labels are shown elsewhere.
	 * @default true
	 */
	controlShouldRenderValue?: boolean;

	/** enable loading state */
	isLoading?: boolean;
	debounce?: number;
	reload?: (query: string, callback: (options: Option<T>[] | OptionGroup<T>[]) => void) => void;

	menuPosition?: "absolute" | "fixed";

	/** enable dynamic option creation from the input */
	dynamicOptionCreation?: boolean;
	/** default options to be displayed */
	defaultOptions?: Option<T>[] | OptionGroup<T>[];
	onChange?: (items: Option<T>[]) => any;
	/** callback function to be called when a new option is created */
	onCreateOption?: (value: string) => void;

	placeholder?: React.ReactNode;

	/** placeholder to be displayed when no results are found */
	noResultsFoundPlaceholder?: string;
	/** placeholder to be displayed when no results currently */
	emptyResultPlaceholder?: React.ReactNode;
	disabled?: boolean;
	inputValue?: string;
	hideDropdownIndicator?: boolean;
	autoFocus?: boolean;

	/** hide search icon present on the left */
	hideSearchIcon?: boolean;
	/** hide plus icon present in the create option */
	hidePlusIcon?: boolean;
	className?: string;
	triggerClassName?: string;
	menuClassName?: string;
	menuListClassName?: string;
	groupClassName?: string;
	menuPlacement?: "auto" | "top" | "bottom";
	value?: any;
	menuIsOpen?: boolean;

	/** format create new option label when dynamically creating option */
	formatCreateLabel?: (inputValue: string) => string;
	defaultValue?: any;
	defaultMenuIsOpen?: boolean;

	/** text to be displayed when static create option */
	createOptionText?: string;
	onBlur?: () => void;
	portalTarget?: HTMLElement;

	/** callback function to be called when input value changes */
	onInputChange?: (inputValue: string, actionMeta: { action: string }) => void;
	onKeyDown?: KeyboardEventHandler;

	/** custom no options message */
	noOptionsMessage?: () => React.ReactNode;
	valueContainerClassName?: string;
	noOptionsMessageClassName?: string;
	views?: {
		clearIndicator?: (props: ClearIndicatorProps<T>) => React.ReactNode;
		control?: (props: ControlProps<T>) => React.ReactNode;
		dropdownIndicator?: (props: DropdownIndicatorProps<T>) => React.ReactNode;
		group?: (props: GroupProps<T>) => React.ReactNode;
		groupHeading?: (props: GroupHeadingProps<T>) => React.ReactNode;
		indicatorsContainer?: (props: IndicatorsContainerProps<T>) => React.ReactNode;
		indicatorSeparator?: (props: IndicatorSeparatorProps) => React.ReactNode;
		input?: (props: InputProps) => React.ReactNode;
		loadingIndicator?: (props: LoadingIndicatorProps) => React.ReactNode;
		menu?: (props: MenuProps) => React.ReactNode;
		menuList?: (props: MenuListProps) => React.ReactNode;
		noOptionsMessage?: (props: NoticeProps) => React.ReactNode;
		multiValue?: (props: MultiValueProps<T>) => React.ReactNode;
		multiValueLabel?: (props: MultiValueGenericProps<T>) => React.ReactNode;
		multiValueContainer?: (props: MultiValueGenericProps<T>) => React.ReactNode;
		multiValueRemove?: (props: MultiValueRemoveProps<T>) => React.ReactNode;
		option?: (props: OptionProps<T>) => React.ReactNode;
		placeholder?: (props: PlaceholderProps) => React.ReactNode;
		singleValue?: (props: SingleValueProps<T>) => React.ReactNode;
		valueContainer?: (props: ValueContainerProps) => React.ReactNode;
	};
}

export function AsyncMultiSelect<T>(props: AsyncMultiSelectProps<T>) {
	const menuOpenRef = useRef(false);
	const containerRef = useRef<HTMLDivElement>(null);

	// Add a native keydown listener at document level in capture phase
	useEffect(() => {
		const handleKeyDownCapture = (event: KeyboardEvent) => {
			// Only intercept Escape when menu is open
			if (menuOpenRef.current && event.key === "Escape") {
				// Stop propagation and prevent default to block Sheet/Dialog
				event.stopPropagation();
				event.stopImmediatePropagation();
				event.preventDefault();

				// Close the menu by blurring the currently focused input
				// When react-select menu is open, its input is focused
				if (document.activeElement && document.activeElement instanceof HTMLElement) {
					document.activeElement.blur();
				}
			}
		};

		// Listen at document level in capture phase with highest priority
		// Must be in capture to beat Sheet/Dialog listeners
		document.addEventListener("keydown", handleKeyDownCapture, { capture: true });
		return () => {
			document.removeEventListener("keydown", handleKeyDownCapture, { capture: true });
		};
	}, []);

	// When having an asynchronous component, this will loadOptions from the reload and debounce passed in through props
	const debouncedReload = useDebouncedFunction(
		useCallback(
			(query: string, callback: (options: Option<T>[] | OptionGroup<T>[]) => void) => {
				if (!props.reload) {
					return;
				}
				props.reload(query, callback);
			},
			[props.reload],
		),
		props.debounce ?? 0,
	);
	const loadOptions = props.debounce && props.reload ? debouncedReload : props.reload;

	// This is a custom implementation of the load options when using the nonAsync flag. This will help in filtering based on input value
	const loadOptionsForNonAsyncComponents = (inputValue: string, callback: (options: Option<T>[] | OptionGroup<T>[]) => void) => {
		const options = props.defaultOptions || [];
		let filtered;

		if (options.length > 0 && "options" in options[0]) {
			// Grouped options
			filtered = (options as OptionGroup<T>[])
				.map((group) => ({
					...group,
					options: group.options.filter((opt) => opt.label.toLowerCase().includes(inputValue.toLowerCase())),
				}))
				.filter((group) => group.options.length > 0);
		} else {
			// Flat options
			filtered = (options as Option<T>[]).filter((opt) => opt.label.toLowerCase().includes(inputValue.toLowerCase()));
		}

		callback(filtered as Option<T>[] | OptionGroup<T>[]);
	};

	const handleKeyDown: KeyboardEventHandler = (event) => {
		// If menu is open and Escape is pressed, stop propagation to prevent parent handlers (like dialogs)
		if (menuOpenRef.current && event.key === "Escape") {
			// Prevent the event from bubbling up to parent components (like Sheet/Dialog)
			event.stopPropagation();
			// Also prevent default to ensure no other handlers process this
			event.preventDefault();
			// Return early to not call the original handler with Escape when menu is open
			return;
		}
		// Call the original onKeyDown handler if provided
		if (props.onKeyDown) {
			props.onKeyDown(event);
		}
	};

	const customOptionProps: CustomOptionProps = {
		dynamicOptionCreation: props.dynamicOptionCreation,
		createOptionText: props.createOptionText,
		checkIconStyling: props.checkIconStyling,
		hideSelectedOptions: props.hideSelectedOptions ?? false,
		hidePlusIcon: props.hidePlusIcon,
	};

	const customDropdownIndicatorProps: CustomDropdownIndicatorProps = {
		hideDropdownIndicator: props.hideDropdownIndicator,
	};

	const customPlaceholderProps: CustomPlaceholderProps = {
		hideSearchIcon: props.hideSearchIcon,
		placeholder: props.placeholder,
	};

	const customComponentsProps: CustomComponentsProps = {
		clearIndicatorView: props.views?.clearIndicator,
		controlView: props.views?.control,
		dropdownIndicatorView: props.views?.dropdownIndicator,
		groupView: props.views?.group,
		groupHeadingView: props.views?.groupHeading,
		indicatorSeparatorView: props.views?.indicatorSeparator,
		inputView: props.views?.input,
		loadingIndicatorView: props.views?.loadingIndicator,
		menuView: props.views?.menu,
		menuListView: props.views?.menuList,
		multiValueView: props.views?.multiValue,
		multiValueRemoveView: props.views?.multiValueRemove,
		multiValueLabelView: props.views?.multiValueLabel,
		optionView: props.views?.option,
		noOptionsMessageView: props.views?.noOptionsMessage,
		placeholderView: props.views?.placeholder,
		singleValueView: props.views?.singleValue,
		valueContainerView: props.views?.valueContainer,
	};

	return (
		<div ref={containerRef}>
			<AsyncCreatableSelect
				isDisabled={props.disabled}
				autoFocus={props.autoFocus}
				onKeyDown={handleKeyDown}
				isClearable={props.isClearable ?? false}
				onCreateOption={props.isCreatable ? props.onCreateOption : undefined}
				isValidNewOption={props.isCreatable ? (option) => option.length > 0 : () => false}
				isLoading={props.isLoading}
				defaultOptions={props.defaultOptions}
				loadOptions={props.isNonAsync ? loadOptionsForNonAsyncComponents : loadOptions}
				isMulti={!props.isSingleSelect}
				placeholder={props.placeholder}
				closeMenuOnSelect={props.closeMenuOnSelect === true || props.isSingleSelect === true}
				onChange={(selection, actionMeta) => {
					switch (actionMeta.action) {
						case "remove-value":
						case "pop-value":
							if ((actionMeta.removedValue as any)?.isFixed) {
								return;
							}
							break;
						case "clear":
							if (selection) {
								selection = (selection as Option<T>[]).filter((v) => !(v as any)?.isFixed);
							}
							break;
					}

					props.onChange && props.onChange(selection as Option<T>[]);
				}}
				formatCreateLabel={props.formatCreateLabel}
				controlShouldRenderValue={props.controlShouldRenderValue ?? true}
				menuPlacement={props.menuPlacement}
				blurInputOnSelect={false}
				menuPortalTarget={props.portalTarget}
				menuPosition={props.menuPosition}
				onInputChange={(newValue, actionMeta) => {
					if (props.onInputChange) {
						props.onInputChange(newValue, { action: actionMeta.action });
					}
				}}
				onBlur={(e) => {
					radixDialogOnBlurWorkaround(e);
					if (props.onBlur) props.onBlur();
				}}
				onMenuOpen={() => {
					menuOpenRef.current = true;
				}}
				onMenuClose={() => {
					menuOpenRef.current = false;
				}}
				menuIsOpen={props.menuIsOpen}
				noOptionsMessage={
					props.noOptionsMessage
						? props.noOptionsMessage
						: ({ inputValue }) => (inputValue.length > 0 ? <div>{props.noResultsFoundPlaceholder}</div> : props.emptyResultPlaceholder)
				}
				inputValue={props.inputValue}
				styles={{
					control: (base) => ({ ...base, boxShadow: "none", minHeight: "32px" }),
					multiValue: () => ({}),
					multiValueLabel: () => ({}),
					multiValueRemove: () => ({}),
					option: () => ({}),
					indicatorSeparator: () => ({
						visibility: "hidden",
					}),
					input: (base) => ({ ...base, margin: 0, padding: 0 }),
					menuPortal: (base) => ({ ...base, zIndex: 51 }),
					noOptionsMessage: () => ({}),
					valueContainer: (base) => ({ ...base, padding: 6, gap: 8 }),
					placeholder: (base) => ({ ...base, marginLeft: 0 }),
					indicatorsContainer: (base) => ({ ...base, height: "32px" }),
				}}
				value={props.value}
				defaultValue={props.defaultValue ?? []}
				defaultMenuIsOpen={props.defaultMenuIsOpen}
				classNames={{
					container: () => cn("min-h-8 border-none", props.className),
					control: ({ isFocused }) =>
						cn(
							"border-border! multiselect-control dark:!bg-accent flex flex-wrap items-start justify-between rounded-md border bg-white",
							props.triggerClassName,
						),
					placeholder: () => "text-sm text-content-disabled truncate p-0 text-ellipsis",
					group: () => cn(props.groupClassName),
					input: () => "text-sm m-0 border-none p-0 !text-secondary-foreground",
					menu: () => cn("dark:!bg-accent p-0", props.menuClassName),
					menuList: () => cn("p-2", props.menuListClassName),
					valueContainer: () => cn("flex h-full w-full", props.valueContainerClassName),
					option: ({ isFocused }) =>
						cn("multiselect-option flex w-full justify-between rounded-sm p-2 text-sm", isFocused && "bg-background-highlight-primary/60"),
					singleValue: () => "text-sm text-content-primary",
					multiValue: () => "bg-accent dark:!bg-card flex cursor-pointer items-center gap-1 rounded-sm px-1 py-0.5 text-sm",
					multiValueLabel: () => "text-content-tertiary",
					multiValueRemove: () => "text-content-tertiary h-inherit flex items-center opacity-60 hover:cursor-pointer hover:opacity-100",
					loadingMessage: () => "text-sm",
					noOptionsMessage: () => cn("text-content-disabled flex items-center justify-center text-sm", props.noOptionsMessageClassName),
					indicatorsContainer: () => "h-8",
				}}
				minMenuHeight={400}
				components={{
					ClearIndicator: CustomClearIndicator,
					Control: CustomControl,
					DropdownIndicator: CustomDropdownIndicator,
					Group: CustomGroup,
					GroupHeading: CustomGroupHeading,
					IndicatorSeparator: CustomIndicatorSeparator,
					Input: CustomInput,
					LoadingIndicator: CustomLoadingIndicator,
					Menu: CustomMenu,
					MenuList: CustomMenuList,
					MultiValue: CustomMultiValue,
					MultiValueContainer: CustomMultiValueContainer,
					MultiValueLabel: CustomMultiValueLabel,
					MultiValueRemove: CustomMultiValueRemove,
					Option: CustomOption,
					NoOptionsMessage: CustomNoOptionsMessage,
					Placeholder: CustomPlaceholder,
					SingleValue: CustomSingleValue,
					ValueContainer: CustomValueContainer,
				}}
				{...customOptionProps}
				{...customDropdownIndicatorProps}
				{...customComponentsProps}
				{...customPlaceholderProps}
			/>
		</div>
	);
}

export function MultiSelectInput<T>(props: AsyncMultiSelectProps<T>) {
	const [inputValue, setInputValue] = useState("");
	const [value, setValue] = useState<readonly Option<T>[]>(props.value ? props.value.map((val: string) => createOption<T>(val)) : []);

	const handleKeyDown: KeyboardEventHandler = (event) => {
		if (!inputValue) return;
		switch (event.key) {
			case "Enter":
			case "Tab": {
				const newOptions = [...value, createOption<T>(inputValue)];
				setValue(newOptions);
				setInputValue("");
				event.preventDefault();
				props.onChange && props.onChange(newOptions);
				break;
			}
		}
	};

	return (
		<AsyncMultiSelect<T>
			{...props}
			isCreatable
			dynamicOptionCreation
			isClearable
			menuIsOpen={false}
			value={value}
			hideDropdownIndicator
			hideSearchIcon
			onChange={(newValue) => {
				setValue(newValue);
				props.onChange && props.onChange(newValue);
			}}
			onKeyDown={handleKeyDown}
			inputValue={inputValue}
			onInputChange={(newValue) => setInputValue(newValue)}
			autoFocus
		/>
	);
}

interface EvaluatorMultiSelectProps<V> extends Omit<AsyncMultiSelectProps<EvaluatorOption<V>>, "onChange"> {
	keepTags?: string[];
	filterTags?: string[];
	typeFilter?: string[];
	filter?: (options: (EvaluatorOption<V> | EvaluatorGroup<V>)[]) => (EvaluatorOption<V> | EvaluatorGroup<V>)[];
	options?: any;
	onChange?: (items: EvaluatorOption<V>[]) => void;
}

export function EvaluatorMultiSelect<V>(props: EvaluatorMultiSelectProps<V>) {
	const shouldFilterEvaluatorOnTags = (option: EvaluatorOption<V>) => {
		const tags = (option as any)?.tags || [];
		const type = (option as any)?.type;

		if (props.keepTags?.length) {
			for (const tag of props.keepTags) {
				if (tags.some((t: any) => t.label === tag)) {
					return true;
				}
			}
			return false;
		}

		if (props.filterTags?.length || props.typeFilter?.length) {
			if (props.typeFilter?.includes(type)) {
				return false;
			}

			for (const tag of props.filterTags || []) {
				if (tags.some((t: any) => t.label === tag)) {
					return false;
				}
			}
		}
		return true;
	};

	const processedOptions = props.options
		? Array.isArray(props.options)
			? props.options.map((group) => ({
					...group,
					options: group.options.filter(shouldFilterEvaluatorOnTags),
				}))
			: props.options
		: undefined;

	const filteredOptions = props.filter ? props.filter(processedOptions as (EvaluatorOption<V> | EvaluatorGroup<V>)[]) : processedOptions;

	return (
		<AsyncMultiSelect
			{...props}
			onChange={(items) => {
				props.onChange?.(items as EvaluatorOption<V>[]);
			}}
			isNonAsync
			defaultOptions={filteredOptions}
			value={props.value}
			views={{
				option: (optionProps) => {
					return (
						<OptionWrapper
							{...optionProps}
							className={cn(
								optionProps.className,
								"text-content-primary hover:bg-background-highlight-primary/60",
								optionProps.isSelected ? "text-content-primary bg-transparent" : "",
							)}
						>
							<div className="flex w-full items-start justify-between">
								<div className="flex grow flex-col">
									<div className="flex">
										<span className="text-content-primary grow truncate text-sm font-medium">{optionProps.data.label}</span>
									</div>
									{optionProps.data.meta?.description && (
										<span className="text-content-tertiary max-w-[70%] text-sm">{optionProps.data.meta.description}</span>
									)}
								</div>
								<button className="flex items-center pt-0.5" type="button">
									{optionProps.isSelected ? (
										<Checkbox
											checked={optionProps.isSelected}
											className="h-4 w-4"
											onCheckedChange={(e) => {
												optionProps.selectOption(optionProps.data);
											}}
										/>
									) : (
										<PlusCircle className="h-4 w-4" strokeWidth={1.5} />
									)}
								</button>
							</div>
						</OptionWrapper>
					);
				},
				groupHeading: (groupProps) => {
					const data = groupProps.data as unknown as EvaluatorGroup<V>;
					return (
						<GroupHeadingWrapper {...groupProps} className={cn(groupProps.className, "bg-content-inverse sticky z-[1] m-0 !px-2 py-2")}>
							<div className="text-content-secondary flex items-center gap-1 capitalize">
								{data.icon && <div className="flex shrink-0 items-center">{data.icon}</div>}
								{data.label}
							</div>
						</GroupHeadingWrapper>
					);
				},
				group: (groupProps) => {
					return <GroupWrapper {...groupProps} className={cn(groupProps.className, "py-0!")} />;
				},
				multiValue: (multiValueProps) => {
					return (
						<MultiValueWrapper {...multiValueProps}>
							<div className="mr-1 flex items-center gap-1">
								{multiValueProps.data.meta?.icon && <div className="flex shrink-0 items-center">{multiValueProps.data.meta.icon}</div>}
								<span className="text-content-tertiary grow truncate text-sm font-medium">{multiValueProps.data.label}</span>
							</div>
						</MultiValueWrapper>
					);
				},
			}}
		/>
	);
}

function CustomOption<T>(props: OptionProps<Option<T>> & { selectProps: CustomOptionProps & CustomComponentsProps }) {
	const { Option } = components;

	if (props.selectProps.optionView) {
		return props.selectProps.optionView(props);
	}

	// So, this is a bit of a hack to style the `Create new option` button for react select. React select populates this property for this option
	if (props.selectProps.dynamicOptionCreation === true && (props.data as any).__isNew__) {
		return (
			<div className={cn("flex w-full flex-col gap-2", props.options.length > 1 ? "pt-2" : "")}>
				{props.options.length > 1 && <Separator />}
				<OptionWrapper {...props} className="flex w-full items-center justify-start gap-1">
					{props.selectProps.hidePlusIcon !== true && <PlusIcon size={14} />}
					{props.children}
				</OptionWrapper>
			</div>
		);
	}

	if ((props.data as any).__isNew__) {
		return (
			<div className={cn("flex w-full flex-col gap-2", props.options.length > 1 ? "pt-2" : "")}>
				{props.options.length > 1 && <Separator />}
				<OptionWrapper {...props} className="flex w-full items-center justify-start gap-1">
					{props.selectProps.hidePlusIcon !== true && <PlusIcon size={14} />}
					<div className="text-content-primary">{props.selectProps.createOptionText}</div>
				</OptionWrapper>
			</div>
		);
	}

	return (
		<OptionWrapper {...props}>
			{props.children}
			<div className="flex items-center justify-between">
				{props.selectProps.hideSelectedOptions !== true && props.isSelected && (
					<CheckIcon size={14} className={cn("text-content-primary", props.selectProps.checkIconStyling)} />
				)}
			</div>
		</OptionWrapper>
	);
}

function CustomControl<T>(props: ControlProps<Option<T>> & { selectProps: CustomComponentsProps }) {
	const { Control } = components;

	if (props.selectProps.controlView) {
		return props.selectProps.controlView(props);
	}

	return <ControlWrapper {...props}>{props.children}</ControlWrapper>;
}

function CustomDropdownIndicator<T>(
	props: DropdownIndicatorProps<Option<T>> & { selectProps: CustomDropdownIndicatorProps & CustomComponentsProps },
) {
	if (props.selectProps.dropdownIndicatorView) {
		return props.selectProps.dropdownIndicatorView(props);
	}

	if (props.selectProps.hideDropdownIndicator) {
		return null;
	}
	return <ChevronDown className="text-content-primary m-2 h-4 w-4 shrink-0 self-start opacity-50" />;
}

function CustomMultiValueRemove<T>(props: MultiValueRemoveProps<Option<T>> & { selectProps: CustomComponentsProps }) {
	const { MultiValueRemove } = components;

	if (props.selectProps.multiValueRemoveView) {
		return props.selectProps.multiValueRemoveView(props);
	}

	return (
		<MultiValueRemoveWrapper {...props}>
			<XIcon size={14} />
		</MultiValueRemoveWrapper>
	);
}

function CustomMultiValueLabel<T>(props: MultiValueGenericProps<Option<T>> & { selectProps: CustomComponentsProps }) {
	if (props.selectProps.multiValueLabelView) {
		return props.selectProps.multiValueLabelView(props);
	}

	return <Label className="text-content-tertiary text-sm font-normal">{props.children}</Label>;
}

function CustomMultiValue<T>(props: MultiValueProps<Option<T>> & { selectProps: CustomComponentsProps }) {
	const { MultiValue } = components;

	if (props.selectProps.multiValueView) {
		return props.selectProps.multiValueView(props);
	}

	return <MultiValueWrapper {...props} />;
}

function CustomGroupHeading<T>(props: GroupHeadingProps<Option<T>> & { selectProps: CustomComponentsProps }) {
	const { GroupHeading } = components;

	if (props.selectProps.groupHeadingView) {
		return props.selectProps.groupHeadingView(props);
	}

	return <GroupHeadingWrapper {...props} />;
}

function CustomGroup<T>(props: GroupProps<Option<T>> & { selectProps: CustomComponentsProps }) {
	const { Group } = components;

	if (props.selectProps.groupView) {
		return props.selectProps.groupView(props);
	}

	return <GroupWrapper {...props} />;
}

function CustomClearIndicator<T>(props: ClearIndicatorProps<Option<T>> & { selectProps: CustomComponentsProps }) {
	const { ClearIndicator } = components;

	if (props.selectProps.clearIndicatorView) {
		return props.selectProps.clearIndicatorView(props);
	}

	return <ClearIndicatorWrapper {...props} />;
}

function CustomIndicatorSeparator<T>(props: IndicatorSeparatorProps<Option<T>> & { selectProps: CustomComponentsProps }) {
	const { IndicatorSeparator } = components;

	if (props.selectProps.indicatorSeparatorView) {
		return props.selectProps.indicatorSeparatorView(props);
	}

	return <IndicatorSeparatorWrapper {...props} />;
}

function CustomInput<T>(props: InputProps<Option<T>> & { selectProps: CustomComponentsProps }) {
	const { Input } = components;

	if (props.selectProps.inputView) {
		return props.selectProps.inputView(props);
	}

	return <InputWrapper {...props} />;
}

function CustomLoadingIndicator<T>(props: LoadingIndicatorProps<Option<T>> & { selectProps: CustomComponentsProps }) {
	const { LoadingIndicator } = components;

	if (props.selectProps.loadingIndicatorView) {
		return props.selectProps.loadingIndicatorView(props);
	}

	return <LoadingIndicatorWrapper {...props} />;
}

function CustomMenu<T>(props: MenuProps<Option<T>> & { selectProps: CustomComponentsProps }) {
	const { Menu } = components;

	if (props.selectProps.menuView) {
		return props.selectProps.menuView(props);
	}

	return <MenuWrapper {...props} />;
}

function CustomMenuList<T>(props: MenuListProps<Option<T>> & { selectProps: CustomComponentsProps }) {
	const { MenuList } = components;

	if (props.selectProps.menuListView) {
		return props.selectProps.menuListView(props);
	}

	return <MenuListWrapper {...props} />;
}

function CustomMultiValueContainer<T>(props: MultiValueGenericProps<Option<T>> & { selectProps: CustomComponentsProps }) {
	const { MultiValueContainer } = components;

	if (props.selectProps.multiValueContainerView) {
		return props.selectProps.multiValueContainerView(props);
	}

	return <MultiValueContainerWrapper {...props} />;
}

function CustomNoOptionsMessage<T>(props: NoticeProps<Option<T>> & { selectProps: CustomComponentsProps }) {
	const { NoOptionsMessage } = components;

	if (props.selectProps.noOptionsMessageView) {
		return props.selectProps.noOptionsMessageView(props);
	}

	return <NoOptionsMessageWrapper {...props} />;
}

function CustomPlaceholder<T>(props: PlaceholderProps<Option<T>> & { selectProps: CustomPlaceholderProps & CustomComponentsProps }) {
	const { Placeholder } = components;

	if (props.selectProps.placeholderView) {
		return props.selectProps.placeholderView(props);
	}

	return (
		<PlaceholderWrapper {...props} className="text-content-disabled flex flex-row items-center">
			{props.selectProps.hideSearchIcon !== true && <Icons.search className="mr-2 h-3.5 w-3.5" strokeWidth={1.5} />}{" "}
			{props.selectProps.placeholder}
		</PlaceholderWrapper>
	);
}

function CustomSingleValue<T>(props: SingleValueProps<Option<T>> & { selectProps: CustomComponentsProps }) {
	const { SingleValue } = components;

	if (props.selectProps.singleValueView) {
		return props.selectProps.singleValueView(props);
	}

	return <SingleValueWrapper {...props} />;
}

function CustomValueContainer<T>(props: ValueContainerProps<Option<T>> & { selectProps: CustomComponentsProps }) {
	const { ValueContainer } = components;

	if (props.selectProps.valueContainerView) {
		return props.selectProps.valueContainerView(props);
	}

	return <ValueContainerWrapper {...props} />;
}
