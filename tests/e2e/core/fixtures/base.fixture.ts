import { test as base, expect } from "@playwright/test";
import { existsSync } from "fs";
import { readFile } from "fs/promises";
import { extname, resolve } from "path";
import { waitForNetworkIdle } from "../utils/test-helpers";
import { SidebarPage } from "../pages/sidebar.page";
import { ProvidersPage } from "../../features/providers/pages/providers.page";
import { ProviderSettingsPage } from "../../features/providers/pages/providerSettings.page";
import { VirtualKeysPage } from "../../features/virtual-keys/pages/virtual-keys.page";
import { VirtualKeysManagementPage } from "../../features/virtual-keys/pages/virtualKeysManagement.page";
import { DashboardPage } from "../../features/dashboard/pages/dashboard.page";
import { LogsPage } from "../../features/logs/pages/logs.page";
import { MCPLogsPage } from "../../features/mcp-logs/pages/mcp-logs.page";
import { RoutingRulesPage } from "../../features/routing-rules/pages/routing-rules.page";
import { MCPRegistryPage } from "../../features/mcp-registry/pages/mcp-registry.page";
import { PluginsPage } from "../../features/plugins/pages/plugins.page";
import { ObservabilityPage } from "../../features/observability/pages/observability.page";
import { ConfigSettingsPage } from "../../features/config/pages/config-settings.page";
import { GovernancePage } from "../../features/governance/pages/governance.page";
import { MCPAuthConfigPage } from "../../features/mcp-auth-config/pages/mcp-auth-config.page";
import { MCPSettingsPage } from "../../features/mcp-settings/pages/mcp-settings.page";
import { MCPToolGroupsPage } from "../../features/mcp-tool-groups/pages/mcp-tool-groups.page";
import { ModelLimitsPage } from "../../features/model-limits/pages/model-limits.page";

/**
 * Custom test fixtures type
 */
type BifrostFixtures = {
	serveMonacoLocally: void;
	closeDevProfiler: void;
	handleLoginRedirect: void;
	skipAutoLogin: boolean;
	sidebarPage: SidebarPage;
	providersPage: ProvidersPage;
	providerSettingsPage: ProviderSettingsPage;
	virtualKeysPage: VirtualKeysPage;
	virtualKeysManagementPage: VirtualKeysManagementPage;
	dashboardPage: DashboardPage;
	logsPage: LogsPage;
	mcpLogsPage: MCPLogsPage;
	routingRulesPage: RoutingRulesPage;
	mcpRegistryPage: MCPRegistryPage;
	pluginsPage: PluginsPage;
	observabilityPage: ObservabilityPage;
	configSettingsPage: ConfigSettingsPage;
	governancePage: GovernancePage;
	modelLimitsPage: ModelLimitsPage;
	mcpSettingsPage: MCPSettingsPage;
	mcpToolGroupsPage: MCPToolGroupsPage;
	mcpAuthConfigPage: MCPAuthConfigPage;
};

/**
 * Extended test with Bifrost-specific fixtures
 */
// The UI's code editor loads Monaco from cdn.jsdelivr.net at runtime; under parallel runs
// that download sometimes stalls and the editor never leaves its spinner. Serve the copy the
// UI build already installed instead. The CDN version can differ, which only matters for
// tests that depend on editor features rather than the rendered text.
const MONACO_VS_DIR = resolve(__dirname, "../../../../ui/node_modules/monaco-editor/min/vs");
const MONACO_CDN = /^https:\/\/cdn\.jsdelivr\.net\/npm\/monaco-editor@[^/]+\/min\/vs\/(.+)$/;
const MONACO_CONTENT_TYPES: Record<string, string> = {
	".js": "application/javascript",
	".css": "text/css",
	".ttf": "font/ttf",
};

export const test = base.extend<BifrostFixtures>({
	serveMonacoLocally: [
		async ({ context }, use) => {
			if (existsSync(MONACO_VS_DIR)) {
				await context.route(MONACO_CDN, async (route) => {
					const rel = MONACO_CDN.exec(route.request().url().split("?")[0])?.[1] ?? "";
					const file = resolve(MONACO_VS_DIR, rel);
					// Anything not in the local copy (or outside it) still comes from the CDN.
					if (!file.startsWith(MONACO_VS_DIR + "/") || !existsSync(file)) return route.continue();
					await route.fulfill({
						body: await readFile(file),
						contentType: MONACO_CONTENT_TYPES[extname(file)] ?? "application/octet-stream",
						headers: { "access-control-allow-origin": "*" },
					});
				});
			}
			await use();
		},
		{ auto: true },
	],

	closeDevProfiler: [
		async ({ page }, use) => {
			// Keep the development profiler from stealing focus or blocking assertions when
			// tests reuse a manually started dev server that was not launched with
			// BIFROST_DISABLE_PROFILER=1.
			await page.addInitScript(() => {
				window.localStorage.setItem("devProfiler.isVisible", "false");
				window.localStorage.setItem("devProfiler.isExpanded", "false");
				// Toasts retired by BasePage.waitForToastsToDisappear vanish at once.
				document.addEventListener("DOMContentLoaded", () => {
					const style = document.createElement("style");
					style.textContent = "[data-e2e-dismissed]{display:none!important}";
					document.head.appendChild(style);
				});
			});

			await page.addLocatorHandler(
				page.getByText("Dev Profiler", { exact: true }),
				async () => {
					await page.evaluate(() => {
						window.localStorage.setItem("devProfiler.isVisible", "false");
						window.localStorage.setItem("devProfiler.isExpanded", "false");
					});
					await page
						.locator('button[title="Dismiss"]')
						.click({ force: true, timeout: 1000 })
						.catch(() => {});
				},
				{ noWaitAfter: true },
			);
			await use();
		},
		{ auto: true },
	],

	skipAutoLogin: [false, { option: true }],


	handleLoginRedirect: [
		async ({ page, skipAutoLogin }, use) => {
			// Any test can hit an auth wall: dashboard auth redirects to /login (via a
			// baseApi 401 handler) whenever the session is missing/expired. Transparently
			// complete the login flow whenever that happens so feature tests don't each
			// need their own auth handling.
			if (skipAutoLogin) {
				await use();
				return;
			}
			await page.addLocatorHandler(
				page.locator("#username"),
				async () => {
					const username = process.env.BIFROST_ADMIN_USERNAME;
					const password = process.env.BIFROST_ADMIN_PASSWORD;
					if (!username || !password) {
						throw new Error(
							"Redirected to /login but BIFROST_ADMIN_USERNAME/BIFROST_ADMIN_PASSWORD are not set.",
						);
					}
					// The login form always redirects to /workspace on success, ignoring
					// ?goto=; capture it here and navigate back so the test lands on the
					// page it originally asked for.
					const goto = new URL(page.url()).searchParams.get("goto");
					await page.locator("#username").fill(username);
					await page.locator("#password").fill(password);
					await page.getByRole("button", { name: /Sign in/i }).click();
					await page.waitForURL((url) => !url.pathname.startsWith("/login"), { timeout: 15000 });
					if (goto) {
						await page.goto(goto);
					}
					await waitForNetworkIdle(page);
				},
			);
			await use();
		},
		{ auto: true },
	],

	sidebarPage: async ({ page }, use) => {
		await use(new SidebarPage(page));
	},

	providersPage: async ({ page }, use) => {
		await use(new ProvidersPage(page));
	},

	providerSettingsPage: async ({ page }, use) => {
		await use(new ProviderSettingsPage(page));
	},

	virtualKeysPage: async ({ page }, use) => {
		await use(new VirtualKeysPage(page));
	},

	virtualKeysManagementPage: async ({ page }, use) => {
		await use(new VirtualKeysManagementPage(page));
	},

	dashboardPage: async ({ page }, use) => {
		await use(new DashboardPage(page));
	},

	logsPage: async ({ page }, use) => {
		await use(new LogsPage(page));
	},

	mcpLogsPage: async ({ page }, use) => {
		await use(new MCPLogsPage(page));
	},

	routingRulesPage: async ({ page }, use) => {
		await use(new RoutingRulesPage(page));
	},


	mcpRegistryPage: async ({ page }, use) => {
		await use(new MCPRegistryPage(page));
	},

	pluginsPage: async ({ page }, use) => {
		await use(new PluginsPage(page));
	},

	observabilityPage: async ({ page }, use) => {
		await use(new ObservabilityPage(page));
	},

	configSettingsPage: async ({ page }, use) => {
		await use(new ConfigSettingsPage(page));
	},

	governancePage: async ({ page }, use) => {
		await use(new GovernancePage(page));
	},

	modelLimitsPage: async ({ page }, use) => {
		await use(new ModelLimitsPage(page));
	},

	mcpSettingsPage: async ({ page }, use) => {
		await use(new MCPSettingsPage(page));
	},

	mcpToolGroupsPage: async ({ page }, use) => {
		await use(new MCPToolGroupsPage(page));
	},

	mcpAuthConfigPage: async ({ page }, use) => {
		await use(new MCPAuthConfigPage(page));
	},
});

export { expect };