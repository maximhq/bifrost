import { describe, test, expect } from "vitest";
import { isHttpUrl } from "./httpUrl";

describe("isHttpUrl", () => {
	test("accepts absolute http and https URLs", () => {
		expect(isHttpUrl("https://idp.example.com/authorize?client_id=a")).toBe(true);
		expect(isHttpUrl("http://localhost:9000/authorize")).toBe(true);
		expect(isHttpUrl("HTTPS://IDP.EXAMPLE.COM/authorize")).toBe(true);
	});

	test("rejects other schemes", () => {
		expect(isHttpUrl("javascript:alert(1)")).toBe(false);
		expect(isHttpUrl("JaVaScRiPt:alert(1)")).toBe(false);
		expect(isHttpUrl("data:text/html,<p>x</p>")).toBe(false);
		expect(isHttpUrl("blob:https://idp.example.com/1")).toBe(false);
		expect(isHttpUrl("file:///etc/hosts")).toBe(false);
		expect(isHttpUrl("myapp://callback")).toBe(false);
	});

	test("rejects relative, empty and non-string values", () => {
		expect(isHttpUrl("/authorize")).toBe(false);
		expect(isHttpUrl("idp.example.com/authorize")).toBe(false);
		expect(isHttpUrl("")).toBe(false);
		expect(isHttpUrl("   ")).toBe(false);
		expect(isHttpUrl(undefined)).toBe(false);
		expect(isHttpUrl(null)).toBe(false);
	});
});