export const SKEW_ERROR_PATTERNS = [
	"failed to fetch dynamically imported module",
	"error loading dynamically imported module",
	"importing a module script failed",
	"chunkloaderror",
	"loading chunk",
	"loading css chunk",
	"failed to load module script",
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

let skewMode: SkewMode = "none";
const subscribers = new Set<() => void>();

export function getSkewMode(): SkewMode {
	return skewMode;
}

export function subscribeSkew(onChange: () => void): () => void {
	subscribers.add(onChange);
	return () => {
		subscribers.delete(onChange);
	};
}

export function reportSkew(mode: "soft" | "hard" = "soft"): void {
	if (skewMode === "hard") return;
	if (skewMode === mode) return;
	skewMode = mode;
	for (const onChange of subscribers) onChange();
}

export function __resetSkewForTests(): void {
	skewMode = "none";
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

function isAssetElementError(target: EventTarget | null): boolean {
	if (!(target instanceof HTMLScriptElement) && !(target instanceof HTMLLinkElement)) return false;
	const url = target instanceof HTMLScriptElement ? target.src : target.href;
	if (!url) return false;
	try {
		const parsed = new URL(url, window.location.href);
		return parsed.origin === window.location.origin && parsed.pathname.includes("/assets/");
	} catch {
		return false;
	}
}

export function installVersionSkewListeners(): void {
	if (typeof window === "undefined") return;

	// Do not preventDefault: Vite must rethrow so the route boundary can escalate to hard mode.
	window.addEventListener("vite:preloadError", () => {
		reportSkew("soft");
	});

	window.addEventListener("unhandledrejection", (event) => {
		if (!isSkewError(event.reason)) return;
		event.preventDefault();
		reportSkew("soft");
	});

	window.addEventListener(
		"error",
		(event) => {
			if (isAssetElementError(event.target)) {
				reportSkew("soft");
				return;
			}
			if (isSkewError((event as ErrorEvent).error ?? (event as ErrorEvent).message)) {
				reportSkew("soft");
			}
		},
		true,
	);
}