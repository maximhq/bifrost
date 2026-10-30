import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ModelAccessChipLabel } from "./modelAccessChip";
import { isRegexEntry, isWildcardList, type ModelAccessMode } from "./utils";

type BadgeVariant = "default" | "secondary" | "destructive" | "outline" | "success";

interface ModelAccessBadgesProps {
	/** The list: model names, "*" alone, or `regex:` entries. */
	value: readonly string[] | undefined | null;
	mode: ModelAccessMode;
	/**
	 * Treat the list as "all models" even without a "*" entry. Access profiles and
	 * projects carry that as a separate all_models_allowed flag.
	 */
	allModels?: boolean;
	/**
	 * For a block list, whether the allow side beside it permits every model. An empty block list
	 * blocks nothing only when it does: against a restricted allow list, every model outside that
	 * list is already denied, so "no models blocked" would read as "everything is available".
	 */
	allowsAllModels?: boolean;
	/** Rendered when the list is empty; defaults to the mode's standard badge. */
	empty?: ReactNode;
	/** Badge variant for individual entries; defaults per mode. */
	entryVariant?: BadgeVariant;
	entryClassName?: string;
	className?: string;
}

/**
 * Read-only rendering of an allow or block side: the "All Models" badge, one
 * badge per entry (patterns in monospace with an icon), or an empty-state badge.
 */
export function ModelAccessBadges({
	value,
	mode,
	allModels,
	allowsAllModels,
	empty,
	entryVariant,
	entryClassName,
	className,
}: ModelAccessBadgesProps) {
	const { t } = useTranslation();
	const entries = (value ?? []).filter((e) => e !== "*");
	const isAll = allModels || isWildcardList(value);

	if (isAll) {
		return mode === "allow" ? (
			<Badge variant="success" className={cn("text-xs", className)}>
				{t("modelAccess.allModels")}
			</Badge>
		) : (
			<Badge variant="destructive" className={cn("text-xs", className)}>
				{t("modelAccess.allModelsBlocked")}
			</Badge>
		);
	}

	if (entries.length === 0) {
		if (empty !== undefined) return <>{empty}</>;
		return mode === "allow" ? (
			<Badge variant="destructive" className={cn("text-xs", className)}>
				{t("modelAccess.noModelsDenyAll")}
			</Badge>
		) : (
			<Badge variant="secondary" className={cn("text-xs", className)}>
				{allowsAllModels === false ? t("modelAccess.allExceptAllowed") : t("modelAccess.noModelsBlocked")}
			</Badge>
		);
	}

	const variant: BadgeVariant = entryVariant ?? (mode === "allow" ? "secondary" : "destructive");
	return (
		<div className={cn("flex flex-wrap gap-1", className)}>
			{entries.map((entry) => (
				<Badge
					key={entry}
					variant={variant}
					className={cn("max-w-full text-xs", isRegexEntry(entry) && "font-mono", entryClassName)}
					data-testid={isRegexEntry(entry) ? "model-access-regex-badge" : undefined}
				>
					<ModelAccessChipLabel entry={entry} />
				</Badge>
			))}
		</div>
	);
}