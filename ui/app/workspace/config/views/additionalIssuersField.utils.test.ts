import { describe, expect, it } from "vitest";
import { sameIssuer } from "./additionalIssuersField.utils";

describe("sameIssuer", () => {
	it("matches a re-typed reference against its saved, resolved form", () => {
		expect(
			sameIssuer({ value: "", ref: "env.TUNNEL", type: "env" }, { value: "https://tunnel.example.com", ref: "env.TUNNEL", type: "env" }),
		).toBe(true);
	});
	it("tells different references apart", () => {
		expect(sameIssuer({ value: "", ref: "env.TUNNEL", type: "env" }, { value: "", ref: "env.OTHER", type: "env" })).toBe(false);
		expect(sameIssuer({ value: "", ref: "vault.tunnel", type: "vault" }, { value: "", ref: "env.tunnel", type: "env" })).toBe(false);
	});
	it("compares plain URLs by value", () => {
		expect(sameIssuer({ value: "https://tunnel.example.com", ref: "" }, { value: "https://tunnel.example.com" })).toBe(true);
		expect(sameIssuer({ value: "https://tunnel.example.com" }, { value: "https://other.example.com" })).toBe(false);
	});
	it("never matches a reference with a plain URL that equals its value", () => {
		expect(
			sameIssuer({ value: "https://tunnel.example.com", ref: "env.TUNNEL", type: "env" }, { value: "https://tunnel.example.com" }),
		).toBe(false);
	});
});