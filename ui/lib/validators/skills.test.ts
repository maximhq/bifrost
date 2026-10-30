import i18n from "@/lib/i18n";
import { beforeAll, describe, expect, it } from "vitest";

import { validateDescription } from "./skills";

beforeAll(async () => {
	await i18n.changeLanguage("en");
});

describe("validateDescription", () => {
	it("reports the actual length of a description over the limit", () => {
		expect(validateDescription("a".repeat(1100))).toBe("Description exceeds 1024 character limit (1100/1024)");
	});

	it("accepts a description at the limit", () => {
		expect(validateDescription("a".repeat(1024))).toBeNull();
	});
});
