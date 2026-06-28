import { describe, expect, test } from "vitest";
import { parseAsSafeArrayOf, parseAsSafeString } from "./queryParamsParser";

describe("parseAsSafeString", () => {
	test("roundtrips model names with double slashes", () => {
		const model = "gpt://b1g9n2rqsrnikgh0uofm/gpt-oss-120b";
		const serialized = parseAsSafeString.serialize(model);
		expect(serialized).not.toContain("://");
		expect(parseAsSafeString.parse(serialized)).toBe(model);
	});
});

describe("parseAsSafeArrayOf", () => {
	test("roundtrips multiple models with special characters", () => {
		const models = ["gpt://b1g9n2rqsrnikgh0uofm/gpt-oss-20b", "gpt://b1g9n2rqsrnikgh0uofm/gpt-oss-120b"];
		const serialized = parseAsSafeArrayOf.serialize(models);
		expect(serialized).not.toContain("://");
		expect(parseAsSafeArrayOf.parse(serialized)).toEqual(models);
	});
});