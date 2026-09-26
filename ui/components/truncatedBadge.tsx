// A badge that gives up on fitting its text and truncates to whatever width the
// column allows, with the full value in a tooltip.
//
// Association cells — "Assigned To" on the virtual keys table, the teams and
// business units a customer reaches a user through — carry names of arbitrary
// length in a fixed-width column. Wrapping would relayout the row on one long
// name, so they truncate; the tooltip is what keeps the truncated half readable.

import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import type { ComponentProps } from "react";

interface TruncatedBadgeProps {
	/** Rendered in the badge, truncated to fit. */
	label: string;
	/** Shown in the tooltip when the badge face is an abbreviation. Defaults to `label`. */
	tooltipLabel?: string;
	variant?: ComponentProps<typeof Badge>["variant"];
	className?: string;
	/** Suffixed `-trigger` / `-content` for the badge and its tooltip. */
	dataTestId?: string;
}

export function TruncatedBadge({ label, tooltipLabel, variant = "outline", className, dataTestId }: TruncatedBadgeProps) {
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<Badge
					variant={variant}
					className={cn("block max-w-full truncate text-left", className)}
					data-testid={dataTestId && `${dataTestId}-trigger`}
				>
					{label}
				</Badge>
			</TooltipTrigger>
			<TooltipContent data-testid={dataTestId && `${dataTestId}-content`}>{tooltipLabel ?? label}</TooltipContent>
		</Tooltip>
	);
}