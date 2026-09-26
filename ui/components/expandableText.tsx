import { cn } from "@/lib/utils";
import { useEffect, useRef, useState } from "react";

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
	className?: string;
}

// Clamps long text to a few lines with a "View more" / "View less" toggle. The toggle
// appears only when the text actually overflows, so short text is left untouched.
export function ExpandableText({ text, lines = 3, toggleAlign = "left", className }: ExpandableTextProps) {
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

	return (
		<div className={className}>
			<p ref={ref} className={cn("break-words", !expanded && (CLAMP_CLASS[lines] ?? CLAMP_CLASS[3]))}>
				{text}
			</p>
			{(overflowing || expanded) && (
				<div className={cn("mt-2 flex", toggleAlign === "right" ? "justify-end" : "justify-start")}>
					<button type="button" className="text-primary text-xs font-medium hover:underline" onClick={() => setExpanded((value) => !value)}>
						{expanded ? "View less" : "View more"}
					</button>
				</div>
			)}
		</div>
	);
}