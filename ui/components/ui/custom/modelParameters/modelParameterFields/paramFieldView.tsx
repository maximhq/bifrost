import fieldViewMap from "./fieldViewMap";
import { Parameter, ParameterType } from "./types";

const { BooleanFieldView, JSONFieldView, NumberFieldView, SelectFieldView, TextArrayFieldView, TextFieldView } = fieldViewMap;

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

	const getField = () => {
		if (field.hidden || (props.forceHideFields && props.forceHideFields.includes(field.id))) return null;
		switch (field.type) {
			case ParameterType.TEXT:
				return (
					<TextFieldView field={field} disabled={props.disabled} config={config} onChange={props.onChange} className={props.className} />
				);
			case ParameterType.ARRAY:
				return (
					<TextArrayFieldView
						field={field}
						disabled={props.disabled}
						config={config}
						onChange={props.onChange}
						onInvalid={onInvalid}
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
						className={props.className}
						disabledText={props.disabledText}
					/>
				);
			case ParameterType.BOOLEAN:
				return (
					<BooleanFieldView field={field} disabled={props.disabled} config={config} onChange={props.onChange} className={props.className} />
				);
			case ParameterType.SELECT:
				return (
					<SelectFieldView
						field={field}
						config={config}
						onChange={props.onChange}
						multiselect={field.multiple}
						disabled={props.disabled}
						className={props.className}
					/>
				);
			case ParameterType.JSON:
				return (
					<JSONFieldView field={field} parentField={parentField} config={config} onChange={props.onChange} disabled={props.disabled} />
				);
		}
	};
	return <>{getField()}</>;
}
