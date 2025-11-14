import { cn } from "@/lib/utils";

interface Props {
	className?: string;
	containerClassName?: string;
	isBeta?: boolean;
	valueClassName?: string;
	label: string;
	value: React.ReactNode | null;
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
					<div className="text-muted-foreground flex shrink-0 flex-row items-center gap-2 pb-2 text-xs font-medium">
						{props.label.toUpperCase().replace(/_/g, " ")}
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
