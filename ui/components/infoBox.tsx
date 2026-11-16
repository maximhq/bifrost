import { cn } from "@/lib/utils";
import { ReactNode } from "react";

interface InfoBoxProps {
	// Small icon shown inline, just left of the title. Size it at the call site (e.g. size-4).
	icon?: ReactNode;
	title?: ReactNode;
	// Primary content, below the title.
	children?: ReactNode;
	// Secondary content below the main content, e.g. a link or hint. Set apart with more space.
	subcontent?: ReactNode;
	className?: string;
}

// A bordered callout: an optional icon + title header, primary content, and secondary content.
// Use it to set apart a note or a resulting value from the data around it.
export function InfoBox({ icon, title, children, subcontent, className }: InfoBoxProps) {
	const hasHeader = icon || title;
	return (
		<div className={cn("bg-muted/30 rounded-md border p-4", className)}>
			{hasHeader && (
				<div className="flex items-center gap-2">
					{icon && <span className="text-muted-foreground flex shrink-0">{icon}</span>}
					{title && <div className="text-foreground text-sm font-medium">{title}</div>}
				</div>
			)}
			{children && <div className={cn("text-sm", hasHeader && "mt-2.5")}>{children}</div>}
			{subcontent && <div className={cn(children || hasHeader ? "mt-4" : "")}>{subcontent}</div>}
		</div>
	);
}