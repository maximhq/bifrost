/**
 * Combinator Selector Component for CEL Rule Builder
 * Allows selection of AND/OR combinators between rules
 */

"use client";

import { Button } from "@/components/ui/button";
import { CombinatorSelectorProps } from "react-querybuilder";

export function CombinatorSelector({ value, handleOnChange, options }: CombinatorSelectorProps) {
	return (
		<div className="flex gap-1">
			{options.map((option) => {
				if ("options" in option) return null; // Skip option groups
				return (
					<Button
						key={option.name}
						type="button"
						variant={value === option.name ? "default" : "outline"}
						size="sm"
						onClick={() => handleOnChange(option.name)}
						className="px-3"
					>
						{option.label.toUpperCase()}
					</Button>
				);
			})}
		</div>
	);
}
