import { describe, expect, it } from "vitest";

import { genericRouteFormSchema, genericUsagePreviewSampleSchema } from "./schemas";

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
describe("genericUsagePreviewSampleSchema path", () => {
	const check = (path: string) => genericUsagePreviewSampleSchema.safeParse({ method: "POST", path, status_code: 200 });
	it("accepts the paths the relay accepts", () => {
		for (const path of [
			"/",
			"/search",
			"/files/a%20b.json",
			"/v1/chat?stream=true",
			"/search?url=https%3A%2F%2Fexample.com",
			"/x?u=http://evil",
		]) {
			expect(check(path).success, path).toBe(true);
		}
	});
	it("refuses inline the paths the relay refuses with 400, decoded variants included", () => {
		for (const [path, message] of [
			["search", "must start with /"],
			["//host/x", "must not start with //"],
			["/a\\b", "backslashes"],
			["/x://evil", "scheme"],
			["/%2F%2Fhost", "must not start with //"],
			["/a%5Cb", "backslashes"],
			["/bad%zz", "percent-escape"],
			["/tab\there", "control characters"],
		] as const) {
			const result = check(path);
			expect(result.success, path).toBe(false);
			expect(JSON.stringify(result.error?.issues), path).toContain(message);
		}
	});
});