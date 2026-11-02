"use client";

import { useCallback, useState } from "react";

/** Every role the log detail Messages filter can show or hide, in menu order. */
export const MESSAGE_ROLES = ["system", "user", "assistant", "tool", "reasoning"] as const;

export type MessageRole = (typeof MESSAGE_ROLES)[number];

export const MESSAGE_ROLE_FILTER_STORAGE_KEY = "bifrost.logs.visibleMessageRoles";

const isMessageRole = (value: unknown): value is MessageRole =>
	typeof value === "string" && (MESSAGE_ROLES as readonly string[]).includes(value);

/**
 * Parses a stored filter. Returns null, meaning "show every role", for a missing,
 * malformed or empty entry: an empty filter would open every later log with no
 * messages on screen, which reads as a broken page rather than a saved choice.
 * Unknown roles (a renamed or removed role) are dropped.
 */
export function parseStoredMessageRoles(raw: string | null): Set<MessageRole> | null {
	if (!raw) return null;
	try {
		const parsed: unknown = JSON.parse(raw);
		if (!Array.isArray(parsed)) return null;
		const roles = new Set(parsed.filter(isMessageRole));
		return roles.size > 0 ? roles : null;
	} catch {
		return null;
	}
}

/** Serializes a filter in menu order so the stored value is stable. */
export function serializeMessageRoles(roles: ReadonlySet<MessageRole>): string {
	return JSON.stringify(MESSAGE_ROLES.filter((role) => roles.has(role)));
}

const readStoredRoles = (): Set<MessageRole> => {
	try {
		return parseStoredMessageRoles(localStorage.getItem(MESSAGE_ROLE_FILTER_STORAGE_KEY)) ?? new Set(MESSAGE_ROLES);
	} catch {
		return new Set(MESSAGE_ROLES);
	}
};

/**
 * The log detail Messages filter, kept in localStorage so it survives reloads,
 * switching logs and new sessions. The stored value is read synchronously on
 * mount (the UI is a client-only SPA) so a filtered log never flashes unfiltered.
 */
export function useMessageRoleFilterPreference(): [
	Set<MessageRole>,
	(update: Set<MessageRole> | ((prev: Set<MessageRole>) => Set<MessageRole>)) => void,
] {
	const [visibleRoles, setVisibleRolesState] = useState<Set<MessageRole>>(readStoredRoles);

	const setVisibleRoles = useCallback((update: Set<MessageRole> | ((prev: Set<MessageRole>) => Set<MessageRole>)) => {
		setVisibleRolesState((prev) => {
			const next = typeof update === "function" ? update(prev) : update;
			try {
				localStorage.setItem(MESSAGE_ROLE_FILTER_STORAGE_KEY, serializeMessageRoles(next));
			} catch {
				// localStorage unavailable: the filter still applies, it just won't persist.
			}
			return next;
		});
	}, []);

	return [visibleRoles, setVisibleRoles];
}