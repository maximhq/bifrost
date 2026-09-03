import { describe, expect, it } from "vitest";
import { interpolate, pluralKey, translate } from "./translate";
import { translatePageTitle } from "./titleCatalog";

describe("interpolate", () => {
	it("replaces named placeholders", () => {
		expect(interpolate("Hello, {{name}}", { name: "Bifrost" })).toBe("Hello, Bifrost");
	});

	it("leaves unknown placeholders intact", () => {
		expect(interpolate("Hello, {{name}}", {})).toBe("Hello, {{name}}");
	});
});

describe("translate", () => {
	it("returns English copy", () => {
		expect(translate("en", "nav.dashboard")).toBe("Dashboard");
	});

	it("returns Chinese copy", () => {
		expect(translate("zh", "nav.dashboard")).toBe("仪表盘");
	});

	it("falls back to the key when missing", () => {
		expect(translate("en", "does.not.exist" as "nav.dashboard")).toBe("does.not.exist");
	});

	it("interpolates params", () => {
		expect(translate("en", "noPermission.title", { entity: "logs" })).toBe("You don't have permission to view logs");
		expect(translate("zh", "noPermission.title", { entity: "日志" })).toBe("你没有查看日志的权限");
	});
});

describe("pluralKey", () => {
	it("picks _one and _other", () => {
		expect(pluralKey("promo.setupStepsLeft" as never, 1)).toBe("promo.setupStepsLeft_one");
		expect(pluralKey("promo.setupStepsLeft" as never, 3)).toBe("promo.setupStepsLeft_other");
	});
});

describe("translatePageTitle", () => {
	it("maps known English titles", () => {
		expect(translatePageTitle("LLM Logs", (key) => translate("zh", key))).toBe("LLM 日志");
	});

	it("leaves unknown titles unchanged", () => {
		expect(translatePageTitle("Custom Page", (key) => translate("zh", key))).toBe("Custom Page");
	});
});