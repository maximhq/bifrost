import { cn } from "@/lib/utils";
import { Regex } from "lucide-react";
import { isRegexEntry, isWildcardEntry, stripRegexPrefix } from "./utils";

interface ModelAccessChipLabelProps {
	entry: string;
	className?: string;
}

/**
 * The label shown for one list entry wherever entries are rendered as chips or
 * badges: "All Models" for the wildcard, a monospace pattern with an icon for a
 * regex entry, the plain name otherwise.
 */
export function ModelAccessChipLabel({ entry, className }: ModelAccessChipLabelProps) {
	if (isWildcardEntry(entry)) {
		return <span className={className}>All Models</span>;
	}
	if (isRegexEntry(entry)) {
		return (
			<span
				className={cn("inline-flex min-w-0 items-center gap-1 font-mono", className)}
				title="Regex pattern (case-insensitive, full match)"
				data-model-entry-kind="regex"
			>
				<Regex className="text-muted-foreground h-3 w-3 shrink-0" aria-hidden />
				<span className="truncate">{stripRegexPrefix(entry)}</span>
			</span>
		);
	}
	return <span className={cn("truncate", className)}>{entry}</span>;
}