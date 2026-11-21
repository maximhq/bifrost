import { cn } from "@/lib/utils";
import { Separator } from "../../separator";
import { DropdownItem } from "./dropdownItem";
import { DropdownOption } from "./types";
import { ReactNode } from "react";

interface DropdownGroupProps {
	label?: string;
	options: DropdownOption[];
	onSelectItem: (option: DropdownOption) => void;
	parentIndex: number;
	isOptionSelected: (option: DropdownOption, groupIndex: number) => boolean;
	onHover: (groupIndex: number) => void;
	icon?: ReactNode;
	groupHeadingClassName?: string;
}

export function DropdownGroup({
	label,
	options,
	onSelectItem,
	isOptionSelected,
	onHover,
	parentIndex,
	icon,
	groupHeadingClassName,
}: DropdownGroupProps) {
	return (
		<>
			{/* Bleeds to the menu edge: the menu has p-1, the separator cancels it. */}
			{parentIndex > 0 && <Separator className="bg-border -mx-1 my-1 w-[calc(100%+0.5rem)]" />}
			<div className="flex flex-col">
				{label && (
					<div className={cn("flex items-center gap-1.5 pt-2 pb-1.5", groupHeadingClassName)}>
						{icon}
						<p className="text-muted-foreground text-xs font-medium">{label}</p>
					</div>
				)}
				<div className="flex flex-col">
					{options.map((option, groupIndex) => (
						<DropdownItem
							key={option.value ?? groupIndex}
							field={option}
							onSelectItem={onSelectItem}
							isSelected={isOptionSelected(option, groupIndex)}
							onHover={() => onHover(groupIndex)}
							description={option.description}
						/>
					))}
				</div>
			</div>
		</>
	);
}