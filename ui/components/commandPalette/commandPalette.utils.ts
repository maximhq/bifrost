import { getNavItemHref, type NavItem } from "@/components/navigation/navItem";

export interface PaletteNavCommand {
	id: string; // the href, unique across the palette
	title: string;
	section?: string; // parent nav entry, rendered as the breadcrumb
	href: string;
	icon: NavItem["icon"];
	isExternal?: boolean;
	keywords: string[];
}

// Something the palette does in place rather than a page it opens: run a callback, or open a nested list.
export interface PaletteAction {
	id: string;
	title: string;
	section?: string;
	icon?: NavItem["icon"];
	keywords: string[];
	checked?: boolean;
	run?: () => void;
	page?: PalettePage;
}

export interface PalettePage {
	id: string;
	title: string;
	placeholder: string;
	actions: PaletteAction[];
}

type Searchable = Pick<PaletteNavCommand, "title" | "section" | "keywords">;

/**
 * Root-level search covers what nested pages hold too, so "dark" finds the theme
 * without opening the theme page first. Nested actions take the page title as breadcrumb.
 */
export function flattenPaletteActions(actions: PaletteAction[]): PaletteAction[] {
	return actions.flatMap((action) =>
		action.page ? [action, ...action.page.actions.map((a) => ({ ...a, section: a.section ?? action.page!.title }))] : [action],
	);
}

/**
 * Flattens the sidebar tree into one jump target per page the user can open.
 * Parents with children are toggles in the sidebar, not pages, so only their
 * children are listed; a child whose href repeats an earlier one is dropped.
 * Outside enterprise, pages that only render an upsell are left out.
 */
export function flattenNavItems(items: NavItem[], isEnterprise: boolean): PaletteNavCommand[] {
	const seen = new Set<string>();
	const out: PaletteNavCommand[] = [];
	const push = (item: NavItem, section?: string) => {
		if (item.hasAccess === false || (item.enterpriseOnly && !isEnterprise)) return;
		const href = getNavItemHref(item);
		if (seen.has(href)) return;
		seen.add(href);
		out.push({
			id: href,
			title: item.title,
			section,
			href,
			icon: item.icon,
			isExternal: item.isExternal,
			keywords: [section, item.description].filter((k): k is string => !!k),
		});
	};
	for (const item of items) {
		if (!item.subItems?.length) {
			push(item);
			continue;
		}
		if (item.enterpriseOnly && !isEnterprise) continue;
		for (const sub of item.subItems) push(sub, item.title);
	}
	return out;
}

const normalize = (s: string) => s.toLowerCase().trim();

// Characters of `query` appear in `text` in order, e.g. "vk" in "virtual keys".
const isSubsequence = (query: string, text: string) => {
	let i = 0;
	for (let j = 0; j < text.length && i < query.length; j++) {
		if (text[j] === query[i]) i++;
	}
	return i === query.length;
};

const initials = (s: string) =>
	s
		.split(/[\s&/-]+/)
		.filter(Boolean)
		.map((w) => w[0])
		.join("");

/**
 * Ranks a command against the query, 0 meaning no match. Title matches beat
 * breadcrumb matches, so "settings" lists the pages named Settings before
 * every page that merely lives under the Settings section.
 */
export function scoreCommand(command: Searchable, rawQuery: string): number {
	const query = normalize(rawQuery);
	if (!query) return 1;
	const title = normalize(command.title);
	const section = normalize(command.section ?? "");
	const full = section ? `${section} ${title}` : title;

	if (title === query) return 100;
	if (title.startsWith(query)) return 90;
	if (title.split(/\s+/).some((w) => w.startsWith(query))) return 80;
	if (initials(title).startsWith(query.replace(/\s+/g, ""))) return 75;
	if (title.includes(query)) return 70;
	// "models routing" or "routing models": every word hits the title or breadcrumb.
	const words = query.split(/\s+/);
	if (words.length > 1 && words.every((w) => full.includes(w))) return 60;
	if (section.startsWith(query)) return 50;
	if (section.includes(query)) return 40;
	if (command.keywords.some((k) => normalize(k).includes(query))) return 30;
	if (isSubsequence(query.replace(/\s+/g, ""), title.replace(/\s+/g, ""))) return 20;
	return 0;
}

export function searchCommands<T extends Searchable>(commands: T[], query: string): T[] {
	return commands
		.map((command, index) => ({ command, index, score: scoreCommand(command, query) }))
		.filter((r) => r.score > 0)
		.sort((a, b) => b.score - a.score || a.index - b.index)
		.map((r) => r.command);
}

// The command for the page being viewed: an exact href match, else the longest path prefix.
export function findCurrentCommandId(commands: PaletteNavCommand[], pathname: string, search: string): string | undefined {
	const exact = commands.find((c) => c.href === `${pathname}${search}` || c.href === pathname);
	if (exact) return exact.id;
	let best: PaletteNavCommand | undefined;
	for (const c of commands) {
		const path = c.href.split("?")[0];
		if (pathname.startsWith(`${path}/`) && (!best || path.length > best.href.split("?")[0].length)) best = c;
	}
	return best?.id;
}

// Code editors bind ⌘K/Ctrl+K chords of their own, so the palette leaves those keystrokes alone.
export function isInsideCodeEditor(target: EventTarget | null): boolean {
	const el = target as Partial<Element> | null;
	return typeof el?.closest === "function" && !!el.closest(".monaco-editor");
}

type ShortcutEvent = Pick<KeyboardEvent, "key" | "metaKey" | "ctrlKey" | "shiftKey" | "altKey">;

// ⌘K / Ctrl+K with no other modifier. Autofill fires keydown with no key, hence the optional chain.
export const isPaletteShortcut = (event: ShortcutEvent): boolean =>
	event.key?.toLowerCase() === "k" && (event.metaKey || event.ctrlKey) && !event.shiftKey && !event.altKey;

export const isApplePlatform = (): boolean => typeof navigator !== "undefined" && /Mac|iPhone|iPad|iPod/.test(navigator.userAgent);

// The label for the platform's command modifier: ⌘ on Apple devices, Ctrl elsewhere.
export const modKeyLabel = (): string => (isApplePlatform() ? "⌘" : "Ctrl");