import React from "react";
import { Switch } from "./switch";
import { cn } from "./utils";

interface Props {
	className?: string;
	label?: string;
	val: boolean;
	setVal: React.Dispatch<React.SetStateAction<boolean>> | ((val: boolean) => void);
	required?: boolean;
	disabled?: boolean;
	caption?: string;
	'data-testid'?: string;
}

const Toggle = ({ className, label, val, setVal, required = false, disabled = false, caption, 'data-testid': testId }: Props) => {
	return (
		<div className="w-full">
			<label
				className={cn(
					`flex w-full items-center justify-between gap-2 py-2 text-sm select-none ${disabled ? "cursor-default" : "cursor-pointer"}`,
					className,
				)}
			>
				{label && (
					<div className="">
						{label} {required && "*"}
					</div>
				)}
				<Switch checked={val} onCheckedChange={setVal} data-testid={testId} />
			</label>
			{caption && <div className="mt-1 text-xs text-gray-400">{caption}</div>}
		</div>
	);
};

export default Toggle;
