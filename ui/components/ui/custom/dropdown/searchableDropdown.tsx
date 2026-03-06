"use client";
import React, { useMemo, useState } from "react";
import { CustomDropdown } from "./dropdown";
import { DropdownOption } from "./types";
import { cn } from "@/lib/utils";
import { Icons } from "../../icons";

const EMPTY_SELECTED_VALUES: ReadonlyArray<DropdownOption<unknown>> = [];
interface SearchableDropdownProps<T = {}> {
	options: DropdownOption<T>[];
	onChange?: (value: DropdownOption<T> | undefined) => void;
	defaultValue?: DropdownOption<T>;
	value?: DropdownOption<T>;
	className?: string;
	selectFirstOptionByDefault?: boolean;
	style?: React.CSSProperties;
	emptyViewText?: string;
	groupHeadingClassName?: string;
	searchPlaceholder?: string;
	searchClassName?: string;
	noResultsText?: string;
	maxHeight?: string | number;
	dropdownClassName?: string;
	removeEmptyGroups?: boolean;
	hideSelectedValues?: boolean;
	selectedValues?: DropdownOption<T>[];
}

export function SearchableDropdown<T = {}>({
	options,
	onChange,
	defaultValue,
	value,
	className,
	selectFirstOptionByDefault,
	style,
	emptyViewText,
	groupHeadingClassName,
	searchPlaceholder = "Search",
	searchClassName,
	noResultsText = "No results found",
	maxHeight = "300px",
	dropdownClassName,
	removeEmptyGroups,
	hideSelectedValues = false,
	selectedValues,
	...props
}: SearchableDropdownProps<T>) {
	const [searchTerm, setSearchTerm] = useState("");
	const selectedValuesSafe = selectedValues ?? EMPTY_SELECTED_VALUES;

	const filteredOptions = useMemo(() => {
		const searchTermLower = searchTerm.toLowerCase();

		// Helper function to check if an option is selected
		const isOptionSelected = (option: DropdownOption<T>): boolean => {
			if (!hideSelectedValues) return false;

			// Check against current value
			if (value && option.value === value.value) return true;

			// Check against selectedValues array
			return selectedValuesSafe.some((selectedOption) => selectedOption.value === option.value);
		};

		const filterOption = (option: DropdownOption<T>): DropdownOption<T> | null => {
			if (option.type === "group") {
				const filteredGroupOptions = option.options?.map(filterOption).filter(Boolean) || [];
				if ((removeEmptyGroups ?? true) && filteredGroupOptions.length === 0) {
					return null;
				}
				return {
					...option,
					options: filteredGroupOptions as DropdownOption<T>[],
				};
			} else {
				// First check if this option should be hidden because it's selected
				if (isOptionSelected(option)) {
					return null;
				}

				// Then check search term filtering
				if (!searchTerm.trim()) {
					return option;
				}

				const labelMatches = option.label?.toLowerCase().includes(searchTermLower);
				const valueMatches = option.value?.toLowerCase().includes(searchTermLower);
				const descriptionMatches =
					typeof option.description === "string" ? option.description.toLowerCase().includes(searchTermLower) : false;

				if (labelMatches || valueMatches || descriptionMatches) {
					return option;
				}
				return null;
			}
		};

		return options.map(filterOption).filter(Boolean) as DropdownOption<T>[];
	}, [options, searchTerm, hideSelectedValues, value, selectedValuesSafe]);

	const handleSearchChange = (e: React.ChangeEvent<HTMLInputElement>) => {
		setSearchTerm(e.target.value);
	};

	const handleSearchKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
		if (e.key === "ArrowDown" || e.key === "ArrowUp" || e.key === "Enter") {
			e.preventDefault();
			const dropdownContainer = e.currentTarget.parentElement?.querySelector('[role="listbox"]') as HTMLElement;
			dropdownContainer?.focus?.();
		}
	};

	return (
		<div className={cn("flex flex-col", className)} style={style}>
			<div className="relative mb-2">
				<div className="pointer-events-none absolute inset-y-0 left-0 flex items-center pl-3">
					<Icons.search
						className="h-4 w-4 text-gray-400"
						fill="none"
						stroke="currentColor"
						viewBox="0 0 24 24"
						aria-hidden="true"
						focusable="false"
					/>
				</div>
				<input
					autoFocus
					type="text"
					placeholder={searchPlaceholder}
					value={searchTerm}
					onChange={handleSearchChange}
					onKeyDown={handleSearchKeyDown}
					aria-label={searchPlaceholder}
					className={cn(
						"w-full rounded-md border border-gray-300 py-2 pr-3 pl-10 text-sm",
						"focus:outline-none",
						"placeholder-gray-400",
						searchClassName,
					)}
				/>
			</div>

			<div className="overflow-y-auto" style={{ maxHeight: typeof maxHeight === "number" ? `${maxHeight}px` : maxHeight }}>
				<CustomDropdown
					options={filteredOptions}
					onChange={onChange}
					defaultValue={defaultValue}
					value={value}
					selectFirstOptionByDefault={selectFirstOptionByDefault}
					emptyViewText={filteredOptions.length === 0 && searchTerm ? noResultsText : emptyViewText}
					groupHeadingClassName={groupHeadingClassName}
					className={cn("border-0 p-0", dropdownClassName)}
					{...props}
				/>
			</div>
		</div>
	);
}
