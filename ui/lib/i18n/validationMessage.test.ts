import { describe, expect, it } from "vitest";
import { z } from "zod";

import i18n from "@/lib/i18n";
import { msg } from "@/lib/i18n/validationMessage";
import { loadCatalog } from "@/lib/i18n/resources";

describe("msg", () => {
	it("resolves the message in the language active when validation runs", async () => {
		const schema = z.string().min(1, msg("validation.pleaseSelectAProvider"));
		await i18n.changeLanguage("en");
		expect(schema.safeParse("").error?.issues[0].message).toBe("Please select a provider");

		i18n.addResourceBundle("ja", "common", await loadCatalog("ja", "common"));
		await i18n.changeLanguage("ja");
		expect(schema.safeParse("").error?.issues[0].message).toBe("プロバイダーを選択してください");
		await i18n.changeLanguage("en");
	});
});