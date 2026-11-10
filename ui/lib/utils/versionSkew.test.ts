import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	classifyLoadFailure,
	clearAutoReloadGuard,
	consumeAutoReload,
	extractAssetUrl,
	getSkewMode,
	getSkewState,
	isSkewError,
	MAX_AUTO_RELOADS,
	MISSING_ASSET_BODY_PREFIX,
	PROBE_TIMEOUT_MS,
	resolveProbeUrl,
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

	it("detects Vite CSS preload failures", () => {
		expect(isSkewError(new Error("Unable to preload CSS for /assets/logs-A1.css"))).toBe(true);
	});

	it("does not misclassify ordinary application errors", () => {
		expect(isSkewError(new TypeError("x is not a function"))).toBe(false);
		expect(isSkewError(new Error("Request failed with status 500"))).toBe(false);
		expect(isSkewError(undefined)).toBe(false);
		expect(isSkewError(null)).toBe(false);
		expect(isSkewError("")).toBe(false);
	});
});

const ORIGIN = "https://bifrost.test";

function installWindow() {
	vi.stubGlobal("window", { location: { href: `${ORIGIN}/workspace/logs`, origin: ORIGIN } });
}

function stubFetch(impl: (url: string) => Promise<Response>) {
	const fetchMock = vi.fn((url: string) => impl(url));
	vi.stubGlobal("fetch", fetchMock);
	return fetchMock;
}

const textResponse = (status: number, body: string) => ({ status, ok: status < 400, text: async () => body }) as unknown as Response;
const bifrostMissing = (path: string) => textResponse(404, `${MISSING_ASSET_BODY_PREFIX}: ${path}`);

describe("extractAssetUrl", () => {
	it("pulls the chunk URL out of Chromium and Firefox import errors", () => {
		expect(extractAssetUrl(new TypeError("Failed to fetch dynamically imported module: https://x/assets/logs-A1.js"))).toBe(
			"https://x/assets/logs-A1.js",
		);
		expect(extractAssetUrl(new TypeError("error loading dynamically imported module: https://x/bifrost/assets/a-B2.js"))).toBe(
			"https://x/bifrost/assets/a-B2.js",
		);
	});

	it("pulls a root-relative path out of a Vite CSS preload error", () => {
		expect(extractAssetUrl(new Error("Unable to preload CSS for /assets/logs-A1.css"))).toBe("/assets/logs-A1.css");
	});

	it("returns undefined when the browser does not name the chunk", () => {
		expect(extractAssetUrl(new TypeError("Importing a module script failed."))).toBeUndefined();
		expect(extractAssetUrl(undefined)).toBeUndefined();
	});
});

describe("resolveProbeUrl", () => {
	beforeEach(installWindow);
	afterEach(() => vi.unstubAllGlobals());

	it("prefers the failed same-origin chunk", () => {
		expect(resolveProbeUrl("/assets/a-1.js", `${ORIGIN}/assets/index-9.js`)).toBe(`${ORIGIN}/assets/a-1.js`);
	});

	it("falls back to the running chunk when the failed URL is unknown or foreign", () => {
		expect(resolveProbeUrl(undefined, `${ORIGIN}/assets/index-9.js`)).toBe(`${ORIGIN}/assets/index-9.js`);
		expect(resolveProbeUrl("https://cdn.example/assets/a-1.js", `${ORIGIN}/assets/index-9.js`)).toBe(`${ORIGIN}/assets/index-9.js`);
	});

	it("has nothing to probe outside a built bundle", () => {
		expect(resolveProbeUrl(undefined, `${ORIGIN}/lib/utils/versionSkew.ts`)).toBeUndefined();
	});
});

describe("classifyLoadFailure", () => {
	afterEach(() => {
		vi.unstubAllGlobals();
		vi.useRealTimers();
	});

	it("reports an upgrade only for Bifrost's own missing-asset 404", async () => {
		stubFetch(async () => bifrostMissing("/assets/trending-up-CnicU8gss.js"));
		expect(await classifyLoadFailure(`${ORIGIN}/assets/trending-up-CnicU8gss.js`)).toBe("upgrade");
	});

	it("treats a 404 from anything other than Bifrost as blocked", async () => {
		stubFetch(async () => textResponse(404, "<html>Not Found</html>"));
		expect(await classifyLoadFailure(`${ORIGIN}/assets/a-1.js`)).toBe("blocked");
	});

	it("treats a reachable asset or a proxy rejection as blocked", async () => {
		for (const status of [200, 403, 407, 502]) {
			stubFetch(async () => textResponse(status, MISSING_ASSET_BODY_PREFIX));
			expect(await classifyLoadFailure(`${ORIGIN}/assets/a-1.js`), String(status)).toBe("blocked");
		}
	});

	it("treats a network failure as blocked", async () => {
		stubFetch(async () => {
			throw new TypeError("Failed to fetch");
		});
		expect(await classifyLoadFailure(`${ORIGIN}/assets/a-1.js`)).toBe("blocked");
	});

	it("does not probe when there is no URL", async () => {
		const fetchMock = stubFetch(async () => bifrostMissing("/assets/a-1.js"));
		expect(await classifyLoadFailure(undefined)).toBe("blocked");
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it("gives up on a probe that never settles", async () => {
		vi.useFakeTimers();
		vi.stubGlobal(
			"fetch",
			vi.fn(
				(_url: string, init: RequestInit) =>
					new Promise<Response>((_, reject) => init.signal?.addEventListener("abort", () => reject(new Error("aborted")))),
			),
		);
		const result = classifyLoadFailure(`${ORIGIN}/assets/a-1.js`);
		await vi.advanceTimersByTimeAsync(PROBE_TIMEOUT_MS);
		expect(await result).toBe("blocked");
	});
});

describe("skew store", () => {
	beforeEach(() => __resetSkewForTests());
	afterEach(() => vi.unstubAllGlobals());

	it("records an upgrade when the failed chunk is gone from the server", async () => {
		installWindow();
		const fetchMock = stubFetch(async () => bifrostMissing("/assets/a-1.js"));

		reportSkew("soft", `${ORIGIN}/assets/a-1.js`);
		expect(getSkewState()).toEqual({ mode: "soft", cause: null });

		await vi.waitFor(() => expect(getSkewState().cause).toBe("upgrade"));
		expect(fetchMock).toHaveBeenCalledWith(`${ORIGIN}/assets/a-1.js`, expect.objectContaining({ cache: "no-store" }));
	});

	it("records blocked when the server still has the chunk", async () => {
		installWindow();
		stubFetch(async () => textResponse(200, "export {}"));

		reportSkew("hard", `${ORIGIN}/assets/a-1.js`);
		await vi.waitFor(() => expect(getSkewState()).toEqual({ mode: "hard", cause: "blocked" }));
	});

	it("probes once however many chunks fail", async () => {
		installWindow();
		const fetchMock = stubFetch(async () => bifrostMissing("/assets/a-1.js"));

		reportSkew("soft", `${ORIGIN}/assets/a-1.js`);
		reportSkew("soft", `${ORIGIN}/assets/b-2.js`);
		reportSkew("hard", `${ORIGIN}/assets/c-3.js`);

		await vi.waitFor(() => expect(getSkewState()).toEqual({ mode: "hard", cause: "upgrade" }));
		expect(fetchMock).toHaveBeenCalledTimes(1);
	});

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