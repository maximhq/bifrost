import { describe, expect, it } from "vitest";
import { clientCredentialsFormValues, clientCredentialsPayload } from "./clientCredentialsFields";

describe("clientCredentialsPayload", () => {
	it("sends only the chosen method's fields", () => {
		const secret = clientCredentialsPayload({
			client_id: { value: "svc" },
			client_secret: { value: "s" },
			auth_method: "client_secret_post",
			key_id: "left-over",
			signing_algorithm: "ES256",
			scopes_text: "mcp:read, mcp:write, ",
			auth_mode: "secret",
		});
		expect(secret).toEqual({
			token_url: undefined,
			client_id: { value: "svc" },
			client_secret: { value: "s" },
			auth_method: "client_secret_post",
			private_key: undefined,
			key_id: undefined,
			signing_algorithm: undefined,
			assertion_audience: undefined,
			scopes: ["mcp:read", "mcp:write"],
			resource: undefined,
		});

		const keyed = clientCredentialsPayload({
			client_id: { value: "svc" },
			private_key: { ref: "env.KEY" },
			client_secret: { value: "s" },
			key_id: " k1 ",
			token_url: " https://as.example.com/token ",
		});
		expect(keyed.client_secret).toBeUndefined();
		expect(keyed.private_key).toEqual({ ref: "env.KEY" });
		expect(keyed.key_id).toBe("k1");
		expect(keyed.signing_algorithm).toBeUndefined();
		expect(keyed.token_url).toBe("https://as.example.com/token");
		expect(keyed.scopes).toBeUndefined();
	});

	it("keeps an omitted resource apart from the default", () => {
		expect(clientCredentialsPayload({ resource: " " }).resource).toBeUndefined();
		expect(clientCredentialsPayload({ resource: "https://api.example.com" }).resource).toBe("https://api.example.com");
		expect(clientCredentialsPayload({ resource: "https://api.example.com", omit_resource: true }).resource).toBe("");
	});
});

describe("clientCredentialsFormValues", () => {
	it("round-trips a stored block through the form", () => {
		const stored = {
			token_url: "https://as.example.com/token",
			client_id: { value: "<redacted>" },
			private_key: { value: "<redacted>" },
			signing_algorithm: "ES256",
			scopes: ["mcp:read"],
			resource: "",
		};
		const values = clientCredentialsFormValues(stored);
		expect(values).toMatchObject({ auth_mode: "private_key", scopes_text: "mcp:read", omit_resource: true });
		expect(clientCredentialsPayload(values)).toMatchObject({ ...stored, scopes: ["mcp:read"] });
	});
});