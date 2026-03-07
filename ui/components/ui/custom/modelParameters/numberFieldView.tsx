
import { HelpCircle } from "lucide-react";
import { useEffect } from "react";
import { Label } from "@/components/ui/label";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Parameter } from "./types";
import { cn } from "@/lib/utils";
import NumberInput from "../number";
import { Slider } from "@/components/ui/slider";

interface Props {
	field: Parameter;
	config: Record<string, unknown>;
	onChange: (value: any) => void;
	disabled?: boolean;
	onInvalid?: (invalid: boolean, field?: string) => void;
	className?: string;
	disabledText?: string;
}

export default function NumberFieldView(props: Props) {
	const { field, config } = props;

	const invalid = isInvalid(config[field.id] as number, field.range!, field.id);

	useEffect(() => {
		if (!props.onInvalid) return;
		if (invalid) {
			props.onInvalid(true, field.id);
		} else {
			props.onInvalid(false, field.id);
		}
	}, [invalid]);

	return (
		<div className={cn("flex flex-col gap-3", props.className)}>
			<div className="flex flex-row items-center overflow-hidden">
				<div className="flex flex-row items-center gap-1 pr-1 grow">
					<Label className="truncate">{field.label}</Label>
					{field.helpText && (
						<TooltipProvider delayDuration={200}>
							<Tooltip>
								<TooltipTrigger>
									<HelpCircle className="text-content-disabled h-3.5 w-3.5" />
								</TooltipTrigger>
								<TooltipContent className="max-w-xs">{field.helpText}</TooltipContent>
							</Tooltip>
						</TooltipProvider>
					)}
				</div>
				{field.range && (
					<NumberInput
						className={cn("ml-auto h-[24px] w-[80px] text-center shrink-0", invalid ? "border-border-error focus-visible:ring-border-error" : "")}
						value={config[field.id] as number}
						defaultValue={field.default ?? ""}
						disabled={props.disabled && props.disabled === true}
						onChange={(value) => props.onChange(value)}
						min={field.range?.min}
						max={field.range?.max}
					/>
				)}
			</div>
			{field.range ? (
				<Slider
					min={field.range?.min || 0}
					max={field.range?.max || 1}
					step={field.range?.step ? field.range?.step : field.range?.max ? field.range?.max / 100 : 1}
					disabled={props.disabled && props.disabled === true}
					value={[(config[field.id] as number) !== undefined ? (config[field.id] as number) : field.default || 0]}
					onValueChange={(value) => {
						props.onChange(value[0]);
					}}
					thumbTooltipText={(props.disabled && props.disabledText) || undefined}
				/>
			) : (
				<NumberInput
					className="w-full"
					value={config[field.id] as number}
					defaultValue={field.default ?? ""}
					disabled={props.disabled && props.disabled === true}
					onChange={(value) => props.onChange(value)}
				/>
			)}
			{invalid && (
				<div className="text-content-error -mt-2">
					Please keep {field.label} between {field.range?.min} to {field.range?.max}.
				</div>
			)}
		</div>
	);
}

const isInvalid = (value: number, range: { min: number; max: number }, f: string): boolean => {
	if (!value || range?.min === undefined) return false;
	return isNaN(value) || value < range.min || value > range.max;
};
