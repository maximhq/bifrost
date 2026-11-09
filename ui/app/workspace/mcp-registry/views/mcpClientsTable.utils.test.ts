import { beforeAll, describe, expect, it } from "vitest";
import i18n from "@/lib/i18n";
import { authScopeLabel, authScopeOf, canReconnectMCPClient, exchangeVerifyCopy } from "./mcpClientsTable.utils";

beforeAll(async () => {
	await i18n.changeLanguage("en");
});

describe("authScopeOf", () => {
	it("puts per-user OAuth and per-user headers per user, and every other auth type shared", () => {
		expect(authScopeOf("per_user_oauth")).toBe("per_user");
		expect(authScopeOf("per_user_headers")).toBe("per_user");
		for (const authType of ["none", "headers", "oauth", "token_exchange", undefined] as const) {
			expect(authScopeOf(authType), String(authType)).toBe("shared");
		}
	});
});

describe("authScopeLabel", () => {
	it("names per-user OAuth, per-user headers and token exchange Per-User, since each call carries the caller's own credential", () => {
		for (const authType of ["per_user_oauth", "per_user_headers", "token_exchange"] as const) {
			expect(authScopeLabel(authType), authType).toBe("Per-User");
		}
	});

	it("names every other auth type Shared, including none and a missing auth type", () => {
		for (const authType of ["none", "headers", "oauth", undefined] as const) {
			expect(authScopeLabel(authType), String(authType)).toBe("Shared");
		}
	});
});

describe("exchangeVerifyCopy", () => {
	it("describes a verification that keeps nothing, for a server with per-caller discovery", () => {
		for (const state of ["pending_verification", "healthy"] as const) {
			const copy = exchangeVerifyCopy({ state, config: { name: "notes", per_user_discovery: true } });
			expect(copy.description, state).not.toContain("discovery credential");
			expect(copy.body, state).toBe("This exchanges your own signed-in identity to verify notes.");
			expect(copy.detail, state).toContain("only verifies it and is not kept");
		}
	});

	it("describes the discovery credential Bifrost keeps, for any other server", () => {
		const pending = exchangeVerifyCopy({ state: "pending_verification", config: { name: "notes" } });
		expect(pending.title).toBe("Verify as me");
		expect(pending.description).toBe("Establish Bifrost's discovery credential using your identity.");
		expect(pending.body).toBe("This exchanges your own signed-in identity to establish Bifrost's discovery credential for notes.");
		expect(pending.detail).not.toContain("badge");

		const verified = exchangeVerifyCopy({ state: "healthy", config: { name: "notes", per_user_discovery: false } });
		expect(verified.title).toBe("Re-verify as me");
		expect(verified.description).toBe("Renew Bifrost's own discovery credential using your identity.");
		expect(verified.detail).toContain("the credential badge shows it's expired");
	});
});

describe("canReconnectMCPClient", () => {
	it("is false for per_user_oauth (always per-call)", () => {
		expect(canReconnectMCPClient({ auth_type: "per_user_oauth", connection_type: "http", needs_session_stickiness: true })).toBe(false);
	});

	it("is false for per_user_headers (always per-call)", () => {
		expect(canReconnectMCPClient({ auth_type: "per_user_headers", connection_type: "http" })).toBe(false);
	});

	it("is false for token_exchange (always per-call)", () => {
		expect(canReconnectMCPClient({ auth_type: "token_exchange", connection_type: "http" })).toBe(false);
	});

	it("is false for a shared oauth http client with needs_session_stickiness omitted (default per-call)", () => {
		expect(canReconnectMCPClient({ auth_type: "oauth", connection_type: "http" })).toBe(false);
	});

	it("is false for a shared oauth http client with needs_session_stickiness explicitly false", () => {
		expect(canReconnectMCPClient({ auth_type: "oauth", connection_type: "http", needs_session_stickiness: false })).toBe(false);
	});

	it("is false for a shared headers http client with needs_session_stickiness omitted", () => {
		expect(canReconnectMCPClient({ auth_type: "headers", connection_type: "http" })).toBe(false);
	});

	it("is true for a shared oauth http client with needs_session_stickiness explicitly true", () => {
		expect(canReconnectMCPClient({ auth_type: "oauth", connection_type: "http", needs_session_stickiness: true })).toBe(true);
	});

	it("is true for a shared headers http client with needs_session_stickiness explicitly true", () => {
		expect(canReconnectMCPClient({ auth_type: "headers", connection_type: "http", needs_session_stickiness: true })).toBe(true);
	});

	it("is true for a shared oauth SSE client regardless of needs_session_stickiness (non-http is always sticky)", () => {
		expect(canReconnectMCPClient({ auth_type: "oauth", connection_type: "sse", needs_session_stickiness: false })).toBe(true);
	});

	it("is true for a shared headers STDIO client regardless of needs_session_stickiness (non-http is always sticky)", () => {
		expect(canReconnectMCPClient({ auth_type: "headers", connection_type: "stdio" })).toBe(true);
	});

	it("is true for auth_type none on a non-http connection", () => {
		expect(canReconnectMCPClient({ auth_type: "none", connection_type: "stdio" })).toBe(true);
	});

	it("is false for auth_type none on http with stickiness off", () => {
		expect(canReconnectMCPClient({ auth_type: "none", connection_type: "http" })).toBe(false);
	});
});