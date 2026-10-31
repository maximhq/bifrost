import { describe, expect, it } from "vitest";
import i18n from "../i18n";

import {
	getRequestTypeLabel,
	mapAppToClientApp,
	mapUserAgentToApp,
	RequestTypeColors,
	RequestTypeLabels,
	RequestTypes,
	RoutingEngineUsedLabels,
} from "./logs";

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

	it("translates live request types in every supported locale", async () => {
		const translations = {
			en: { live: "Live", live_session: "Live Session", live_content: "Live Recording", live_sideband: "Live Sideband" },
			de: { live: "Live", live_session: "Live-Sitzung", live_content: "Live-Aufzeichnung", live_sideband: "Live-Sideband" },
			es: { live: "En vivo", live_session: "Sesión en vivo", live_content: "Grabación en vivo", live_sideband: "Sideband en vivo" },
			fr: {
				live: "En direct",
				live_session: "Session en direct",
				live_content: "Enregistrement en direct",
				live_sideband: "Sideband en direct",
			},
			it: { live: "Live", live_session: "Sessione live", live_content: "Registrazione live", live_sideband: "Sideband live" },
			ja: { live: "ライブ", live_session: "ライブセッション", live_content: "ライブ録音", live_sideband: "ライブサイドバンド" },
			ko: { live: "라이브", live_session: "라이브 세션", live_content: "라이브 녹음", live_sideband: "라이브 사이드밴드" },
			pt: { live: "Ao vivo", live_session: "Sessão ao vivo", live_content: "Gravação ao vivo", live_sideband: "Sideband ao vivo" },
			ru: { live: "Прямой эфир", live_session: "Живая сессия", live_content: "Запись эфира", live_sideband: "Живой sideband" },
			"zh-CN": { live: "实时", live_session: "实时会话", live_content: "实时录音", live_sideband: "实时旁路" },
			"zh-TW": { live: "即時", live_session: "即時工作階段", live_content: "即時錄音", live_sideband: "即時旁路" },
		} as const;

		try {
			for (const [locale, labels] of Object.entries(translations)) {
				await i18n.changeLanguage(locale);
				const t = i18n.getFixedT(locale, "observability");
				for (const [key, label] of Object.entries(labels)) {
					expect(i18n.getResource(locale, "observability", `requestTypes.${key}`), `${locale} ${key} resource`).toBe(label);
					const requestType = key === "live_session" ? "live.session" : key;
					expect(getRequestTypeLabel(requestType, t), `${locale} ${requestType} label`).toBe(label);
				}
			}
		} finally {
			await i18n.changeLanguage("en");
		}
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