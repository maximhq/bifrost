// Shape of a dropdown row, and how a `values` entry becomes one.
//
// Kept out of providerSelector.tsx so it can be exercised on its own: the component
// pulls in the providers API, and through it the store.

import { resolveProviderIconKey, type ProviderIconType } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import { cn } from "@/lib/utils";
import type React from "react";

/** One row in the dropdown, after the consumer has had a say in it. */
export interface ProviderSelectorOption {
	value: string;
	label: string;
	/** Which mark to draw. Already resolved through the custom-provider fallback. */
	iconKey?: ProviderIconType;
	/** Drawn instead of `iconKey`, for an action row that has no provider mark of its own. */
	icon?: React.ReactNode;
	isCustom?: boolean;
	/** Non-selectable. The row still renders, greyed, with `disabledReason` beside it. */
	disabled?: boolean;
	/** Short phrase shown on the row explaining why it cannot be picked. */
	disabledReason?: string;
}

/** A `values` entry: a bare provider name, or a row the caller has already specified. */
export type ProviderSelectorValue = string | ProviderSelectorOption;

// A bare name gets the standard provider label and mark. An object is passed through
// untouched, so a caller that already knows a row's label, or that it cannot be picked,
// does not lose that by handing the list over.
export function normalizeValueOption(value: ProviderSelectorValue): ProviderSelectorOption {
	if (typeof value !== "string") return value;
	return { value, label: getProviderLabel(value), iconKey: resolveProviderIconKey(value) };
}

/** Heading over the `isCustom` rows: the caller's label, else the translated "Custom providers". */
export function customGroupHeading(customGroupLabel: string | undefined, defaultLabel: string): string {
	return customGroupLabel ?? defaultLabel;
}

/** The list's classes. A caller's max-h replaces the 300px default rather than stacking with it. */
export function providerListClassName(listClassName?: string): string {
	return cn("max-h-[300px]", listClassName);
}