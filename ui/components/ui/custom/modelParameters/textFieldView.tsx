import { Parameter } from "./types";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import FieldLabel from "./fieldLabel";

interface Props {
	field: Parameter;
	config: Record<string, unknown>;
	onChange: (value: any) => void;
	disabled?: boolean;
	onClear?: () => void;
	className?: string;
}

export default function TextFieldView(props: Props) {
	const { field, config } = props;

	return (
		<div className={cn("flex flex-col gap-2", props.className)}>
			<FieldLabel label={field.label} helpText={field.helpText} htmlFor={field.id} onClear={props.onClear} />

			<Input
				id={field.id}
				className="mr-2 ml-auto h-[35px] w-full"
				value={(config[field.id] as string) ?? ""}
				disabled={props.disabled && props.disabled === true}
				onChange={(e) => props.onChange(e.target.value || undefined)}
			/>
		</div>
	);
}
