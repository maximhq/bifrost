// Runtime registry for the governance business unit picker.
//
// Business units are an enterprise table: OSS has no API to list them, so the
// base build registers nothing and consumers (the virtual key sheet's owner
// assignment) hide their "Business unit" options. Downstream builds
// (enterprise) register a picker by importing their registration module via
// the @enterprise alias; see
// ui/app/_fallbacks/enterprise/lib/registrations/businessUnitPicker.ts for the
// OSS-build fallback.

import type { ComponentType } from "react";

export interface BusinessUnitPickerProps {
	value: string;
	onChange: (value: string) => void;
	disabled?: boolean;
	// Guarantees the currently-selected business unit is selectable even when
	// it falls outside the page fetched by the picker. Callers pass the edited
	// row's own business unit id when editing an existing row.
	fallbackOption?: { value: string; label: string } | null;
	placeholder?: string;
	className?: string;
	/** Extra classes for the combobox trigger, e.g. `h-9` to line up with a sibling control. */
	triggerClassName?: string;
}

let businessUnitPicker: ComponentType<BusinessUnitPickerProps> | undefined;

/**
 * Registers (or replaces) the business unit picker. Intended to be called at
 * module load, once, before the first render that reads the registry.
 */
export function registerBusinessUnitPicker(component: ComponentType<BusinessUnitPickerProps>): void {
	businessUnitPicker = component;
}

/** Returns the registered business unit picker, or undefined in builds without one. */
export function getBusinessUnitPicker(): ComponentType<BusinessUnitPickerProps> | undefined {
	return businessUnitPicker;
}