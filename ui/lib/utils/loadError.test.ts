import { configureStore } from "@reduxjs/toolkit";
import { createApi, fakeBaseQuery } from "@reduxjs/toolkit/query";
import { describe, expect, it } from "vitest";
import { showLoadError } from "./loadError";

describe("showLoadError", () => {
	it("shows the load error when the query failed and there is no cached data", () => {
		expect(showLoadError({ status: 500 }, undefined)).toBe(true);
		expect(showLoadError({ status: 500 }, null)).toBe(true);
	});

	it("keeps cached data when a later fetch fails", () => {
		expect(showLoadError({ status: 500 }, [{ name: "openai" }])).toBe(false);
		expect(showLoadError({ status: 500 }, { total_count: 0, rules: [] })).toBe(false);
	});

	it("keeps the empty state when the query succeeded with nothing", () => {
		expect(showLoadError(undefined, [])).toBe(false);
		expect(showLoadError(undefined, undefined)).toBe(false);
	});

	it("keeps the load error while a refetch after the failure is in flight", async () => {
		let attempt = 0;
		let finishRetry: (result: { data: string[] }) => void = () => {};
		const api = createApi({
			baseQuery: fakeBaseQuery<{ status: number }>(),
			endpoints: (build) => ({
				list: build.query<string[], void>({
					queryFn: () => {
						attempt++;
						if (attempt === 1) return { error: { status: 500 } };
						return new Promise<{ data: string[] }>((resolve) => {
							finishRetry = resolve;
						});
					},
				}),
			}),
		});
		const store = configureStore({
			reducer: { [api.reducerPath]: api.reducer },
			middleware: (getDefaultMiddleware) => getDefaultMiddleware().concat(api.middleware),
		});
		const select = api.endpoints.list.select();

		const first = store.dispatch(api.endpoints.list.initiate());
		await first;
		const retry = store.dispatch(api.endpoints.list.initiate(undefined, { forceRefetch: true }));

		const pending = select(store.getState());
		expect(pending.isError).toBe(false);
		expect(showLoadError(pending.error, pending.data)).toBe(true);

		finishRetry({ data: [] });
		await retry;
		const done = select(store.getState());
		expect(showLoadError(done.error, done.data)).toBe(false);

		first.unsubscribe();
		retry.unsubscribe();
	});
});