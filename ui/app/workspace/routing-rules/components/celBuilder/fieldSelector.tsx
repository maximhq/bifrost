/**
 * Field Selector Component for CEL Rule Builder
 * Allows selection of fields for building CEL expressions
 * For keyValue fields (headers/params), also renders "has value" label and key input
 */

"use client";

import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useCallback, useMemo } from "react";
import { FieldSelectorProps, RuleGroupType, RuleType } from "react-querybuilder";

/**
 * Recursively find and update a rule's value by path in the query tree.
 */
function updateRuleValueAtPath(
	query: RuleGroupType,
	targetPath: number[],
	newValue: string,
): RuleGroupType {
	if (targetPath.length === 0) return query;

	const [currentIndex, ...restPath] = targetPath;
	const newRules = [...query.rules];

	if (restPath.length === 0) {
		// We're at the target rule
		const rule = newRules[currentIndex] as RuleType;
		newRules[currentIndex] = { ...rule, value: newValue };
	} else {
		// Recurse into nested group
		newRules[currentIndex] = updateRuleValueAtPath(
			newRules[currentIndex] as RuleGroupType,
			restPath,
			newValue,
		);
	}

	return { ...query, rules: newRules };
}

export function FieldSelector({ value, handleOnChange, options, rule, path, schema }: FieldSelectorProps) {
	// Check if this is a keyValue field (headers/params)
	const fieldData = useMemo(
		() => schema?.fields?.find((f) => "value" in f && f.value === value),
		[schema?.fields, value],
	);
	const isKeyValueField = fieldData && "inputType" in fieldData && fieldData.inputType === "keyValue";

	// Parse the key from the rule's value ("key:value" or just "key")
	const headerKey = useMemo(() => {
		if (!isKeyValueField || !rule?.value || typeof rule.value !== "string") return "";
		const colonIndex = rule.value.indexOf(":");
		if (colonIndex > 0) return rule.value.substring(0, colonIndex).trim();
		return rule.value.trim();
	}, [isKeyValueField, rule?.value]);

	const handleKeyChange = useCallback(
		(newKey: string) => {
			if (!schema || !path) return;
			// Preserve the existing value part
			const currentValue = typeof rule?.value === "string" ? rule.value : "";
			const colonIndex = currentValue.indexOf(":");
			const valuePart = colonIndex > 0 ? currentValue.substring(colonIndex + 1).trim() : "";

			let updatedValue: string;
			if (newKey && valuePart) {
				updatedValue = `${newKey}:${valuePart}`;
			} else if (newKey) {
				updatedValue = newKey;
			} else {
				updatedValue = "";
			}

			// Update the rule value via query dispatch
			const currentQuery = schema.getQuery() as RuleGroupType;
			const updatedQuery = updateRuleValueAtPath(currentQuery, path, updatedValue);
			schema.dispatchQuery(updatedQuery);
		},
		[schema, path, rule?.value],
	);

	return (
		<div className="flex items-center gap-2">
			<Select value={value || ""} onValueChange={handleOnChange}>
				<SelectTrigger className="w-[180px]" data-testid="cel-builder-field-selector-select">
					<SelectValue placeholder="Select field..." />
				</SelectTrigger>
				<SelectContent>
					{options.map((option) => {
						// Handle option groups (not currently used, but type-safe)
						if ("options" in option) {
							return null;
						}
						// Handle regular options - skip empty values
						if (!option.name) {
							return null;
						}
						return (
							<SelectItem key={option.name} value={option.name} disabled={option.disabled}>
								{option.label}
							</SelectItem>
						);
					})}
				</SelectContent>
			</Select>
			{isKeyValueField && (
				<>
					<span className="text-muted-foreground text-sm whitespace-nowrap">has key</span>
					<Input
						type="text"
						value={headerKey}
						onChange={(e) => handleKeyChange(e.target.value)}
						placeholder={`${fieldData?.label || "Key"} name (e.g., x-api-key)`}
						className="w-[180px]"
						data-testid="cel-builder-field-selector-key-input"
					/>
				</>
			)}
		</div>
	);
}
