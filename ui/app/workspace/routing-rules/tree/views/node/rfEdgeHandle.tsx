import { cn } from "@/lib/utils";
import { Handle, type HandleProps } from "@xyflow/react";

/** Visual diameter; React Flow’s default is ~6px — larger so half the disc reads clearly past the node edge. */
export const RF_HANDLE_SIZE_PX = 14;

export type RFEdgeHandleProps = Omit<HandleProps, "className"> & {
	className?: string;
	accentColor?: string;
};

export function RFEdgeHandle({ className, accentColor, style, ...rest }: RFEdgeHandleProps) {
	return (
		<Handle
			className={cn(
				"!pointer-events-auto !z-0",
				"!h-[14px] !min-h-[14px] !w-[14px] !min-w-[14px]",
				"!rounded-full !border-0 !border-none !p-0 !shadow-none",
				className,
			)}
			style={{
				...style,
				...(accentColor ? { background: accentColor } : {}),
			}}
			{...rest}
		/>
	);
}