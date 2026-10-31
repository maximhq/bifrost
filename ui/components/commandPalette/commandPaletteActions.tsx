import { useRouter } from "@tanstack/react-router";
import { Languages, Laptop, Moon, Plus, SunMoon, Sun } from "lucide-react";
import { useTheme } from "next-themes";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import { withActionParam, withActionParamOnPage } from "@/hooks/useActionParam";
import { changeLocale, getLocale, SUPPORTED_LOCALES } from "@/lib/i18n";
import { RbacOperation, useRbacContext } from "@enterprise/lib";

import type { PaletteAction, PaletteNavCommand } from "./commandPalette.utils";
import { CREATE_COMMANDS } from "./createCommands";

const THEMES = [
	{ value: "light", labelKey: "theme.light", icon: Sun },
	{ value: "dark", labelKey: "theme.dark", icon: Moon },
	{ value: "system", labelKey: "theme.system", icon: Laptop },
] as const;

// Names for a locale besides its native label, so "japanese" or "日语" finds 日本語.
function localeKeywords(code: string, uiLocale: string): string[] {
	const names = [code];
	for (const display of ["en", uiLocale]) {
		try {
			const name = new Intl.DisplayNames([display], { type: "language" }).of(code);
			if (name) names.push(name);
		} catch {
			// An engine without Intl.DisplayNames still matches on the native label.
		}
	}
	return names;
}

interface PaletteActionsOptions {
	close: () => void;
	commands: PaletteNavCommand[];
	currentId?: string;
}

interface PaletteActions {
	actions: PaletteAction[];
	// Commands that act on the page being viewed, listed first in the palette.
	thisPage: PaletteAction[];
}

/** The palette's root actions. Each one that opens a nested page brings that page's actions with it. */
export function usePaletteActions({ close, commands, currentId }: PaletteActionsOptions): PaletteActions {
	const { t, i18n } = useTranslation("shell");
	const { t: tCommon } = useTranslation("common");
	const { theme, setTheme } = useTheme();
	const { isAllowed } = useRbacContext();
	const router = useRouter();
	const locale = i18n.resolvedLanguage ?? getLocale();

	const createActions = useMemo(() => {
		const pagesByHref = new Map(commands.map((c) => [c.href, c]));
		return CREATE_COMMANDS.flatMap((def): PaletteAction[] => {
			const target = pagesByHref.get(def.href);
			// Only for a page the viewer can open and create on.
			if (!target || !isAllowed(def.resource, RbacOperation.Create)) return [];
			const sameRoute = target.id === currentId;
			return [
				{
					id: def.id,
					title: t(`commandPalette.create.${def.title}`),
					// The page's own section, since "Create" as a breadcrumb would only repeat the title.
					section: target.section,
					icon: target.icon,
					keywords: [target.title, "new", "add"],
					run: () => {
						close();
						const action = def.action ?? "create";
						// Replace on the page already open, or Back would land on the same page.
						if (sameRoute) router.history.replace(withActionParamOnPage(def.href, router.history.location.search, action));
						else router.history.push(withActionParam(def.href, action));
					},
				},
			];
		});
	}, [commands, currentId, isAllowed, t, close, router]);

	const thisPage = useMemo(() => {
		const current = commands.find((c) => c.id === currentId);
		return createActions.filter((a) => CREATE_COMMANDS.some((def) => def.id === a.id && def.href === current?.href));
	}, [commands, currentId, createActions]);

	const actions = useMemo(() => {
		const themeTitle = t("commandPalette.pages.theme.title");
		const languageTitle = t("commandPalette.pages.language.title");
		const createTitle = t("commandPalette.pages.create.title");
		const createPage: PaletteAction[] = createActions.length
			? [
					{
						id: "page:create",
						title: t("commandPalette.actions.create"),
						icon: Plus,
						keywords: [createTitle, "new", "add"],
						page: {
							id: "create",
							title: createTitle,
							placeholder: t("commandPalette.pages.create.placeholder"),
							actions: createActions,
						},
					},
				]
			: [];
		return [
			...createPage,
			{
				id: "page:theme",
				title: t("commandPalette.actions.changeTheme"),
				icon: SunMoon,
				keywords: [themeTitle, "dark", "light", "mode"],
				page: {
					id: "theme",
					title: themeTitle,
					placeholder: t("commandPalette.pages.theme.placeholder"),
					actions: THEMES.map(({ value, labelKey, icon }) => ({
						id: `theme:${value}`,
						title: tCommon(labelKey),
						icon,
						keywords: [value],
						checked: theme === value,
						run: () => {
							setTheme(value);
							close();
						},
					})),
				},
			},
			{
				id: "page:language",
				title: t("commandPalette.actions.changeLanguage"),
				icon: Languages,
				keywords: [languageTitle, "locale", "language"],
				page: {
					id: "language",
					title: languageTitle,
					placeholder: t("commandPalette.pages.language.placeholder"),
					actions: SUPPORTED_LOCALES.map(({ code, label }) => ({
						id: `language:${code}`,
						title: label,
						keywords: localeKeywords(code, locale),
						checked: locale === code,
						run: () => {
							if (locale !== code) void changeLocale(code);
							close();
						},
					})),
				},
			},
		];
	}, [t, tCommon, theme, setTheme, locale, close, createActions]);

	return { actions, thisPage };
}