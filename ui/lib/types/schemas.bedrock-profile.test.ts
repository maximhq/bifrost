import { describe, expect, it } from "vitest";

import { bedrockKeyConfigSchema, modelProviderKeySchema } from "./schemas";

const region = { value: "us-east-1" };
const profile = { value: "team-a" };
const base = { id: "k1", name: "bedrock", models: ["*"], blacklisted_models: [], weight: 1 };

describe("Bedrock profile authentication", () => {
	it("requires a profile when the AWS Profile / SSO method is selected", () => {
		const result = bedrockKeyConfigSchema.safeParse({ region, _auth_type: "profile" });
		expect(result.success).toBe(false);
		expect(result.error?.issues.map((issue) => issue.path.join("."))).toContain("profile");
	});

	it("accepts a profile as the source identity for AssumeRole", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			bedrock_key_config: {
				region,
				profile,
				role_arn: { value: "arn:aws:iam::123456789012:role/Bedrock" },
				_auth_type: "profile",
			},
		});
		expect(result.success).toBe(true);
	});

	it("rejects a profile combined with explicit credentials", () => {
		const result = bedrockKeyConfigSchema.safeParse({
			region,
			profile,
			access_key: { value: "AKIAEXAMPLE" },
			secret_key: { value: "secret" },
		});
		expect(result.success).toBe(false);
	});

	it("rejects a profile combined with a session token", () => {
		const result = bedrockKeyConfigSchema.safeParse({
			region,
			profile,
			session_token: { value: "temporary-token" },
		});
		expect(result.success).toBe(false);
	});

	it("rejects a profile combined with a Bedrock API key", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			value: { value: "bedrock-api-key" },
			bedrock_key_config: { region, profile },
		});
		expect(result.success).toBe(false);
	});
});