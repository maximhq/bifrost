import { describe, expect, it } from "vitest";

import { mapAppToClientApp, mapUserAgentToApp, RequestTypeColors, RequestTypeLabels, RequestTypes } from "./logs";

describe("logs constants", () => {
	it("registers realtime turn as a known request type", () => {
		expect(RequestTypes).toContain("realtime.turn");
		expect(RequestTypeLabels["realtime.turn"]).toBe("Realtime Turn");
		expect(RequestTypeColors["realtime.turn"]).toBeTruthy();
	});

	it("maps backend app names to display metadata", () => {
		expect(mapAppToClientApp("Claude Code").name).toBe("Claude Code");
		expect(mapAppToClientApp("Claude Code").icon).toBe("/images/claude-code.png");
		expect(mapAppToClientApp("Claude Chat Web").icon).toBe("/images/claude-desktop.png");
		expect(mapAppToClientApp("Custom App").name).toBe("Custom App");
	});

	it("maps versioned user agents as a fallback for older rows", () => {
		expect(mapUserAgentToApp("claude-cli/2.1.168 (external, cli)").name).toBe("Claude Code");
	});
	it("maps the shared OpenCode desktop and TUI user agent", () => {
		expect(mapUserAgentToApp("opencode/1.17.10 ai-sdk/provider-utils/3.0.20 runtime/bun/1.3.5").name).toBe("OpenCode");
		expect(mapAppToClientApp("OpenCode").icon).toBe("/images/opencode.png");
	});
});