import { Parameter } from "./types";
import { cn } from "@/lib/utils";
import { HelpCircle } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";

interface Props {
	field: Parameter;
	config: Record<string, unknown>;
	onChange: (value: any) => void;
	disabled?: boolean;
	className?: string;
}

export default function TextFieldView(props: Props) {
	const { field, config } = props;

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

			<Input
				className="mr-2 ml-auto h-[35px] w-full"
				value={config[field.id] as string}
				disabled={props.disabled && props.disabled === true}
				onChange={(e) => props.onChange(e.target.value)}
				defaultValue={field.default}
			/>
		</div>
	);
}
