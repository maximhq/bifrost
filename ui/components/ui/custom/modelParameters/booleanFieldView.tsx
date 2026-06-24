import { useId } from "react";
import { Parameter } from "./types";
import { cn } from "@/lib/utils";
import ParameterFieldView from "./paramFieldView";
import { Switch } from "@/components/ui/switch";
import FieldLabel from "./fieldLabel";

interface Props {
	field: Parameter;
	config: Record<string, unknown>;
	onChange: (value: unknown, overrides?: Record<string, unknown>) => void;
	disabled?: boolean;
	onClear?: () => void;
	className?: string;
	forceHideFields?: string[];
}

export default function BooleanFieldView(props: Props) {
	const { field, config } = props;
	const switchId = useId();

	// use provided trueValue when present, otherwise default to true
	const trueVal = field.trueValue !== undefined ? field.trueValue : true;

	let value = false;
	if (field.accesorKey) {
		const parent = config[field.id] as Record<string, unknown> | undefined;
		const v = parent ? parent[field.accesorKey] : undefined;
		if (v !== undefined) value = v === trueVal;
	} else {
		if (config[field.id] !== undefined) value = config[field.id] === trueVal;
	}

	const onFieldChange = (fieldValue: boolean) => {
		// When turning on => set to trueVal
		if (fieldValue) {
			const valToSet = trueVal;
			const res = field.accesorKey ? { [field.accesorKey]: valToSet } : valToSet;
			props.onChange(res);
			return;
		}

		// Turning off => either remove the field or set to falseValue/false depending on config
		const falseVal = field.falseValue !== undefined ? field.falseValue : false;
		if (field.accesorKey) {
			if (field.removeFieldOnFalse) {
				props.onChange(undefined);
			} else {
				props.onChange({ [field.accesorKey]: falseVal });
			}
		} else {
			if (field.removeFieldOnFalse) {
				props.onChange(undefined);
			} else {
				props.onChange(falseVal);
			}
		}
	};

	const onSubFieldChange = (subFieldId: string, subFieldValue: unknown) => {
		const falseVal = field.falseValue !== undefined ? field.falseValue : false;
		const parentVal = value ? trueVal : field.removeFieldOnFalse ? undefined : falseVal;
		if (field.accesorKey) {
			const existing = config[field.id] && typeof config[field.id] === "object" ? (config[field.id] as Record<string, unknown>) : {};
			props.onChange({
				...existing,
				[field.accesorKey]: parentVal,
				[subFieldId]: subFieldValue,
			});
		} else {
			// No accesorKey: keep field.id as primitive, pass subfield via overrides
			props.onChange(parentVal, { [subFieldId]: subFieldValue });
		}
	};

	const currentField = field.options?.find((f) => f.value === String(value));

	return (
		<div className={cn("flex flex-col gap-2", props.className)}>
			<FieldLabel label={field.label} helpText={field.helpText} htmlFor={switchId} onClear={props.onClear}>
				<Switch
					id={switchId}
					className="ml-auto"
					onCheckedChange={(e) => onFieldChange(!!e)}
					checked={!!value}
					disabled={props.disabled && props.disabled === true}
				/>
			</FieldLabel>

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