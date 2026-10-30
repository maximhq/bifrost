import i18n, { type BackendModule } from "i18next";
import LanguageDetector from "i18next-browser-languagedetector";
import { initReactI18next } from "react-i18next";

import { NAMESPACES, loadCatalog, resources } from "./resources";

export const LOCALE_STORAGE_KEY = "bifrost.locale";

export const SUPPORTED_LOCALES = [
	{ code: "en", label: "English" },
	{ code: "zh-CN", label: "简体中文" },
	{ code: "zh-TW", label: "繁體中文" },
	{ code: "ja", label: "日本語" },
	{ code: "ko", label: "한국어" },
	{ code: "es", label: "Español" },
	{ code: "pt", label: "Português" },
	{ code: "fr", label: "Français" },
	{ code: "de", label: "Deutsch" },
	{ code: "it", label: "Italiano" },
	{ code: "ru", label: "Русский" },
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

// 在异常保护下持久化语言选择，避免存储失败中断切换。
export async function changeLocale(locale: SupportedLocale): Promise<SupportedLocale> {
	await i18n.changeLanguage(locale);
	if (typeof window !== "undefined") {
		try {
			window.localStorage.setItem(LOCALE_STORAGE_KEY, locale);
		} catch {
			// 存储不可用时仍保留已完成的语言切换。
		}
	}
	return getLocale();
}

const lazyCatalogBackend: BackendModule = {
	type: "backend",
	init: () => {},
	read: (language, namespace, callback) => {
		loadCatalog(language, namespace).then(
			(catalog) => callback(null, catalog),
			(error: Error) => callback(error, false),
		);
	},
};

// Uses the resolved language, so a failed catalog load does not label English text with the requested language.
function syncDocumentLanguage() {
	if (typeof document !== "undefined") document.documentElement.lang = getLocale();
}

let ready: Promise<unknown> = Promise.resolve();

if (!i18n.isInitialized) {
	i18n.on("languageChanged", syncDocumentLanguage);
	ready = i18n
		.use(lazyCatalogBackend)
		.use(LanguageDetector)
		.use(initReactI18next)
		.init({
			resources,
			// English is bundled; other languages come from lazyCatalogBackend.
			partialBundledLanguages: true,
			ns: [...NAMESPACES],
			defaultNS: "common",
			// Namespaces are chosen via useTranslation(ns) or { ns }, never a "ns:key" prefix.
			// Keep in sync with CustomTypeOptions in i18next.d.ts.
			nsSeparator: false,
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
				caches: [],
			},
			react: {
				useSuspense: false,
			},
		});
}

// Resolves once the detected language's catalogs are loaded, so the first render is not English.
export const i18nReady = ready.catch(() => undefined);

export { NAMESPACES };
export type { I18nNamespace } from "./resources";
export default i18n;