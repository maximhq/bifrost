/**
 * Field Selector Component for CEL Rule Builder
 * Allows selection of fields for building CEL expressions
 */

"use client";

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { FieldSelectorProps } from "react-querybuilder";

export function FieldSelector({ value, handleOnChange, options }: FieldSelectorProps) {
	return (
		<Select value={value || ""} onValueChange={handleOnChange}>
			<SelectTrigger className="w-[180px]">
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
	);
}
