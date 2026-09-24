import type { Budget, RateLimit } from "@/lib/types/governance";
import type { AccessProfileEntityKind } from "@enterprise/lib/types/accessProfile";

interface Props {
	entityType: AccessProfileEntityKind;
	entityId: string;
	entityName: string;
	legacyLimits?: { budgets: unknown[]; rateLimit?: unknown };
	canManage: boolean;
	enabled: boolean;
}

// OSS build has no access-profile backend, so an entity can hold no profile and there is nothing
// to show. Both render nothing rather than an upsell: the sheets hosting them are themselves
// enterprise surfaces.
export function EntityAccessProfileField(_props: Props) {
	return null;
}

export function EntityUsage(_props: Props & { onRemoveLegacyLimits?: () => Promise<void> }) {
	return null;
}

export interface EntityProfileLimitsView {
	name: string;
	calendarAligned: boolean;
	budgets: Budget[];
	rateLimit?: RateLimit;
}

// No entity holds a profile without the access-profile backend, so every row keeps its own limits.
export function useEntityProfileLimits(_entityType: AccessProfileEntityKind, _enabled: boolean): Record<string, EntityProfileLimitsView> {
	return {};
}