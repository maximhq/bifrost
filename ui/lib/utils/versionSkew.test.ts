import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	clearAutoReloadGuard,
	consumeAutoReload,
	getSkewMode,
	isSkewError,
	MAX_AUTO_RELOADS,
	POLL_INTERVAL_MS,
	POLL_TIMEOUT_MS,
	reportSkew,
	RELOAD_WINDOW_MS,
	STABLE_POLLS_REQUIRED,
	startVersionPoll,
	subscribeSkew,
	__resetSkewForTests,
} from "./versionSkew";

function installSessionStorage() {
	const store = new Map<string, string>();
	vi.stubGlobal("sessionStorage", {
		getItem: (k: string) => store.get(k) ?? null,
		setItem: (k: string, v: string) => void store.set(k, v),
		removeItem: (k: string) => void store.delete(k),
	});
	return store;
}

describe("isSkewError", () => {
	it("detects failed dynamic imports across browsers", () => {
		expect(isSkewError(new TypeError("Failed to fetch dynamically imported module: https://x/assets/logs-A1.js"))).toBe(true);
		expect(isSkewError(new TypeError("error loading dynamically imported module"))).toBe(true);
		expect(isSkewError(new TypeError("Importing a module script failed."))).toBe(true);
	});

	it("detects bundler chunk failures by message and by name", () => {
		expect(isSkewError(new Error("Loading chunk 42 failed."))).toBe(true);
		expect(isSkewError(new Error("Loading CSS chunk 7 failed."))).toBe(true);
		expect(isSkewError(Object.assign(new Error("boom"), { name: "ChunkLoadError" }))).toBe(true);
	});

	it("accepts bare strings", () => {
		expect(isSkewError("Failed to fetch dynamically imported module")).toBe(true);
	});

	it("does not misclassify ordinary application errors", () => {
		expect(isSkewError(new TypeError("x is not a function"))).toBe(false);
		expect(isSkewError(new Error("Request failed with status 500"))).toBe(false);
		expect(isSkewError(undefined)).toBe(false);
		expect(isSkewError(null)).toBe(false);
		expect(isSkewError("")).toBe(false);
	});
});

describe("skew store", () => {
	beforeEach(() => __resetSkewForTests());

	it("notifies subscribers once per level change", () => {
		const onChange = vi.fn();
		subscribeSkew(onChange);

		expect(getSkewMode()).toBe("none");
		reportSkew("soft");
		expect(getSkewMode()).toBe("soft");
		reportSkew("soft");

		expect(onChange).toHaveBeenCalledTimes(1);
	});

	it("escalates soft to hard when a route boundary trips", () => {
		reportSkew("soft");
		reportSkew("hard");
		expect(getSkewMode()).toBe("hard");
	});

	it("never downgrades hard back to soft", () => {
		reportSkew("hard");
		reportSkew("soft");
		expect(getSkewMode()).toBe("hard");
	});

	it("defaults to the non-destructive level", () => {
		reportSkew();
		expect(getSkewMode()).toBe("soft");
	});

	it("stops notifying after unsubscribe", () => {
		const onChange = vi.fn();
		subscribeSkew(onChange)();
		reportSkew("soft");
		expect(onChange).not.toHaveBeenCalled();
	});
});

describe("auto-reload guard", () => {
	beforeEach(() => {
		installSessionStorage();
	});

	it("allows exactly MAX_AUTO_RELOADS inside the window", () => {
		const t0 = 1_000_000;
		expect(consumeAutoReload(t0)).toBe(true);
		expect(consumeAutoReload(t0 + 1_000)).toBe(true);
		expect(MAX_AUTO_RELOADS).toBe(2);
		expect(consumeAutoReload(t0 + 2_000)).toBe(false);
	});

	it("starts a fresh budget once the window ages out", () => {
		const t0 = 1_000_000;
		consumeAutoReload(t0);
		consumeAutoReload(t0);
		expect(consumeAutoReload(t0)).toBe(false);

		expect(consumeAutoReload(t0 + RELOAD_WINDOW_MS + 1)).toBe(true);
	});

	it("resets when the guard is cleared after a healthy boot", () => {
		const t0 = 1_000_000;
		consumeAutoReload(t0);
		consumeAutoReload(t0);
		expect(consumeAutoReload(t0)).toBe(false);

		clearAutoReloadGuard();
		expect(consumeAutoReload(t0)).toBe(true);
	});

	it("degrades safely when sessionStorage is unavailable", () => {
		vi.stubGlobal("sessionStorage", {
			getItem: () => {
				throw new Error("denied");
			},
			setItem: () => {
				throw new Error("denied");
			},
			removeItem: () => {
				throw new Error("denied");
			},
		});

		// An unpersistable count would reset on every boot, so reloading is denied instead of looping forever.
		expect(() => consumeAutoReload()).not.toThrow();
		expect(consumeAutoReload()).toBe(false);
		expect(() => clearAutoReloadGuard()).not.toThrow();
	});

	it("denies reloads when the count cannot be persisted", () => {
		const store = installSessionStorage();
		const t0 = 1_000_000;
		expect(consumeAutoReload(t0)).toBe(true);

		vi.stubGlobal("sessionStorage", {
			getItem: (k: string) => store.get(k) ?? null,
			setItem: () => {
				throw new Error("quota exceeded");
			},
			removeItem: (k: string) => void store.delete(k),
		});

		// The budget cannot advance, so no further reload is authorized regardless of how often this is called.
		expect(consumeAutoReload(t0 + 1_000)).toBe(false);
		expect(consumeAutoReload(t0 + 2_000)).toBe(false);
	});
});

describe("startVersionPoll", () => {
	beforeEach(() => vi.useFakeTimers());
	afterEach(() => vi.useRealTimers());

	const okResponse = (version: string) => ({ ok: true, text: async () => version }) as unknown as Response;

	it("reloads once the reported version stays stable", async () => {
		const onUpdateReady = vi.fn();
		const fetchVersion = vi.fn(async () => okResponse("v2"));

		const stop = startVersionPoll({ fetchVersion, onUpdateReady, onTimeout: vi.fn() });
		await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS * STABLE_POLLS_REQUIRED);

		expect(fetchVersion).toHaveBeenCalledTimes(STABLE_POLLS_REQUIRED);
		expect(onUpdateReady).toHaveBeenCalledTimes(1);

		// The loop stopped, so no further requests go out.
		await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS * 3);
		expect(fetchVersion).toHaveBeenCalledTimes(STABLE_POLLS_REQUIRED);
		stop();
	});

	it("times out a request that never settles instead of stalling forever", async () => {
		const onTimeout = vi.fn();
		let signal: AbortSignal | undefined;
		const fetchVersion = vi.fn(
			(s: AbortSignal) =>
				new Promise<Response>((_, reject) => {
					signal = s;
					s.addEventListener("abort", () => reject(new Error("aborted")));
				}),
		);

		const stop = startVersionPoll({ fetchVersion, onUpdateReady: vi.fn(), onTimeout });
		await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS);
		expect(onTimeout).not.toHaveBeenCalled();

		await vi.advanceTimersByTimeAsync(POLL_TIMEOUT_MS);
		expect(signal?.aborted).toBe(true);
		expect(onTimeout).toHaveBeenCalledTimes(1);

		// The poll is done: no retry is scheduled behind the timeout.
		await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS * 3);
		expect(fetchVersion).toHaveBeenCalledTimes(1);
		expect(onTimeout).toHaveBeenCalledTimes(1);
		stop();
	});

	it("retries after a failed request until the budget runs out", async () => {
		const onTimeout = vi.fn();
		const fetchVersion = vi.fn(async () => {
			throw new Error("network down");
		});

		const stop = startVersionPoll({ fetchVersion, onUpdateReady: vi.fn(), onTimeout });
		await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS * 3);
		expect(fetchVersion).toHaveBeenCalledTimes(3);
		expect(onTimeout).not.toHaveBeenCalled();

		await vi.advanceTimersByTimeAsync(POLL_TIMEOUT_MS);
		expect(onTimeout).toHaveBeenCalledTimes(1);
		stop();
	});

	it("aborts the in-flight request and stops polling when cancelled", async () => {
		const onTimeout = vi.fn();
		const onUpdateReady = vi.fn();
		let signal: AbortSignal | undefined;
		const fetchVersion = vi.fn(
			(s: AbortSignal) =>
				new Promise<Response>((_, reject) => {
					signal = s;
					s.addEventListener("abort", () => reject(new Error("aborted")));
				}),
		);

		const stop = startVersionPoll({ fetchVersion, onUpdateReady, onTimeout });
		await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS);
		stop();

		expect(signal?.aborted).toBe(true);
		expect(vi.getTimerCount()).toBe(0);

		await vi.advanceTimersByTimeAsync(POLL_TIMEOUT_MS * 2);
		expect(fetchVersion).toHaveBeenCalledTimes(1);
		expect(onTimeout).not.toHaveBeenCalled();
		expect(onUpdateReady).not.toHaveBeenCalled();
	});
});