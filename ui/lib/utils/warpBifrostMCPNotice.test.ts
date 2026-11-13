import { describe, expect, it } from "vitest";
import {
	isAuthorizationError,
	shouldShowWarpBifrostMCPNotice,
	shownSecretVar,
	warpBifrostMCPSettings,
	warpManagementMode,
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
	it("is traffic-only without bifrost_url, whatever writes say", () => {
		expect(warpManagementMode({ bifrostURL: "", openAPIURL: "", allowWrites: true })).toBe("traffic_only");
		expect(warpManagementMode({ bifrostURL: "", openAPIURL: "", allowWrites: false })).toBe("traffic_only");
	});
	it("reads and creates with bifrost_url and writes allowed", () => {
		expect(warpManagementMode({ bifrostURL: "https://b.example.com", openAPIURL: "", allowWrites: true })).toBe("reads_and_creates");
	});
	it("only reads with writes turned off", () => {
		expect(warpManagementMode({ bifrostURL: "https://b.example.com", openAPIURL: "", allowWrites: false })).toBe("reads");
	});
});

describe("shouldShowWarpBifrostMCPNotice", () => {
	it("shows only to an admin of an enabled Warp who has not acknowledged it", () => {
		expect(shouldShowWarpBifrostMCPNotice({ warpEnabled: true, canUpdateWarp: true, acknowledged: false })).toBe(true);
		expect(shouldShowWarpBifrostMCPNotice({ warpEnabled: false, canUpdateWarp: true, acknowledged: false })).toBe(false);
		expect(shouldShowWarpBifrostMCPNotice({ warpEnabled: true, canUpdateWarp: false, acknowledged: false })).toBe(false);
		expect(shouldShowWarpBifrostMCPNotice({ warpEnabled: true, canUpdateWarp: true, acknowledged: true })).toBe(false);
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