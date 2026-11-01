import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const documentElement = { lang: "" };

// Catalogs for "de" fail to load, so i18next falls back to English for that selection.
async function loadI18n() {
	vi.resetModules();
	vi.stubGlobal("document", { documentElement });
	vi.doMock("./resources", async (importOriginal) => {
		const actual = await importOriginal<typeof import("./resources")>();
		return {
			...actual,
			loadCatalog: (lang: string, ns: string) => (lang === "de" ? Promise.reject(new Error("offline")) : actual.loadCatalog(lang, ns)),
		};
	});
	return import("./index");
}

beforeEach(() => {
	documentElement.lang = "";
});

afterEach(() => {
	vi.doUnmock("./resources");
	vi.unstubAllGlobals();
	vi.resetModules();
});

describe("document language", () => {
	it("follows the active language", async () => {
		const { changeLocale, i18nReady } = await loadI18n();
		await i18nReady;
		await changeLocale("ja");
		expect(documentElement.lang).toBe("ja");
	});

	it("stays on the displayed language when the requested catalogs fail to load", async () => {
		const { changeLocale, i18nReady } = await loadI18n();
		await i18nReady;
		await changeLocale("ja");
		await changeLocale("de");
		expect(documentElement.lang).toBe("en");
	});
});