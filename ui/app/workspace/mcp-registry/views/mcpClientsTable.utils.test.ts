import { describe, expect, it } from "vitest";
import { canReconnectMCPClient } from "./mcpClientsTable.utils";

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
