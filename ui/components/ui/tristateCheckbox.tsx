import React, { useMemo } from "react";
import { Check, Minus, X } from "lucide-react";
import { cn } from "@/lib/utils";

type TriState = "all" | "some" | "none";

export interface TriStateCheckboxProps {
	/** All item ids that this checkbox controls */
	allIds: string[];
	/** Currently selected item ids (subset of allIds) */
	selectedIds: string[];

	/** Called with the *next* set of selected ids after toggle */
	onChange: (nextSelectedIds: string[]) => void;

	/** Optional label to render to the right of the checkbox */
	label?: React.ReactNode;

	/** Optional disabled state */
	disabled?: boolean;

	/** Extra tailwind classes for the wrapper */
	className?: string;

	/** Accessible name for icon-only checkbox (e.g. when label is rendered elsewhere) */
	ariaLabel?: string;
}

export const TriStateCheckbox: React.FC<TriStateCheckboxProps> = ({
	allIds,
	selectedIds,
	onChange,
	label,
	disabled = false,
	className = "",
	ariaLabel,
}) => {
	const state: TriState = useMemo(() => {
		if (!allIds.length) return "none";

		const selectedSet = new Set(selectedIds);
		const selectedCount = allIds.filter((id) => selectedSet.has(id)).length;

		if (selectedCount === 0) return "none";
		if (selectedCount === allIds.length) return "all";
		return "some";
	}, [allIds, selectedIds]);

	const handleClick = () => {
		if (disabled) return;

		let nextSelected: string[];

		switch (state) {
			case "all":
				// clear all
				nextSelected = [];
				break;
			case "some":
			case "none":
			default:
				// select all
				nextSelected = [...allIds];
				break;
		}

		onChange(nextSelected);
	};

	const ariaChecked: boolean | "mixed" = state === "all" ? true : state === "none" ? false : "mixed";

	const isChecked = state === "all";
	const isIndeterminate = state === "some";

	return (
		<button
			type="button"
			onClick={handleClick}
			disabled={disabled}
			role="checkbox"
			aria-checked={ariaChecked}
			aria-label={ariaLabel}
			className={cn(
				"inline-flex items-center gap-2 focus:outline-none",
				"focus-visible:ring-ring focus-visible:ring-offset-background focus-visible:ring-2 focus-visible:ring-offset-2",
				disabled ? "cursor-not-allowed opacity-50" : "cursor-pointer",
				className,
			)}
		>
			<span
				className={cn(
					"peer flex h-5 w-5 items-center justify-center rounded-[4px] border shadow-xs transition-shadow",
					"border-input dark:bg-input/30",
					isChecked && "bg-primary border-primary text-primary-foreground dark:bg-primary dark:border-primary",
					isIndeterminate && "bg-primary/80 border-primary/80 text-primary-foreground dark:bg-primary/90 dark:border-primary/90",
					state === "none" && "bg-destructive/5 border-destructive/30 text-destructive dark:bg-destructive/20 dark:border-destructive/40",
					!isChecked && !isIndeterminate && state !== "none" && "bg-background",
					"focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px]",
				)}
			>
				{isChecked && <Check className="size-3.5" />}
				{isIndeterminate && <Minus className="size-3.5" />}
				{state === "none" && <X className="size-3.5" />}
			</span>

			{label && <span className="text-foreground text-sm">{label}</span>}
		</button>
	);
};
