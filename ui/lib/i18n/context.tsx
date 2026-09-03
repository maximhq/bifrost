import { setNumberLocale } from "@/lib/utils/numbers";
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { applyDocumentLocale, localeToBcp47, persistLocale, resolveInitialLocale } from "./detect";
import { pluralKey, translate, type MessageKey, type TranslateFn } from "./translate";
import { type Locale, type MessageParams } from "./types";

interface I18nContextValue {
	locale: Locale;
	bcp47: string;
	setLocale: (locale: Locale) => void;
	t: TranslateFn;
}

const I18nContext = createContext<I18nContextValue | null>(null);

export function I18nProvider({ children }: { children: ReactNode }) {
	const [locale, setLocaleState] = useState<Locale>(resolveInitialLocale);

	useEffect(() => {
		applyDocumentLocale(locale);
		setNumberLocale(localeToBcp47(locale));
	}, [locale]);

	const setLocale = useCallback((next: Locale) => {
		setLocaleState(next);
		persistLocale(next);
	}, []);

	const t = useCallback<TranslateFn>((key, params) => translate(locale, key, params), [locale]);

	const value = useMemo<I18nContextValue>(
		() => ({
			locale,
			bcp47: localeToBcp47(locale),
			setLocale,
			t,
		}),
		[locale, setLocale, t],
	);

	return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nContextValue {
	const ctx = useContext(I18nContext);
	if (!ctx) {
		throw new Error("useI18n must be used within I18nProvider");
	}
	return ctx;
}

export function useT(): TranslateFn {
	return useI18n().t;
}

export function usePlural() {
	const { t } = useI18n();
	return useCallback((key: MessageKey, count: number, params?: MessageParams) => t(pluralKey(key, count), { count, ...params }), [t]);
}