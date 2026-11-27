import { describe, expect, it } from "vitest";

import { isReasonedErrorBody, textOrJSONError } from "./responseHandlers";

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

describe("isReasonedErrorBody", () => {
	it("recognizes a typed unavailable answer, so its message is kept for the page", () => {
		expect(
			isReasonedErrorBody({ reason: "no_vector_store", message: 'Warp\'s vector store "warp-qdrant" could not be reached at startup' }),
		).toBe(true);
	});
	it("leaves everything else to the usual handling", () => {
		expect(isReasonedErrorBody({ error: { message: "bad request" } })).toBe(false);
		expect(isReasonedErrorBody({ reason: "no_vector_store" })).toBe(false);
		expect(isReasonedErrorBody({ reason: "", message: "x" })).toBe(false);
		expect(isReasonedErrorBody({ reason: 1, message: "x" })).toBe(false);
		expect(isReasonedErrorBody("Service Unavailable")).toBe(false);
		expect(isReasonedErrorBody(null)).toBe(false);
	});
});