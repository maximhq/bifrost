
import { cn } from "@/lib/utils";
import { Separator } from "../../separator";
import { DropdownItem } from "./dropdownItem";
import { DropdownOption } from "./types";

interface DropdownGroupProps {
	label?: string;
	options: DropdownOption[];
	onSelectItem: (option: DropdownOption) => void;
	parentIndex: number;
	isOptionSelected: (option: DropdownOption, groupIndex: number) => boolean;
	onHover: (groupIndex: number) => void;
	icon?: React.ReactNode;
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
			{parentIndex > 0 && <Separator className="bg-border mt-2 mb-4" />}
			<div className="flex flex-col gap-1">
				{label && (
					<div className={cn("flex items-center gap-1", groupHeadingClassName)}>
						{icon}
						<p className="text-content-tertiary text-sm font-medium">{label}</p>
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
