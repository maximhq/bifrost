import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { useRef, useState, type ComponentProps, type ReactNode } from "react";

/**
 * A card-header action that shows its label on a wide card and only its icon on a narrow one. The
 * tooltip names the action only while the label is hidden; it is portaled outside the container, so
 * it reads the label's computed display rather than relying on the container query.
 */
export function HeaderActionButton({
	icon,
	label,
	className,
	...props
}: { icon: ReactNode; label: string } & Omit<ComponentProps<typeof Button>, "children">) {
	const labelRef = useRef<HTMLSpanElement>(null);
	const [open, setOpen] = useState(false);
	return (
		<Tooltip
			open={open}
			onOpenChange={(next) => setOpen(next && !!labelRef.current && getComputedStyle(labelRef.current).display === "none")}
		>
			<TooltipTrigger asChild>
				<Button className={cn("size-9 px-0 @3xl/card-header:h-9 @3xl/card-header:w-auto @3xl/card-header:px-4", className)} {...props}>
					{icon}
					<span ref={labelRef} className="hidden @3xl/card-header:inline">
						{label}
					</span>
				</Button>
			</TooltipTrigger>
			<TooltipContent>{label}</TooltipContent>
		</Tooltip>
	);
}