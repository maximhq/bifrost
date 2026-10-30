import { describe, expect, it } from "vitest";

import { type NavItem, preserveTimeFilters } from "@/components/navigation/navItem";

import {
	findCurrentCommandId,
	flattenNavItems,
	flattenPaletteActions,
	isInsideCodeEditor,
	isPaletteShortcut,
	type PaletteAction,
	scoreCommand,
	searchCommands,
} from "./commandPalette.utils";

const Icon = () => null;
const nav = (title: string, url: string, extra: Partial<NavItem> = {}): NavItem => ({
	id: url,
	title,
	url,
	icon: Icon,
	hasAccess: true,
	...extra,
});

const tree: NavItem[] = [
	nav("Home", "/workspace/home"),
	nav("Models", "/workspace/providers", {
		subItems: [
			nav("Model Providers", "/workspace/providers"),
			nav("Routing Rules", "/workspace/routing-rules"),
			nav("Model Settings", "/workspace/custom-pricing", { hasAccess: false }),
		],
	}),
	nav("Governance", "/workspace/governance", {
		subItems: [nav("Virtual Keys", "/workspace/governance/virtual-keys"), nav("Teams", "/workspace/governance/teams")],
	}),
	nav("Settings", "/workspace/config", {
		subItems: [
			nav("Client Settings", "/workspace/config/client-settings"),
			nav("Settings", "/workspace/cluster/settings"),
			nav("Caching", "/workspace/config/caching", { queryParam: "semantic" }),
		],
	}),
	nav("Plugins", "/workspace/plugins", { hasAccess: false }),
];

describe("flattenNavItems", () => {
	const commands = flattenNavItems(tree, true);

	it("lists children of parents instead of the parent toggle, with the parent as breadcrumb", () => {
		expect(commands.map((c) => [c.title, c.section ?? null])).toEqual([
			["Home", null],
			["Model Providers", "Models"],
			["Routing Rules", "Models"],
			["Virtual Keys", "Governance"],
			["Teams", "Governance"],
			["Client Settings", "Settings"],
			["Settings", "Settings"],
			["Caching", "Settings"],
		]);
	});

	it("drops pages the user has no access to", () => {
		expect(commands.find((c) => c.title === "Model Settings" || c.title === "Plugins")).toBeUndefined();
	});

	it("keeps the tab query param in the href", () => {
		expect(commands.find((c) => c.title === "Caching")?.href).toBe("/workspace/config/caching?tab=semantic");
	});

	it("drops a repeated href", () => {
		const dup = flattenNavItems([nav("A", "/x"), nav("Group", "/g", { subItems: [nav("B", "/x")] })], true);
		expect(dup.map((c) => c.title)).toEqual(["A"]);
	});

	it("hides enterprise-only pages and groups outside enterprise", () => {
		const items = [
			nav("Governance", "/g", { subItems: [nav("Virtual Keys", "/g/vk"), nav("Users", "/g/users", { enterpriseOnly: true })] }),
			nav("Guardrails", "/gr", { enterpriseOnly: true, subItems: [nav("Rules", "/gr/rules")] }),
			nav("Audit Logs", "/audit", { enterpriseOnly: true }),
		];
		expect(flattenNavItems(items, false).map((c) => c.title)).toEqual(["Virtual Keys"]);
		expect(flattenNavItems(items, true).map((c) => c.title)).toEqual(["Virtual Keys", "Users", "Rules", "Audit Logs"]);
	});
});

describe("searchCommands", () => {
	const commands = flattenNavItems(tree, true);
	const titles = (q: string) => searchCommands(commands, q).map((c) => c.title);

	it("ranks a title hit above a page that only lives under a matching section", () => {
		expect(titles("settings").slice(0, 2)).toEqual(["Settings", "Client Settings"]);
		expect(titles("settings")).toContain("Caching");
		expect(titles("settings").indexOf("Caching")).toBeGreaterThan(titles("settings").indexOf("Client Settings"));
	});

	it("matches initials and in-order letters", () => {
		expect(titles("vk")[0]).toBe("Virtual Keys");
		expect(titles("rtng")).toEqual(["Routing Rules"]);
	});

	it("matches words split across breadcrumb and title", () => {
		expect(titles("models routing")).toEqual(["Routing Rules"]);
	});

	it("is case and whitespace insensitive and returns nothing for no match", () => {
		expect(titles("  TEAMS ")).toEqual(["Teams"]);
		expect(titles("zzz")).toEqual([]);
	});

	it("scores every command when the query is empty", () => {
		expect(scoreCommand(commands[0], "")).toBeGreaterThan(0);
	});
});

describe("flattenPaletteActions", () => {
	const theme: PaletteAction = {
		id: "page:theme",
		title: "Change theme…",
		keywords: [],
		page: {
			id: "theme",
			title: "Theme",
			placeholder: "",
			actions: [
				{ id: "theme:light", title: "Light", keywords: [] },
				{ id: "theme:dark", title: "Dark", keywords: [] },
			],
		},
	};
	const flat = flattenPaletteActions([theme, { id: "x", title: "Other", keywords: [] }]);

	it("lists a nested page's actions after the action that opens it, with the page title as breadcrumb", () => {
		expect(flat.map((a) => [a.id, a.section ?? null])).toEqual([
			["page:theme", null],
			["theme:light", "Theme"],
			["theme:dark", "Theme"],
			["x", null],
		]);
	});

	it("lets a nested action outrank a page that matches only by letters", () => {
		const pages = [...flattenNavItems([nav("Dashboard Alerts Rules Keys", "/darks")], true)];
		const results = searchCommands<(typeof pages)[number] | PaletteAction>([...pages, ...flat], "dark");
		expect(results[0].title).toBe("Dark");
	});
});

describe("findCurrentCommandId", () => {
	const commands = flattenNavItems(tree, true);

	it("prefers an exact match, including the tab param", () => {
		expect(findCurrentCommandId(commands, "/workspace/config/caching", "?tab=semantic")).toBe("/workspace/config/caching?tab=semantic");
		expect(findCurrentCommandId(commands, "/workspace/governance/teams", "")).toBe("/workspace/governance/teams");
	});

	it("falls back to the deepest page containing the path", () => {
		expect(findCurrentCommandId(commands, "/workspace/governance/virtual-keys/abc", "")).toBe("/workspace/governance/virtual-keys");
		expect(findCurrentCommandId(commands, "/elsewhere", "")).toBeUndefined();
	});
});

describe("isInsideCodeEditor", () => {
	const inside = (match: boolean) =>
		({ closest: (sel: string) => (match && sel === ".monaco-editor" ? {} : null) }) as unknown as EventTarget;

	it("is true only for targets inside a Monaco editor", () => {
		expect(isInsideCodeEditor(inside(true))).toBe(true);
		expect(isInsideCodeEditor(inside(false))).toBe(false);
		expect(isInsideCodeEditor(null)).toBe(false);
		expect(isInsideCodeEditor({} as EventTarget)).toBe(false);
	});
});

describe("preserveTimeFilters", () => {
	it("carries the period between time-filtered pages", () => {
		expect(preserveTimeFilters("/workspace/logs", "/workspace/logs", "/workspace/dashboard", "?period=24h")).toBe(
			"/workspace/logs?period=24h",
		);
	});

	it("carries an absolute range", () => {
		const href = preserveTimeFilters("/workspace/logs", "/workspace/logs", "/workspace/dashboard", "?start_time=a&end_time=b");
		expect(new URLSearchParams(href.split("?")[1]).get("end_time")).toBe("b");
	});

	it("leaves other destinations and sources alone", () => {
		expect(preserveTimeFilters("/workspace/plugins", "/workspace/plugins", "/workspace/dashboard", "?period=24h")).toBe(
			"/workspace/plugins",
		);
		expect(preserveTimeFilters("/workspace/logs", "/workspace/logs", "/workspace/plugins", "?period=24h")).toBe("/workspace/logs");
	});
});

describe("isPaletteShortcut", () => {
	const key = (k: string | undefined, mods: Partial<KeyboardEvent> = {}) =>
		({ key: k, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, ...mods }) as KeyboardEvent;

	it("matches ⌘K and Ctrl+K, in either case", () => {
		expect(isPaletteShortcut(key("k", { metaKey: true }))).toBe(true);
		expect(isPaletteShortcut(key("K", { ctrlKey: true }))).toBe(true);
	});

	it("ignores K without the command modifier or with an extra one", () => {
		expect(isPaletteShortcut(key("k"))).toBe(false);
		expect(isPaletteShortcut(key("k", { metaKey: true, shiftKey: true }))).toBe(false);
		expect(isPaletteShortcut(key("k", { ctrlKey: true, altKey: true }))).toBe(false);
	});

	it("returns false for an autofill keydown that carries no key", () => {
		expect(isPaletteShortcut(key(undefined, { metaKey: true }))).toBe(false);
	});
});