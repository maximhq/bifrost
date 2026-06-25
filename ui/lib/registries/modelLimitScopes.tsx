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
import { ComboboxSelect } from "@/components/ui/combobox";
import { useGetVirtualKeysQuery } from "@/lib/store";

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

// ---------------------------------------------------------------------------
// OSS default registrations.
// ---------------------------------------------------------------------------

function VirtualKeyPicker({ value, onChange, disabled, fallbackOption }: ScopePickerProps) {
	const { data: vksData } = useGetVirtualKeysQuery();
	const virtualKeys = vksData?.virtual_keys ?? [];
	const options = [
		...(fallbackOption && !virtualKeys.some((vk) => vk.id === fallbackOption.value) ? [fallbackOption] : []),
		...virtualKeys.map((vk) => ({ label: vk.name, value: vk.id })),
	];
	return (
		<ComboboxSelect
			options={options}
			value={value || null}
			onValueChange={(v: string | null) => onChange(v ?? "")}
			placeholder="Select a virtual key..."
			disabled={disabled}
			noPortal
		/>
	);
}

registerModelLimitScope({
	value: "global",
	label: "Global",
});

registerModelLimitScope({
	value: "virtual_key",
	label: "Virtual Key",
	PickerComponent: VirtualKeyPicker,
	buildDeepLink: (scopeId) => ({
		to: "/workspace/governance/virtual-keys",
		search: { vk: scopeId },
	}),
});