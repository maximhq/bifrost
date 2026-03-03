import { HelpCircle, Trash } from "lucide-react";
import { useEffect, useState } from "react";
import { Parameter } from "./types";
import { Label } from "@/components/ui/label";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

interface Props {
	field: Parameter;
	config: Record<string, unknown>;
	onChange: (value: any) => void;
	disabled?: boolean;
	onInvalid?: (invalid: boolean, field?: string) => void;
	className?: string;
}

export default function TextArrayFieldView(props: Props) {
	const [shouldFocus, setShouldFocus] = useState(false);
	const { field, config } = props;
	const fieldValue = (config[field.id] as string[]) || [];

	const invalid = isInvalid(fieldValue.length as number, {
		max: (field.array?.maxElements as number) || Infinity,
		min: field.array?.minElements || 1,
	});

	useEffect(() => {
		if (!props.onInvalid) return;
		if (invalid) {
			props.onInvalid(true, field.id);
		} else {
			props.onInvalid(false);
		}
	}, [props, invalid, field.id]);

	return (
		<div
			className={cn("flex flex-col items-start gap-2", props.className)}
		>
			<div className="flex flex-row items-center pr-1">
				<div className="flex flex-row items-center gap-1 pr-1">
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
			</div>
			{fieldValue?.map((_value, i) => (
				<StringInput
					key={i}
					index={i}
					onDelete={(index) => {
						props.onChange(fieldValue?.filter((_, idx) => idx !== index) || []);
					}}
					onChange={(index, value) => {
						props.onChange(fieldValue?.map((v, idx) => (idx === index ? value : v)) || []);
					}}
					value={_value}
					onEnterPress={() => {
						if ((fieldValue && !fieldValue[fieldValue.length - 1]) || invalid) return;
						setShouldFocus(true);
						props.onChange([...(fieldValue || []), ""]);
					}}
					autoFocus={shouldFocus && i === fieldValue.length - 1}
					disabled={(invalid && i === fieldValue.length) || props.disabled}
				/>
			))}

			{invalid && (
				<div className="mt-1 text-red-600">
					Please keep {field.label} between {field.array?.minElements} to {field.array?.maxElements}.
				</div>
			)}

			<Button
				variant={"link"}
				disabled={invalid || props.disabled}
				className="h-auto px-0"
				onClick={() => {
					if (invalid) return;
					setShouldFocus(true);
					props.onChange([...(config[field.id] as string[] || []), ""]);
				}}
			>
				Add string
			</Button>
		</div>
	);
}

// TODO - @SURESH - move this to a UI Library - DO IT

const StringInput = (props: {
	onDelete: (index: number) => void;
	index: number;
	onChange: (index: number, value: string) => void;
	value?: string;
	autoFocus?: boolean;
	disabled?: boolean;
	onEnterPress?: () => void;
}) => {
	return (
		<div className="relative mb-2 w-full gap-1">
			<Input
				tabIndex={10}
				className="ml-auto h-[35px] w-full"
				value={props.value}
				placeholder=""
				disabled={props.disabled}
				onChange={(e) => {
					props.onChange(props.index, e.target.value);
				}}
				autoFocus={props.autoFocus}
				onBlur={(e) => {
					if (!e.target.value || e.target.value === "" || e.target.value.trim().length === 0) {
						props.onDelete(props.index);
					}
				}}
				onKeyDown={(e) => {
					if (e.key === "Enter" || e.code === "Enter") {
						props.onEnterPress && props.onEnterPress();
					}
				}}
			/>

			<Trash
				onClick={() => props.onDelete(props.index)}
				className="text-content-error absolute top-1/2 right-2 h-3.5 w-3.5 -translate-y-1/2 cursor-pointer opacity-80 hover:opacity-100"
			/>
		</div>
	);
};

const isInvalid = (value: number, range: { min: number; max: number }): boolean => {
	if (!value) return false;
	return isNaN(value) || value < range.min || value >= range.max;
};
