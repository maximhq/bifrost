import { describe, expect, it } from "vitest";
import { resourceCredentialResendRequired } from "./schemas";

const masked = { value: "<redacted>" };
const saved = {
	audience: "https://as.example.com",
	resource_token_endpoint: "https://as.example.com/token",
	resource_assertion_audience: "",
	resource_client_secret: masked,
};

describe("resourceCredentialResendRequired", () => {
	it("asks for the saved secret again when the target changes", () => {
		for (const edit of [
			{ audience: "https://evil.example.com" },
			{ resource_token_endpoint: "https://evil.example.com/token" },
			{ resource_assertion_audience: "https://as.example.com" },
		]) {
			expect(resourceCredentialResendRequired(saved, { ...saved, ...edit }), JSON.stringify(edit)).toBe("resource_client_secret");
		}
	});

	it("asks for a saved private key the same way", () => {
		const keyed = { ...saved, resource_client_secret: undefined, resource_private_key: masked };
		expect(resourceCredentialResendRequired(keyed, { ...keyed, audience: "https://evil.example.com" })).toBe("resource_private_key");
	});

	it("accepts a re-entered secret, a reference, or an unchanged target", () => {
		const moved = { ...saved, audience: "https://new.example.com" };
		expect(resourceCredentialResendRequired(saved, { ...moved, resource_client_secret: { value: "new-secret" } })).toBeUndefined();
		expect(resourceCredentialResendRequired(saved, { ...moved, resource_client_secret: { value: "", ref: "env.SECRET" } })).toBeUndefined();
		expect(resourceCredentialResendRequired(saved, { ...saved, audience: " https://as.example.com " })).toBeUndefined();
		expect(resourceCredentialResendRequired(saved, saved)).toBeUndefined();
	});

	it("never asks again for a credential kept as an env or vault reference", () => {
		// The API returns a reference with its resolved value masked; the server keeps the reference.
		const referenced = { ...saved, resource_client_secret: { value: "<redacted>", ref: "env.UPSTREAM_SECRET", type: "env" } };
		expect(resourceCredentialResendRequired(referenced, { ...referenced, audience: "https://new.example.com" })).toBeUndefined();
		const keyRef = { ...saved, resource_client_secret: undefined, resource_private_key: { value: "<redacted>", ref: "vault.keys/upstream", type: "vault" } };
		expect(resourceCredentialResendRequired(keyRef, { ...keyRef, audience: "https://new.example.com" })).toBeUndefined();
	});
});
