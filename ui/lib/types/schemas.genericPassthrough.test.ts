import { describe, expect, it } from "vitest";

import { genericRouteFormSchema } from "./schemas";

describe("generic route header matchers", () => {
	const route = (headers: { name: string; value: string }[]) => ({ method: "POST" as const, path: "/", headers });

	it("flags a header name repeated on one route, ignoring case", () => {
		const result = genericRouteFormSchema.safeParse(
			route([
				{ name: "X-Amz-Target", value: "Textract.Get*" },
				{ name: "x-amz-target", value: "Textract.Start*" },
			]),
		);
		expect(result.success).toBe(false);
		expect(result.error?.issues[0]?.path).toEqual(["headers", 1, "name"]);
	});

	it("accepts distinct names and ignores blank rows", () => {
		const result = genericRouteFormSchema.safeParse(
			route([
				{ name: "X-Amz-Target", value: "Textract.Get*" },
				{ name: "", value: "" },
				{ name: "X-Api-Version", value: "2" },
			]),
		);
		expect(result.success, JSON.stringify(result.error?.issues)).toBe(true);
	});
});