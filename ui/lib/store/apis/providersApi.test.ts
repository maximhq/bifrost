import { configureStore } from "@reduxjs/toolkit";
import { afterEach, describe, expect, it, vi } from "vitest";

import { baseApi } from "./baseApi";
import { providersApi } from "./providersApi";

// The API base is relative in a browser; a request built outside one needs a host.
vi.mock("@/lib/utils/port", async (importOriginal) => ({
	...(await importOriginal<typeof import("@/lib/utils/port")>()),
	getApiBaseUrl: () => "http://gateway.test/api",
}));

function setup() {
	const requested: string[] = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: Request) => {
			requested.push(`${input.method} ${input.url}`);
			const isListing = new URL(input.url).pathname.endsWith("/models");
			const body = isListing ? { models: [], total: 0 } : { id: "key-1", name: "Key", models: ["*"] };
			return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
		}),
	);
	const store = configureStore({
		reducer: { [baseApi.reducerPath]: baseApi.reducer },
		middleware: (getDefault) => getDefault({ serializableCheck: false }).concat(baseApi.middleware),
	});
	return { store, listings: () => requested.filter((r) => r.startsWith("GET") && r.includes("/models?")) };
}

describe("getModels", () => {
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("asks for aliases only when told to", async () => {
		const { store, listings } = setup();
		await store.dispatch(providersApi.endpoints.getModels.initiate({ provider: "openai", unfiltered: true }));
		await store.dispatch(providersApi.endpoints.getModels.initiate({ provider: "openai", unfiltered: true, includeAliases: true }));
		expect(listings().map((l) => l.includes("include_aliases=true"))).toEqual([false, true]);
	});

	it.each([
		["creating", () => providersApi.endpoints.createProviderKey.initiate({ provider: "openai", key: {} as never })],
		["updating", () => providersApi.endpoints.updateProviderKey.initiate({ provider: "openai", keyId: "key-1", key: {} as never })],
		["deleting", () => providersApi.endpoints.deleteProviderKey.initiate({ provider: "openai", keyId: "key-1" })],
	])("refetches listings after %s a key, whose aliases they carry", async (_, mutation) => {
		const { store, listings } = setup();
		await store.dispatch(providersApi.endpoints.getModels.initiate({ provider: "openai", unfiltered: true, includeAliases: true }));
		expect(listings()).toHaveLength(1);

		await store.dispatch(mutation() as ReturnType<typeof providersApi.endpoints.deleteProviderKey.initiate>);
		await vi.waitFor(() => expect(listings()).toHaveLength(2));
	});
});