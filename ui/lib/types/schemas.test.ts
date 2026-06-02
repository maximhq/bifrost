import { describe, expect, it } from "vitest";

import { modelProviderKeySchema } from "./schemas";

describe("modelProviderKeySchema GigaChat auth", () => {
	const baseKey = {
		id: "gigachat-key",
		name: "gigachat-key",
		weight: 1,
		models: ["*"],
	};

	it("accepts bearer auth sources", () => {
		const authConfigs = [
			{
				credentials: { value: "credentials" },
			},
			{
				access_token: { value: "access-token" },
			},
			{
				user: { value: "user" },
				password: { value: "password" },
			},
		];

		for (const gigachat_key_config of authConfigs) {
			const result = modelProviderKeySchema.safeParse({
				...baseKey,
				gigachat_key_config,
			});

			expect(result.success).toBe(true);
		}
	});

	it("accepts key value as a pre-obtained access token", () => {
		const result = modelProviderKeySchema.safeParse({
			...baseKey,
			value: { value: "access-token" },
			gigachat_key_config: {
				cert_file: "/secure/client.pem",
				key_file: "/secure/client.key",
			},
		});

		expect(result.success).toBe(true);
	});

	it("rejects TLS material without bearer auth", () => {
		const result = modelProviderKeySchema.safeParse({
			...baseKey,
			gigachat_key_config: {
				cert_file: "/secure/client.pem",
				key_file: "/secure/client.key",
				ca_bundle_file: "/secure/ca.pem",
			},
		});

		expect(result.success).toBe(false);
		if (result.success) return;
		expect(result.error.issues[0]?.message).toBe(
			"GigaChat credentials, access token, user/password, or key value access token is required",
		);
	});

	it("rejects encrypted client key passwords", () => {
		const result = modelProviderKeySchema.safeParse({
			...baseKey,
			gigachat_key_config: {
				credentials: { value: "credentials" },
				cert_file: "/secure/client.pem",
				key_file: "/secure/client.key",
				key_file_password: { value: "secret" },
			},
		});

		expect(result.success).toBe(false);
		if (result.success) return;
		expect(result.error.issues[0]?.message).toBe("Encrypted GigaChat client private keys are not supported");
	});
});
