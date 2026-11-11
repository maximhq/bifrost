import { describe, expect, it } from "vitest";

import { aliasConfigSchema, isLiteralHTTPSURL } from "./schemas";

const literal = (value: string) => ({ value, ref: "" });
const deploymentURL = "https://model-abc123.api.baseten.co/environments/production/sync/v1";

describe("Baseten deployment_url", () => {
	it("keeps a literal https deployment URL on the alias", () => {
		const alias = { model_id: "not-required", deployment_url: literal(deploymentURL) };
		expect(aliasConfigSchema.parse(alias)).toEqual(alias);
	});

	it("rejects literal URLs the server would refuse", () => {
		for (const value of ["http://model-abc123.api.baseten.co/sync/v1", "model-abc123.api.baseten.co/sync/v1", "https://", "not a url"]) {
			expect(aliasConfigSchema.safeParse({ model_id: "not-required", deployment_url: literal(value) }).success, value).toBe(false);
		}
	});

	// The browser's URL parser skips extra or missing slashes after https: and takes the next
	// segment as the host; Go's url.Parse finds no host there and the server rejects the value.
	it("rejects literal URLs without an explicit authority", () => {
		for (const value of [
			"https:///model-abc123.api.baseten.co/environments/production/sync/v1",
			"https:/model-abc123.api.baseten.co/environments/production/sync/v1",
			"https:model-abc123.api.baseten.co/environments/production/sync/v1",
			"https:\\\\model-abc123.api.baseten.co/environments/production/sync/v1",
		]) {
			expect(isLiteralHTTPSURL(literal(value)), value).toBe(false);
		}
		expect(isLiteralHTTPSURL(literal(` ${deploymentURL} `))).toBe(true);
		expect(isLiteralHTTPSURL(literal(deploymentURL.replace("https", "HTTPS")))).toBe(true);
	});

	it("rejects literal URLs with userinfo, a query or a fragment", () => {
		for (const value of [
			"https://user:pass@model-abc123.api.baseten.co/environments/production/sync/v1",
			`${deploymentURL}?x=1`,
			`${deploymentURL}#frag`,
		]) {
			expect(isLiteralHTTPSURL(literal(value)), value).toBe(false);
		}
	});

	it("accepts env and vault references without resolving them", () => {
		for (const deployment_url of [
			{ value: "", ref: "env.BASETEN_DEPLOYMENT_URL", type: "env" as const },
			{ value: "", ref: "vault.baseten/deployment", type: "vault" as const },
		]) {
			expect(aliasConfigSchema.safeParse({ model_id: "not-required", deployment_url }).success, deployment_url.ref).toBe(true);
		}
	});

	it("treats an unset deployment URL as valid", () => {
		expect(isLiteralHTTPSURL(undefined)).toBe(true);
		expect(isLiteralHTTPSURL(literal(""))).toBe(true);
		expect(aliasConfigSchema.safeParse({ model_id: "openai/gpt-oss-120b" }).success).toBe(true);
	});
});