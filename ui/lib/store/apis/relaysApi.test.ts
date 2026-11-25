import { configureStore } from "@reduxjs/toolkit";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { baseApi } from "./baseApi";
import { relaysApi } from "./relaysApi";

// The API base is relative in a browser; a request built outside one needs a host.
vi.mock("@/lib/utils/port", async (importOriginal) => ({
	...(await importOriginal<typeof import("@/lib/utils/port")>()),
	getApiBaseUrl: () => "http://gateway.test/api",
}));

describe("relay reads", () => {
	let reply: string;
	const newStore = () =>
		configureStore({
			reducer: { [baseApi.reducerPath]: baseApi.reducer },
			middleware: (getDefaultMiddleware) => getDefaultMiddleware().concat(baseApi.middleware),
		});

	beforeEach(() => {
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => new Response(reply, { status: 200, headers: { "Content-Type": "application/json" } })),
		);
	});
	afterEach(() => vi.unstubAllGlobals());

	test("a refetch that changes a whole number past 2^53 keeps its exact digits", async () => {
		const store = newStore();
		reply = `{"name":"r","body_overrides":{"seed":9007199254740993}}`;
		await store.dispatch(relaysApi.endpoints.getRelay.initiate("r"));
		reply = `{"name":"r","body_overrides":{"seed":9007199254740995}}`;
		const second = await store.dispatch(relaysApi.endpoints.getRelay.initiate("r", { forceRefetch: true }));
		expect(JSON.stringify(second.data?.body_overrides)).toBe(`{"seed":9007199254740995}`);
	});
});