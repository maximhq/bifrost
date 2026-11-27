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

describe("Portuguese variants", () => {
	it("offers pt-BR and pt as separate, distinguishable languages", async () => {
		const { SUPPORTED_LOCALES } = await loadI18n();
		const labels = SUPPORTED_LOCALES.filter((locale) => locale.code.startsWith("pt")).map((locale) => [locale.code, locale.label]);
		expect(labels).toEqual([
			["pt-BR", "Português (Brasil)"],
			["pt", "Português (Portugal)"],
		]);
	});

	it.each([
		[["pt-BR"], "pt-BR"],
		[["pt-PT"], "pt"],
		[["pt"], "pt"],
	])("resolves a browser asking for %j to %s", async (languages, expected) => {
		vi.stubGlobal("navigator", { languages, language: languages[0] });
		const { getLocale, i18nReady, default: i18n } = await loadI18n();
		await i18nReady;
		// vi.resetModules() keeps the i18next singleton, so detection has to be re-run for this navigator.
		await i18n.changeLanguage();
		expect(getLocale()).toBe(expected);
		expect(documentElement.lang).toBe(expected);
	});

	it("serves pt-BR from its own catalog, not the European one or English", async () => {
		const { changeLocale, i18nReady, default: i18n } = await loadI18n();
		await i18nReady;
		const label = () => i18n.t("nav.modelProviders", { ns: "shell" });
		await changeLocale("pt");
		const european = label();
		await changeLocale("pt-BR");
		expect(i18n.resolvedLanguage).toBe("pt-BR");
		expect(label()).not.toBe(european);
	});

	// CLDR puts 0 in the `one` category for pt-BR, so these read "0 ferramenta" without a `_zero` form.
	it("uses the _zero form for a count of zero in pt-BR and keeps the singular for one", async () => {
		const { changeLocale, i18nReady, default: i18n } = await loadI18n();
		await i18nReady;
		await changeLocale("pt-BR");
		expect(i18n.t("trialBanner.expiresIn", { ns: "common", count: 0 })).toContain("expira em 0 dias.");
		expect(i18n.t("trialBanner.expiresIn", { ns: "common", count: 1 })).toContain("expira em 1 dia.");
		expect(i18n.t("multiSelect.announce.found", { ns: "common", count: 0, query: "x" })).toBe('Nenhuma opção encontrada para "x"');
		expect(i18n.t("multiSelect.announce.found", { ns: "common", count: 1, query: "x" })).toBe('1 opção encontrada para "x"');
		expect(i18n.t("openapi.preview.toolCount", { ns: "mcp", count: 0 })).toBe("0 ferramentas");
		expect(i18n.t("openapi.preview.toolCount", { ns: "mcp", count: 1 })).toBe("1 ferramenta");
		expect(i18n.t("relays.openapiImport.routeCount", { ns: "config", count: 0 })).toBe("0 rotas");
		expect(i18n.t("relays.openapiImport.routeCount", { ns: "config", count: 1 })).toBe("1 rota");
		expect(i18n.t("relays.openapiImport.importCount", { ns: "config", count: 0 })).toBe("Importar 0 rotas");
		expect(i18n.t("relays.openapiImport.importCount", { ns: "config", count: 1 })).toBe("Importar 1 rota");
	});
});