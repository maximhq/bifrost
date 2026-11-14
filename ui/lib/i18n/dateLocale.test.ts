import { de, pt, ptBR, zhCN, zhTW } from "date-fns/locale";
import { describe, expect, it } from "vitest";

import { dateFnsLocale } from "./dateLocale";

describe("dateFnsLocale", () => {
	it("keeps the date-fns default for English and no language", () => {
		expect(dateFnsLocale("en")).toBeUndefined();
		expect(dateFnsLocale(null)).toBeUndefined();
		expect(dateFnsLocale(undefined)).toBeUndefined();
	});

	it("uses the Brazilian locale for pt-BR, not the European one", () => {
		expect(dateFnsLocale("pt-BR")).toBe(ptBR);
		expect(dateFnsLocale("pt")).toBe(pt);
	});

	it("resolves regional tags to their base language", () => {
		expect(dateFnsLocale("pt-PT")).toBe(pt);
		expect(dateFnsLocale("de-AT")).toBe(de);
		expect(dateFnsLocale("zh-Hant-HK")).toBe(zhTW);
		expect(dateFnsLocale("zh-SG")).toBe(zhCN);
	});
});