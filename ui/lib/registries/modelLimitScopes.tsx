// Runtime registry of model_config scopes available to the UI.
//
// OSS ships the base set (global + virtual_key) registered at module load.
// Downstream builds (enterprise) extend the registry by importing their
// registration module via the @enterprise alias — see
// ui/app/_fallbacks/enterprise/lib/registrations/modelLimitScopes.ts
// for the OSS-build fallback.
//
// Each entry can supply:
//   - PickerComponent: the React component used by the Model Limit sheet to
//     pick the scope target (e.g. a VK picker). Scopes without a target —
//     e.g. global — omit this.
//   - buildDeepLink: a function returning the route to navigate to when the
//     user clicks the Scope Target badge on the Model Limits table.

import type { ComponentType } from "react";
import { VirtualKeySelector } from "@/components/entitySelectors/virtualKeySelector";
import type { ModelConfig } from "@/lib/types/governance";

export interface ScopePickerProps {
	value: string;
	onChange: (value: string) => void;
	disabled?: boolean;
	// Optional fallback option to guarantee the currently-selected target is
	// always selectable, even if it falls outside the first page fetched by
	// the picker. The Model Limit sheet passes the model_config's own
	// scope_id/scope_name when editing an existing row.
	fallbackOption?: { value: string; label: string } | null;
}

export interface ScopeDeepLink {
	to: string;
	search?: Record<string, string>;
}

export interface ModelLimitScopeEntry {
	value: string;
	label: string;
	// Optional. Scopes without a target (e.g. global) omit this.
	PickerComponent?: ComponentType<ScopePickerProps>;
	// Optional. Scopes without a navigable target omit this.
	buildDeepLink?: (scopeId: string) => ScopeDeepLink;
	// When true, this scope is system-generated only: never offered as a choice
	// when creating a limit, and the Model Limit sheet renders it as a pure
	// read-only summary (no editable fields, no save, no delete) instead of the
	// normal form — e.g. enterprise's "access_profile" scope, whose rows must
	// only be changed by editing the owning access profile.
	readOnly?: boolean;
	// When explicitly false, this scope is never offered as a choice when
	// creating a limit, but an EXISTING row of it stays fully editable and
	// deletable through the normal form — unlike readOnly, which also locks
	// editing. Defaults to true (creatable) when omitted. For a scope whose
	// creation is retired but whose pre-existing rows must keep working exactly
	// as before — e.g. enterprise's "user" scope.
	creatable?: boolean;
	// Optional. Overrides the label shown in the Scope column/field for rows of
	// this scope, while `label` above still names the scope itself (e.g. in the
	// filter dropdown). Lets a scope whose rows apply to a user — but aren't the
	// literal "user" scope value — still read as "User" wherever a single row is
	// displayed, without colliding with "user" as a distinct filterable option.
	displayAsScope?: string;
	// Optional. Groups this scope under another scope's filter option, so the
	// Scope filter offers one choice covering both and queries them together.
	// The value names the scope that owns the option (e.g. enterprise's
	// "access_profile" rows filter under "user": they are still a user's limits,
	// just materialized from a profile, so splitting them across two filter
	// options only hides rows from someone filtering by User). The grouped scope
	// keeps its own `label` for anywhere it names itself.
	filterUnderScope?: string;
	// Optional. Renders additional context next to the Scope Target for a row of
	// this scope — e.g. enterprise's "Managed by Access Profile: X" note. Passed
	// the row's own ModelConfig; renders nothing (returns null) itself when there
	// is nothing to show.
	ManagedByComponent?: ComponentType<{ modelConfig: ModelConfig }>;
	// Optional. Replaces the generic read-only alert body in the Model Limit
	// sheet for a readOnly scope, letting the owner name itself — e.g.
	// enterprise's "managed by access profile X". Passed the row's own
	// ModelConfig; falls back to the generic message when omitted.
	ReadOnlyNotice?: ComponentType<{ modelConfig: ModelConfig }>;
}

const registry = new Map<string, ModelLimitScopeEntry>();

/**
 * Registers (or replaces) a scope entry. Intended to be called at module
 * load — once, before the first render that reads the registry.
 */
export function registerModelLimitScope(entry: ModelLimitScopeEntry): void {
	if (!entry.value) {
		return;
	}
	registry.set(entry.value, entry);
}

/** Returns all registered scope entries, in registration order. */
export function getModelLimitScopes(): ModelLimitScopeEntry[] {
	return Array.from(registry.values());
}

export function getModelLimitScope(value: string): ModelLimitScopeEntry | undefined {
	return registry.get(value);
}

/**
 * A single Scope filter choice: the option's own label, and every scope value it
 * should query. Scopes that declare `filterUnderScope` are folded into their
 * owner's option instead of getting one of their own, so the filter reads the way
 * a user thinks about the rows rather than mirroring storage.
 */
export interface ModelLimitScopeFilterOption {
	value: string;
	label: string;
	scopes: string[];
}

export function getModelLimitScopeFilterOptions(): ModelLimitScopeFilterOption[] {
	const options: ModelLimitScopeFilterOption[] = [];
	const byValue = new Map<string, ModelLimitScopeFilterOption>();
	for (const entry of getModelLimitScopes()) {
		if (entry.filterUnderScope) continue;
		const option = { value: entry.value, label: entry.label, scopes: [entry.value] };
		options.push(option);
		byValue.set(entry.value, option);
	}
	for (const entry of getModelLimitScopes()) {
		if (!entry.filterUnderScope) continue;
		const owner = byValue.get(entry.filterUnderScope);
		// An unknown owner would silently drop the scope from the filter, so fall
		// back to giving it its own option rather than hiding its rows.
		if (owner) {
			owner.scopes.push(entry.value);
		} else {
			options.push({ value: entry.value, label: entry.label, scopes: [entry.value] });
		}
	}
	return options;
}

// ---------------------------------------------------------------------------
// OSS default registrations.
// ---------------------------------------------------------------------------

registerModelLimitScope({
	value: "global",
	label: "Global",
});

registerModelLimitScope({
	value: "virtual_key",
	label: "Virtual Key",
	PickerComponent: VirtualKeySelector,
	buildDeepLink: (scopeId) => ({
		to: "/workspace/governance/virtual-keys",
		search: { vk: scopeId },
	}),
});