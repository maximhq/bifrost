import { describe, expect, it } from "vitest";

import { exactJSON, textOrJSONError } from "./responseHandlers";

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

describe("exactJSON", () => {
	it("keeps a whole number past 2^53 exact", async () => {
		const out = await exactJSON(new Response(`{"seed":9007199254740993}`, { status: 200 }));
		expect(JSON.stringify(out)).toBe(`{"seed":9007199254740993}`);
	});
	it("leaves a successful reply that is not JSON as a parsing error", async () => {
		await expect(exactJSON(new Response("<html>proxy</html>", { status: 200 }))).rejects.toThrow();
	});
	it("keeps a non-JSON error body as text", async () => {
		expect(await exactJSON(new Response("bad gateway", { status: 502 }))).toBe("bad gateway");
	});
});