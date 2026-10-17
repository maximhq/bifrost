import { describe, expect, it } from "vitest";

import { modelProviderKeySchema } from "./schemas";

const base = { id: "k1", name: "copilot", models: ["*"], blacklisted_models: [], weight: 1 };
const literal = (value: string) => ({ value, ref: "" });
const envRef = (ref: string) => ({ value: "", ref, type: "env" as const });
const validPEM = "-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAKj3\n-----END RSA PRIVATE KEY-----";

const appConfig = (overrides: Record<string, unknown> = {}) => ({
	app_id: literal("123456"),
	installation_id: literal("87654321"),
	repository_id: literal("999000111"),
	private_key: literal(validPEM),
	...overrides,
});

const parse = (key: Record<string, unknown>) => modelProviderKeySchema.safeParse({ ...base, ...key });

// The key form validates through modelProviderKeySchema, so these rules decide what the
// form can save for a GitHub Copilot key.
describe("modelProviderKeySchema, github-copilot", () => {
	it("accepts GitHub App credentials with no token", () => {
		const parsed = parse({ value: literal(""), github_copilot_key_config: appConfig() });
		expect(parsed.success, parsed.success ? "" : JSON.stringify(parsed.error.issues)).toBe(true);
	});

	it("accepts a token with no app config", () => {
		expect(parse({ value: literal("tid=abc") }).success).toBe(true);
	});

	it("accepts a token with the empty app config that the form leaves behind", () => {
		// The App fields are always mounted, so a token key is submitted with this block.
		expect(parse({ value: literal("tid=abc"), github_copilot_key_config: {} }).success).toBe(true);
		const blank = { app_id: literal(""), installation_id: literal(""), repository_id: literal(""), private_key: literal("") };
		expect(parse({ value: literal("tid=abc"), github_copilot_key_config: blank }).success).toBe(true);
	});

	it("rejects a key with neither a token nor app credentials", () => {
		expect(parse({ value: literal("") }).success).toBe(false);
		expect(parse({ value: literal(""), github_copilot_key_config: {} }).success).toBe(false);
	});

	it("rejects an app config that only carries github_domain", () => {
		expect(parse({ value: literal(""), github_copilot_key_config: { github_domain: literal("acme.ghe.com") } }).success).toBe(false);
	});

	it("rejects a partially filled app config, with or without a token", () => {
		const partial = { app_id: literal("123456") };
		expect(parse({ value: literal(""), github_copilot_key_config: partial }).success).toBe(false);
		expect(parse({ value: literal("tid=abc"), github_copilot_key_config: partial }).success).toBe(false);
	});

	it("rejects malformed literal credentials", () => {
		expect(parse({ value: literal(""), github_copilot_key_config: appConfig({ installation_id: literal("my-install") }) }).success).toBe(
			false,
		);
		expect(parse({ value: literal(""), github_copilot_key_config: appConfig({ repository_id: literal("my-repo") }) }).success).toBe(false);
		expect(parse({ value: literal(""), github_copilot_key_config: appConfig({ private_key: literal("not a key") }) }).success).toBe(false);
	});

	it("does not format-check environment references", () => {
		const parsed = parse({
			value: literal(""),
			github_copilot_key_config: {
				app_id: envRef("env.COPILOT_APP_ID"),
				installation_id: envRef("env.COPILOT_INSTALLATION_ID"),
				repository_id: envRef("env.COPILOT_REPOSITORY_ID"),
				private_key: envRef("env.COPILOT_PRIVATE_KEY"),
			},
		});
		expect(parsed.success, parsed.success ? "" : JSON.stringify(parsed.error.issues)).toBe(true);
	});
});