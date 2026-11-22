import { afterEach, describe, expect, it, vi } from "vitest";

import { boundedWait } from "./boundedWait";

afterEach(() => {
	vi.useRealTimers();
});

describe("boundedWait", () => {
	it("settles when the promise settles first", async () => {
		await expect(boundedWait(Promise.resolve("done"), 1000)).resolves.toBeUndefined();
	});

	it("settles after the timeout when the promise stays pending", async () => {
		vi.useFakeTimers();
		let settled = false;
		const wait = boundedWait(new Promise(() => {}), 3000).then(() => {
			settled = true;
		});
		await vi.advanceTimersByTimeAsync(2999);
		expect(settled).toBe(false);
		await vi.advanceTimersByTimeAsync(1);
		await wait;
		expect(settled).toBe(true);
	});

	it("settles when the promise rejects", async () => {
		await expect(boundedWait(Promise.reject(new Error("offline")), 1000)).resolves.toBeUndefined();
	});

	it("clears the timer once the promise settles", async () => {
		vi.useFakeTimers();
		await boundedWait(Promise.resolve(), 3000);
		expect(vi.getTimerCount()).toBe(0);
	});
});