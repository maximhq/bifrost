import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { TruncatedLabel } from "@/components/ui/truncatedLabel";
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

const isPrimitive = (value: React.ReactNode): boolean =>
	typeof value === "string" || typeof value === "number" || typeof value === "boolean";

export default function LogEntryDetailsView(props: Props) {
	if (props.value === null) {
		return null;
	}
	const orientation = props.orientation || "vertical";
	return (
		<div
			className={cn("items-top flex min-w-0 max-w-full flex-col gap-2", {
				[`${props.className}`]: props.className !== undefined,
				"items-start": props.align === "left" || props.align === undefined,
				"items-end": props.align === "right",
			})}
		>
			<div className={cn("min-w-0 max-w-full", props.containerClassName)}>
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
					className={cn("text-md flex min-w-0 text-xs font-medium overflow-ellipsis transition-transform delay-75", {
						"w-full flex-col items-center gap-2": orientation === "horizontal",
						"flex-row items-start gap-2": orientation === "vertical",
						[`${props.valueClassName}`]: props.valueClassName !== undefined,
						"text-end": props.align === "right",
					})}
				>
					<TruncatedLabel
						className="text-bifrost-gray-300 max-w-full min-w-0 flex-1 text-sm"
						tooltipSide="top"
						tooltip={isPrimitive(props.value) ? String(props.value) : undefined}
					>
						{typeof props.value === "boolean" ? String(props.value) : props.value}
					</TruncatedLabel>
				</div>
			</div>
		</div>
	);
}