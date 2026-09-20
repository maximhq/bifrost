import { describe, expect, it } from "vitest";

import { mapAppToClientApp, mapUserAgentToApp, RequestTypeColors, RequestTypeLabels, RequestTypes, RoutingEngineUsedLabels } from "./logs";

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

	it("registers session-affinity as a routing engine", () => {
		expect(RoutingEngineUsedLabels["session-affinity"]).toBe("Session");
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
// Edge reports stable app keys, while gateway logs may contain display names.
describe("Edge app icon identity", () => {
	it.each(["claude-code", "codex-cli", "codex-desktop", "cursor", "opencode"])(
		"resolves %s to the same icon as its display name",
		(key) => {
			const app = mapAppToClientApp(key);
			expect(app.icon).toBeTruthy();
			expect(mapAppToClientApp(app.name)).toEqual(app);
		},
	);
});