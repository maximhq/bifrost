import fs from "node:fs";
import path from "node:path";
import { createInstance } from "i18next";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ENTERPRISE_NAMESPACES, enterpriseResources } from "../../app/_fallbacks/enterprise/lib/i18n/resources";

const OSS_NAMESPACES = ["common", "shell", "login", "observability", "models", "mcp", "governance", "config"];
const LOCALES_DIR = path.resolve(__dirname, "../../locales");
const localeDirs = fs
	.readdirSync(LOCALES_DIR, { withFileTypes: true })
	.filter((entry) => entry.isDirectory())
	.map((entry) => entry.name)
	.sort();

// The enterprise repo supplies this file in real builds; here it is a stand-in with one namespace.
const agentCatalog = {
	en: { agent: { title: "Agent sign-in" } },
	ja: { agent: { title: "エージェントのサインイン" } },
	de: { agent: { title: "Agent-Anmeldung" } },
};

// Stand-in namespaces are not part of the typed catalog, so look the key up through an untyped t().
const translate = (instance: ReturnType<typeof createInstance>) =>
	(instance.t as unknown as (key: string, options: object) => string)("title", { ns: "agent" });

async function loadResourcesWith(enterprise: { namespaces: readonly string[]; byLocale: Record<string, object> }) {
	vi.resetModules();
	vi.doMock("@enterprise/lib/i18n/resources", () => ({
		ENTERPRISE_NAMESPACES: enterprise.namespaces,
		enterpriseResources: Object.fromEntries(localeDirs.map((lang) => [lang, enterprise.byLocale[lang] ?? {}])),
	}));
	return import("./resources");
}

afterEach(() => {
	vi.doUnmock("@enterprise/lib/i18n/resources");
	vi.resetModules();
});

describe("OSS enterprise fallback", () => {
	it("registers no namespaces", () => {
		expect(ENTERPRISE_NAMESPACES).toEqual([]);
	});

	it("has an empty entry for every locale directory", () => {
		expect(Object.keys(enterpriseResources).sort()).toEqual(localeDirs);
		for (const lang of localeDirs) expect(enterpriseResources[lang as keyof typeof enterpriseResources]).toEqual({});
	});

	it("leaves the OSS namespaces untouched", async () => {
		const { NAMESPACES, resources } = await loadResourcesWith({ namespaces: [], byLocale: {} });
		expect([...NAMESPACES]).toEqual(OSS_NAMESPACES);
		for (const lang of localeDirs)
			expect(Object.keys(resources[lang as keyof typeof resources]).sort()).toEqual([...OSS_NAMESPACES].sort());
	});
});

describe("enterprise catalogs", () => {
	it("appends enterprise namespaces after the OSS ones", async () => {
		const { NAMESPACES } = await loadResourcesWith({ namespaces: ["agent"], byLocale: agentCatalog });
		expect([...NAMESPACES]).toEqual([...OSS_NAMESPACES, "agent"]);
	});

	it("merges enterprise namespaces into each locale without dropping OSS ones", async () => {
		const { resources } = await loadResourcesWith({ namespaces: ["agent"], byLocale: agentCatalog });
		for (const lang of ["en", "ja", "de"] as const) {
			expect(Object.keys(resources[lang])).toEqual(expect.arrayContaining([...OSS_NAMESPACES, "agent"]));
		}
		expect(Object.keys(resources.ru)).not.toContain("agent");
	});

	it("serves the translated string after a language switch and falls back to English", async () => {
		const { NAMESPACES, resources } = await loadResourcesWith({ namespaces: ["agent"], byLocale: agentCatalog });
		const instance = createInstance();
		await instance.init({ resources, ns: [...NAMESPACES], defaultNS: "common", nsSeparator: false, fallbackLng: "en", lng: "en" });
		expect(translate(instance)).toBe("Agent sign-in");

		await instance.changeLanguage("ja");
		expect(translate(instance)).toBe("エージェントのサインイン");

		await instance.changeLanguage("ru");
		expect(translate(instance)).toBe("Agent sign-in");
	});
});