import { describe, expect, it } from "vitest";
import { BaseProviderNames } from "../types/config";
import {
	getPasswordPolicyFailures,
	hasCopilotApiToken,
	isRedacted,
	isRequestTypeDisabled,
	isSecretReference,
	isValidBaseURL,
	isValidVertexAuthCredentials,
} from "./validation";

describe("isRedacted", () => {
	it.each(["<redacted>", "<REDACTED>", "[redacted]", "[REDACTED]"])("recognizes the backend sentinel %s", (value) => {
		expect(isRedacted(value)).toBe(true);
	});

	it("does not classify a real password as redacted", () => {
		expect(isRedacted("StrongPassword1!")).toBe(false);
	});
});

describe("getPasswordPolicyFailures", () => {
	it.each([
		["<redacted>", ["at least 12 characters", "one uppercase letter", "one number"]],
		["[REDACTED]", ["at least 12 characters", "one lowercase letter", "one number"]],
	])("validates a newly entered sentinel %s", (password, expectedFailures) => {
		expect(getPasswordPolicyFailures(password, false)).toEqual(expectedFailures);
	});

	it.each(["<redacted>", "[REDACTED]"])("skips an unchanged server sentinel %s", (password) => {
		expect(getPasswordPolicyFailures(password, true)).toEqual([]);
	});

	it("accepts a strong newly entered password", () => {
		expect(getPasswordPolicyFailures("StrongPassword1!", false)).toEqual([]);
	});
});
describe("hasCopilotApiToken", () => {
	// key.value is read as a bare string in some places in the provider form and as a
	// SecretVar object in others, so a helper that only understands one shape reports "no
	// token" for a key that has one, and the App-credential labels then contradict the
	// section note telling the operator they can leave those fields blank.
	it("recognises a bare string token", () => {
		expect(hasCopilotApiToken("tid=abc")).toBe(true);
	});

	it("recognises a SecretVar literal token", () => {
		expect(hasCopilotApiToken({ value: "tid=abc", ref: "" })).toBe(true);
	});

	it("recognises a SecretVar reference token", () => {
		expect(hasCopilotApiToken({ value: "", ref: "COPILOT_TOKEN", type: "env" })).toBe(true);
	});

	it.each([undefined, null, "", "   ", { value: "", ref: "" }, { value: "  ", ref: "  " }, {}])("treats %p as no token", (input) => {
		expect(hasCopilotApiToken(input as never)).toBe(false);
	});
});
describe("isValidVertexAuthCredentials", () => {
	it("accepts every Google credential JSON type the backend allowlists", () => {
		for (const type of [
			"service_account",
			"impersonated_service_account",
			"authorized_user",
			"external_account",
			"external_account_authorized_user",
		]) {
			expect(isValidVertexAuthCredentials(JSON.stringify({ type })), type).toBe(true);
		}
	});

	it("accepts a Workload Identity Federation credential config", () => {
		const wif = JSON.stringify({
			type: "external_account",
			audience: "//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/eks/providers/oidc",
			subject_token_type: "urn:ietf:params:oauth:token-type:jwt",
			token_url: "https://sts.googleapis.com/v1/token",
			credential_source: { file: "/var/run/secrets/tokens/gcp-token" },
		});
		expect(isValidVertexAuthCredentials(wif)).toBe(true);
	});

	it("rejects JSON without a recognised type, non-JSON, and empty input", () => {
		expect(isValidVertexAuthCredentials(JSON.stringify({ type: "api_key" }))).toBe(false);
		expect(isValidVertexAuthCredentials(JSON.stringify({ project_id: "p" }))).toBe(false);
		expect(isValidVertexAuthCredentials("not json")).toBe(false);
		expect(isValidVertexAuthCredentials("   ")).toBe(false);
	});

	it("accepts references and masked previews without parsing them", () => {
		expect(isValidVertexAuthCredentials("env.VERTEX_CREDENTIALS")).toBe(true);
		expect(isValidVertexAuthCredentials("vault.secret/vertex")).toBe(true);
	});
});

describe("isRequestTypeDisabled", () => {
	it("offers typesafe as a custom provider base format", () => {
		expect(BaseProviderNames).toContain("typesafe");
	});

	it("enables only decisions and list models for a typesafe base", () => {
		expect(isRequestTypeDisabled("typesafe", "decisions")).toBe(false);
		expect(isRequestTypeDisabled("typesafe", "list_models")).toBe(false);
		expect(isRequestTypeDisabled("typesafe", "chat_completion")).toBe(true);
		expect(isRequestTypeDisabled("typesafe", "embedding")).toBe(true);
	});

	it("offers live sessions on an openai base, which is the one provider that serves them", () => {
		expect(isRequestTypeDisabled("openai", "live")).toBe(false);
		expect(isRequestTypeDisabled("anthropic", "live")).toBe(true);
	});

	it("offers decisions on an openai base, which serves them natively on /v1/decisions", () => {
		expect(isRequestTypeDisabled("openai", "decisions")).toBe(false);
	});

	it("keeps decisions off for bases that do not serve them natively", () => {
		expect(isRequestTypeDisabled("anthropic", "decisions")).toBe(true);
	});

	it("allows everything when no base format is picked", () => {
		expect(isRequestTypeDisabled(undefined, "decisions")).toBe(false);
	});
});

describe("isSecretReference", () => {
	it.each(["env.UPSTREAM_URL", "vault.bifrost/upstream/url"])("accepts %s", (value) => {
		expect(isSecretReference(value)).toBe(true);
	});

	it.each(["", "env.", "vault.", "env. spaced", "environment", "https://env.example.com"])("rejects %s", (value) => {
		expect(isSecretReference(value)).toBe(false);
	});
});

describe("isValidBaseURL", () => {
	it.each([
		"https://api.example.com",
		"http://localhost:8000",
		"https://vllm-endpoint:8000/v1",
		"env.UPSTREAM_URL",
		"vault.bifrost/upstream/url",
	])("accepts %s", (value) => {
		expect(isValidBaseURL(value)).toBe(true);
	});

	it.each(["", "api.example.com", "ftp://files.example.com", "env.", "not a url", "https://"])("rejects %s", (value) => {
		expect(isValidBaseURL(value)).toBe(false);
	});
});