import i18n from "i18next";
import LanguageDetector from "i18next-browser-languagedetector";
import { initReactI18next } from "react-i18next";

import { NAMESPACES, resources } from "./resources";

export const LOCALE_STORAGE_KEY = "bifrost.locale";

export const SUPPORTED_LOCALES = [
	{ code: "en", label: "English" },
	{ code: "zh-CN", label: "简体中文" },
] as const;

export type SupportedLocale = (typeof SUPPORTED_LOCALES)[number]["code"];

export const DEFAULT_LOCALE: SupportedLocale = "en";

const supportedLocaleCodes: readonly string[] = SUPPORTED_LOCALES.map((locale) => locale.code);

export function isSupportedLocale(value: string | null | undefined): value is SupportedLocale {
	return !!value && supportedLocaleCodes.includes(value);
}

export function getLocale(): SupportedLocale {
	const current = i18n.resolvedLanguage ?? i18n.language;
	return isSupportedLocale(current) ? current : DEFAULT_LOCALE;
}

export async function changeLocale(locale: SupportedLocale): Promise<SupportedLocale> {
	await i18n.changeLanguage(locale);
	if (typeof window !== "undefined") {
		window.localStorage.setItem(LOCALE_STORAGE_KEY, locale);
	}
	return getLocale();
}

if (!i18n.isInitialized) {
	void i18n
		.use(LanguageDetector)
		.use(initReactI18next)
		.init({
			resources,
			ns: [...NAMESPACES],
			defaultNS: "common",
			fallbackLng: DEFAULT_LOCALE,
			supportedLngs: [...supportedLocaleCodes],
			nonExplicitSupportedLngs: false,
			load: "currentOnly",
			interpolation: {
				escapeValue: false,
			},
			detection: {
				order: ["localStorage", "navigator"],
				lookupLocalStorage: LOCALE_STORAGE_KEY,
				caches: ["localStorage"],
			},
			react: {
				useSuspense: false,
			},
		});
}

export { NAMESPACES };
export type { I18nNamespace } from "./resources";
export default i18n;
