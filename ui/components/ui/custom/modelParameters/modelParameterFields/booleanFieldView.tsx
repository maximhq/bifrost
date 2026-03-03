import { Parameter } from "./types";
import { cn } from "@/lib/utils";
import { HelpCircle } from "lucide-react";
import ParameterFieldView from "./paramFieldView";
import { Label } from "@/components/ui/label";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Switch } from "@/components/ui/switch";

interface Props {
	field: Parameter;
	config: Record<string, unknown>;
	onChange: (value: unknown, overrides?: Record<string, unknown>) => void;
	disabled?: boolean;
	className?: string;
}

export default function BooleanFieldView(props: Props) {
	const { field, config } = props;

	// use provided trueValue when present, otherwise default to true
	const trueVal = field.trueValue !== undefined ? field.trueValue : true;

	let value = false;
	if (field.accesorKey) {
		const parent = config[field.id] as Record<string, unknown> | undefined;
		const v = parent ? parent[field.accesorKey] : undefined;
		value = v === trueVal;
	} else {
		value = config[field.id] === trueVal;
	}

	const onFieldChange = (fieldValue: boolean) => {
		// When turning on => set to trueVal
		if (fieldValue) {
			const valToSet = trueVal;
			const res = field.accesorKey ? { [field.accesorKey]: valToSet } : valToSet;
			props.onChange(res);
			return;
		}

		// Turning off => either remove the field or set to false depending on config
		if (field.accesorKey) {
			// nested field inside parent object
			if (field.removeFieldOnFalse) {
				// remove the entire parent object when configured to remove on false
				props.onChange(undefined);
			} else {
				props.onChange({ [field.accesorKey]: false });
			}
		} else {
			// top-level
			if (field.removeFieldOnFalse) {
				props.onChange(undefined);
			} else {
				props.onChange(false);
			}
		}
	};

	const onSubFieldChange = (subFieldId: string, subFieldValue: unknown) => {
		const parentKey = field.accesorKey ?? field.id;
		const parentVal = value ? trueVal : field.removeFieldOnFalse ? undefined : false;
		const res: Record<string, unknown> = {
			[parentKey]: parentVal,
			[subFieldId]: subFieldValue,
		};
		props.onChange(res);
	};

	const currentField = field.options?.find((f) => f.value === String(value));

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

				<Switch
					className="ml-auto"
					onCheckedChange={(e) => onFieldChange(!!e)}
					checked={!!value}
					disabled={props.disabled && props.disabled === true}
					defaultChecked={field.default}
				/>
			</div>

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
						/>
					))}
				</div>
			)}
		</div>
	);
}
