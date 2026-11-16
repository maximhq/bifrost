import { describe, expect, it } from "vitest";
import { isRSSFeedLogoValue, RSS_FEED_LOGO_MAX_BYTES, rssFeedIconSource, rssFeedLogoFileError } from "./rssFeedIcon.utils";

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
	it("accepts image URLs and image data URIs only", () => {
		expect(isRSSFeedLogoValue("https://example.com/logo.png")).toBe(true);
		expect(isRSSFeedLogoValue("data:image/webp;base64,AAAA")).toBe(true);
		expect(isRSSFeedLogoValue("data:text/html;base64,AAAA")).toBe(false);
		expect(isRSSFeedLogoValue("javascript:alert(1)")).toBe(false);
	});

	it("rejects uploads of the wrong type or over the size cap", () => {
		expect(rssFeedLogoFileError({ type: "image/png", size: 1024 })).toBeUndefined();
		expect(rssFeedLogoFileError({ type: "application/pdf", size: 1024 })).toMatch(/PNG/);
		expect(rssFeedLogoFileError({ type: "image/png", size: RSS_FEED_LOGO_MAX_BYTES + 1 })).toMatch(/96 KB/);
	});
});