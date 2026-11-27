// Runtime registry for live notices in the topbar notification tray.
//
// Stored notifications (GET /api/notifications) are events: they are read,
// dismissed and expire. A live notice is a condition that holds right now, such
// as "the cluster is split into 2 groups": it shows while the condition holds,
// disappears when it clears, and cannot be dismissed. OSS registers no sources;
// downstream builds (enterprise) register theirs from
// @enterprise/lib/registrations/liveNotices, whose OSS-build fallback is
// ui/app/_fallbacks/enterprise/lib/registrations/liveNotices.ts.

import type { NotificationSeverity } from "@/lib/types/notifications";

export interface LiveNotice {
	/** Stable across polls, so React keeps the row and the tray does not flicker. */
	id: string;
	severity: NotificationSeverity;
	title: string;
	message: string;
	/** Short lines under the message, e.g. the affected nodes. */
	details?: string[];
	actionLabel?: string;
	actionPath?: string;
}

/**
 * A source is a React hook returning the notices that hold right now. It runs
 * on every page, so it must skip its queries when the caller lacks access and
 * poll slowly.
 */
export type LiveNoticeSource = () => LiveNotice[];

const sources: LiveNoticeSource[] = [];

/**
 * Registers a source. Intended to be called at module load, once, before the
 * first render that reads the registry: useLiveNotices calls every source as a
 * hook, so the list must not change between renders.
 */
export function registerLiveNoticeSource(source: LiveNoticeSource): void {
	if (!sources.includes(source)) sources.push(source);
}

/** Every registered source's current notices, in registration order. */
export function useLiveNotices(): LiveNotice[] {
	return sources.flatMap((source) => source());
}

/** Notices that count toward the badge: an info notice is shown but does not ask for attention. */
export function countAttentionNotices(notices: LiveNotice[]): number {
	return notices.filter((n) => n.severity === "error" || n.severity === "warning").length;
}