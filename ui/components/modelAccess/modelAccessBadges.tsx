import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import type { ReactNode } from "react";
import { ModelAccessChipLabel } from "./modelAccessChip";
import { isWildcardList, type ModelAccessMode } from "./utils";

type BadgeVariant = "default" | "secondary" | "destructive" | "outline" | "success";

interface ModelAccessBadgesProps {
	/** The exact list: model names, or "*" alone. */
	value: readonly string[] | undefined | null;
	/** The pattern twin of `value`. */
	patterns?: readonly string[] | undefined | null;
	mode: ModelAccessMode;
	/**
	 * Treat the list as "all models" even without a "*" entry. Access profiles and
	 * projects carry that as a separate all_models_allowed flag.
	 */
	allModels?: boolean;
	/** Rendered when both lists are empty; defaults to the mode's standard badge. */
	empty?: ReactNode;
	/** Badge variant for individual entries; defaults per mode. */
	entryVariant?: BadgeVariant;
	entryClassName?: string;
	className?: string;
}

/**
 * Read-only rendering of an allow or block side: the "All Models" badge, one
 * badge per exact entry, one monospace badge with an icon per pattern, or an
 * empty-state badge. Replaces the badge blocks that the VK, access profile and
 * project detail views used to each carry.
 */
export function ModelAccessBadges({ value, patterns, mode, allModels, empty, entryVariant, entryClassName, className }: ModelAccessBadgesProps) {
	const entries = (value ?? []).filter((e) => e !== "*");
	const patternEntries = patterns ?? [];
	const isAll = allModels || isWildcardList(value);

	if (isAll) {
		return mode === "allow" ? (
			<Badge variant="success" className={cn("text-xs", className)}>
				All Models
			</Badge>
		) : (
			<Badge variant="destructive" className={cn("text-xs", className)}>
				All Models Blocked
			</Badge>
		);
	}

	if (entries.length === 0 && patternEntries.length === 0) {
		if (empty !== undefined) return <>{empty}</>;
		return mode === "allow" ? (
			<Badge variant="destructive" className={cn("text-xs", className)}>
				No models (deny all)
			</Badge>
		) : (
			<Badge variant="secondary" className={cn("text-xs", className)}>
				No models blocked
			</Badge>
		);
	}

	const variant: BadgeVariant = entryVariant ?? (mode === "allow" ? "secondary" : "destructive");
	return (
		<div className={cn("flex flex-wrap gap-1", className)}>
			{entries.map((entry) => (
				<Badge key={`model:${entry}`} variant={variant} className={cn("max-w-full text-xs", entryClassName)}>
					<ModelAccessChipLabel entry={entry} />
				</Badge>
			))}
			{patternEntries.map((pattern) => (
				<Badge
					key={`pattern:${pattern}`}
					variant={variant}
					className={cn("max-w-full font-mono text-xs", entryClassName)}
					data-testid="model-access-regex-badge"
				>
					<ModelAccessChipLabel entry={pattern} kind="pattern" />
				</Badge>
			))}
		</div>
	);
}
