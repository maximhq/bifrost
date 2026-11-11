import { describe, expect, it } from "vitest";

import { textOrJSONError } from "./responseHandlers";

describe("textOrJSONError", () => {
	it("reads a successful response as text", async () => {
		expect(await textOrJSONError(new Response("openapi: 3.0.3", { status: 200 }))).toBe("openapi: 3.0.3");
	});
	it("parses a JSON error body, so the server's message reaches the caller", async () => {
		const body = { error: { message: "This client has no stored spec" } };
		const res = new Response(JSON.stringify(body), { status: 404, headers: { "Content-Type": "application/json" } });
		expect(await textOrJSONError(res)).toEqual(body);
	});
	it("falls back to the raw text for a non-JSON error body", async () => {
		expect(await textOrJSONError(new Response("bad gateway", { status: 502 }))).toBe("bad gateway");
	});
});