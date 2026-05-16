export enum ParameterType {
	NUMBER = "number",
	BOOLEAN = "boolean",
	TEXT = "text",
	SELECT = "select",
	ARRAY = "array",
	JSON = "json",
}

export type Parameter = {
	id: string;
	label: string;
	helpText?: string;
	type: ParameterType;
	/**
	 * use when value is nested in object
	 * e.g. { config: { response_format: { type: "json_schema", json_schema: { type: "object" } } } }
	 * parameter.id = response_format
	 * parameter.accesorKey = response_format.json_schema
	 */
	accesorKey?: string;
	default?: any;
	multiple?: boolean;
	range?: {
		min: number;
		max: number;
		step?: number;
	};
	array?: {
		type: ParameterType;
		maxElements?: number;
		minElements?: number;
	};
	options?: {
		label: string;
		value: string;
		subFields?: Parameter[];
	}[];
	disabled?: boolean;
	disabledText?: string;
	trueValue?: unknown; // When using boolean field, if `trueValue` is set, then this will be passed as the value when the boolean is true
	falseValue?: unknown; // When using boolean field, if `falseValue` is set, then this will be passed as the value when the boolean is false
	removeFieldOnFalse?: boolean; // When using boolean field, if `removeFieldOnFalse` is set to true, then the field will be removed from the config when the boolean is false
	disabledCondition?: {
		paramId: string;
		operator: "eq" | "neq";
		value: any;
		disabledText?: string;
		setValue?: any;
	};
	/**
	 * When true, the parameter is completely excluded from UI rendering
	 * (see `paramFieldView.tsx` early return).
	 */
	hidden?: boolean;
};