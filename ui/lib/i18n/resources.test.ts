import fs from "node:fs";
import path from "node:path";
import { createInstance } from "i18next";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ENTERPRISE_NAMESPACES, enterpriseResources } from "../../app/_fallbacks/enterprise/lib/i18n/resources";

const OSS_NAMESPACES = ["common", "shell", "login", "observability", "models", "mcp", "governance", "config"];
const LOCALES_DIR = path.resolve(__dirname, "../../locales");
const otherLocales = fs
	.readdirSync(LOCALES_DIR, { withFileTypes: true })
	.filter((entry) => entry.isDirectory() && entry.name !== "en")
	.map((entry) => entry.name)
	.sort();

const readCatalog = (lang: string, ns: string) => JSON.parse(fs.readFileSync(path.join(LOCALES_DIR, lang, `${ns}.json`), "utf8"));

async function loadResourcesWith(enterprise: { namespaces: readonly string[]; en: object }) {
	vi.resetModules();
	vi.doMock("@enterprise/lib/i18n/resources", () => ({
		ENTERPRISE_NAMESPACES: enterprise.namespaces,
		enterpriseResources: { en: enterprise.en },
	}));
	return import("./resources");
}

afterEach(() => {
	vi.doUnmock("@enterprise/lib/i18n/resources");
	vi.resetModules();
});

describe("OSS enterprise fallback", () => {
	it("registers no namespaces and no English catalogs", () => {
		expect(ENTERPRISE_NAMESPACES).toEqual([]);
		expect(enterpriseResources).toEqual({ en: {} });
	});

	it("bundles only the English OSS namespaces", async () => {
		const { NAMESPACES, resources } = await loadResourcesWith({ namespaces: [], en: {} });
		expect([...NAMESPACES]).toEqual(OSS_NAMESPACES);
		expect(Object.keys(resources)).toEqual(["en"]);
		expect(Object.keys(resources.en).sort()).toEqual([...OSS_NAMESPACES].sort());
	});
});

describe("enterprise catalogs", () => {
	it("appends enterprise namespaces after the OSS ones and bundles their English", async () => {
		const agent = { title: "Agent sign-in" };
		const { NAMESPACES, resources } = await loadResourcesWith({ namespaces: ["agent"], en: { agent } });
		expect([...NAMESPACES]).toEqual([...OSS_NAMESPACES, "agent"]);
		expect((resources.en as Record<string, unknown>).agent).toEqual(agent);
	});
});

describe("loadCatalog", () => {
	it("loads every non-English OSS catalog from disk", async () => {
		const { loadCatalog } = await import("./resources");
		for (const lang of otherLocales) for (const ns of OSS_NAMESPACES) expect(await loadCatalog(lang, ns)).toEqual(readCatalog(lang, ns));
	});

	it("rejects a language or namespace that has no catalog", async () => {
		const { loadCatalog } = await import("./resources");
		await expect(loadCatalog("xx", "common")).rejects.toThrow();
		await expect(loadCatalog("ja", "nope")).rejects.toThrow();
	});
});

describe("lazy language switch", () => {
	it("serves the translated string after a switch and falls back to English for a missing catalog", async () => {
		const { NAMESPACES, loadCatalog, resources } = await import("./resources");
		const instance = createInstance();
		await instance
			.use({
				type: "backend",
				init: () => {},
				read: (lng: string, ns: string, cb: (err: unknown, data: unknown) => void) =>
					loadCatalog(lng, ns).then(
						(data) => cb(null, data),
						(err) => cb(err, false),
					),
			})
			.init({
				resources,
				partialBundledLanguages: true,
				ns: [...NAMESPACES],
				defaultNS: "common",
				nsSeparator: false,
				fallbackLng: "en",
				lng: "en",
			});
		const save = () => (instance.t as unknown as (key: string) => string)("save");
		expect(save()).toBe(readCatalog("en", "common").save);

		await instance.changeLanguage("ja");
		expect(save()).toBe(readCatalog("ja", "common").save);

		await instance.changeLanguage("xx");
		expect(save()).toBe(readCatalog("en", "common").save);
	});
});