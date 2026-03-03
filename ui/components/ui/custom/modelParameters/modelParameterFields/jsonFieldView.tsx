import { Parameter } from "./types";
import { cn } from "@/lib/utils";
import { HelpCircle } from "lucide-react";
import { useEffect, useState } from "react";
import { Label } from "@/components/ui/label";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Switch } from "@/components/ui/switch";
import { CodeEditor } from "@/app/workspace/logs/views/codeEditor";

interface Props {
	field: Parameter;
	parentField?: Parameter;
	config: Record<string, unknown>;
	onChange: (value: any) => void;
	disabled?: boolean;
	className?: string;
}

export default function JSONFieldView(props: Props) {
	const { field, parentField, config } = props;

	const rawValue = parentField
		? (config[parentField.id] as any)?.[field.id] ?? field.default
		: config[field.id] ?? field.default;
	const value = JSON.stringify(rawValue, null, 2);
	const [currentValue, setCurrentValue] = useState<string>(value);

	// Sync local state when config changes externally (e.g., session load)
	useEffect(() => {
		setCurrentValue(value);
	}, [value]);

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
