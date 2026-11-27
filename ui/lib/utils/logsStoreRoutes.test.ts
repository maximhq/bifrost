import { describe, expect, it } from "vitest";
import { canRenderWithStores, isLogsStoreOnlyRoute } from "./logsStoreRoutes";

describe("isLogsStoreOnlyRoute", () => {
	it("matches the logs-backed pages and their subpaths", () => {
		expect(isLogsStoreOnlyRoute("/workspace/logs")).toBe(true);
		expect(isLogsStoreOnlyRoute("/workspace/logs/abc")).toBe(true);
		expect(isLogsStoreOnlyRoute("/workspace/mcp-logs")).toBe(true);
		expect(isLogsStoreOnlyRoute("/workspace/dashboard")).toBe(true);
		expect(isLogsStoreOnlyRoute("/workspace/dashboard/")).toBe(true);
	});

	it("does not match other pages or lookalike prefixes", () => {
		expect(isLogsStoreOnlyRoute("/workspace/providers")).toBe(false);
		expect(isLogsStoreOnlyRoute("/workspace/config/logging")).toBe(false);
		expect(isLogsStoreOnlyRoute("/workspace/dashboards")).toBe(false);
		expect(isLogsStoreOnlyRoute("/workspace/logsx")).toBe(false);
	});
});

describe("canRenderWithStores", () => {
	it("renders every page when the config store is connected", () => {
		expect(canRenderWithStores("/workspace/providers", { isDbConnected: true, isLogsConnected: false })).toBe(true);
	});

	it("renders the dashboard in config-file-only mode when the logs store is connected", () => {
		expect(canRenderWithStores("/workspace/dashboard", { isDbConnected: false, isLogsConnected: true })).toBe(true);
		expect(canRenderWithStores("/workspace/mcp-logs", { isDbConnected: false, isLogsConnected: true })).toBe(true);
	});

	it("blocks config pages in config-file-only mode", () => {
		expect(canRenderWithStores("/workspace/providers", { isDbConnected: false, isLogsConnected: true })).toBe(false);
	});

	it("blocks the dashboard when no logs store is connected", () => {
		expect(canRenderWithStores("/workspace/dashboard", { isDbConnected: false, isLogsConnected: false })).toBe(false);
	});
});