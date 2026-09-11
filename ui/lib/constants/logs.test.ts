import { describe, expect, it } from "vitest";

import { mapAppToClientApp, mapUserAgentToApp, RequestTypeColors, RequestTypeLabels, RequestTypes } from "./logs";

describe("logs constants", () => {
	it("recognizes Cowork independently of Claude Code", () => {
		expect(mapUserAgentToApp("claude-cowork/1.49585.0").name).toBe("Claude Cowork");
		expect(mapUserAgentToApp("Claude-Cowork/0.1").name).toBe("Claude Cowork");
		expect(mapAppToClientApp("Claude Cowork").icon).toBe("/images/claude-desktop.png");
	});
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
});