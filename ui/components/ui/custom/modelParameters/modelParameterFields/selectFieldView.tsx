
import { HelpCircle } from "lucide-react";
import ParameterFieldView from "./paramFieldView";
import { Parameter } from "./types";
import { cn } from "@/lib/utils";
import { Label } from "@/components/ui/label";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { ComboboxSelect } from "@/components/ui/combobox";

interface Props {
	field: Parameter;
	config: Record<string, unknown>;
	onChange: (value: any) => void;
	disabled?: boolean;
	multiselect?: boolean;
	placeholder?: string;
	isLoading?: boolean;
	className?: string;
	forceHideFields?: string[];
}

export default function SelectFieldView(props: Props) {
	const { field, config } = props;
	const value = field.accesorKey ? (config[field.id] as any)?.[field.accesorKey] || "" : config[field.id];

	const onFieldChange = (fieldValue: string | null) => {
		if (fieldValue === null) {
			props.onChange(undefined);
			return;
		}
		const res = field.accesorKey ? { [field.accesorKey]: fieldValue } : fieldValue;
		props.onChange(res);
	};

	const onSubFieldChange = (subFieldId: string, subFieldValue: string) => {
		const res = {
			[field.accesorKey ?? field.id]: value,
			[subFieldId]: subFieldValue,
		};
		props.onChange(res);
	};

	const currentField = field.options?.find((f) => f.value === value);

	return (
		<div className={cn("flex flex-col gap-2", props.className)}>
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

			{props.multiselect ? (
				<ComboboxSelect
					multiple
					options={field.options || []}
					value={Array.isArray(value) ? value : []}
					onValueChange={(vals) => props.onChange(vals)}
					disabled={props.disabled}
					placeholder={`Add ${field.label}`}
				/>
			) : (
				<ComboboxSelect
					options={field.options || []}
					value={(value as string) || null}
					onValueChange={onFieldChange}
					disabled={props.disabled}
					placeholder="Select"
					disableSearch
				/>
			)}

			{currentField?.subFields && (
				<div className="mt-2">
					{currentField.subFields.map((subField) => (
						<ParameterFieldView
							key={subField.id}
							field={subField}
							parentField={field}
							config={config}
							onChange={(fieldValue) => onSubFieldChange(subField.id, fieldValue)}
							disabled={props.disabled && props.disabled === true}
							forceHideFields={props.forceHideFields}
						/>
					))}
				</div>
			)}
		</div>
	);
}
