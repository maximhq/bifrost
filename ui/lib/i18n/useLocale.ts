import { useTranslation } from "react-i18next";

import { getLocale, type SupportedLocale } from "./index";

// Subscribes the caller to language changes, so memoized components re-render and re-run Intl formatting.
export function useLocale(): SupportedLocale {
	useTranslation();
	return getLocale();
}