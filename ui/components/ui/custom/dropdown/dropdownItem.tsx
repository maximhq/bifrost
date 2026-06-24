import { ReactNode, useEffect, useRef } from "react";
import { DropdownOption } from "./types";
import { cn } from "@/lib/utils";

interface DropdownItemProps<T extends DropdownOption> {
	field: T;
	onSelectItem: (field: T) => void;
	isSelected?: boolean;
	onHover?: () => void;
	description?: ReactNode;
}

export function DropdownItem<T extends DropdownOption>({ field, onSelectItem, isSelected, onHover, description }: DropdownItemProps<T>) {
	const itemRef = useRef<HTMLDivElement>(null);

	useEffect(() => {
		if (isSelected && itemRef.current) {
			itemRef.current.scrollIntoView({
				block: "nearest",
				inline: "nearest",
			});
		}
	}, [isSelected]);

	return (
		<div
			ref={itemRef}
			role="option"
			tabIndex={0}
			aria-selected={isSelected}
			className={cn(
				"text-content-primary text-body-medium flex cursor-pointer items-center gap-1 rounded-sm px-2 py-1.5 font-normal outline-hidden select-none",
				isSelected ? "bg-background-highlight-primary" : "",
			)}
			onMouseDown={(e) => {
				e.preventDefault();
				e.stopPropagation();
				onSelectItem(field);
			}}
			onTouchStart={(e) => {
				e.preventDefault();
				e.stopPropagation();
				onSelectItem(field);
			}}
			onKeyDown={(e) => {
				if (e.key === "Enter") {
					e.preventDefault();
					e.stopPropagation();
					onSelectItem(field);
				}
			}}
			onMouseEnter={onHover}
			onMouseLeave={() => {}}
		>
			{field.view ?? (
				<div className="flex flex-col overflow-hidden overscroll-auto">
					<div className="flex items-center gap-1">
						{field.icon}
						<span className="block truncate overscroll-auto">{field.label ?? field.value}</span>
					</div>
					{description && <span className="text-content-tertiary block truncate overscroll-auto text-xs">{description}</span>}
				</div>
			)}
		</div>
	);
}