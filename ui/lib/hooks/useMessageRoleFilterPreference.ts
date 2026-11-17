"use client";

import { useEffect, useRef, useState, type Dispatch, type SetStateAction } from "react";

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
 *
 * The write happens in an effect on the committed selection, never inside a state
 * updater: React may run an updater twice or discard its render, which would store
 * a filter the view never showed. The value read on mount is not written back.
 */
export function useMessageRoleFilterPreference(): [Set<MessageRole>, Dispatch<SetStateAction<Set<MessageRole>>>] {
	const [visibleRoles, setVisibleRoles] = useState<Set<MessageRole>>(readStoredRoles);
	const lastPersisted = useRef<string | null>(null);

	useEffect(() => {
		const serialized = serializeMessageRoles(visibleRoles);
		if (lastPersisted.current === null) {
			lastPersisted.current = serialized;
			return;
		}
		if (serialized === lastPersisted.current) return;
		lastPersisted.current = serialized;
		try {
			localStorage.setItem(MESSAGE_ROLE_FILTER_STORAGE_KEY, serialized);
		} catch {
			// localStorage unavailable: the filter still applies, it just won't persist.
		}
	}, [visibleRoles]);

	return [visibleRoles, setVisibleRoles];
}