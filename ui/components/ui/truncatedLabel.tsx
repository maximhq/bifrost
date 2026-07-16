import * as React from "react";
import { useCallback, useLayoutEffect, useState } from "react";

import { cn } from "@/lib/utils";

import { Tooltip, TooltipContent, TooltipTrigger } from "./tooltip";

type TruncatedLabelProps = {
	children: React.ReactNode;
	className?: string;
	tooltip?: React.ReactNode;
	tooltipSide?: React.ComponentProps<typeof TooltipContent>["side"];
} & Omit<React.ComponentProps<"span">, "children">;

function TruncatedLabel({ children, className, tooltip, tooltipSide = "right", ...props }: TruncatedLabelProps) {
	const [measureEl, setMeasureEl] = useState<HTMLSpanElement | null>(null);
	const [isTruncated, setIsTruncated] = useState(false);

	const setTextRef = useCallback((node: HTMLSpanElement | null) => {
		setMeasureEl(node);
	}, []);

	useLayoutEffect(() => {
		if (!measureEl) return;

		const checkTruncation = () => {
			setIsTruncated(measureEl.scrollWidth > measureEl.clientWidth);
		};

		checkTruncation();
		const observer = new ResizeObserver(checkTruncation);
		observer.observe(measureEl);
		return () => observer.disconnect();
	}, [measureEl, children]);

	const tooltipContent = tooltip ?? (typeof children === "string" ? children : undefined);

	const inner = (
		<span ref={setTextRef} className={cn("min-w-0 truncate", className)} {...props}>
			{children}
		</span>
	);

	if (!isTruncated || tooltipContent == null) return inner;

	return (
		<Tooltip>
			<TooltipTrigger asChild>{inner}</TooltipTrigger>
			<TooltipContent side={tooltipSide}>{tooltipContent}</TooltipContent>
		</Tooltip>
	);
}

export { TruncatedLabel };