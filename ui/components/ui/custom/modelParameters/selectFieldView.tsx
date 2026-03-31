
import ParameterFieldView from "./paramFieldView";
import { Parameter } from "./types";
import { cn } from "@/lib/utils";
import { ComboboxSelect } from "@/components/ui/combobox";
import FieldLabel from "./fieldLabel";

interface Props {
	field: Parameter;
	config: Record<string, unknown>;
	onChange: (value: any, overrides?: Record<string, any>) => void;
	disabled?: boolean;
	multiselect?: boolean;
	placeholder?: string;
	isLoading?: boolean;
	onClear?: () => void;
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
		if (field.accesorKey) {
			const existing = config[field.id] && typeof config[field.id] === "object" ? (config[field.id] as Record<string, unknown>) : {};
			props.onChange({
				...existing,
				[field.accesorKey]: value,
				[subFieldId]: subFieldValue,
			});
		} else {
			props.onChange(value, { [subFieldId]: subFieldValue });
		}
	};

	const currentField = field.options?.find((f) => f.value === value);

	return (
		<div className={cn("flex flex-col gap-2", props.className)}>
			<FieldLabel label={field.label} helpText={field.helpText} onClear={props.onClear} />

			{props.multiselect ? (
				<ComboboxSelect
					multiple
					options={field.options || []}
					value={Array.isArray(value) ? value : []}
					onValueChange={(vals) => props.onChange(field.accesorKey ? { [field.accesorKey]: vals } : vals)}
					disabled={props.disabled}
					placeholder={`Add ${field.label}`}
					className="h-8"
				/>
			) : (
				<ComboboxSelect
					options={field.options || []}
					value={(value as string) || null}
					onValueChange={onFieldChange}
					disabled={props.disabled}
					placeholder="Select"
					disableSearch
					className="h-8"
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
