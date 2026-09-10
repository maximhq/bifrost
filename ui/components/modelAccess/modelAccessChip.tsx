import { cn } from "@/lib/utils";
import { Regex } from "lucide-react";
import { isWildcardEntry } from "./utils";

export type ModelAccessEntryKind = "model" | "pattern";

interface ModelAccessChipLabelProps {
	entry: string;
	/** "model" for an exact-list entry (the default), "pattern" for a pattern-list entry. */
	kind?: ModelAccessEntryKind;
	className?: string;
}

/**
 * The label shown for one list entry wherever entries are rendered as chips or
 * badges: "All Models" for the wildcard, a monospace pattern with an icon for a
 * pattern entry, the plain name otherwise.
 */
export function ModelAccessChipLabel({ entry, kind = "model", className }: ModelAccessChipLabelProps) {
	if (kind === "pattern") {
		return (
			<span
				className={cn("inline-flex min-w-0 items-center gap-1 font-mono", className)}
				title="Regex pattern (case-insensitive, full match)"
				data-model-entry-kind="regex"
			>
				<Regex className="text-muted-foreground h-3 w-3 shrink-0" aria-hidden />
				<span className="truncate">{entry}</span>
			</span>
		);
	}
	if (isWildcardEntry(entry)) {
		return <span className={className}>All Models</span>;
	}
	return <span className={cn("truncate", className)}>{entry}</span>;
}
