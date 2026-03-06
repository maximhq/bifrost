import BooleanFieldView from "./booleanFieldView";
import JSONFieldView from "./jsonFieldView";
import NumberFieldView from "./numberFieldView";
import SelectFieldView from "./selectFieldView";
import TextArrayFieldView from "./textArrayFieldView";
import TextFieldView from "./textFieldView";
import { Parameter, ParameterType } from "./types";

interface Props {
	field: Parameter;
	parentField?: Parameter;
	config: Record<string, unknown>;
	onChange: (value: any, overrides?: Record<string, any>) => void;
	disabled?: boolean;
	onInvalid?: (invalid: boolean, field?: string) => void;
	className?: string;
	disabledText?: string;
	forceHideFields?: string[];
}

export default function ParameterFieldView(props: Props) {
	const { field, parentField, config, onInvalid } = props;

	const hasValue = config[field.id] !== undefined;
	const onClear = hasValue && !props.disabled ? () => props.onChange(undefined) : undefined;

	const getField = () => {
		if (field.hidden || (props.forceHideFields && props.forceHideFields.includes(field.id))) return null;
		switch (field.type) {
			case ParameterType.TEXT:
				return (
					<TextFieldView field={field} disabled={props.disabled} config={config} onChange={props.onChange} onClear={onClear} className={props.className} />
				);
			case ParameterType.ARRAY:
				return (
					<TextArrayFieldView
						field={field}
						disabled={props.disabled}
						config={config}
						onChange={props.onChange}
						onInvalid={onInvalid}
						onClear={onClear}
						className={props.className}
					/>
				);
			case ParameterType.NUMBER:
				return (
					<NumberFieldView
						field={field}
						disabled={props.disabled}
						config={config}
						onChange={props.onChange}
						onInvalid={onInvalid}
						onClear={onClear}
						className={props.className}
						disabledText={props.disabledText}
					/>
				);
			case ParameterType.BOOLEAN:
				return (
					<BooleanFieldView field={field} disabled={props.disabled} config={config} onChange={props.onChange} onClear={onClear} className={props.className} forceHideFields={props.forceHideFields} />
				);
			case ParameterType.SELECT:
				return (
					<SelectFieldView
						field={field}
						config={config}
						onChange={props.onChange}
						multiselect={field.multiple}
						disabled={props.disabled}
						onClear={onClear}
						className={props.className}
					/>
				);
			case ParameterType.JSON:
				return (
					<JSONFieldView field={field} parentField={parentField} config={config} onChange={props.onChange} onClear={onClear} disabled={props.disabled} />
				);
		}
	};
	return <>{getField()}</>;
}
