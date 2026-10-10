import { expect, test } from "../../core/fixtures/base.fixture";
import {
  createHeadersAuthClientData,
  createOAuthClientData,
  createPerUserOAuthClientData,
} from "./mcp-registry.data";

async function completeOAuthFlow(
  page: { context: () => any; request: any },
  flow: {
    authorize_url: string;
    oauth_config_id: string;
    complete_url?: string;
    status_url?: string;
  },
) {
  const popup = await page.context().newPage();
  await popup.goto(flow.authorize_url);
  await popup.getByTestId("oauth-demo-user-input").fill("demo-user");
  await popup.getByTestId("oauth-demo-signin-btn").click();
  await popup.waitForLoadState("networkidle").catch(() => {});
  await popup.close().catch(() => {});

  const statusUrl =
    flow.status_url ?? `/api/oauth/config/${flow.oauth_config_id}/status`;
  let authorized = false;
  for (let i = 0; i < 30; i++) {
    const statusResponse = await page.request.get(statusUrl);
    if (!statusResponse.ok()) {
      await new Promise((resolve) => setTimeout(resolve, 500));
      continue;
    }
    const statusBody = await statusResponse.json().catch(() => null);
    if (statusBody?.status === "authorized") {
      authorized = true;
      break;
    }
    await new Promise((resolve) => setTimeout(resolve, 500));
  }

  expect(authorized).toBe(true);

  const completeUrl =
    flow.complete_url ??
    `/api/mcp/client/${flow.oauth_config_id}/complete-oauth`;
  const completeResponse = await page.request.post(completeUrl);
  expect(completeResponse.ok()).toBe(true);
}

// Track created clients for cleanup
const createdClients: string[] = [];

test.describe("MCP Registry - Authentication", () => {
  // MCP client creation can be slow (backend connects to MCP server); give tests room to complete
  test.setTimeout(120000);

  test.beforeEach(async ({ mcpRegistryPage }) => {
    await mcpRegistryPage.goto();
  });

  test.afterEach(async ({ mcpRegistryPage }) => {
    const toClean = [...createdClients];
    createdClients.length = 0;
    if (toClean.length > 0) {
      await mcpRegistryPage.cleanupMCPClients(toClean);
    }
  });

  test.describe("MCP Header Authentication", () => {
    test("should display header fields when headers auth type is selected", async ({
      mcpRegistryPage,
    }) => {
      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();

      await mcpRegistryPage.selectAuthType("headers");

      const headersTable = mcpRegistryPage.page.locator(
        '[data-testid="mcp-headers-table"]',
      );
      await expect(headersTable).toBeVisible();

      await mcpRegistryPage.cancelCreation();
    });

    test("should create MCP client with header auth and connect to auth-demo-server", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createHeadersAuthClientData();
      createdClients.push(clientData.name);

      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true);

      const exists = await mcpRegistryPage.clientExists(clientData.name);
      expect(exists).toBe(true);

      // Server requires X-API-Key — should connect and expose tools (public_info, secret_data)
      await mcpRegistryPage.viewClientDetails(clientData.name);
      const toolsCount = await mcpRegistryPage.getToolsCount();
      expect(toolsCount).toBeGreaterThanOrEqual(2);

      await mcpRegistryPage.closeDetailSheet();
    });
  });

  test.describe("MCP OAuth 2.0", () => {
    test("should display OAuth fields when OAuth 2.0 auth type is selected", async ({
      mcpRegistryPage,
    }) => {
      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();

      await mcpRegistryPage.selectAuthType("oauth");
      await mcpRegistryPage.expandOAuthAdvancedIfCollapsed();

      // All fields are optional — auto-discovered from server metadata
      await expect(mcpRegistryPage.oauthClientIdInput).toBeVisible();
      await expect(mcpRegistryPage.oauthClientSecretInput).toBeVisible();
      await expect(mcpRegistryPage.oauthAuthorizeUrlInput).toBeVisible();
      await expect(mcpRegistryPage.oauthTokenUrlInput).toBeVisible();

      await mcpRegistryPage.cancelCreation();
    });

    test("should create OAuth 2.0 client and complete authorization flow", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createOAuthClientData();
      createdClients.push(clientData.name);

      const flow = await mcpRegistryPage.createOAuthClient(clientData);
      await completeOAuthFlow(mcpRegistryPage.page, flow);
      await mcpRegistryPage.goto();

      // Client should now be connected and visible in table
      const exists = await mcpRegistryPage.clientExists(clientData.name);
      expect(exists).toBe(true);
    });
  });

  test.describe("MCP Per-User OAuth 2.0", () => {
    test("should display OAuth fields when Per-User OAuth 2.0 auth type is selected", async ({
      mcpRegistryPage,
    }) => {
      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();

      await mcpRegistryPage.selectAuthType("per_user_oauth");
      await mcpRegistryPage.expandOAuthAdvancedIfCollapsed();

      await expect(mcpRegistryPage.oauthClientIdInput).toBeVisible();
      await expect(mcpRegistryPage.oauthClientSecretInput).toBeVisible();
      await expect(mcpRegistryPage.oauthAuthorizeUrlInput).toBeVisible();
      await expect(mcpRegistryPage.oauthTokenUrlInput).toBeVisible();

      await mcpRegistryPage.cancelCreation();
    });

    test("should create Per-User OAuth 2.0 client and complete authorization flow", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createPerUserOAuthClientData();
      createdClients.push(clientData.name);

      const flow = await mcpRegistryPage.createOAuthClient(clientData);
      await completeOAuthFlow(mcpRegistryPage.page, flow);
      await mcpRegistryPage.goto();

      // Client should be visible in table
      const exists = await mcpRegistryPage.clientExists(clientData.name);
      expect(exists).toBe(true);
    });
  });
});