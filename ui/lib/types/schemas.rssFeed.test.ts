import { describe, expect, it } from "vitest";

import { rssFeedFormSchema } from "./schemas";

const parse = (url: string, icon_url = "") => rssFeedFormSchema.safeParse({ name: "Feed", url, icon_url });

describe("rssFeedFormSchema", () => {
	it("accepts http(s) feed URLs and PNG or JPEG logos", () => {
		expect(parse("https://example.com/feed.xml").success).toBe(true);
		expect(parse("http://example.com:8080/feed", "https://example.com/logo.png").success).toBe(true);
		expect(parse("https://example.com/feed", "data:image/png;base64,iVBORw0KGgo=").success).toBe(true);
	});

	it("rejects feed URLs the server would refuse", () => {
		for (const url of [
			"ftp://example.com/feed",
			"javascript:alert(1)",
			"https://user:secret@example.com/feed",
			"https://",
			"https://exa mple.com/",
		]) {
			expect(parse(url).success, url).toBe(false);
		}
	});

	it("rejects logos the server would refuse", () => {
		for (const icon of [
			"data:image/svg+xml;base64,PHN2Zz4=",
			"data:image/gif;base64,R0lGODlh",
			"data:image/png;utf8,<svg>",
			"https://user:secret@example.com/logo.png",
			"javascript:alert(1)",
			`https://example.com/${"a".repeat(2048)}`,
		]) {
			expect(parse("https://example.com/feed", icon).success, icon).toBe(false);
		}
	});
});