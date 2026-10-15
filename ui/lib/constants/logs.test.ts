import { describe, expect, it } from "vitest";

import { mapAppToClientApp, mapUserAgentToApp, RequestTypeColors, RequestTypeLabels, RequestTypes } from "./logs";

describe("logs constants", () => {
	it.each([
		["antigravity/2.23.0", "Antigravity Desktop"],
		["Antigravity-Desktop/2.23.0", "Antigravity Desktop"],
		["antigravity-cli/1.0", "Antigravity CLI"],
		["antigravity/2.23.0 gemini/1.0", "Antigravity Desktop"],
		["antigravity-cli/1.0 antigravity/2.23.0", "Antigravity CLI"],
	])("classifies synthetic Antigravity agent %s", (userAgent, name) => {
		expect(mapUserAgentToApp(userAgent)).toEqual({ name, icon: "/images/harness/antigravity.svg" });
		expect(mapAppToClientApp(name)).toEqual(mapUserAgentToApp(userAgent));
	});
	it.each(["unrelated-antigravity-tool/1.0", "bifrost-edge/0.1.0"])("does not mislabel %s as Antigravity", (userAgent) => {
		expect(mapUserAgentToApp(userAgent).name).toBe("Other");
	});
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

	it("registers model retrieve as a known request type", () => {
		expect(RequestTypes).toContain("model_retrieve");
		expect(RequestTypeLabels.model_retrieve).toBe("Retrieve Model");
		expect(RequestTypeColors.model_retrieve).toBeTruthy();
	});

	it("registers live sessions as known request types", () => {
		for (const type of ["live", "live.session", "live_content", "live_sideband"] as const) {
			expect(RequestTypes).toContain(type);
			expect(RequestTypeLabels[type]).toBeTruthy();
			expect(RequestTypeColors[type]).toBeTruthy();
		}
		expect(RequestTypeLabels["live.session"]).toBe("Live Session");
		expect(RequestTypeLabels["live_content"]).toBe("Live Recording");
		expect(RequestTypeLabels["live_sideband"]).toBe("Live Sideband");
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
	it.each(["claude-code", "codex-cli", "codex-desktop", "cursor", "opencode", "antigravity-cli", "antigravity-desktop"])(
		"resolves %s to the same icon as its display name",
		(key) => {
			const app = mapAppToClientApp(key);
			expect(app.icon).toBeTruthy();
			expect(mapAppToClientApp(app.name)).toEqual(app);
		},
	);
});