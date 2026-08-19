import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { Info } from "lucide-react";

interface Props {
	className?: string;
	containerClassName?: string;
	isBeta?: boolean;
	valueClassName?: string;
	label: string;
	value: React.ReactNode | null;
	tooltip?: string;
	hideExpandable?: boolean;
	orientation?: "horizontal" | "vertical";
	align?: "left" | "right";
}

export default function LogEntryDetailsView(props: Props) {
	if (props.value === null) {
		return null;
	}
	const orientation = props.orientation || "vertical";
	return (
		<div
			className={cn("items-top flex flex-col gap-2", {
				[`${props.className}`]: props.className !== undefined,
				"items-start": props.align === "left" || props.align === undefined,
				"items-end": props.align === "right",
			})}
		>
			<div className={props.containerClassName}>
				{props.label !== "" && (
					<div className="text-muted-foreground flex shrink-0 flex-row items-center gap-1.5 pb-2 text-xs font-medium">
						{props.label.toUpperCase().replace(/_/g, " ")}
						{props.tooltip && (
							<Tooltip>
								<TooltipTrigger asChild>
									<Info className="text-muted-foreground/60 hover:text-muted-foreground h-3 w-3 cursor-help" />
								</TooltipTrigger>
								<TooltipContent sideOffset={6} className="max-w-xs">
									{props.tooltip}
								</TooltipContent>
							</Tooltip>
						)}
					</div>
				)}
				<div
					className={cn("text-md flex text-xs font-medium overflow-ellipsis transition-transform delay-75", {
						"w-full flex-col items-center gap-2": orientation === "horizontal",
						"flex-row items-start gap-2": orientation === "vertical",
						[`${props.valueClassName}`]: props.valueClassName !== undefined,
						"text-end": props.align === "right",
					})}
				>
					<div className="text-bifrost-gray-300 flex-1 text-sm break-all">
						{typeof props.value === "boolean" ? String(props.value) : props.value}
					</div>
				</div>
			</div>
		</div>
	);
}