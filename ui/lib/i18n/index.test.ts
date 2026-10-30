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
	vi.restoreAllMocks();
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

describe("changeLocale storage", () => {
	it("keeps the language switch successful when localStorage persistence fails", async () => {
		const setItem = vi.fn();
		const storage = { setItem, getItem: vi.fn(() => null), removeItem: vi.fn() };
		vi.stubGlobal("window", { localStorage: storage });
		vi.stubGlobal("localStorage", storage);
		const { changeLocale, getLocale, i18nReady, LOCALE_STORAGE_KEY } = await loadI18n();
		await i18nReady;
		setItem.mockImplementation(() => {
			throw new DOMException("Storage is full", "QuotaExceededError");
		});

		await expect(changeLocale("zh-CN")).resolves.toBe("zh-CN");
		expect(getLocale()).toBe("zh-CN");
		expect(setItem).toHaveBeenCalledWith(LOCALE_STORAGE_KEY, "zh-CN");
	});

	it("persists only the latest selection when catalog loads finish out of order", async () => {
		const setItem = vi.fn();
		const storage = { setItem, getItem: vi.fn(() => null), removeItem: vi.fn() };
		vi.stubGlobal("window", { localStorage: storage });
		vi.stubGlobal("localStorage", storage);
		const { changeLocale, default: i18n, i18nReady, LOCALE_STORAGE_KEY } = await loadI18n();
		await i18nReady;
		const resolvers = new Map<string, () => void>();
		vi.spyOn(i18n, "changeLanguage").mockImplementation((locale) => {
			return new Promise<void>((resolve) => resolvers.set(locale, resolve)).then(() => i18n.t);
		});

		const japanese = changeLocale("ja");
		const french = changeLocale("fr");
		resolvers.get("fr")!();
		await french;
		resolvers.get("ja")!();
		await japanese;

		expect(setItem).toHaveBeenLastCalledWith(LOCALE_STORAGE_KEY, "fr");
	});
});