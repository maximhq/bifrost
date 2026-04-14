import { Slider } from "@/components/ui/slider";
import { cn } from "@/lib/utils";
import { useEffect } from "react";
import NumberInput from "../number";
import FieldLabel from "./fieldLabel";
import { Parameter } from "./types";

interface Props {
	field: Parameter;
	config: Record<string, unknown>;
	onChange: (value: any) => void;
	disabled?: boolean;
	onInvalid?: (invalid: boolean, field?: string) => void;
	onClear?: () => void;
	className?: string;
	disabledText?: string;
}

export default function NumberFieldView(props: Props) {
	const { field, config } = props;

	const invalid = field.range ? isInvalid(config[field.id] as number, field.range) : false;

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
			<FieldLabel label={field.label} helpText={field.helpText} onClear={props.onClear}>
				{field.range && config[field.id] !== undefined && (
					<NumberInput
						className={cn(
							"ml-auto h-[24px] w-[80px] text-center shrink-0",
							invalid ? "border-border-error focus-visible:ring-border-error" : "",
						)}
						value={config[field.id] as number}
						disabled={props.disabled && props.disabled === true}
						onChange={(value) => props.onChange(value)}
						preventOnBlurFallback
						min={field.range?.min}
						max={field.range?.max}
					/>
				)}
			</FieldLabel>
			{field.range ? (
				<Slider
					min={field.range?.min ?? 0}
					max={field.range?.max ?? 1}
					step={field.range?.step ?? (field.range?.max ?? 1) / 100}
					disabled={props.disabled && props.disabled === true}
					value={[(config[field.id] as number) !== undefined ? (config[field.id] as number) : 0]}
					onValueChange={(value) => {
						props.onChange(value[0]);
					}}
					thumbTooltipText={(props.disabled && props.disabledText) || undefined}
				/>
			) : (
				<NumberInput
					className="w-full"
					value={config[field.id] as number}
					disabled={props.disabled && props.disabled === true}
					onChange={(value) => props.onChange(value)}
					preventOnBlurFallback
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

const isInvalid = (value: number, range: { min: number; max: number }): boolean => {
	if (value === undefined || value === null || range?.min === undefined) return false;
	return isNaN(value) || value < range.min || value > range.max;
};