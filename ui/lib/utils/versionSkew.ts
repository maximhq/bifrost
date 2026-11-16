export const SKEW_ERROR_PATTERNS = [
	"failed to fetch dynamically imported module",
	"error loading dynamically imported module",
	"importing a module script failed",
	"chunkloaderror",
	"loading chunk",
	"loading css chunk",
	"failed to load module script",
	"unable to preload css",
] as const;

export function isSkewError(err: unknown): boolean {
	if (!err) return false;

	if (typeof err === "object" && "name" in err && (err as { name?: unknown }).name === "ChunkLoadError") {
		return true;
	}

	const message = typeof err === "object" && "message" in err ? String((err as { message?: unknown }).message ?? "") : String(err);
	if (!message) return false;

	const haystack = message.toLowerCase();
	return SKEW_ERROR_PATTERNS.some((pattern) => haystack.includes(pattern));
}

// Soft failures preserve the app; hard failures replace an already-unmounted route.
export type SkewMode = "none" | "soft" | "hard";

// "upgrade": the server no longer has the chunk. "blocked": the server has it but the browser could not get it
// (proxy, firewall, extension, network), so telling the user Bifrost is upgrading would be wrong.
export type SkewCause = "upgrade" | "blocked";

export interface SkewState {
	mode: SkewMode;
	cause: SkewCause | null; // null until the probe settles
}

// Must match the body ui.go sends for a missing static asset; a proxy 404 does not carry it.
export const MISSING_ASSET_BODY_PREFIX = "404 - Static asset not found";
export const PROBE_TIMEOUT_MS = 10_000;

const ASSET_URL_PATTERN = /(?:https?:\/\/[^\s'"()]+?|[^\s'"()]*?)\/assets\/[^\s'"()]+/i;

/** Pulls the failed chunk URL out of a browser or Vite error message, when the message carries one. */
export function extractAssetUrl(err: unknown): string | undefined {
	if (!err) return undefined;
	const message = typeof err === "object" && "message" in err ? String((err as { message?: unknown }).message ?? "") : String(err);
	const match = message.match(ASSET_URL_PATTERN);
	return match?.[0].replace(/[.,;:]+$/, "");
}

function sameOriginAssetUrl(url: string | undefined): string | undefined {
	if (!url || typeof window === "undefined") return undefined;
	try {
		const parsed = new URL(url, window.location.href);
		if (parsed.origin !== window.location.origin || !parsed.pathname.includes("/assets/")) return undefined;
		return parsed.href;
	} catch {
		return undefined;
	}
}

/**
 * Picks the URL to probe: the failed chunk when known, otherwise the chunk this module was loaded from. If the
 * running build's own chunk is gone too, the server moved to a new build; if it still loads, the build is unchanged.
 */
export function resolveProbeUrl(failedUrl?: string, selfUrl: string = import.meta.url): string | undefined {
	return sameOriginAssetUrl(failedUrl) ?? sameOriginAssetUrl(selfUrl);
}

/** Re-requests the asset and reports "upgrade" only for Bifrost's own missing-asset 404. */
export async function classifyLoadFailure(url: string | undefined): Promise<SkewCause> {
	if (!url) return "blocked";
	const controller = new AbortController();
	const timer = setTimeout(() => controller.abort(), PROBE_TIMEOUT_MS);
	try {
		const response = await fetch(url, { cache: "no-store", credentials: "same-origin", signal: controller.signal });
		if (response.status !== 404) return "blocked";
		const body = await response.text();
		return body.startsWith(MISSING_ASSET_BODY_PREFIX) ? "upgrade" : "blocked";
	} catch {
		return "blocked";
	} finally {
		clearTimeout(timer);
	}
}

let skewState: SkewState = { mode: "none", cause: null };
let probeStarted = false;
let generation = 0;
const subscribers = new Set<() => void>();

function setSkewState(next: SkewState): void {
	skewState = next;
	for (const onChange of subscribers) onChange();
}

export function getSkewState(): SkewState {
	return skewState;
}

export function getSkewMode(): SkewMode {
	return skewState.mode;
}

export function subscribeSkew(onChange: () => void): () => void {
	subscribers.add(onChange);
	return () => {
		subscribers.delete(onChange);
	};
}

/** Records a chunk load failure. The first report probes failedUrl once to decide between upgrade and blocked. */
export function reportSkew(mode: "soft" | "hard" = "soft", failedUrl?: string): void {
	if (!probeStarted) {
		probeStarted = true;
		const startedIn = generation;
		void classifyLoadFailure(resolveProbeUrl(failedUrl)).then((cause) => {
			if (startedIn !== generation) return;
			setSkewState({ ...skewState, cause });
		});
	}

	if (skewState.mode === "hard" || skewState.mode === mode) return;
	setSkewState({ ...skewState, mode });
}

export function __resetSkewForTests(): void {
	skewState = { mode: "none", cause: null };
	probeStarted = false;
	generation += 1;
	subscribers.clear();
}

const RELOAD_GUARD_KEY = "bifrost:skew-reloads";
export const MAX_AUTO_RELOADS = 2;
export const RELOAD_WINDOW_MS = 60_000;

interface ReloadGuardState {
	count: number;
	firstAt: number;
}

function readGuard(): ReloadGuardState | null {
	try {
		const raw = sessionStorage.getItem(RELOAD_GUARD_KEY);
		if (!raw) return null;
		const parsed = JSON.parse(raw) as Partial<ReloadGuardState>;
		if (typeof parsed?.count !== "number" || typeof parsed?.firstAt !== "number") return null;
		return { count: parsed.count, firstAt: parsed.firstAt };
	} catch {
		return null;
	}
}

function writeGuard(state: ReloadGuardState): boolean {
	try {
		sessionStorage.setItem(RELOAD_GUARD_KEY, JSON.stringify(state));
		return true;
	} catch {
		return false;
	}
}

// A reload is only authorized when the new count survives a reload; otherwise the budget could never be spent.
export function consumeAutoReload(now: number = Date.now()): boolean {
	const existing = readGuard();

	if (!existing || now - existing.firstAt > RELOAD_WINDOW_MS) {
		return writeGuard({ count: 1, firstAt: now });
	}

	if (existing.count >= MAX_AUTO_RELOADS) return false;

	return writeGuard({ count: existing.count + 1, firstAt: existing.firstAt });
}

export function clearAutoReloadGuard(): void {
	try {
		sessionStorage.removeItem(RELOAD_GUARD_KEY);
	} catch {}
}

export const POLL_INTERVAL_MS = 3_000;
export const POLL_TIMEOUT_MS = 90_000;

// Require repeated matches because polls may hit different pods during a rollout.
export const STABLE_POLLS_REQUIRED = 3;

export interface VersionPollHandlers {
	fetchVersion: (signal: AbortSignal) => Promise<Response>;
	// The reported version stayed stable long enough to consider the rollout done.
	onUpdateReady: () => void;
	// The overall polling budget ran out, including while a request was still in flight.
	onTimeout: () => void;
}

/** Polls the version endpoint until it stabilises, times out, or is stopped. Returns the stop function. */
export function startVersionPoll({ fetchVersion, onUpdateReady, onTimeout }: VersionPollHandlers): () => void {
	const startedAt = Date.now();
	let cancelled = false;
	let timer: ReturnType<typeof setTimeout> | undefined;
	let abortTimer: ReturnType<typeof setTimeout> | undefined;
	let controller: AbortController | undefined;
	let lastVersion: string | null = null;
	let stableCount = 0;

	const clearRequest = () => {
		if (abortTimer) clearTimeout(abortTimer);
		abortTimer = undefined;
		controller = undefined;
	};

	const poll = async () => {
		timer = undefined;
		if (cancelled) return;

		const remaining = POLL_TIMEOUT_MS - (Date.now() - startedAt);
		if (remaining <= 0) {
			onTimeout();
			return;
		}

		// Cap each request at the remaining budget so a request that never settles cannot stall the poll forever.
		let timedOut = false;
		controller = new AbortController();
		abortTimer = setTimeout(() => {
			timedOut = true;
			controller?.abort();
		}, remaining);

		try {
			const response = await fetchVersion(controller.signal);
			if (cancelled) return;

			if (response.ok) {
				const version = (await response.text()).trim();
				if (cancelled) return;

				if (version && version === lastVersion) {
					stableCount += 1;
				} else {
					lastVersion = version;
					stableCount = 1;
				}

				if (stableCount >= STABLE_POLLS_REQUIRED) {
					onUpdateReady();
					return;
				}
			}
		} catch {
			if (timedOut && !cancelled) onTimeout();
			if (timedOut) return;
		} finally {
			clearRequest();
		}

		if (!cancelled) timer = setTimeout(poll, POLL_INTERVAL_MS);
	};

	timer = setTimeout(poll, POLL_INTERVAL_MS);

	return () => {
		cancelled = true;
		if (timer) clearTimeout(timer);
		timer = undefined;
		controller?.abort();
		clearRequest();
	};
}

function failedAssetElementUrl(target: EventTarget | null): string | undefined {
	if (!(target instanceof HTMLScriptElement) && !(target instanceof HTMLLinkElement)) return undefined;
	return sameOriginAssetUrl(target instanceof HTMLScriptElement ? target.src : target.href);
}

export function installVersionSkewListeners(): void {
	if (typeof window === "undefined") return;

	// Do not preventDefault: Vite must rethrow so the route boundary can escalate to hard mode.
	window.addEventListener("vite:preloadError", (event) => {
		reportSkew("soft", extractAssetUrl((event as Event & { payload?: unknown }).payload));
	});

	window.addEventListener("unhandledrejection", (event) => {
		if (!isSkewError(event.reason)) return;
		event.preventDefault();
		reportSkew("soft", extractAssetUrl(event.reason));
	});

	window.addEventListener(
		"error",
		(event) => {
			const elementUrl = failedAssetElementUrl(event.target);
			if (elementUrl) {
				reportSkew("soft", elementUrl);
				return;
			}
			const err = (event as ErrorEvent).error ?? (event as ErrorEvent).message;
			if (isSkewError(err)) {
				reportSkew("soft", extractAssetUrl(err));
			}
		},
		true,
	);
}