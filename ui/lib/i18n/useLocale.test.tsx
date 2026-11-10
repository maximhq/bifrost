import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import i18n, { changeLocale } from "./index";
import { loadCatalog } from "./resources";
import { useLocale } from "./useLocale";

function Probe() {
	return createElement("span", null, useLocale());
}

describe("useLocale", () => {
	it("returns the active language", async () => {
		i18n.addResourceBundle("ja", "common", await loadCatalog("ja", "common"));
		await changeLocale("ja");
		expect(renderToStaticMarkup(createElement(Probe))).toBe("<span>ja</span>");
		await changeLocale("en");
		expect(renderToStaticMarkup(createElement(Probe))).toBe("<span>en</span>");
	});
});