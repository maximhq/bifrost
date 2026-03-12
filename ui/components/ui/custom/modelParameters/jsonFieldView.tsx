import { Parameter } from "./types";
import { cn } from "@/lib/utils";
import { useEffect, useState } from "react";
import { CodeEditor } from "@/components/ui/codeEditor";
import FieldLabel from "./fieldLabel";

interface Props {
	field: Parameter;
	parentField?: Parameter;
	config: Record<string, unknown>;
	onChange: (value: any) => void;
	disabled?: boolean;
	onClear?: () => void;
	className?: string;
}

export default function JSONFieldView(props: Props) {
	const { field, parentField, config } = props;

	const rawValue = parentField
		? (config[parentField.id] as any)?.[field.id]
		: config[field.id];
	const value = rawValue !== undefined ? JSON.stringify(rawValue, null, 2) : "";
	const [currentValue, setCurrentValue] = useState<string>(value);

	// Sync local state when config changes externally (e.g., session load)
	useEffect(() => {
		setCurrentValue(value);
	}, [value]);

	return (
		<div className={cn("flex flex-col gap-2", props.className)}>
			<FieldLabel label={field.label} helpText={field.helpText} onClear={props.onClear} />

			<CodeEditor
				code={currentValue}
				readonly={props.disabled}
				onChange={(v) => {
					setCurrentValue(v);
					try {
						props.onChange(JSON.parse(v));
					} catch (error) {}
				}}
				onBlur={() => {
					try {
						setCurrentValue(JSON.stringify(JSON.parse(currentValue), null, 2));
					} catch (ignored) {}
				}}
				lang="json"
				wrap={true}
				height={200}
				className="h-[200px] w-full border rounded-md py-1"
				options={{
					scrollBeyondLastLine: false,
				}}
			/>
		</div>
	);
}
