export const LOCALES = ["en", "zh"] as const;

export type Locale = (typeof LOCALES)[number];

export const DEFAULT_LOCALE: Locale = "en";

export const LOCALE_STORAGE_KEY = "bifrost.locale";

export const LOCALE_META: Record<Locale, { label: string; nativeLabel: string; bcp47: string }> = {
	en: { label: "English", nativeLabel: "English", bcp47: "en-US" },
	zh: { label: "Chinese", nativeLabel: "中文", bcp47: "zh-CN" },
};

export function isLocale(value: unknown): value is Locale {
	return typeof value === "string" && (LOCALES as readonly string[]).includes(value);
}

export type MessageParams = Record<string, string | number>;