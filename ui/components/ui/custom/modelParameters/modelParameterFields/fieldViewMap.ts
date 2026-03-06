import BooleanFieldView from "./booleanFieldView";
import JSONFieldView from "./jsonFieldView";
import NumberFieldView from "./numberFieldView";
import SelectFieldView from "./selectFieldView";
import TextArrayFieldView from "./textArrayFieldView";
import TextFieldView from "./textFieldView";

// NOTE: adding fields here to avoid circular dependency, don't add ParameterFieldView here
const fieldViewMap = {
	BooleanFieldView,
	JSONFieldView,
	NumberFieldView,
	SelectFieldView,
	TextArrayFieldView,
	TextFieldView,
};

export default fieldViewMap;
