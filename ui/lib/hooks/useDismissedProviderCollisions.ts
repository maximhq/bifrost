"use client";

import { normalizeProviderName } from "@/lib/utils/providerCollision";
import { useCallback, useEffect, useState } from "react";

const STORAGE_KEY = "bifrost.dismissedProviderCollisions";

const readStored = (): string[] => {
	try {
		const raw = localStorage.getItem(STORAGE_KEY);
		if (!raw) return [];
		const parsed: unknown = JSON.parse(raw);
		return Array.isArray(parsed) ? parsed.filter((v): v is string => typeof v === "string") : [];
	} catch {
		return [];
	}
};

/**
 * Tracks which custom-provider names the user has chosen to keep even though a
 * first-party integration exists. Persisted in localStorage as normalized names.
 *
 * Reads localStorage lazily in an effect to avoid hydration mismatches; `hydrated`
 * is false until that read completes, so callers should not act on `dismissed` before then.
 */
export function useDismissedProviderCollisions(): {
	dismissed: Set<string>;
	dismiss: (customName: string) => void;
	hydrated: boolean;
} {
	const [dismissed, setDismissed] = useState<Set<string>>(() => new Set());
	const [hydrated, setHydrated] = useState(false);

	useEffect(() => {
		setDismissed(new Set(readStored()));
		setHydrated(true);
	}, []);

	const dismiss = useCallback((customName: string) => {
		const normalized = normalizeProviderName(customName);
		setDismissed((prev) => {
			const next = new Set(prev);
			next.add(normalized);
			try {
				localStorage.setItem(STORAGE_KEY, JSON.stringify(Array.from(next)));
			} catch {
				// localStorage unavailable — preference won't persist.
			}
			return next;
		});
	}, []);

	return { dismissed, dismiss, hydrated };
}
