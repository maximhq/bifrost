/**
 * Value Editor Component for CEL Rule Builder
 * Smart input component that adapts based on operator and field type
 */

"use client";

import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { useEffect, useState } from "react";
import { ValueEditorProps, ValueEditorType } from "react-querybuilder";
import { validateRegexPattern } from "@/lib/utils/celConverterRouting";
import { AsyncMultiSelect } from "@/components/ui/asyncMultiselect";
import { Option } from "@/components/ui/multiselectUtils";
import { RenderProviderIcon, ProviderIconType } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";

export function ValueEditor({ value, handleOnChange, operator, fieldData, type, context }: ValueEditorProps) {
	// Compute all conditions upfront before any early returns
	const isArrayOperator = operator === "in" || operator === "notIn";
	const isRegexOperator = operator === "matches";
	const isNullOperator = operator === "null" || operator === "notNull";

	// Get valueEditorType, handling both string and function types
	const valueEditorType =
		typeof fieldData?.valueEditorType === "function" ? fieldData.valueEditorType(operator) : fieldData?.valueEditorType;
	const isKeyValueType = valueEditorType === ("keyValue" as ValueEditorType);
	const isSelectType = valueEditorType === ("select" as ValueEditorType);

	// Parse keyValue format: "key:value" or just "key" for null/notNull operators
	const [keyValuePair, setKeyValuePair] = useState(() => {
		if (!isKeyValueType) return { key: "", value: "" };
		if (typeof value === "string" && value) {
			if (value.includes(":")) {
				const parts = value.split(":");
				const key = parts[0] || "";
				const valuePart = parts.slice(1).join(":") || "";
				return { key: key.trim(), value: valuePart.trim() };
			}
			// Key-only value (for null/notNull operators)
			return { key: value.trim(), value: "" };
		}
		return { key: "", value: "" };
	});

	useEffect(() => {
		if (isKeyValueType && typeof value === "string" && value) {
			if (value.includes(":")) {
				const parts = value.split(":");
				const key = parts[0] || "";
				const valuePart = parts.slice(1).join(":") || "";
				setKeyValuePair({ key: key.trim(), value: valuePart.trim() });
			} else {
				// Key-only value (for null/notNull operators)
				setKeyValuePair({ key: value.trim(), value: "" });
			}
		} else {
			setKeyValuePair({ key: "", value: "" });
		}
	}, [value, isKeyValueType]);

	// For null/notNull operators, no value input needed
	// (keyValue fields show key input in FieldSelector)
	if (isNullOperator) {
		return null;
	}

	// For keyValue fields, the key input is in FieldSelector.
	// Here we handle updating the value part while preserving the key.
	const handleKeyValueValueChange = (newValue: string) => {
		const key = keyValuePair.key;
		setKeyValuePair({ ...keyValuePair, value: newValue });
		if (key && newValue) {
			handleOnChange(`${key}:${newValue}`);
		} else if (key) {
			handleOnChange(key);
		} else {
			handleOnChange("");
		}
	};

	// Handle model field with ModelMultiselect
	const isModelField = fieldData?.name === "model";
	if (isModelField && isSelectType) {
		// For array operators (in, notIn), use multi-select
		if (isArrayOperator) {
			let selectedModels: string[] = [];
			if (typeof value === "string" && value) {
				try {
					selectedModels = JSON.parse(value);
					if (!Array.isArray(selectedModels)) {
						selectedModels = [value];
					}
				} catch {
					selectedModels = value
						.split(",")
						.map((v) => v.trim())
						.filter((v) => v);
				}
			}

			const handleMultiModelChange = (selected: string[]) => {
				handleOnChange(selected.length > 0 ? JSON.stringify(selected) : "");
			};

			return (
				<ModelMultiselect
					value={selectedModels}
					onChange={handleMultiModelChange}
					placeholder="Select models..."
					loadModelsOnEmptyProvider
					className="!min-h-9 w-[360px]"
				/>
			);
		}

		// For single operators (=, !=), use single select
		return (
			<ModelMultiselect
				value={value || ""}
				onChange={handleOnChange}
				placeholder="Search for a model..."
				isSingleSelect
				loadModelsOnEmptyProvider
				className="w-[360px] border-input"
			/>
		);
	}

	// Handle select type (for provider dropdown)
	if (isSelectType && fieldData?.values) {
		// For array operators with provider, use multi-select dropdown
		if (isArrayOperator) {
			// Parse comma-separated or JSON array value
			let selectedValues: string[] = [];
			if (typeof value === "string" && value) {
				try {
					// Try parsing as JSON array first
					selectedValues = JSON.parse(value);
					if (!Array.isArray(selectedValues)) {
						selectedValues = [value];
					}
				} catch {
					// Fall back to comma-separated
					selectedValues = value
						.split(",")
						.map((v) => v.trim())
						.filter((v) => v);
				}
			}

			const selectedOptions: Option<string>[] = selectedValues.map((val) => ({
				value: val,
				label: (fieldData.values as any[]).find((opt) => (opt as any).name === val)?.label || val,
			}));

			const allOptions: Option<string>[] = (fieldData.values as any[])
				.filter((opt) => !("options" in opt) && (opt as any).name)
				.map((opt) => ({
					value: (opt as any).name,
					label: (opt as any).label,
				}));

			const handleMultiselectChange = (selected: Option<string>[]) => {
				const values = selected.map((opt) => opt.value);
				handleOnChange(values.length > 0 ? JSON.stringify(values) : "");
			};

			return (
				<AsyncMultiSelect
					value={selectedOptions}
					onChange={handleMultiselectChange}
					defaultOptions={allOptions}
					isNonAsync={true}
					isClearable={false}
					placeholder="Select providers..."
					className="w-[360px]"
					triggerClassName="!shadow-none !border-border h-10"
					menuClassName="!z-[100] w-full cursor-pointer"
				/>
			);
		}

		// Check if this is a provider field to render icons in trigger
		const isProviderField = fieldData?.name === "provider";

		return (
			<Select value={value || ""} onValueChange={handleOnChange}>
				<SelectTrigger className="w-[360px]">
					{isProviderField && value ? (
						<div className="flex items-center gap-2">
							<RenderProviderIcon
								provider={value as ProviderIconType}
								size="sm"
								className="h-4 w-4"
							/>
							<span>{getProviderLabel(value)}</span>
						</div>
					) : (
						<SelectValue placeholder={fieldData.placeholder || "Select..."} />
					)}
				</SelectTrigger>
				<SelectContent>
					{fieldData.values.map((option) => {
						if ("options" in option) return null; // Skip option groups
						const optName = (option as any).name || "";
						if (!optName) return null; // Skip empty values — SelectItem requires non-empty value
						const optLabel = (option as any).label || optName;
						const optDisabled = (option as any).disabled || false;

						let iconElement: React.ReactNode | undefined;
						let displayLabel = optLabel;

						if (isProviderField) {
							iconElement = (
								<RenderProviderIcon
									provider={optName as ProviderIconType}
									size="sm"
									className="h-4 w-4"
								/>
							);
							displayLabel = getProviderLabel(optName);
						}

						return (
							<SelectItem key={optName} value={optName} disabled={optDisabled} icon={iconElement}>
								{displayLabel}
							</SelectItem>
						);
					})}
				</SelectContent>
			</Select>
		);
	}

	// Handle keyValue type (for header and parameter)
	// Key input is rendered in FieldSelector, only show value input here
	if (isKeyValueType) {
		return (
			<Input
				type="text"
				value={keyValuePair.value}
				onChange={(e) => handleKeyValueValueChange(e.target.value)}
				placeholder="Value"
				className="w-[180px]"
				data-testid="cel-builder-keyvalue-value-input"
			/>
		);
	}


	const placeholder = isArrayOperator
		? "Enter comma-separated values or JSON array"
		: isRegexOperator
			? "e.g., .* (any), openai|anthropic (multiple), ^gpt.* (prefix)"
			: fieldData?.placeholder || "Enter value...";

	// Use textarea for array inputs
	if (isArrayOperator) {
		return (
			<Textarea
				value={value || ""}
				onChange={(e) => handleOnChange(e.target.value)}
				placeholder={placeholder}
				className="min-h-[80px] w-[360px] font-mono text-sm"
			/>
		);
	}

	// Use text input with validation for regex
	if (isRegexOperator) {
		const regexError = value ? validateRegexPattern(String(value)) : null;

		return (
			<div className="flex flex-col gap-1">
				<Input
					type="text"
					value={value || ""}
					onChange={(e) => handleOnChange(e.target.value)}
					placeholder={placeholder}
					className={`w-[360px] font-mono text-sm ${regexError ? "border-red-500 bg-red-50 dark:bg-red-950" : ""
						}`}
				/>
				{regexError && (
					<p className="text-xs text-red-600 dark:text-red-400">{regexError}</p>
				)}
			</div>
		);
	}

	// Use regular input for single values
	return (
		<Input
			type={type === ("number" as ValueEditorType) ? "number" : "text"}
			value={value || ""}
			onChange={(e) => handleOnChange(e.target.value)}
			placeholder={placeholder}
			className="w-[360px]"
		/>
	);
}
