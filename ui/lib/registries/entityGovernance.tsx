// Runtime registry for the probe that says what already governs a team, business unit or
// customer's spend beyond the budgets and rate limit it holds itself.
//
// OSS has no such second way to govern an entity, so the base build registers nothing and every
// entity reads as governed by nothing. A downstream build that can attach something else to an
// entity registers a probe by importing its registration module via the @enterprise alias; see
// ui/app/_fallbacks/enterprise/lib/registrations/entityGovernance.ts for the OSS-build fallback.
//
// Sheets that edit an entity's own limits ask this before offering the fields: an entity governed
// another way cannot take limits of its own as well, and the write would be refused.

export type EntityGovernanceKind = "team" | "business_unit" | "customer";

export interface EntityGovernanceProbe {
	// The name of what governs the entity, or undefined when nothing does.
	governedBy?: string;
	// True while the answer is still being read. Callers hold the fields back meanwhile, rather than
	// offer limits to an entity that turns out to be governed.
	isLoading: boolean;
}

/** Shape of the registered hook: called unconditionally on every render, like any hook. */
export type UseEntityGovernanceProbe = (kind: EntityGovernanceKind, entityId: string, enabled: boolean) => EntityGovernanceProbe;

const useNoProbe: UseEntityGovernanceProbe = () => ({ isLoading: false });

let probe: UseEntityGovernanceProbe = useNoProbe;

/**
 * Registers (or replaces) the probe. Intended to be called at module load, once, before the first
 * render that reads the registry, so every render of a component sees the same hook.
 */
export function registerEntityGovernanceProbe(hook: UseEntityGovernanceProbe): void {
	probe = hook;
}

/** Resets the registry to the base build's answer. For tests. */
export function resetEntityGovernanceProbeForTests(): void {
	probe = useNoProbe;
}

/** Asks what governs the entity. In builds with nothing registered, nothing does. */
export function useEntityGovernanceProbe(kind: EntityGovernanceKind, entityId: string, enabled: boolean): EntityGovernanceProbe {
	// The registered hook is fixed for the lifetime of the build, so this is one hook call per
	// render whichever build is running.
	const useProbe = probe;
	return useProbe(kind, entityId, enabled);
}