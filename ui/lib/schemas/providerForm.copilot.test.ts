import { describe, expect, it } from "vitest";
import { DefaultNetworkConfig } from "@/lib/constants/config";
import { ProviderFormSchema } from "./providerForm";

// The provider form validates through ProviderFormSchema, and zodResolver hands
// react-hook-form the parsed result. Zod strips undeclared keys, so a credential the schema
// does not know about never reaches the API. That failure is silent: the form saves, reports
// success, and the key arrives with no GitHub App credentials at all.
describe("ProviderFormSchema github-copilot credentials", () => {
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

	const form = (keyOverrides: Record<string, unknown>) => ({
		selectedProvider: "github-copilot",
		isDirty: true,
		networkConfig: DefaultNetworkConfig,
		keys: [{ id: "k1", name: "copilot", value: "", models: ["*"], weight: 1, ...keyOverrides }],
	});

	const parse = (keyOverrides: Record<string, unknown>) => ProviderFormSchema.safeParse(form(keyOverrides));

	it("keeps github_copilot_key_config through parsing", () => {
		const parsed = parse({ github_copilot_key_config: appConfig() });

		expect(parsed.success, parsed.success ? "" : JSON.stringify(parsed.error.issues)).toBe(true);
		if (!parsed.success) return;

		const key = parsed.data.keys[0];
		expect(key.github_copilot_key_config, "credentials were stripped, so the key would save with no auth").toBeDefined();
		expect(key.github_copilot_key_config?.app_id?.value).toBe("123456");
		expect(key.github_copilot_key_config?.private_key?.value).toContain("BEGIN RSA PRIVATE KEY");
	});

	it("accepts a direct Copilot token with no app config", () => {
		expect(parse({ value: "tid=abc" }).success).toBe(true);
	});

	it("rejects a key with neither a token nor app credentials", () => {
		expect(parse({}).success, "a key with no credential at all must not save").toBe(false);
	});

	it("rejects an app config that only carries github_domain", () => {
		// The nested schema permits an empty block so token auth stays valid, which means
		// the outer check is the only thing standing between this and a credential-less key.
		expect(parse({ github_copilot_key_config: { github_domain: literal("acme.ghe.com") } }).success).toBe(false);
	});

	it("rejects a partially filled app config", () => {
		expect(parse({ github_copilot_key_config: { app_id: literal("123456") } }).success).toBe(false);
	});

	describe("literal credential formats", () => {
		it("rejects a non-numeric installation_id", () => {
			expect(parse({ github_copilot_key_config: appConfig({ installation_id: literal("my-install") }) }).success).toBe(false);
		});

		it("rejects a path-traversal installation_id", () => {
			expect(parse({ github_copilot_key_config: appConfig({ installation_id: literal("1/../../../user") }) }).success).toBe(false);
		});

		it("rejects a non-numeric repository_id", () => {
			expect(parse({ github_copilot_key_config: appConfig({ repository_id: literal("my-repo") }) }).success).toBe(false);
		});

		it("rejects a private key that is not PEM", () => {
			expect(parse({ github_copilot_key_config: appConfig({ private_key: literal("not a key") }) }).success).toBe(false);
		});

		it("accepts a PKCS#8 private key", () => {
			const pkcs8 = "-----BEGIN PRIVATE KEY-----\nMIIBOgIBAAJBAKj3\n-----END PRIVATE KEY-----";
			expect(parse({ github_copilot_key_config: appConfig({ private_key: literal(pkcs8) }) }).success).toBe(true);
		});

		it("rejects a literal disguised by a type tag with no ref", () => {
			// type: "env" with an empty ref is still a literal - the value is right there.
			// Trusting the tag alone lets any malformed value skip every format check.
			const disguised = { value: "not-numeric", ref: "", type: "env" as const };
			expect(parse({ github_copilot_key_config: appConfig({ installation_id: disguised }) }).success).toBe(false);
		});

		it.each([
			["EC key", "-----BEGIN EC PRIVATE KEY-----\nMHcCAQE\n-----END EC PRIVATE KEY-----"],
			["encrypted key", "-----BEGIN ENCRYPTED PRIVATE KEY-----\nMIIBu\n-----END ENCRYPTED PRIVATE KEY-----"],
			["mismatched headers", "-----BEGIN RSA PRIVATE KEY-----\nMIIBu\n-----END PRIVATE KEY-----"],
			["empty body", "-----BEGIN RSA PRIVATE KEY-----\n\n-----END RSA PRIVATE KEY-----"],
			["header only", "-----BEGIN RSA PRIVATE KEY-----"],
		])("rejects a private key that is a %s", (_name, body) => {
			expect(parse({ github_copilot_key_config: appConfig({ private_key: literal(body) }) }).success).toBe(false);
		});

		it("does not format-check environment references", () => {
			// Their values resolve on the server, so judging their shape here would reject
			// every legitimate headless configuration.
			const parsed = parse({
				github_copilot_key_config: {
					app_id: envRef("COPILOT_APP_ID"),
					installation_id: envRef("COPILOT_INSTALLATION_ID"),
					repository_id: envRef("COPILOT_REPOSITORY_ID"),
					private_key: envRef("COPILOT_PRIVATE_KEY"),
				},
			});
			expect(parsed.success, parsed.success ? "" : JSON.stringify(parsed.error.issues)).toBe(true);
		});
	});
});
