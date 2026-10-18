import { describe, expect, it } from "vitest";
import {
	checkRSSFeedLogoFile,
	isRSSFeedLogoValue,
	RSS_FEED_LOGO_MAX_BYTES,
	RSS_FEED_LOGO_TYPES,
	rssFeedIconSource,
	rssFeedLogoBytesError,
	rssFeedLogoFileError,
} from "./rssFeedIcon.utils";

describe("rssFeedIconSource", () => {
	it("uses bundled icons for builtin keys", () => {
		expect(rssFeedIconSource("bifrost", "https://www.getbifrost.ai/announcements/rss.xml")).toEqual({ kind: "bifrost" });
		expect(rssFeedIconSource("openai", "https://openai.com/news/rss.xml")).toEqual({ kind: "provider", provider: "openai" });
		expect(rssFeedIconSource("anthropic", "https://raw.githubusercontent.com/x/feed.xml")).toEqual({
			kind: "provider",
			provider: "anthropic",
		});
		expect(rssFeedIconSource("gemini", "https://blog.google/products/gemini/rss/")).toEqual({ kind: "provider", provider: "gemini" });
	});

	it("maps builtin keys without a bundled asset to the brand's site favicon", () => {
		expect(rssFeedIconSource("cursor", "https://cursor.com/changelog/rss.xml")).toEqual({
			kind: "image",
			url: "https://cursor.com/favicon.ico",
		});
	});

	it("falls back to the feed host's favicon for custom feeds", () => {
		expect(rssFeedIconSource(undefined, "https://github.com/maximhq/bifrost/releases.atom")).toEqual({
			kind: "image",
			url: "https://github.com/favicon.ico",
		});
		expect(rssFeedIconSource("", "http://news.example.com:8080/feed")).toEqual({
			kind: "image",
			url: "http://news.example.com:8080/favicon.ico",
		});
	});

	it("uses the generic glyph when the URL cannot be resolved", () => {
		expect(rssFeedIconSource(undefined, "not a url")).toEqual({ kind: "fallback" });
		expect(rssFeedIconSource(undefined, "javascript:alert(1)")).toEqual({ kind: "fallback" });
	});

	it("prefers a custom logo over the favicon, but never over a builtin icon", () => {
		const logo = "data:image/png;base64,iVBORw0KGgo=";
		expect(rssFeedIconSource(undefined, "https://github.com/maximhq/bifrost/releases.atom", logo)).toEqual({ kind: "image", url: logo });
		expect(rssFeedIconSource(undefined, "https://example.com/feed", "https://cdn.example.com/logo.svg")).toEqual({
			kind: "image",
			url: "https://cdn.example.com/logo.svg",
		});
		expect(rssFeedIconSource("openai", "https://openai.com/news/rss.xml", logo)).toEqual({ kind: "provider", provider: "openai" });
	});

	it("ignores a logo value the server would not have stored", () => {
		expect(rssFeedIconSource(undefined, "https://example.com/feed", "javascript:alert(1)")).toEqual({
			kind: "image",
			url: "https://example.com/favicon.ico",
		});
	});
});

describe("rss feed logo validation", () => {
	it("accepts http(s) logo URLs and PNG or JPEG data URIs only", () => {
		expect(isRSSFeedLogoValue("https://example.com/logo.png")).toBe(true);
		expect(isRSSFeedLogoValue("data:image/png;base64,iVBORw0KGgo=")).toBe(true);
		expect(isRSSFeedLogoValue("data:image/jpeg;base64,/9j/4A==")).toBe(true);
		expect(isRSSFeedLogoValue("data:image/webp;base64,AAAA")).toBe(false);
		expect(isRSSFeedLogoValue("data:image/svg+xml;base64,PHN2Zz4=")).toBe(false);
		expect(isRSSFeedLogoValue("data:image/gif;base64,R0lGODlh")).toBe(false);
		expect(isRSSFeedLogoValue("data:image/png;base64,<svg onload=alert(1)>")).toBe(false);
		expect(isRSSFeedLogoValue("data:text/html;base64,AAAA")).toBe(false);
		expect(isRSSFeedLogoValue("javascript:alert(1)")).toBe(false);
		expect(isRSSFeedLogoValue("https://user:secret@example.com/logo.png")).toBe(false);
		expect(isRSSFeedLogoValue("https://")).toBe(false);
	});

	it("only offers PNG and JPEG uploads", () => {
		expect(RSS_FEED_LOGO_TYPES).toEqual(["image/png", "image/jpeg"]);
		expect(rssFeedLogoFileError({ type: "image/svg+xml", size: 1024 })).toMatch(/PNG or JPEG/);
		expect(rssFeedLogoFileError({ type: "image/gif", size: 1024 })).toMatch(/PNG or JPEG/);
	});

	it("checks that an upload's bytes are the declared image type", () => {
		const png = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
		const jpeg = new Uint8Array([0xff, 0xd8, 0xff, 0xe0, 0, 0, 0, 0]);
		const svg = new TextEncoder().encode("<svg xml");
		expect(rssFeedLogoBytesError(png, "image/png")).toBeUndefined();
		expect(rssFeedLogoBytesError(jpeg, "image/jpeg")).toBeUndefined();
		expect(rssFeedLogoBytesError(jpeg, "image/png")).toMatch(/not a valid/);
		expect(rssFeedLogoBytesError(svg, "image/png")).toMatch(/not a valid/);
		expect(rssFeedLogoBytesError(svg, "image/jpeg")).toMatch(/not a valid/);
	});

	it("ignores prototype keys when mapping builtin icons", () => {
		expect(rssFeedIconSource("constructor", "https://example.com/feed.xml")).toEqual({
			kind: "image",
			url: "https://example.com/favicon.ico",
		});
	});

	it("rejects uploads of the wrong type or over the size cap", () => {
		expect(rssFeedLogoFileError({ type: "image/png", size: 1024 })).toBeUndefined();
		expect(rssFeedLogoFileError({ type: "application/pdf", size: 1024 })).toMatch(/PNG/);
		expect(rssFeedLogoFileError({ type: "image/png", size: RSS_FEED_LOGO_MAX_BYTES + 1 })).toMatch(/96 KB/);
	});
});
describe("checkRSSFeedLogoFile", () => {
	it("refuses a file whose bytes are not the declared image type", async () => {
		const png = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0])], "logo.png", { type: "image/png" });
		const disguised = new File(["<svg onload=alert(1)>"], "logo.png", { type: "image/png" });
		const svg = new File(["<svg/>"], "logo.svg", { type: "image/svg+xml" });
		await expect(checkRSSFeedLogoFile(png)).resolves.toBeUndefined();
		await expect(checkRSSFeedLogoFile(disguised)).resolves.toMatch(/not a valid/);
		await expect(checkRSSFeedLogoFile(svg)).resolves.toMatch(/PNG or JPEG/);
	});
});