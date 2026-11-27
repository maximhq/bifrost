import { describe, expect, it } from "vitest";
import { DefaultNetworkConfig } from "@/lib/constants/config";
import { modelProviderKeySchema } from "@/lib/types/schemas";
import { ProviderFormSchema } from "./providerForm";

// A GitHub Copilot key always carries a token: pasted, or filled in by Sign in with GitHub.
describe("github-copilot key credentials", () => {
	const form = (keyOverrides: Record<string, unknown>) => ({
		selectedProvider: "github-copilot",
		isDirty: true,
		networkConfig: DefaultNetworkConfig,
		keys: [{ id: "k1", name: "copilot", value: "", models: ["*"], weight: 1, ...keyOverrides }],
	});
	const key = (value: string, extra: Record<string, unknown> = {}) =>
		modelProviderKeySchema.safeParse({ id: "k1", name: "copilot", value: { value, ref: "" }, models: ["*"], weight: 1, ...extra });

	it("accepts a token", () => {
		expect(ProviderFormSchema.safeParse(form({ value: "github_pat_abc" })).success).toBe(true);
		expect(key("ghu_signed_in").success).toBe(true);
	});

	it("rejects a key without a token", () => {
		expect(ProviderFormSchema.safeParse(form({})).success).toBe(false);
		expect(key("").success).toBe(false);
	});

	it("does not treat a leftover sign-in config as a credential", () => {
		expect(key("", { github_copilot_key_config: { oauth_client_id: { value: "Iv23liAbC", ref: "" } } }).success).toBe(false);
	});
});