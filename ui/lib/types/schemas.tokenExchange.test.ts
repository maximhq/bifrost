import { describe, expect, it } from "vitest";
import {
	clientCredentialsIssues,
	clientCredentialsResendRequired,
	hasPrivateKeySettingsWithoutKey,
	hasSendMethodWithoutSecret,
	idjagUpstreamAuth,
	resourceCredentialResendRequired,
} from "./schemas";

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
		const keyRef = {
			...saved,
			resource_client_secret: undefined,
			resource_private_key: { value: "<redacted>", ref: "vault.keys/upstream", type: "vault" },
		};
		expect(resourceCredentialResendRequired(keyRef, { ...keyRef, audience: "https://new.example.com" })).toBeUndefined();
	});
});

describe("upstream client authentication checks", () => {
	it("check the id_jag profile only", () => {
		const direct = { profile: "", resource_auth_mode: "private_key" };
		expect(idjagUpstreamAuth(direct)).toBeUndefined();
		expect(hasPrivateKeySettingsWithoutKey(idjagUpstreamAuth(direct))).toBe(false);
		expect(hasPrivateKeySettingsWithoutKey(idjagUpstreamAuth({ ...direct, profile: "id_jag" }))).toBe(true);
		expect(hasSendMethodWithoutSecret(idjagUpstreamAuth({ profile: "id_jag", resource_client_auth_method: "client_secret_post" }))).toBe(
			true,
		);
	});
});

describe("clientCredentialsIssues", () => {
	const paths = (cc: Parameters<typeof clientCredentialsIssues>[0]) => clientCredentialsIssues(cc).map((i) => i.path);

	it("accepts a client ID with a secret or a private key", () => {
		expect(paths({ client_id: { value: "svc" }, client_secret: { value: "s" } })).toEqual([]);
		expect(paths({ client_id: { value: "svc" }, private_key: { ref: "env.KEY" }, signing_algorithm: "ES256" })).toEqual([]);
	});

	it("requires a client ID and one credential", () => {
		expect(paths({})).toEqual(["client_id", "client_secret"]);
		expect(paths({ client_id: { value: "svc" }, client_secret: { value: "s" }, private_key: { value: "k" } })).toEqual(["private_key"]);
	});

	it("refuses settings that belong to the other method", () => {
		expect(paths({ client_id: { value: "svc" }, auth_mode: "private_key" })).toEqual(["private_key"]);
		expect(paths({ client_id: { value: "svc" }, client_secret: { value: "s" }, key_id: "k1" })).toEqual(["private_key"]);
		expect(paths({ client_id: { value: "svc" }, private_key: { value: "k" }, auth_method: "client_secret_post" })).toEqual([
			"client_secret",
		]);
	});

	it("requires https URLs, allowing http only on localhost", () => {
		const base = { client_id: { value: "svc" }, client_secret: { value: "s" } };
		expect(paths({ ...base, token_url: "http://as.example.com/token" })).toEqual(["token_url"]);
		expect(paths({ ...base, token_url: "http://localhost:9000/token" })).toEqual([]);
		expect(paths({ client_id: { value: "svc" }, private_key: { value: "k" }, assertion_audience: "http://as.example.com" })).toEqual([
			"assertion_audience",
		]);
	});
});

describe("clientCredentialsResendRequired", () => {
	const saved = { token_url: "https://as.example.com/token", assertion_audience: "", client_secret: masked };

	it("asks for the saved secret again when the token URL or assertion audience changes", () => {
		expect(clientCredentialsResendRequired(saved, { ...saved, token_url: "https://evil.example.com/token" })).toBe("client_secret");
		expect(clientCredentialsResendRequired(saved, { ...saved, token_url: "" })).toBe("client_secret");
		const keyed = { ...saved, client_secret: undefined, private_key: masked };
		expect(clientCredentialsResendRequired(keyed, { ...keyed, assertion_audience: "https://as.example.com" })).toBe("private_key");
	});

	it("accepts an unchanged target, a re-entered secret, or a reference", () => {
		expect(clientCredentialsResendRequired(saved, { ...saved, token_url: " https://as.example.com/token " })).toBeUndefined();
		const moved = { ...saved, token_url: "https://new.example.com/token" };
		expect(clientCredentialsResendRequired(saved, { ...moved, client_secret: { value: "new" } })).toBeUndefined();
		expect(
			clientCredentialsResendRequired(saved, { ...moved, client_secret: { value: "<redacted>", ref: "env.S", type: "env" } }),
		).toBeUndefined();
	});
});