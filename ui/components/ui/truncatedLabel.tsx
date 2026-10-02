import * as React from "react";
import { useCallback, useLayoutEffect, useState } from "react";

import { cn } from "@/lib/utils";

import { Tooltip, TooltipContent, TooltipTrigger } from "./tooltip";

type TruncatedLabelProps = {
	children: React.ReactNode;
	className?: string;
	tooltip?: React.ReactNode;
	tooltipSide?: React.ComponentProps<typeof TooltipContent>["side"];
	/**
	 * Which end of the string loses characters. `"start"` puts the ellipsis on
	 * the left so the distinctive tail survives. See {@link StartTruncatedLabel}.
	 */
	truncateFrom?: "start" | "end";
} & Omit<React.ComponentProps<"span">, "children">;

function TruncatedLabel({ children, className, tooltip, tooltipSide = "right", truncateFrom = "end", ...props }: TruncatedLabelProps) {
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

	const fromStart = truncateFrom === "start";
	const inner = (
		<span ref={setTextRef} className={cn("min-w-0", fromStart ? "truncate-start text-left" : "truncate", className)} {...props}>
			{fromStart ? <bdi>{children}</bdi> : children}
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

type StartTruncatedLabelProps = {
	children: React.ReactNode;
	className?: string;
} & Omit<React.ComponentProps<"span">, "children">;

/**
 * Like `truncate`, but the ellipsis lands at the start of the string so the
 * distinctive tail stays readable. Model ids share long prefixes
 * (`us.anthropic.claude-…`, `workers-ai/@cf/…`) and the end is what tells them
 * apart. `<bdi>` keeps the characters in left-to-right order inside the
 * right-to-left box that moves the ellipsis.
 *
 * Give it a width or `max-w-*`; without one there is nothing to truncate to.
 */
function StartTruncatedLabel({ children, className, ...props }: StartTruncatedLabelProps) {
	return (
		<span className={cn("truncate-start min-w-0 text-left", className)} {...props}>
			<bdi>{children}</bdi>
		</span>
	);
}

export { StartTruncatedLabel, TruncatedLabel };