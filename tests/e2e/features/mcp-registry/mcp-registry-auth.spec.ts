import { expect, test } from "../../core/fixtures/base.fixture";
import {
  createAWSSigV4ClientData,
  createClientCredentialsClientData,
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

    test("should register with Bifrost's client metadata document only when toggled on, and refuse a local address", async ({
      mcpRegistryPage,
    }) => {
      await mcpRegistryPage.dismissToasts();
      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();
      await mcpRegistryPage.fillClientForm(createOAuthClientData());
      await mcpRegistryPage.expandOAuthAdvancedIfCollapsed();

      // Off by default: the client ID and secret stay editable.
      await expect(mcpRegistryPage.oauthClientMetadataDocumentSwitch).toHaveAttribute("data-state", "unchecked");
      await expect(mcpRegistryPage.oauthClientIdInput).toBeEnabled();
      await mcpRegistryPage.oauthClientIdInput.fill("typed-before-toggling");

      await mcpRegistryPage.oauthClientMetadataDocumentSwitch.click();
      await expect(mcpRegistryPage.oauthClientMetadataDocumentSwitch).toHaveAttribute("data-state", "checked");
      await expect(mcpRegistryPage.oauthClientIdInput).toBeDisabled();
      await expect(mcpRegistryPage.oauthClientSecretInput).toBeDisabled();

      const responsePromise = mcpRegistryPage.page.waitForResponse(
        (response) =>
          response.url().includes("/mcp/client") && !response.url().endsWith("/mcp/clients") && response.request().method() === "POST",
        { timeout: 60000 },
      );
      await expect(mcpRegistryPage.saveBtn).toBeEnabled({ timeout: 10000 });
      await mcpRegistryPage.saveBtn.click({ force: true });
      const response = await responsePromise;

      const sent = response.request().postDataJSON();
      expect(sent.oauth_config.use_client_metadata_document).toBe(true);
      expect(sent.oauth_config.client_id?.value ?? "").toBe("");
      // The dashboard is served from localhost, which no authorization server can fetch the document from.
      expect(response.status(), await response.text()).toBe(400);
      expect(await response.text()).toContain("public HTTPS address");

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

  test.describe("MCP OAuth Client Credentials", () => {
    // A listed client_credentials server, as the API returns it: credentials masked.
    const ccServer = () => ({
      config: {
        client_id: "cc-edit-1",
        name: "cc_edit_server",
        connection_type: "http",
        connection_string: { value: "https://mcp.example.com/mcp", type: "plain_text" },
        auth_type: "client_credentials",
        client_credentials: {
          token_url: "https://as.example.com/token",
          client_id: { value: "svc-bifrost", type: "plain_text" },
          client_secret: { value: "<redacted>", type: "plain_text" },
          auth_method: "client_secret_basic",
        },
        tools_to_execute: ["*"],
      },
      tools: [],
      state: "healthy",
      vk_configs: [],
    });

    test("should create a client with a client secret, sending only the secret's fields", async ({ mcpRegistryPage }) => {
      const page = mcpRegistryPage.page;
      let sent: Record<string, any> | undefined;
      await page.route("**/api/mcp/client", async (route) => {
        if (route.request().method() !== "POST") return route.continue();
        sent = route.request().postDataJSON();
        await route.fulfill({ json: { status: "success", message: "MCP client created successfully" } });
      });

      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();
      await mcpRegistryPage.fillClientForm(createClientCredentialsClientData());
      await expect(page.getByTestId("client-credentials-fields")).toBeVisible();
      // Client credentials are shared only, so there is no scope to pick.
      await expect(page.getByTestId("auth-scope-select")).toHaveCount(0);

      // Saving without credentials is refused beside the fields.
      await mcpRegistryPage.saveBtn.click({ force: true });
      await expect(page.getByText("Client ID is required")).toBeVisible();
      await expect(page.getByText("Enter a client secret or a private key")).toBeVisible();
      expect(sent).toBeUndefined();

      await page.getByTestId("client-credentials-client-id-input").fill("svc-bifrost");
      await page.getByTestId("client-credentials-client-secret-input").fill("s3cret");
      await page.getByTestId("client-credentials-scopes-input").fill("mcp:read, mcp:write");
      await mcpRegistryPage.saveBtn.click({ force: true });

      await expect.poll(() => sent?.auth_type).toBe("client_credentials");
      expect(sent?.client_credentials).toEqual({
        client_id: { value: "svc-bifrost", ref: "" },
        client_secret: { value: "s3cret", ref: "" },
        scopes: ["mcp:read", "mcp:write"],
      });
      await expect(mcpRegistryPage.sheet).not.toBeVisible({ timeout: 10000 });
    });

    test("should switch to a private key, clearing the secret, and send the key's fields", async ({ mcpRegistryPage }) => {
      const page = mcpRegistryPage.page;
      let sent: Record<string, any> | undefined;
      await page.route("**/api/mcp/client", async (route) => {
        if (route.request().method() !== "POST") return route.continue();
        sent = route.request().postDataJSON();
        await route.fulfill({ json: { status: "success", message: "MCP client created successfully" } });
      });

      await mcpRegistryPage.createBtn.click();
      await mcpRegistryPage.fillClientForm(createClientCredentialsClientData());
      await page.getByTestId("client-credentials-client-id-input").fill("svc-bifrost");
      await page.getByTestId("client-credentials-client-secret-input").fill("typed-before-switching");

      await page.getByTestId("client-credentials-auth-method-select").click();
      await page.getByRole("option", { name: "Private key (private_key_jwt)" }).click();
      await expect(page.getByTestId("client-credentials-client-secret-input")).toHaveCount(0);
      await page.getByTestId("client-credentials-private-key-input").fill("env.CC_PRIVATE_KEY");
      await page.getByTestId("client-credentials-key-id-input").fill("key-1");
      // The algorithm defaults to "Automatic": the server chooses it from the key.
      await expect(page.getByTestId("client-credentials-signing-algorithm-select")).toContainText("Automatic");
      await page.getByTestId("client-credentials-omit-resource-switch").click();
      await mcpRegistryPage.saveBtn.click({ force: true });

      await expect.poll(() => sent?.client_credentials).toBeDefined();
      const cc = sent?.client_credentials;
      expect(cc.client_secret).toBeUndefined();
      expect(cc.auth_method).toBeUndefined();
      expect(cc.private_key).toEqual({ value: "", ref: "env.CC_PRIVATE_KEY", type: "env" });
      expect(cc.key_id).toBe("key-1");
      expect(cc.signing_algorithm).toBeUndefined();
      expect(cc.resource).toBe("");
    });

    test("should ask for the secret again when the token URL changes, then send the whole block", async ({ mcpRegistryPage }) => {
      const page = mcpRegistryPage.page;
      await page.route("**/api/mcp/clients?**", (route) =>
        route.fulfill({ json: { clients: [ccServer()], count: 1, total_count: 1, limit: 25, offset: 0 } }),
      );
      let sent: Record<string, any> | undefined;
      await page.route("**/api/mcp/client/cc-edit-1", async (route) => {
        if (route.request().method() !== "PUT") return route.continue();
        sent = route.request().postDataJSON();
        await route.fulfill({ json: { status: "success", message: "MCP client updated successfully" } });
      });
      await mcpRegistryPage.goto();
      await mcpRegistryPage.openClientEditor("cc_edit_server");
      await mcpRegistryPage.showEditorTab("authentication");

      const sheet = mcpRegistryPage.editSheet;
      const tokenUrl = sheet.getByTestId("client-credentials-token-url-input");
      await expect(tokenUrl).toHaveValue("https://as.example.com/token");
      await expect(sheet.getByTestId("client-credentials-retarget-warning")).toHaveCount(0);
      await tokenUrl.fill("https://as2.example.com/token");
      await expect(sheet.getByTestId("client-credentials-retarget-warning")).toBeVisible();

      const save = sheet.getByTestId("mcpclient-save-btn");
      await save.click();
      await expect(sheet.getByText(/Re-enter it: the token URL or assertion audience changed/)).toBeVisible();
      expect(sent).toBeUndefined();

      await sheet.getByTestId("client-credentials-client-secret-input").fill("rotated-secret");
      await save.click();
      await expect.poll(() => sent?.client_credentials).toBeDefined();
      expect(sent?.client_credentials).toMatchObject({
        token_url: "https://as2.example.com/token",
        client_id: { value: "svc-bifrost" },
        client_secret: { value: "rotated-secret" },
        auth_method: "client_secret_basic",
      });
    });
  });

  test.describe("MCP AWS SigV4", () => {
    // A listed aws_sigv4 server, as the API returns it: the env-backed region is masked.
    const awsServer = () => ({
      config: {
        client_id: "aws-edit-1",
        name: "aws_edit_server",
        connection_type: "http",
        connection_string: { value: "https://gw-1.gateway.bedrock-agentcore.us-east-1.amazonaws.com/mcp", type: "plain_text" },
        auth_type: "aws_sigv4",
        aws_sigv4: {
          region: { value: "us-e****t-1", ref: "env.AWS_REGION", type: "env" },
          service: "bedrock-agentcore",
          role_arn: { value: "arn:aws:iam::123456789012:role/gateway", type: "plain_text" },
        },
        tools_to_execute: ["*"],
      },
      tools: [],
      state: "healthy",
      vk_configs: [],
    });

    test("should create a client with a region, and an assumed role's settings only with a role", async ({ mcpRegistryPage }) => {
      const page = mcpRegistryPage.page;
      let sent: Record<string, any> | undefined;
      await page.route("**/api/mcp/client", async (route) => {
        if (route.request().method() !== "POST") return route.continue();
        sent = route.request().postDataJSON();
        await route.fulfill({ json: { status: "success", message: "MCP client created successfully" } });
      });

      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();
      await mcpRegistryPage.fillClientForm(createAWSSigV4ClientData());
      await expect(page.getByTestId("aws-sigv4-fields")).toBeVisible();
      // SigV4 signs with Bifrost's own identity, so there is no scope to pick.
      await expect(page.getByTestId("auth-scope-select")).toHaveCount(0);
      await expect(page.getByTestId("aws-sigv4-service-select")).toContainText("bedrock-agentcore");
      // The external ID and session name belong to an assumed role.
      await expect(page.getByTestId("aws-sigv4-external-id-input")).toHaveCount(0);

      await mcpRegistryPage.saveBtn.click();
      await expect(page.getByText("Region is required")).toBeVisible();
      expect(sent).toBeUndefined();

      await page.getByTestId("aws-sigv4-region-input").fill("us-east");
      await page.getByTestId("aws-sigv4-role-arn-input").fill("arn:aws:iam::123:role/gateway");
      await mcpRegistryPage.saveBtn.click();
      await expect(page.getByText("Enter an AWS region such as us-east-1")).toBeVisible();
      await expect(page.getByText("Enter an IAM role ARN such as arn:aws:iam::123456789012:role/name")).toBeVisible();
      expect(sent).toBeUndefined();

      await page.getByTestId("aws-sigv4-region-input").fill("us-east-1");
      await page.getByTestId("aws-sigv4-role-arn-input").fill("arn:aws:iam::123456789012:role/gateway");
      await page.getByTestId("aws-sigv4-external-id-input").fill("env.GATEWAY_EXTERNAL_ID");
      await page.getByTestId("aws-sigv4-session-name-input").fill("bifrost-mcp");
      await mcpRegistryPage.saveBtn.click();

      await expect.poll(() => sent?.auth_type).toBe("aws_sigv4");
      expect(sent?.aws_sigv4).toEqual({
        region: { value: "us-east-1", ref: "" },
        service: "bedrock-agentcore",
        role_arn: { value: "arn:aws:iam::123456789012:role/gateway", ref: "" },
        external_id: { value: "", ref: "env.GATEWAY_EXTERNAL_ID", type: "env" },
        session_name: { value: "bifrost-mcp", ref: "" },
      });
      await expect(mcpRegistryPage.sheet).not.toBeVisible({ timeout: 10000 });
    });

    test("should drop the external ID and session name when the role is cleared", async ({ mcpRegistryPage }) => {
      const page = mcpRegistryPage.page;
      let sent: Record<string, any> | undefined;
      await page.route("**/api/mcp/client", async (route) => {
        if (route.request().method() !== "POST") return route.continue();
        sent = route.request().postDataJSON();
        await route.fulfill({ json: { status: "success", message: "MCP client created successfully" } });
      });

      await mcpRegistryPage.createBtn.click();
      await mcpRegistryPage.fillClientForm(createAWSSigV4ClientData());
      await page.getByTestId("aws-sigv4-region-input").fill("us-east-1");
      await page.getByTestId("aws-sigv4-role-arn-input").fill("arn:aws:iam::123456789012:role/gateway");
      await page.getByTestId("aws-sigv4-external-id-input").fill("ext");
      await page.getByTestId("aws-sigv4-session-name-input").fill("bifrost-mcp");
      await page.getByTestId("aws-sigv4-role-arn-input").fill("");
      await expect(page.getByTestId("aws-sigv4-external-id-input")).toHaveCount(0);
      await mcpRegistryPage.saveBtn.click();

      await expect.poll(() => sent?.aws_sigv4).toBeDefined();
      expect(sent?.aws_sigv4).toEqual({ region: { value: "us-east-1", ref: "" }, service: "bedrock-agentcore" });
    });

    test("should refuse a header the SigV4 signer sets", async ({ mcpRegistryPage }) => {
      const page = mcpRegistryPage.page;
      let sent: Record<string, any> | undefined;
      await page.route("**/api/mcp/client", async (route) => {
        if (route.request().method() !== "POST") return route.continue();
        sent = route.request().postDataJSON();
        await route.fulfill({ json: { status: "success", message: "MCP client created successfully" } });
      });

      await mcpRegistryPage.createBtn.click();
      await mcpRegistryPage.fillClientForm(createAWSSigV4ClientData());
      await page.getByTestId("aws-sigv4-region-input").fill("us-east-1");
      await page.getByTestId("allowed-extra-headers-input").fill("X-Request-Id, X-Amz-Security-Token");
      await mcpRegistryPage.saveBtn.click();
      await expect(page.getByText("Header not allowed").first()).toBeVisible();
      expect(sent).toBeUndefined();

      await page.getByTestId("allowed-extra-headers-input").fill("X-Request-Id");
      await mcpRegistryPage.saveBtn.click();
      await expect.poll(() => sent?.allowed_extra_headers).toEqual(["X-Request-Id"]);
    });

    test("should offer SigV4 for an OpenAPI server", async ({ mcpRegistryPage }) => {
      const page = mcpRegistryPage.page;
      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();
      await page.getByTestId("mcp-server-source-openapi").click();
      await mcpRegistryPage.authTypeSelect.click();
      await expect(page.getByTestId("auth-type-aws-sigv4")).toBeVisible();
      // client_credentials runs an OAuth handshake against an MCP server, which OpenAPI has none of.
      await expect(page.getByTestId("auth-type-client-credentials")).toHaveCount(0);
    });

    test("should clear a stored role's external ID and session name when the role is removed", async ({ mcpRegistryPage }) => {
      const page = mcpRegistryPage.page;
      const server = awsServer();
      Object.assign(server.config.aws_sigv4, {
        external_id: { value: "ext", type: "plain_text" },
        session_name: { value: "bifrost-mcp", type: "plain_text" },
      });
      await page.route("**/api/mcp/clients?**", (route) =>
        route.fulfill({ json: { clients: [server], count: 1, total_count: 1, limit: 25, offset: 0 } }),
      );
      let sent: Record<string, any> | undefined;
      await page.route("**/api/mcp/client/aws-edit-1", async (route) => {
        if (route.request().method() !== "PUT") return route.continue();
        sent = route.request().postDataJSON();
        await route.fulfill({ json: { status: "success", message: "MCP client updated successfully" } });
      });
      await mcpRegistryPage.goto();
      await mcpRegistryPage.openClientEditor("aws_edit_server");
      await mcpRegistryPage.showEditorTab("authentication");

      const sheet = mcpRegistryPage.editSheet;
      await expect(sheet.getByTestId("aws-sigv4-external-id-input")).toHaveValue("ext");
      await sheet.getByTestId("aws-sigv4-role-arn-input").fill("");
      await expect(sheet.getByTestId("aws-sigv4-external-id-input")).toHaveCount(0);
      // Re-entering a role shows the cleared settings as empty, as they will be saved.
      await sheet.getByTestId("aws-sigv4-role-arn-input").fill("arn:aws:iam::123456789012:role/other");
      await expect(sheet.getByTestId("aws-sigv4-external-id-input")).toHaveValue("");
      await expect(sheet.getByTestId("aws-sigv4-session-name-input")).toHaveValue("");
      await sheet.getByTestId("mcpclient-save-btn").click();

      await expect.poll(() => sent?.aws_sigv4).toBeDefined();
      expect(sent?.aws_sigv4).toEqual({
        region: { value: "us-e****t-1", ref: "env.AWS_REGION", type: "env" },
        service: "bedrock-agentcore",
        role_arn: { value: "arn:aws:iam::123456789012:role/other", ref: "" },
      });
    });

    test("should keep the stored region reference and resend the whole block on an edit", async ({ mcpRegistryPage }) => {
      const page = mcpRegistryPage.page;
      await page.route("**/api/mcp/clients?**", (route) =>
        route.fulfill({ json: { clients: [awsServer()], count: 1, total_count: 1, limit: 25, offset: 0 } }),
      );
      let sent: Record<string, any> | undefined;
      await page.route("**/api/mcp/client/aws-edit-1", async (route) => {
        if (route.request().method() !== "PUT") return route.continue();
        sent = route.request().postDataJSON();
        await route.fulfill({ json: { status: "success", message: "MCP client updated successfully" } });
      });
      await mcpRegistryPage.goto();
      await mcpRegistryPage.openClientEditor("aws_edit_server");
      await mcpRegistryPage.showEditorTab("authentication");

      const sheet = mcpRegistryPage.editSheet;
      await expect(sheet.getByTestId("aws-sigv4-advanced-heading")).toBeVisible();
      await expect(sheet.getByText("env.AWS_REGION")).toBeVisible();
      // The reference is shown as its badge; the masked resolved value is never put up for editing.
      await expect(sheet.getByTestId("aws-sigv4-region-input")).toHaveValue("");
      await expect(sheet.getByTestId("aws-sigv4-role-arn-input")).toHaveValue("arn:aws:iam::123456789012:role/gateway");

      await sheet.getByTestId("aws-sigv4-service-select").click();
      await page.getByTestId("aws-sigv4-service-execute-api").click();
      await sheet.getByTestId("mcpclient-save-btn").click();

      await expect.poll(() => sent?.aws_sigv4).toBeDefined();
      expect(sent?.aws_sigv4).toEqual({
        region: { value: "us-e****t-1", ref: "env.AWS_REGION", type: "env" },
        service: "execute-api",
        role_arn: { value: "arn:aws:iam::123456789012:role/gateway", type: "plain_text" },
      });
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

  test.describe("MCP Per-User Tool Discovery", () => {
    test("should offer Per-Caller Discovery only for per-user auth types", async ({
      mcpRegistryPage,
    }) => {
      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();

      await mcpRegistryPage.selectAuthType("oauth");
      await expect(mcpRegistryPage.perUserDiscoverySwitch).toBeHidden();
      await mcpRegistryPage.selectAuthType("per_user_oauth");
      await expect(mcpRegistryPage.perUserDiscoverySwitch).toBeVisible();
      // The per-user scope carries over, so headers here is per-user headers.
      await mcpRegistryPage.selectAuthType("headers");
      await expect(mcpRegistryPage.perUserDiscoverySwitch).toBeVisible();
      await mcpRegistryPage.selectAuthScope("shared");
      await expect(mcpRegistryPage.perUserDiscoverySwitch).toBeHidden();

      await mcpRegistryPage.cancelCreation();
    });

    test("should start a new sign-in when Verify is retried after per-caller discovery is turned off", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createPerUserOAuthClientData({ perUserDiscovery: true });
      createdClients.push(clientData.name);
      const flow = await mcpRegistryPage.createOAuthClient(clientData);
      await completeOAuthFlow(mcpRegistryPage.page, flow);
      const turnedOff = await mcpRegistryPage.page.request.put(`/api/mcp/client/${flow.mcp_client_id}`, {
        data: { per_user_discovery: false },
      });
      expect(turnedOff.ok()).toBe(true);
      await mcpRegistryPage.goto();

      // Every sign-in fails, so the dialog reaches its failure step; the flow ids it polls are recorded.
      const polled: string[] = [];
      const flowStatus = (url: URL) => /\/api\/oauth\/config\/[^/]+\/status$/.test(url.pathname) && url.searchParams.has("flow_id");
      await mcpRegistryPage.page.route(flowStatus, (route) => {
        polled.push(new URL(route.request().url()).searchParams.get("flow_id") ?? "");
        return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ status: "failed" }) });
      });
      const page = mcpRegistryPage.page;
      const initiated = (response: { url: () => string; request: () => { method: () => string } }) =>
        response.url().includes(`/api/mcp/client/${flow.mcp_client_id}/initiate-verification`) && response.request().method() === "POST";

      const row = mcpRegistryPage.getClientRow(clientData.name);
      await row.getByRole("button", { name: /MCP server actions/i }).click();
      const firstInitiate = page.waitForResponse(initiated);
      await page.getByTestId(`mcp-client-authorize-${flow.mcp_client_id}-menu-item`).click();
      const first = await (await firstInitiate).json();
      expect(first.flow_id).toBeTruthy();
      const firstPopup = page.context().waitForEvent("page");
      await page.getByTestId("per-user-oauth-confirm").click();
      await (await firstPopup).close().catch(() => {});
      await expect(page.getByTestId("oauth-failed-retry-btn")).toBeVisible({ timeout: 15000 });
      expect(polled).toContain(first.flow_id);

      // Retry asks for a new sign-in (the server keeps one admin flow per server, so its id stays and its
      // state and deadline are new), and the dialog polls it again instead of resetting on the dead attempt.
      const retryInitiate = page.waitForResponse(initiated);
      await page.getByTestId("oauth-failed-retry-btn").click();
      const retried = await (await retryInitiate).json();
      expect(retried.flow_id).toBe(first.flow_id);
      expect(retried.authorize_url).not.toBe(first.authorize_url);
      const pollsBefore = polled.length;
      // The second popup opens the new sign-in, recognised by its new state parameter.
      const newState = new URL(retried.authorize_url).searchParams.get("state");
      expect(newState).toBeTruthy();
      expect(newState).not.toBe(new URL(first.authorize_url).searchParams.get("state"));
      const openedNew = page.context().waitForEvent("request", {
        predicate: (request) => {
          const url = new URL(request.url());
          return url.origin === new URL(retried.authorize_url).origin && url.searchParams.get("state") === newState;
        },
        timeout: 15000,
      });
      const secondPopup = page.context().waitForEvent("page");
      await page.getByTestId("per-user-oauth-confirm").click();
      await openedNew;
      await (await secondPopup).close().catch(() => {});
      await expect.poll(() => polled.slice(pollsBefore).includes(retried.flow_id), { timeout: 15000 }).toBe(true);
      await page.getByTestId("oauth-failed-close-btn").click();
      await page.unroute(flowStatus);
    });

    test("should verify a Per-User OAuth 2.0 client again after per-caller discovery is turned off", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createPerUserOAuthClientData({ perUserDiscovery: true });
      createdClients.push(clientData.name);
      const flow = await mcpRegistryPage.createOAuthClient(clientData);
      await completeOAuthFlow(mcpRegistryPage.page, flow);

      // Turning it off, as the edit sheet saves it, sends it back to pending verification.
      const turnedOff = await mcpRegistryPage.page.request.put(`/api/mcp/client/${flow.mcp_client_id}`, {
        data: { per_user_discovery: false },
      });
      expect(turnedOff.ok()).toBe(true);
      await mcpRegistryPage.goto();

      // The pending text says how it is verified, not that config.json declared it.
      await mcpRegistryPage.viewClientDetails(clientData.name);
      const alert = mcpRegistryPage.viewSheet.getByTestId("mcpclient-view-alert");
      await expect(alert).toContainText("An admin sign-in is needed to verify this server and discover its tools.");
      await expect(alert).not.toContainText("config.json");
      await mcpRegistryPage.closeDetailSheet();

      // Verify signs in on the client the server kept. A provider that forgot that client
      // rejects the sign-in on its own page and never redirects back, so the flow stays
      // pending: the dialog offers a new client while it waits, not only once it fails.
      const page = mcpRegistryPage.page;
      let flowState = "pending";
      const flowStatus = (url: URL) => /\/api\/oauth\/config\/[^/]+\/status$/.test(url.pathname) && url.searchParams.has("flow_id");
      await page.route(flowStatus, (route) =>
        route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ status: flowState }) }),
      );
      const row = mcpRegistryPage.getClientRow(clientData.name);
      await row.getByRole("button", { name: /MCP server actions/i }).click();
      await page.getByTestId(`mcp-client-authorize-${flow.mcp_client_id}-menu-item`).click();
      const popupPromise = page.context().waitForEvent("page");
      await page.getByTestId("per-user-oauth-confirm").click();
      await (await popupPromise).close().catch(() => {});
      await expect(page.getByTestId("oauth-polling-cancel-btn")).toBeVisible();
      await expect(page.getByTestId("oauth-polling-reregister-btn")).toHaveText("Reauthorize with a new client");

      // Once the flow fails, the same action sits beside Close and Retry, and on a narrow
      // screen the three wrap inside the dialog instead of running off its edge.
      await page.setViewportSize({ width: 320, height: 720 });
      flowState = "failed";
      const registerNew = page.getByTestId("oauth-failed-reregister-btn");
      await expect(registerNew).toBeVisible({ timeout: 15000 });
      const dialog = page.getByRole("dialog").filter({ has: registerNew });
      const bounds = await dialog.boundingBox();
      expect(bounds).not.toBeNull();
      for (const id of ["oauth-failed-close-btn", "oauth-failed-reregister-btn", "oauth-failed-retry-btn"]) {
        const box = await page.getByTestId(id).boundingBox();
        expect(box, id).not.toBeNull();
        expect(box!.x, `${id} starts inside the dialog`).toBeGreaterThanOrEqual(bounds!.x);
        expect(box!.x + box!.width, `${id} ends inside the dialog`).toBeLessThanOrEqual(bounds!.x + bounds!.width + 0.5);
      }
      await registerNew.click();

      // The reregister confirmation, without pointing at a Reauthorize the pending server doesn't offer.
      const confirm = mcpRegistryPage.page.getByRole("alertdialog");
      await expect(confirm).toContainText("Every user of this server will be signed out");
      await expect(confirm).not.toContainText("Otherwise use Reauthorize");
      await expect(confirm.getByTestId("mcp-client-reregister-confirm")).toBeVisible();
      await confirm.getByRole("button", { name: "Cancel" }).click();
      await page.unroute(flowStatus);
      await page.setViewportSize({ width: 1280, height: 720 });
    });

    test("should create a Per-User OAuth 2.0 client with per-caller discovery", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createPerUserOAuthClientData({ perUserDiscovery: true });
      createdClients.push(clientData.name);

      const flow = await mcpRegistryPage.createOAuthClient(clientData);
      await completeOAuthFlow(mcpRegistryPage.page, flow);
      await mcpRegistryPage.goto();
      expect(await mcpRegistryPage.clientExists(clientData.name)).toBe(true);

      // It keeps no admin credential and no tool list of its own, so nothing refreshes either.
      const actions = await mcpRegistryPage.getClientActionLabels(clientData.name);
      expect(actions).not.toContain("Refresh admin credential");
      expect(actions).not.toContain("Refresh tools");

      // A callers request that fails says so, with a retry, instead of reading as nobody having connected.
      const sessionsRoute = "**/api/mcp/sessions**";
      await mcpRegistryPage.page.route(sessionsRoute, (route) =>
        route.fulfill({ status: 500, contentType: "application/json", body: '{"error":{"message":"sessions unavailable"}}' }),
      );

      // The view says what the server keeps: no admin credential, and only the tool names callers' discoveries return.
      await mcpRegistryPage.viewClientDetails(clientData.name);
      const view = mcpRegistryPage.viewSheet;
      await expect(view.getByTestId("mcpclient-view-summary")).toContainText("Per-Caller Discovery");
      await expect(view.getByTestId("mcpclient-view-row-per_user_discovery")).toHaveText("On");
      await expect(view.getByTestId("mcpclient-view-row-tool_sync_interval")).toHaveText("Not used");
      await expect(view.getByTestId("mcpclient-credential-section")).toHaveCount(0);
      await expect(view.getByTestId("mcpclient-view-tab-callers")).toBeVisible();
      await expect(view.getByTestId("mcpclient-view-stat-callers")).toContainText("couldn't load callers");
      await view.getByTestId("mcpclient-view-tab-callers").click();
      await expect(view.getByTestId("mcpclient-view-callers-error")).toBeVisible();
      await mcpRegistryPage.page.unroute(sessionsRoute);

      // Switching the filter never shows the previous filter's rows under the new one: while the
      // Pending rows load only the rows table reads as loading; the filter grid and the Callers
      // figure come from the counts, which are already in, so they stay put.
      const activeRow = {
        id: "e2e-active-row",
        kind: "token",
        auth_kind: "oauth",
        auth_mode: "vk",
        virtual_key: { id: "vk-e2e", name: "e2e-active-key" },
        status: "active",
        created_at: new Date().toISOString(),
        can_reauth: true,
      };
      let releasePending = () => {};
      const pendingHeld = new Promise<void>((resolve) => {
        releasePending = resolve;
      });
      await mcpRegistryPage.page.route(sessionsRoute, async (route) => {
        const params = new URL(route.request().url()).searchParams;
        const status = params.get("status");
        // Hold only the Pending rows page; the one-row count pages answer at once.
        if (status === "pending" && params.get("limit") !== "1") await pendingHeld;
        const sessions = status === null || status === "active" ? [activeRow] : [];
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ sessions, count: sessions.length, total_count: sessions.length, limit: 5, offset: 0 }),
        });
      });
      await view.getByTestId("mcpclient-view-callers-retry").click();
      await expect(view.getByTestId("mcpclient-view-callers-error")).toHaveCount(0);
      await expect(view.getByTestId("mcpclient-view-callers-filter-active")).toBeVisible();
      await expect(view.getByTestId("mcpclient-view-caller-e2e-active-row")).toBeVisible();
      await view.getByTestId("mcpclient-view-callers-filter-pending").click();
      await expect(view.getByTestId("mcpclient-view-caller-e2e-active-row")).toHaveCount(0);
      await expect(view.getByTestId("mcpclient-view-callers-rows-loading")).toBeVisible();
      // The Discovery column makes four, and the loading row spans them all.
      await expect(view.getByTestId("mcpclient-view-callers-rows-loading").locator("td")).toHaveAttribute("colspan", "4");
      await expect(view.getByTestId("mcpclient-view-callers-filter-pending")).toHaveAttribute("aria-pressed", "true");
      await expect(view.getByTestId("mcpclient-view-callers-filter-pending")).toBeFocused();
      await expect(view.getByTestId("mcpclient-view-stat-callers")).toContainText("1");
      releasePending();
      await expect(view.getByTestId("mcpclient-view-callers-filter-pending")).toHaveAttribute("aria-pressed", "true");
      await expect(view.getByRole("tabpanel").getByText("No sessions yet.")).toBeVisible();
      await mcpRegistryPage.page.unroute(sessionsRoute);
      await view.getByTestId("mcpclient-view-tab-tools").click();
      await expect(view.getByRole("tabpanel").getByText("No caller has connected yet")).toBeVisible();
      await mcpRegistryPage.closeDetailSheet();

      await mcpRegistryPage.openClientEditor(clientData.name);
      const sheet = mcpRegistryPage.editSheet;
      const discoverySwitch = sheet.getByTestId("mcpclient-per-user-discovery-switch");
      await expect(discoverySwitch).toHaveAttribute("data-state", "checked");
      await expect(sheet.getByTestId("mcpclient-tool-sync-interval-input")).toBeDisabled();

      // Turning it off says what saving would delete, before anything is saved:
      // a per-user OAuth server goes back to pending verification, like the
      // other per-user auth types.
      await discoverySwitch.click();
      await expect(sheet.getByTestId("mcpclient-per-user-discovery-warning")).toContainText("goes back to pending verification");
      await discoverySwitch.click();
      await expect(sheet.getByTestId("mcpclient-per-user-discovery-warning")).toBeHidden();

      // No caller has connected yet, so no tool names have been seen.
      await sheet.getByTestId("mcpclient-tab-tools").click();
      await expect(sheet.getByText("No caller has connected yet")).toBeVisible();

      await mcpRegistryPage.closeDetailSheet();
    });
  });
});