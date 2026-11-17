import { afterEach, describe, expect, it, vi } from "vitest";
import {
	isAuthorizationError,
	readWarpBifrostMCPNoticeLaterCount,
	shownSecretVar,
	WARP_BIFROST_MCP_NOTICE_LATER_COUNT_KEY,
	WARP_BIFROST_MCP_NOTICE_MAX_REMINDERS,
	warpBifrostMCPNoticeMode,
	warpBifrostMCPSettings,
	warpManagementMode,
	writeWarpBifrostMCPNoticeLaterCount,
} from "./warpBifrostMCPNotice";

describe("shownSecretVar", () => {
	it("shows a reference by its name, never a resolved value", () => {
		expect(shownSecretVar({ type: "env", ref: "env.BIFROST_URL", value: "https://resolved.example.com" })).toBe("env.BIFROST_URL");
	});
	it("shows a plain value", () => {
		expect(shownSecretVar({ type: "plain_text", value: " https://bifrost.example.com " })).toBe("https://bifrost.example.com");
	});
	it("is empty when unset or blank", () => {
		expect(shownSecretVar(undefined)).toBe("");
		expect(shownSecretVar({ value: "  " })).toBe("");
	});
});

describe("warpBifrostMCPSettings", () => {
	it("defaults writes to allowed, matching the server", () => {
		expect(warpBifrostMCPSettings({}, {})).toEqual({ bifrostURL: "", openAPIURL: "", allowWrites: true });
		expect(warpBifrostMCPSettings(undefined, undefined).allowWrites).toBe(true);
	});
	it("reads all three settings", () => {
		expect(
			warpBifrostMCPSettings(
				{ bifrost_url: { value: "https://bifrost.example.com" }, bifrost_mcp_allow_writes: false },
				{ openapi_url: " specs/openapi.json " },
			),
		).toEqual({ bifrostURL: "https://bifrost.example.com", openAPIURL: "specs/openapi.json", allowWrites: false });
	});
});

describe("warpManagementMode", () => {
	const url = { bifrostURL: "https://b.example.com", openAPIURL: "" };
	it("is off without bifrost_url, whatever writes say", () => {
		expect(warpManagementMode({ bifrostURL: "", openAPIURL: "", allowWrites: true }, ["create"])).toBe("off");
		expect(warpManagementMode({ bifrostURL: "", openAPIURL: "", allowWrites: false })).toBe("off");
	});
	it("only reads by default, with no kind of change allowed in Warp's settings", () => {
		expect(warpManagementMode({ ...url, allowWrites: true })).toBe("reads");
		expect(warpManagementMode({ ...url, allowWrites: true }, [])).toBe("reads");
	});
	it("reads and writes once Warp's settings allow a kind and the Bifrost MCP serves writes", () => {
		expect(warpManagementMode({ ...url, allowWrites: true }, ["create", "update"])).toBe("reads_and_writes");
	});
	it("only reads when the Bifrost MCP serves no writes, whatever Warp's settings allow", () => {
		expect(warpManagementMode({ ...url, allowWrites: false }, ["create"])).toBe("reads");
	});
});

describe("warpBifrostMCPNoticeMode", () => {
	const admin = { warpEnabled: true, canUpdateWarp: true, bifrostMCPAvailable: true, acknowledged: false, laterCount: 0 };
	it("shows only to an admin of an enabled Warp who has not acknowledged it", () => {
		expect(warpBifrostMCPNoticeMode(admin)).toBe("dialog");
		expect(warpBifrostMCPNoticeMode({ ...admin, warpEnabled: false })).toBe("hidden");
		expect(warpBifrostMCPNoticeMode({ ...admin, canUpdateWarp: false })).toBe("hidden");
		expect(warpBifrostMCPNoticeMode({ ...admin, acknowledged: true })).toBe("hidden");
	});
	it("opens by itself until it has been put off the maximum number of times, then becomes a banner", () => {
		expect(warpBifrostMCPNoticeMode({ ...admin, laterCount: WARP_BIFROST_MCP_NOTICE_MAX_REMINDERS - 1 })).toBe("dialog");
		expect(warpBifrostMCPNoticeMode({ ...admin, laterCount: WARP_BIFROST_MCP_NOTICE_MAX_REMINDERS })).toBe("banner");
		expect(warpBifrostMCPNoticeMode({ ...admin, laterCount: WARP_BIFROST_MCP_NOTICE_MAX_REMINDERS + 3 })).toBe("banner");
	});
	it("hides the banner too once acknowledged", () => {
		expect(warpBifrostMCPNoticeMode({ ...admin, acknowledged: true, laterCount: WARP_BIFROST_MCP_NOTICE_MAX_REMINDERS })).toBe("hidden");
	});
	it("without the Bifrost MCP ignores an earlier acknowledgement but keeps the reminder cap", () => {
		const off = { ...admin, bifrostMCPAvailable: false };
		expect(warpBifrostMCPNoticeMode(off)).toBe("dialog");
		expect(warpBifrostMCPNoticeMode({ ...off, acknowledged: true })).toBe("dialog");
		expect(warpBifrostMCPNoticeMode({ ...off, laterCount: WARP_BIFROST_MCP_NOTICE_MAX_REMINDERS - 1 })).toBe("dialog");
		expect(warpBifrostMCPNoticeMode({ ...off, laterCount: WARP_BIFROST_MCP_NOTICE_MAX_REMINDERS })).toBe("banner");
		expect(warpBifrostMCPNoticeMode({ ...off, acknowledged: true, laterCount: WARP_BIFROST_MCP_NOTICE_MAX_REMINDERS })).toBe("banner");
		expect(warpBifrostMCPNoticeMode({ ...off, canUpdateWarp: false })).toBe("hidden");
		expect(warpBifrostMCPNoticeMode({ ...off, warpEnabled: false })).toBe("hidden");
	});
});

describe("notice reminder count", () => {
	const store = new Map<string, string>();
	vi.stubGlobal("localStorage", {
		getItem: (key: string) => store.get(key) ?? null,
		setItem: (key: string, value: string) => void store.set(key, value),
	});
	afterEach(() => store.clear());

	it("is zero when unset or not a positive number", () => {
		expect(readWarpBifrostMCPNoticeLaterCount()).toBe(0);
		store.set(WARP_BIFROST_MCP_NOTICE_LATER_COUNT_KEY, "garbage");
		expect(readWarpBifrostMCPNoticeLaterCount()).toBe(0);
		store.set(WARP_BIFROST_MCP_NOTICE_LATER_COUNT_KEY, "-2");
		expect(readWarpBifrostMCPNoticeLaterCount()).toBe(0);
	});
	it("reads back what was written", () => {
		writeWarpBifrostMCPNoticeLaterCount(3);
		expect(readWarpBifrostMCPNoticeLaterCount()).toBe(3);
	});
});

describe("isAuthorizationError", () => {
	it("is a 401 or 403 only", () => {
		expect(isAuthorizationError({ status: 401 })).toBe(true);
		expect(isAuthorizationError({ status: 403 })).toBe(true);
		expect(isAuthorizationError({ status: 500 })).toBe(false);
		expect(isAuthorizationError({ status: "FETCH_ERROR", error: "TypeError: Failed to fetch" })).toBe(false);
		expect(isAuthorizationError(undefined)).toBe(false);
	});
});