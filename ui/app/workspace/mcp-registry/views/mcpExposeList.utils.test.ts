import { beforeAll, describe, expect, it } from "vitest";
import i18n from "@/lib/i18n";
import {
	exposedByRule,
	exposeRowLabel,
	exposeRowState,
	isExposed,
	nextExposeList,
	promptExposeItems,
	resourceExposeItems,
} from "./mcpExposeList.utils";

beforeAll(async () => {
	await i18n.changeLanguage("en");
});

describe("isExposed", () => {
	it("serves everything when unset or ['*']", () => {
		expect(isExposed(undefined, "review")).toBe(true);
		expect(isExposed(null, "review")).toBe(true);
		expect(isExposed(["*"], "review")).toBe(true);
	});
	it("serves only listed keys otherwise", () => {
		expect(isExposed([], "review")).toBe(false);
		expect(isExposed(["review"], "review")).toBe(true);
		expect(isExposed(["summarize"], "review")).toBe(false);
	});
});

describe("nextExposeList", () => {
	const all = ["review", "summarize"];
	it("turning one off from all lists the rest", () => {
		expect(nextExposeList(undefined, all, "review", false)).toEqual(["summarize"]);
	});
	it("turning the last one back on collapses to ['*']", () => {
		expect(nextExposeList(["summarize"], all, "review", true)).toEqual(["*"]);
	});
	it("turning every one off leaves an empty list", () => {
		expect(nextExposeList(["review"], all, "review", false)).toEqual([]);
	});
});
describe("matching the gateway", () => {
	it("matches plain entries case-insensitively and regex: entries in full", () => {
		expect(isExposed(["Review"], "review")).toBe(true);
		expect(isExposed(["regex:file:///docs/.*"], "file:///docs/a.md")).toBe(true);
		expect(isExposed(["regex:file:///docs/.*"], "file:///other/a.md")).toBe(false);
		expect(isExposed(["regex:("], "review")).toBe(false);
	});
	it("reports items served only by a rule", () => {
		expect(exposedByRule(["regex:rev.*"], "review")).toBe(true);
		expect(exposedByRule(["review", "regex:rev.*"], "summarize")).toBe(false);
		expect(exposedByRule(["*"], "review")).toBe(false);
	});
});

describe("nextExposeList keeps what the switch does not own", () => {
	const all = ["review", "summarize"];
	it("keeps a regex rule when another item is toggled", () => {
		expect(nextExposeList(["regex:sum.*"], all, "review", true)).toEqual(["regex:sum.*", "review"]);
		expect(nextExposeList(["regex:sum.*", "review"], all, "review", false)).toEqual(["regex:sum.*"]);
	});
	it("keeps saved keys missing from discovery and does not collapse to ['*']", () => {
		expect(nextExposeList(["review", "hidden"], all, "summarize", true)).toEqual(["review", "hidden", "summarize"]);
	});
	it("replaces a differently cased entry instead of duplicating it", () => {
		expect(nextExposeList(["Review"], all, "review", false)).toEqual([]);
	});
	it("keeps one entry for keys that differ only by case, as the gateway matches them", () => {
		const cased = ["file:///A", "file:///a", "file:///B"];
		expect(nextExposeList(undefined, cased, "file:///B", false)).toEqual(["file:///A"]);
		expect(nextExposeList(["file:///A"], cased, "file:///B", true)).toEqual(["*"]);
	});
});

describe("exposeRowLabel", () => {
	it("reads what the switch would show, for a list that cannot be edited", () => {
		expect(exposeRowLabel(undefined, "review")).toBe("Served");
		expect(exposeRowLabel(["review"], "review")).toBe("Served");
		expect(exposeRowLabel([], "review")).toBe("Not served");
		expect(exposeRowLabel(["regex:rev.*"], "review")).toBe("Served by a pattern rule");
	});

	it("keys the badge by state, not by the translated label", () => {
		expect(exposeRowState(undefined, "review")).toBe("served");
		expect(exposeRowState([], "review")).toBe("notServed");
		expect(exposeRowState(["regex:rev.*"], "review")).toBe("rule");
	});
});

describe("expose items", () => {
	it("lists prompts by the server's own name, without the client prefix", () => {
		const items = promptExposeItems({
			config: { client_id: "c1", name: "github", connection_type: "http" },
			prompts: [
				{
					name: "github-review",
					title: "Review a pull request",
					description: "Asks for a review",
					arguments: [{ name: "diff", required: true }, { name: "focus" }],
				},
				{ name: "summarize" },
			],
		});
		expect(items).toEqual([
			{
				key: "review",
				label: "Review a pull request",
				detail: "review",
				description: "Asks for a review",
				arguments: [
					{ name: "diff", required: true },
					{ name: "focus", required: false },
				],
			},
			{ key: "summarize", label: "summarize", detail: undefined, description: undefined, arguments: [] },
		]);
	});

	it("lists resources by URI and templates by URI template", () => {
		const items = resourceExposeItems({
			resources: [{ uri: "file:///handbook.md", name: "handbook", title: "Handbook", mimeType: "text/markdown" }],
			resource_templates: [{ uriTemplate: "file:///docs/{name}", name: "docs", description: "Any doc" }],
		});
		expect(items).toEqual([
			{ key: "file:///handbook.md", label: "Handbook", detail: "file:///handbook.md", description: undefined, mimeType: "text/markdown" },
			{ key: "file:///docs/{name}", label: "docs", detail: "file:///docs/{name}", description: "Any doc", mimeType: undefined },
		]);
	});
});