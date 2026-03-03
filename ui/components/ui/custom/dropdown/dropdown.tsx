"use client";

import React, { useEffect, useMemo, useRef } from "react";
import { DropdownGroup } from "./dropdownGroup";
import { DropdownItem } from "./dropdownItem";
import { DropdownOption, FlattenedDropdownOption } from "./types";
import { cn } from "@/lib/utils";

interface CustomDropdownProps<T = {}> {
	options: DropdownOption<T>[];
	onChange?: (value: DropdownOption<T> | undefined) => void;
	defaultValue?: DropdownOption<T>;
	value?: DropdownOption<T>;
	className?: string;
	selectFirstOptionByDefault?: boolean;
	style?: React.CSSProperties;
	emptyViewText?: string;
	groupHeadingClassName?: string;
	selectedIndex?: number;
	onHover?: (index: number) => void;
}

export function CustomDropdown<T = {}>({
	options,
	onChange,
	defaultValue,
	style,
	className,
	selectFirstOptionByDefault,
	emptyViewText,
	groupHeadingClassName,
	selectedIndex: controlledSelectedIndex,
	onHover,
}: CustomDropdownProps<T>) {
	const [internalSelectedIndex, setInternalSelectedIndex] = React.useState(selectFirstOptionByDefault ? 0 : -1);
	const isControlled = controlledSelectedIndex !== undefined;
	const selectedIndex = isControlled ? controlledSelectedIndex : internalSelectedIndex;
	const containerRef = useRef<HTMLDivElement>(null);

	const flattenedOptions = useMemo(() => {
		return options.reduce<FlattenedDropdownOption[]>((acc, option, parentIndex) => {
			if (option.type === "group" && option.options) {
				const groupOptions = option.options.map((groupOption, groupIndex) => ({
					option: groupOption,
					groupIndex,
					parentIndex,
				}));
				return [...acc, ...groupOptions];
			}
			return [...acc, { option, parentIndex, groupIndex: undefined }];
		}, []);
	}, [options]);

	useEffect(() => {
		if (isControlled) return;

		const handleKeyDown = (e: KeyboardEvent) => {
			switch (e.key) {
				case "ArrowDown":
					e.preventDefault();
					setInternalSelectedIndex((prev) => (prev < flattenedOptions.length - 1 ? prev + 1 : prev));
					break;
				case "ArrowUp":
					e.preventDefault();
					setInternalSelectedIndex((prev) => (prev > 0 ? prev - 1 : prev));
					break;
				case "Enter":
					e.preventDefault();
					e.stopPropagation();
					if (selectedIndex >= 0 && selectedIndex < flattenedOptions.length) {
						const newValue = flattenedOptions[selectedIndex].option;
						onChange?.(newValue);
					}
					break;
				case "Escape":
					e.preventDefault();
					// onChange?.(undefined);
					break;
			}
		};

		document.addEventListener("keydown", handleKeyDown);
		return () => {
			document.removeEventListener("keydown", handleKeyDown);
		};
	}, [flattenedOptions, selectedIndex, onChange, isControlled]);

	const isOptionSelected = (option: DropdownOption, groupIndex?: number, parentIndex?: number): boolean => {
		const selectedItem = flattenedOptions[selectedIndex];
		if (!selectedItem) return false;

		if (groupIndex !== undefined && parentIndex !== undefined) {
			return selectedItem.groupIndex === groupIndex && selectedItem.parentIndex === parentIndex;
		}

		return selectedItem.option === option;
	};

	useEffect(() => {
		// Resetting the selected index if the options change
		if (!isControlled && internalSelectedIndex != (selectFirstOptionByDefault ? 0 : -1)) {
			setInternalSelectedIndex(selectFirstOptionByDefault ? 0 : -1);
		}
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [flattenedOptions, isControlled, selectFirstOptionByDefault]);

	const handleSelectItem = (selectedOption: DropdownOption) => {
		onChange?.(selectedOption);
	};

	useEffect(() => {
		if (selectedIndex === flattenedOptions.length - 1) {
			containerRef.current?.parentElement?.scrollTo({ top: containerRef.current.scrollHeight + 100, behavior: "smooth" });
		} else if (selectedIndex === 0) {
			containerRef.current?.parentElement?.scrollTo({ top: 0, behavior: "smooth" });
		}
	}, [flattenedOptions, selectedIndex]);

	return (
		<div className={cn("flex flex-col rounded-md border p-2", className)} ref={containerRef} style={style}>
			{options.length === 0 ? (
				<div className="w-[350px]">
					<p className="text-content-secondary text-md">{emptyViewText}</p>
				</div>
			) : (
				<>
					{options.map((option, parentIndex) => {
						if (option.type === "group") {
							return (
								<DropdownGroup
									key={`group-${parentIndex}`}
									onSelectItem={handleSelectItem}
									label={option.label}
									icon={option.icon}
									options={option.options ?? []}
									parentIndex={parentIndex}
									isOptionSelected={(groupOption, groupIndex) => isOptionSelected(groupOption, groupIndex, parentIndex)}
									onHover={(groupIndex) => {
										const flatIndex = flattenedOptions.findIndex(
											(item) => item.parentIndex === parentIndex && item.groupIndex === groupIndex,
										);
										if (flatIndex !== -1) {
											if (!isControlled) setInternalSelectedIndex(flatIndex);
											onHover?.(flatIndex);
										}
									}}
									groupHeadingClassName={groupHeadingClassName}
								/>
							);
						}
						return (
							<DropdownItem
								field={option}
								key={`item-${option.value ?? parentIndex}`}
								onSelectItem={handleSelectItem}
								isSelected={isOptionSelected(option)}
								onHover={() => {
									const flatIndex = flattenedOptions.findIndex((item) => item.option === option);
									if (flatIndex !== -1) {
										if (!isControlled) setInternalSelectedIndex(flatIndex);
										onHover?.(flatIndex);
									}
								}}
								description={option.description}
							/>
						);
					})}
				</>
			)}
		</div>
	);
}
