import { DEFAULT_LOCALE, isLocale, LOCALE_META, LOCALE_STORAGE_KEY, type Locale } from "./types";

export function detectBrowserLocale(): Locale {
	if (typeof navigator === "undefined") return DEFAULT_LOCALE;
	const candidates = [navigator.language, ...(navigator.languages ?? [])];
	for (const candidate of candidates) {
		const normalized = candidate.toLowerCase();
		if (normalized.startsWith("zh")) return "zh";
		if (normalized.startsWith("en")) return "en";
	}
	return DEFAULT_LOCALE;
}

export function readStoredLocale(): Locale | null {
	if (typeof window === "undefined") return null;
	try {
		const stored = window.localStorage.getItem(LOCALE_STORAGE_KEY);
		return isLocale(stored) ? stored : null;
	} catch {
		return null;
	}
}

export function persistLocale(locale: Locale): void {
	if (typeof window === "undefined") return;
	try {
		window.localStorage.setItem(LOCALE_STORAGE_KEY, locale);
	} catch {
		// localStorage unavailable — preference won't persist.
	}
}

export function resolveInitialLocale(): Locale {
	return readStoredLocale() ?? detectBrowserLocale();
}

export function localeToBcp47(locale: Locale): string {
	return LOCALE_META[locale].bcp47;
}

export function applyDocumentLocale(locale: Locale): void {
	if (typeof document === "undefined") return;
	document.documentElement.lang = localeToBcp47(locale);
}