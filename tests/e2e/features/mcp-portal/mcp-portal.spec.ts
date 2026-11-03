import { expect, test } from "../../core/fixtures/base.fixture";
import {
  createPortalKeyData,
  createPortalOAuthServerData,
  createPortalServerData,
} from "./mcp-portal.data";

const createdClients: string[] = [];
const createdKeys: string[] = [];

test.describe("MCP Portal", () => {
  test.afterEach(async ({ request }) => {
    for (const id of createdKeys.splice(0)) {
      const response = await request.delete(`/api/governance/virtual-keys/${id}`);
      if (!response.ok()) console.error(`Portal key cleanup failed: ${await response.text()}`);
    }
    for (const id of createdClients.splice(0)) {
      const response = await request.delete(`/api/mcp/client/${id}`);
      if (!response.ok()) console.error(`Portal client cleanup failed: ${await response.text()}`);
    }
  });

  test("configures API keys for one virtual key without configuring another", async ({
    request,
    mcpPortalPage,
  }) => {
    const server = createPortalServerData();
    createdClients.push(server.client_id);
    const created = await request.post("/api/mcp/client", { data: server });
    expect(created.ok(), await created.text()).toBe(true);
    const keyResponse = await request.post("/api/governance/virtual-keys", {
      data: createPortalKeyData(server.name),
    });
    expect(keyResponse.ok(), await keyResponse.text()).toBe(true);
    const { virtual_key: firstKey } = await keyResponse.json();
    createdKeys.push(firstKey.id);
    const secondResponse = await request.post("/api/governance/virtual-keys", {
      data: createPortalKeyData(server.name),
    });
    expect(secondResponse.ok(), await secondResponse.text()).toBe(true);
    const { virtual_key: secondKey } = await secondResponse.json();
    createdKeys.push(secondKey.id);

    await mcpPortalPage.goto(firstKey.id);
    await expect(mcpPortalPage.server(server.client_id)).toContainText(server.name);
    await expect(mcpPortalPage.status(server.client_id)).toHaveText("Not configured");
    await expect(
      mcpPortalPage.page.getByTestId(`mcp-portal-required-headers-${server.client_id}`),
    ).toContainText("x-api-key");
    const popup = await mcpPortalPage.configure(server.client_id);
    await expect(
      popup.getByRole("heading", { name: `Submit credentials for ${server.name}` }),
    ).toBeVisible();
    await popup.getByTestId("mcp-headers-submit-input-x-api-key").fill("portal-user-secret");
    await popup.getByTestId("mcp-headers-submit-submit-btn").click();
    await expect(popup.getByRole("heading", { name: "Headers saved" })).toBeVisible();
    await popup.close();
    await mcpPortalPage.refresh();
    await expect(mcpPortalPage.status(server.client_id)).toHaveText("Ready");
    await expect(mcpPortalPage.server(server.client_id)).not.toContainText("portal-user-secret");
    const list = await request.get(`/api/mcp/portal?virtual_key_id=${firstKey.id}`);
    expect(list.ok(), await list.text()).toBe(true);
    expect(await list.text()).not.toContain("portal-user-secret");

    await mcpPortalPage.goto(secondKey.id);
    await expect(mcpPortalPage.page.getByTestId("mcp-portal-identity")).toContainText(
      secondKey.name,
    );
    await expect(mcpPortalPage.status(server.client_id)).toHaveText("Not configured");
  });

  test("does not list or configure a server explicitly denied to the selected key", async ({
    request,
    mcpPortalPage,
  }) => {
    const server = createPortalServerData();
    createdClients.push(server.client_id);
    const created = await request.post("/api/mcp/client", { data: server });
    expect(created.ok(), await created.text()).toBe(true);
    const keyResponse = await request.post("/api/governance/virtual-keys", {
      data: createPortalKeyData(server.name, []),
    });
    expect(keyResponse.ok(), await keyResponse.text()).toBe(true);
    const { virtual_key: key } = await keyResponse.json();
    createdKeys.push(key.id);
    await mcpPortalPage.goto(key.id);
    await expect(mcpPortalPage.page.getByTestId("mcp-portal-identity")).toContainText(key.name);
    await expect(mcpPortalPage.server(server.client_id)).toHaveCount(0);
    const result = await request.post(`/api/mcp/portal/${server.client_id}/authenticate`, {
      data: { virtual_key_id: key.id },
    });
    expect(result.status(), await result.text()).toBe(403);
    expect(await result.text()).not.toContain("auth_url");
  });

  test("authorizes OAuth from the portal and shows the resulting credential", async ({
    request,
    mcpPortalPage,
  }) => {
    test.setTimeout(120000);
    const server = createPortalOAuthServerData();
    createdClients.push(server.client_id);
    const created = await request.post("/api/mcp/client", { data: server });
    expect(created.ok(), await created.text()).toBe(true);
    const flow = await created.json();
    expect(flow.status).toBe("pending_oauth");
    const bootstrap = await mcpPortalPage.page.context().newPage();
    await bootstrap.goto(flow.authorize_url);
    await bootstrap.getByTestId("oauth-demo-user-input").fill("portal-admin");
    await bootstrap.getByTestId("oauth-demo-signin-btn").click();
    await bootstrap.waitForURL(/\/workspace\/mcp-registry\/oauth-callback/);
    const complete = await request.post(flow.complete_url);
    expect(complete.ok(), await complete.text()).toBe(true);
    await bootstrap.close();

    const keyResponse = await request.post("/api/governance/virtual-keys", {
      data: createPortalKeyData(server.name, ["*"]),
    });
    expect(keyResponse.ok(), await keyResponse.text()).toBe(true);
    const { virtual_key: key } = await keyResponse.json();
    createdKeys.push(key.id);
    await mcpPortalPage.goto(key.id);
    await expect(mcpPortalPage.status(server.client_id)).toHaveText("Not configured");
    const popup = await mcpPortalPage.configure(server.client_id);
    await popup.getByTestId("mcp-auth-authenticate-button").click();
    await popup.getByTestId("oauth-demo-user-input").fill("portal-end-user");
    await popup.getByTestId("oauth-demo-signin-btn").click();
    await popup.waitForURL(/\/workspace\/mcp-sessions/);
    await popup.close();
    await mcpPortalPage.refresh();
    await expect(mcpPortalPage.status(server.client_id)).toHaveText("Ready");
  });
});