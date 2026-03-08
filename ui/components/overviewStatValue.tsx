"use client";

import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import type { ReactNode } from "react";

type OverviewStatValueProps = {
	value: ReactNode;
	tooltip?: string;
	className?: string;
	"data-testid"?: string;
};

export function OverviewStatValue({ value, tooltip, className, "data-testid": dataTestId }: OverviewStatValueProps) {
	const content = (
		<div data-testid={dataTestId} className={cn("truncate font-mono text-xl font-medium sm:text-2xl", className)}>
			{value}
		</div>
	);

	if (!tooltip) {
		return content;
	}

	return (
		<Tooltip>
			<TooltipTrigger asChild>{content}</TooltipTrigger>
			<TooltipContent side="top" className="max-w-80 break-all font-mono" data-testid={dataTestId ? `${dataTestId}-tooltip` : undefined}>
				{tooltip}
			</TooltipContent>
		</Tooltip>
	);
}
