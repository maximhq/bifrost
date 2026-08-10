// Runtime registry for the governance user picker.
//
// OSS has no user directory, so the base build registers nothing and
// consumers (pricing overrides, routing rules) hide their "User" scope
// options. Downstream builds (enterprise) register a picker by importing
// their registration module via the @enterprise alias; see
// ui/app/_fallbacks/enterprise/lib/registrations/userPicker.ts for the
// OSS-build fallback.

import type { ComponentType } from "react";

export interface UserPickerProps {
	value: string;
	onChange: (value: string) => void;
	disabled?: boolean;
	// Optional fallback option to guarantee the currently-selected user is
	// always selectable, even if it falls outside the page fetched by the
	// picker. Callers pass the edited row's own user id when editing an
	// existing row.
	fallbackOption?: { value: string; label: string } | null;
	/** Placeholder for the empty state — e.g. "All Users" when used as a filter. */
	placeholder?: string;
	className?: string;
	/** Extra classes for the combobox trigger, e.g. `h-9` to line up with a search input. */
	triggerClassName?: string;
}

let userPicker: ComponentType<UserPickerProps> | undefined;

/**
 * Registers (or replaces) the user picker. Intended to be called at module
 * load, once, before the first render that reads the registry.
 */
export function registerUserPicker(component: ComponentType<UserPickerProps>): void {
	userPicker = component;
}

/** Returns the registered user picker, or undefined in builds without one. */
export function getUserPicker(): ComponentType<UserPickerProps> | undefined {
	return userPicker;
}

// ---------------------------------------------------------------------------
// Raw search hook — for surfaces that need to drive their own inline
// search+checkbox list (e.g. a filter sidebar matching the VK/MCP-client
// filter UI) rather than the single-select popover above. Same OSS gap: no
// user directory exists here, so nothing is registered by default and
// getUserSearchQuery() returns undefined.
// ---------------------------------------------------------------------------

export interface UserSearchResult {
	id: string;
	label: string;
}

export interface UserSearchQueryResult {
	data?: { users: UserSearchResult[] };
	isFetching: boolean;
}

/** Shape of an RTK Query search hook: same calling convention as `useGetXQuery(params, options)`. */
export type UseUserSearchQuery = (
	params: { search?: string; limit?: number },
	options?: { skip?: boolean },
) => UserSearchQueryResult;

let userSearchQuery: UseUserSearchQuery | undefined;

/** Registers (or replaces) the user search hook. Same load-once contract as registerUserPicker. */
export function registerUserSearchQuery(hook: UseUserSearchQuery): void {
	userSearchQuery = hook;
}

/** Returns the registered user search hook, or undefined in builds without one. */
export function getUserSearchQuery(): UseUserSearchQuery | undefined {
	return userSearchQuery;
}
