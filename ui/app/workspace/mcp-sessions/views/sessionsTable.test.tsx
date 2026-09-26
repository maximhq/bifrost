import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import i18n from "@/lib/i18n";
import type { MCPSessionRow } from "@/lib/types/mcpSessions";
import SessionsTable from "./sessionsTable";

vi.mock("@/lib/store", () => ({
	getErrorMessage: String,
	useReauthMCPSessionMutation: () => [vi.fn(), { isLoading: false }],
	useRevokeMCPSessionMutation: () => [vi.fn(), { isLoading: false }],
}));

vi.mock("@/components/pageTitle", () => ({ default: () => null }));

function renderTable({
	sessions = [],
	isFetching = false,
	hasActiveFilters = false,
}: {
	sessions?: MCPSessionRow[];
	isFetching?: boolean;
	hasActiveFilters?: boolean;
} = {}) {
	return renderToStaticMarkup(
		<SessionsTable
			sessions={sessions}
			totalCount={sessions.length}
			isFetching={isFetching}
			search=""
			onSearchChange={() => {}}
			hasActiveFilters={hasActiveFilters}
			offset={0}
			limit={20}
			onOffsetChange={() => {}}
		/>,
	);
}

afterEach(async () => {
	await i18n.changeLanguage("en");
});

describe.each(["en", "zh-CN"])("MCP session refresh in %s", (locale) => {
	it("shows a loading status instead of an empty result during a fetch", async () => {
		await i18n.changeLanguage(locale);
		for (const hasActiveFilters of [false, true]) {
			const html = renderTable({ isFetching: true, hasActiveFilters });
			expect(html).toContain('role="status"');
			expect(html).toContain(locale === "en" ? "Loading…" : "加载中…");
			expect(html).not.toContain(i18n.t("sessions.empty", { ns: "mcp" }));
			expect(html).not.toContain(i18n.t("sessions.noMatch", { ns: "mcp" }));
		}
	});

	it("shows the appropriate empty result after fetching completes", async () => {
		await i18n.changeLanguage(locale);
		for (const hasActiveFilters of [false, true]) {
			const html = renderTable({ hasActiveFilters });
			expect(html).not.toContain('role="status"');
			expect(html).toContain(i18n.t(hasActiveFilters ? "sessions.noMatch" : "sessions.empty", { ns: "mcp" }));
		}
	});

	it("keeps existing rows visible while refreshing", async () => {
		await i18n.changeLanguage(locale);
		const row = {
			id: "session-1",
			kind: "header",
			auth_kind: "headers",
			auth_mode: "session",
			status: "active",
			created_at: "2026-09-01T12:00:00Z",
			mcp_client: { name: "Example MCP Server" },
		} as MCPSessionRow;
		const html = renderTable({ sessions: [row], isFetching: true });
		expect(html).toContain("Example MCP Server");
		expect(html).not.toContain('role="status"');
		expect(html).toContain(locale === "en" ? "Page 1 of 1" : "第 1 页，共 1 页");
	});
});