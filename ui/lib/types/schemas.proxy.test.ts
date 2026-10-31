import { describe, expect, it } from "vitest";

import { globalProxyConfigSchema } from "./schemas";

const base = {
	enabled: true,
	type: "socks5" as const,
	url: "",
	enable_for_scim: false,
	enable_for_inference: true,
	enable_for_api: true,
};

const urlIssues = (url: string, type: "http" | "socks5" = "socks5") => {
	const result = globalProxyConfigSchema.safeParse({ ...base, type, url });
	return result.success ? [] : result.error.issues.filter((issue) => issue.path.join(".") === "url").map((issue) => issue.message);
};

describe("globalProxyConfigSchema SOCKS5 url", () => {
	// The server, the client factory and config.schema.json give a bare host:port the type's
	// scheme, so a stored bare SOCKS5 address must not leave Save disabled.
	it("accepts a bare host:port, which takes its scheme from the type", () => {
		expect(urlIssues("proxy.corp:1080")).toEqual([]);
	});

	// A bare IP literal is not a valid WHATWG URL on its own ("10.0.0.5:1080" has no scheme), so
	// it must be parsed with the type's scheme, as the server does.
	it("accepts a bare IP address for SOCKS5 and for http", () => {
		expect(urlIssues("10.0.0.5:1080")).toEqual([]);
		expect(urlIssues("10.0.0.5:3128", "http")).toEqual([]);
	});

	// socks5 is not a WHATWG special scheme, so "socks5:///" and "socks5://" parse with an
	// empty host. The server refuses a proxy URL that names no host.
	it("rejects a SOCKS5 proxy URL with no host", () => {
		expect(urlIssues("/")).toContain("Must be a valid URL (e.g., http://proxy.example.com:8080)");
		expect(urlIssues("socks5://")).toContain("Must be a valid URL (e.g., http://proxy.example.com:8080)");
	});

	it("still rejects a bare address that is not host:port", () => {
		expect(urlIssues("not a host", "http")).toContain("Must be a valid URL (e.g., http://proxy.example.com:8080)");
	});

	it("accepts socks5:// and socks5h:// urls", () => {
		expect(urlIssues("socks5://proxy.corp:1080")).toEqual([]);
		expect(urlIssues("SOCKS5H://proxy.corp:1080")).toEqual([]);
	});

	it("rejects a SOCKS5 proxy named by an http url", () => {
		expect(urlIssues("http://proxy.corp:1080")).toContain("A SOCKS5 proxy URL must start with socks5:// or socks5h://");
	});

	// The server trims the URL before reading it, so padding must not fail the form.
	it("accepts a bare address with surrounding spaces", () => {
		expect(urlIssues(" 10.0.0.5:1080 ")).toEqual([]);
		expect(urlIssues(" proxy.corp:3128", "http")).toEqual([]);
	});

	// The server refuses a port outside 1-65535. WHATWG URL parsing already rejects ports above
	// 65535 but accepts 0, so the form must reject it before Save.
	it("rejects port 0 for SOCKS5 and for http", () => {
		expect(urlIssues("socks5://proxy.corp:0")).toContain("Must be a valid URL (e.g., http://proxy.example.com:8080)");
		expect(urlIssues("proxy.corp:00")).toContain("Must be a valid URL (e.g., http://proxy.example.com:8080)");
		expect(urlIssues("http://proxy.corp:0", "http")).toContain("Must be a valid URL (e.g., http://proxy.example.com:8080)");
		expect(urlIssues("socks5://proxy.corp:1")).toEqual([]);
		expect(urlIssues("http://proxy.corp:65535", "http")).toEqual([]);
	});

	// Switching the type keeps the URL, and the server refuses an http proxy named by a
	// socks5 URL, so the form must say so instead of failing on Save.
	it("rejects an http proxy named by a socks5 url", () => {
		expect(urlIssues("socks5://proxy.corp:1080", "http")).toContain("An HTTP proxy URL must start with http:// or https://");
		expect(urlIssues("https://proxy.corp:3128", "http")).toEqual([]);
		expect(urlIssues("proxy.corp:3128", "http")).toEqual([]);
	});
});