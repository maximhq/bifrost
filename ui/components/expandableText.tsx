import { cn } from "@/lib/utils";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

// Static class names so Tailwind keeps them; a computed `line-clamp-${n}` would be purged.
const CLAMP_CLASS: Record<number, string> = {
	2: "line-clamp-2",
	3: "line-clamp-3",
	4: "line-clamp-4",
	5: "line-clamp-5",
};

interface ExpandableTextProps {
	text: string;
	// Lines shown before clamping. Defaults to 3.
	lines?: 2 | 3 | 4 | 5;
	// Which side the View more / View less toggle sits on. Defaults to left.
	toggleAlign?: "left" | "right";
	// Puts the toggle at the end of the last visible line instead of on a line
	// of its own (toggleAlign does not apply).
	inlineToggle?: boolean;
	className?: string;
}

// Clamps long text to a few lines with a "View more" / "View less" toggle. The toggle
// appears only when the text actually overflows, so short text is left untouched.
export function ExpandableText({ text, lines = 3, toggleAlign = "left", inlineToggle = false, className }: ExpandableTextProps) {
	const { t } = useTranslation("common");
	const ref = useRef<HTMLParagraphElement>(null);
	const [expanded, setExpanded] = useState(false);
	const [overflowing, setOverflowing] = useState(false);

	useEffect(() => {
		const el = ref.current;
		if (!el) return;
		const measure = () => setOverflowing(el.scrollHeight > el.clientHeight + 1);
		measure();
		// Re-measure when the available width changes (viewport rotate, container resize), so the
		// toggle appears/disappears correctly rather than staying stale from the initial measure.
		const observer = new ResizeObserver(measure);
		observer.observe(el);
		return () => observer.disconnect();
	}, [text, lines, expanded]);

	const toggleLabel = expanded ? t("viewToggle.less") : t("viewToggle.more");
	if (inlineToggle) {
		// The toggle floats right below a spacer as tall as the text less one line,
		// so it sits at the end of the last visible line and the text wraps around
		// it. The flex wrapper gives the text the definite height the spacer's
		// percentage needs.
		return (
			<div className={cn("flex", className)}>
				<p
					ref={ref}
					className={cn(
						"min-w-0 break-words before:float-right before:h-[calc(100%-1lh)] before:content-['']",
						!expanded && (CLAMP_CLASS[lines] ?? CLAMP_CLASS[3]),
					)}
				>
					{(overflowing || expanded) && (
						<button
							type="button"
							className="text-primary float-right clear-both ml-1 text-xs font-medium hover:underline"
							onClick={() => setExpanded((value) => !value)}
						>
							{toggleLabel}
						</button>
					)}
					{text}
				</p>
			</div>
		);
	}

	return (
		<div className={className}>
			<p ref={ref} className={cn("break-words", !expanded && (CLAMP_CLASS[lines] ?? CLAMP_CLASS[3]))}>
				{text}
			</p>
			{(overflowing || expanded) && (
				<div className={cn("mt-2 flex", toggleAlign === "right" ? "justify-end" : "justify-start")}>
					<button type="button" className="text-primary text-xs font-medium hover:underline" onClick={() => setExpanded((value) => !value)}>
						{expanded ? t("viewToggle.less") : t("viewToggle.more")}
					</button>
				</div>
			)}
		</div>
	);
}