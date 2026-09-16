import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import i18n from "@/lib/i18n";
import { formatRelativePast, formatTokenExpiry, missingHeaderKeys } from "./mcpCredential";

describe("formatTokenExpiry", () => {
	const now = new Date("2026-09-03T12:00:00Z");

	beforeEach(() => {
		vi.useFakeTimers();
		vi.setSystemTime(now);
	});

	afterEach(() => {
		vi.useRealTimers();
	});

	test("missing expiry renders a dash", () => {
		expect(formatTokenExpiry(undefined, "active")).toBe("-");
		expect(formatTokenExpiry(null, "active")).toBe("-");
		expect(formatTokenExpiry("", "active")).toBe("-");
	});

	test("future expiry is relative", () => {
		expect(formatTokenExpiry("2026-09-03T12:42:00Z", "active")).toBe("in 42 min");
		expect(formatTokenExpiry("2026-09-03T15:30:00Z", "active")).toBe("in 3 hours");
		expect(formatTokenExpiry("2026-09-10T12:00:00Z", "active")).toBe("in 7 days");
	});

	test("a past expiry only reads as expired when the credential is dead", () => {
		const past = "2026-09-01T12:00:00Z";
		expect(formatTokenExpiry(past, "active")).toBe("Refreshes on next use");
		expect(formatTokenExpiry(past, "active", true)).toBe("Refreshes on next use");
		expect(formatTokenExpiry(past, "orphaned")).toBe("Refreshes when access is restored");
		expect(formatTokenExpiry(past, "needs_reauth")).toBe("expired");
	});

	test("the exact expiry instant counts as expired", () => {
		const exact = "2026-09-03T12:00:00Z";
		expect(formatTokenExpiry(exact, "needs_reauth")).toBe("expired");
		expect(formatTokenExpiry(exact, "active", false)).toBe("expired");
		expect(formatTokenExpiry(exact, "active", true)).toBe("Refreshes on next use");
	});

	test("a past expiry without a refresh token is expired whatever the status", () => {
		const past = "2026-09-01T12:00:00Z";
		expect(formatTokenExpiry(past, "active", false)).toBe("expired");
		expect(formatTokenExpiry(past, "orphaned", false)).toBe("expired");
		// Still in the future: the refresh token only matters once it lapses.
		expect(formatTokenExpiry("2026-09-03T12:42:00Z", "active", false)).toBe("in 42 min");
	});

	test("unparseable input passes through", () => {
		expect(formatTokenExpiry("not-a-date", "active")).toBe("not-a-date");
	});

	test("uses the current language without changing expiry and refresh semantics", async () => {
		try {
			await i18n.changeLanguage("zh-CN");
			expect(formatRelativePast("2026-09-03T11:55:00Z")).toBe("5 分钟前");
			expect(formatTokenExpiry("2026-09-03T12:42:00Z", "active")).toBe("42 分钟后");
			expect(formatTokenExpiry("2026-09-01T12:00:00Z", "active", true)).toBe("下次使用时刷新");
			expect(formatTokenExpiry("2026-09-01T12:00:00Z", "orphaned", true)).toBe("访问权限恢复后刷新");
			expect(formatTokenExpiry("2026-09-01T12:00:00Z", "active", false)).toBe("已过期");
			await i18n.changeLanguage("en");
			expect(formatRelativePast("2026-09-03T11:55:00Z")).toBe("5 min ago");
			expect(formatTokenExpiry("2026-09-03T12:42:00Z", "active")).toBe("in 42 min");
		} finally {
			await i18n.changeLanguage("en");
		}
	});
});

describe("missingHeaderKeys", () => {
	test("empty required list has nothing missing", () => {
		expect(missingHeaderKeys(undefined, ["X-API-Key"])).toEqual([]);
		expect(missingHeaderKeys([], undefined)).toEqual([]);
	});

	test("everything is missing when nothing is covered", () => {
		expect(missingHeaderKeys(["X-API-Key", "X-Tenant-ID"], undefined)).toEqual(["X-API-Key", "X-Tenant-ID"]);
	});

	test("compares header names case-insensitively and ignores blanks", () => {
		expect(missingHeaderKeys(["X-API-Key", " X-Tenant-ID ", "", "X-Region"], ["x-api-key", "X-Tenant-Id"])).toEqual(["X-Region"]);
	});
});