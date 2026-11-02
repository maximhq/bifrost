import { expect, test } from "../../core/fixtures/base.fixture";
import {
  createCodeModeClientData,
  createHTTPClientData,
  createSSEClientData,
  createSTDIOClientData,
} from "./mcp-registry.data";
import { expectListLoadError } from "../../core/utils/test-helpers";
import { coreConfigApi } from "../../core/actions/api";
import type { MCPRegistryPage } from "./pages/mcp-registry.page";

const hasSSEHeaders = Boolean(process.env.MCP_SSE_HEADERS);

// Track created clients for cleanup
const createdClients: string[] = [];

test.describe("MCP Registry", () => {
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

  test.describe("MCP Client Display", () => {
    test("should display MCP clients table", async ({ mcpRegistryPage }) => {
      await expect(mcpRegistryPage.table).toBeVisible();
    });

    test("should display create button", async ({ mcpRegistryPage }) => {
      await expect(mcpRegistryPage.createBtn).toBeVisible();
    });

    test("should show empty state or client list", async ({
      mcpRegistryPage,
    }) => {
      const count = await mcpRegistryPage.getClientCount();
      const isEmptyStateVisible = await mcpRegistryPage.isEmptyStateVisible();

      if (count === 0) {
        expect(isEmptyStateVisible).toBe(true);
      } else {
        expect(count).toBeGreaterThan(0);
        expect(isEmptyStateVisible).toBe(false);
      }
    });

    test("should show the load error instead of the empty state when clients fail to load", async ({ page }) => {
      await expectListLoadError(page, "/api/mcp/clients", "mcp-servers-load-error", "mcp-registry-button-read-more");
    });
  });

  test.describe("MCP Client Creation", () => {
    test("should open client creation sheet", async ({ mcpRegistryPage }) => {
      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();
      await expect(mcpRegistryPage.nameInput).toBeVisible();

      // Cancel to clean up
      await mcpRegistryPage.cancelCreation();
    });

    test("should create basic HTTP client", async ({ mcpRegistryPage }) => {
      const clientData = createHTTPClientData();

      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true); // Client creation must succeed

      createdClients.push(clientData.name);
      const exists = await mcpRegistryPage.clientExists(clientData.name);
      expect(exists).toBe(true);

      // Verify connection type displayed correctly
      const connectionType = await mcpRegistryPage.getClientConnectionType(
        clientData.name,
      );
      expect(connectionType).toBe("HTTP");
    });

    test("should create SSE client", async ({ mcpRegistryPage }) => {
      test.skip(
        !hasSSEHeaders,
        "Requires MCP_SSE_HEADERS for authenticated SSE MCP endpoint",
      );
      const clientData = createSSEClientData({
        name: `sse_test_${Date.now()}`,
      });

      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true); // Client creation must succeed

      createdClients.push(clientData.name);
      const exists = await mcpRegistryPage.clientExists(clientData.name);
      expect(exists).toBe(true);

      // Verify connection type displayed correctly
      const connectionType = await mcpRegistryPage.getClientConnectionType(
        clientData.name,
      );
      expect(connectionType).toBe("SSE");
    });

    test("should create STDIO client with command", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createSTDIOClientData();

      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true); // Client creation must succeed

      createdClients.push(clientData.name);
      const exists = await mcpRegistryPage.clientExists(clientData.name);
      expect(exists).toBe(true);
    });

    test("should create client with code mode enabled", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createCodeModeClientData({
        name: `codemode_test_${Date.now()}`,
      });

      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true); // Client creation must succeed

      createdClients.push(clientData.name);
      const exists = await mcpRegistryPage.clientExists(clientData.name);
      expect(exists).toBe(true);
    });

    test("should cancel client creation", async ({ mcpRegistryPage }) => {
      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();

      const testName = `cancelled_client_${Date.now()}`;
      await mcpRegistryPage.nameInput.fill(testName);

      await mcpRegistryPage.cancelCreation();

      // Sheet should be closed
      await expect(mcpRegistryPage.sheet).not.toBeVisible();

      // Client should not exist
      const exists = await mcpRegistryPage.clientExists(testName);
      expect(exists).toBe(false);
    });
  });

  test.describe("Max Protocol Version", () => {
    test("should create a client with a max protocol version and switch it to the global setting", async ({
      mcpRegistryPage,
      request,
    }) => {
      const clientData = createHTTPClientData({
        name: `capped_${Date.now()}`,
        maxProtocolVersion: "2025-06-18",
      });
      const stored = () => mcpRegistryPage.storedMaxProtocolVersion(request, clientData.name);

      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true);
      createdClients.push(clientData.name);
      expect(await stored()).toBe("2025-06-18");

      await mcpRegistryPage.editClient(clientData.name, { maxProtocolVersion: "global" });
      expect(await stored()).toBe("");
    });

    test("should default a new client to the global setting", async ({ mcpRegistryPage }) => {
      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();
      await expect(mcpRegistryPage.sheet.getByTestId("mcp-client-max-protocol-version-select")).toContainText("Use global setting");
      // The form label must target the select, not the wrapper.
      await expect(mcpRegistryPage.sheet.getByLabel("Max Protocol Version", { exact: true })).toHaveAttribute(
        "data-testid",
        "mcp-client-max-protocol-version-select",
      );
      await mcpRegistryPage.closeSheet();
    });

    test.describe("following the global setting", () => {
      // Rewrites the shared global cap, so these must not interleave with each other.
      test.describe.configure({ mode: "serial" });
      let originalGlobal: string;

      test.beforeEach(async ({ request, mcpRegistryPage }) => {
        originalGlobal =
          ((await coreConfigApi.get(request)).client_config.mcp_max_protocol_version as string | undefined) || "auto";
        await coreConfigApi.updateClientConfig(request, { mcp_max_protocol_version: "2025-11-25" });
        // The outer beforeEach loaded the page with the previous global.
        await mcpRegistryPage.goto();
      });

      test.afterEach(async ({ request }) => {
        await coreConfigApi.updateClientConfig(request, { mcp_max_protocol_version: originalGlobal });
      });

      test("should create a client that follows the global setting when none is picked", async ({
        mcpRegistryPage,
        request,
      }) => {
        const clientData = createHTTPClientData({ name: `follows_global_${Date.now()}` });

        const created = await mcpRegistryPage.createClient(clientData);
        expect(created).toBe(true);
        createdClients.push(clientData.name);

        expect(
          await mcpRegistryPage.storedMaxProtocolVersion(request, clientData.name),
          "a new client has no cap of its own",
        ).toBe("");

        await mcpRegistryPage.viewClientDetails(clientData.name);
        await expect(mcpRegistryPage.detailSheet.getByTestId("mcp-client-max-protocol-version-select")).toContainText(
          "Use global setting (2025-11-25)",
        );
        await mcpRegistryPage.closeDetailSheet();
      });
    });
  });

  test.describe("MCP Server Connection Validation", () => {
    test("should connect to HTTP server and list tools", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createHTTPClientData({
        name: `http_validation_${Date.now()}`,
      });

      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true);
      createdClients.push(clientData.name);

      // Wait for the connection check to settle; health checks run every 10s
      await expect
        .poll(() => mcpRegistryPage.getClientStatus(clientData.name), {
          timeout: 30_000,
        })
        .toBe("healthy");

      // Verify tools are loaded (http-no-ping-server has: echo, add, greet)
      await mcpRegistryPage.viewClientDetails(clientData.name);
      const toolsCount = await mcpRegistryPage.getToolsCount();
      expect(toolsCount).toBeGreaterThanOrEqual(3);

      await mcpRegistryPage.closeDetailSheet();
    });

    test("should show a tool's title and output schema", async ({ mcpRegistryPage }) => {
      const clientData = createHTTPClientData({ name: `tool_metadata_${Date.now()}` });
      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true);
      createdClients.push(clientData.name);

      await expect
        .poll(() => mcpRegistryPage.getClientStatus(clientData.name), { timeout: 30_000 })
        .toBe("healthy");

      // http-no-ping-server's chart tool is titled "Chart" and declares an output schema.
      await mcpRegistryPage.viewClientDetails(clientData.name);
      const sheet = mcpRegistryPage.detailSheet;
      await sheet.getByTestId("mcpclient-tab-tools").click();
      await expect(sheet.getByTestId("mcp-tool-title-chart")).toHaveText("Chart");
      await expect(sheet.getByTestId("mcp-tool-output-schema-chart")).toHaveCount(0);
      await sheet.getByTestId("mcp-tool-expand-chart").click();
      await expect(sheet.getByTestId("mcp-tool-output-schema-chart")).toContainText("points");
      await mcpRegistryPage.closeDetailSheet();
    });

    test("should connect to SSE server and list tools", async ({
      mcpRegistryPage,
    }) => {
      test.skip(
        !hasSSEHeaders,
        "Requires MCP_SSE_HEADERS for authenticated SSE MCP endpoint",
      );
      const clientData = createSSEClientData({
        name: `sse_validation_${Date.now()}`,
      });

      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true);
      createdClients.push(clientData.name);

      // Verify tools are loaded
      await mcpRegistryPage.viewClientDetails(clientData.name);
      const toolsCount = await mcpRegistryPage.getToolsCount();
      expect(toolsCount).toBeGreaterThanOrEqual(3);

      await mcpRegistryPage.closeDetailSheet();
    });

    test("should connect to STDIO server and list tools", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createSTDIOClientData({
        name: `stdio_validation_${Date.now()}`,
      });

      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true);
      createdClients.push(clientData.name);

      // Verify tools from test-tools-server (echo, calculator, get_weather, delay, throw_error)
      await mcpRegistryPage.viewClientDetails(clientData.name);
      const toolsCount = await mcpRegistryPage.getToolsCount();
      expect(toolsCount).toBeGreaterThanOrEqual(5);

      await mcpRegistryPage.closeDetailSheet();
    });
  });

  // http-no-ping-server lists one prompt (review) and one resource (file:///fixture/handbook.md).
  test.describe("MCP Client Prompts and Resources", () => {
    const PROMPT = "review";
    const RESOURCE = "file:///fixture/handbook.md";

    async function createServedClient(mcpRegistryPage: MCPRegistryPage, prefix: string): Promise<string> {
      const clientData = createHTTPClientData({ name: `${prefix}_${Date.now()}` });
      expect(await mcpRegistryPage.createClient(clientData)).toBe(true);
      createdClients.push(clientData.name);
      await expect
        .poll(() => mcpRegistryPage.getClientStatus(clientData.name), { timeout: 30_000 })
        .toBe("healthy");
      return clientData.name;
    }

    test("should list the server's prompts and resources, all served by default", async ({ mcpRegistryPage, request }) => {
      const name = await createServedClient(mcpRegistryPage, "served_default");
      await mcpRegistryPage.openPromptsAndResources(name);

      const sheet = mcpRegistryPage.detailSheet;
      await expect(sheet.getByTestId(`mcp-expose-prompts-row-${PROMPT}`)).toContainText(PROMPT);
      await expect(sheet.getByTestId(`mcp-expose-resources-row-${RESOURCE}`)).toContainText(RESOURCE);
      await expect(mcpRegistryPage.exposeStatus("prompts")).toHaveText("All served");
      await expect(mcpRegistryPage.exposeStatus("resources")).toHaveText("All served");
      await expect(mcpRegistryPage.exposeToggle("prompts", PROMPT)).toHaveAttribute("aria-checked", "true");
      await expect(mcpRegistryPage.exposeToggle("resources", RESOURCE)).toHaveAttribute("aria-checked", "true");
      await mcpRegistryPage.closeDetailSheet();

      const stored = await mcpRegistryPage.storedClientConfig(request, name);
      expect(stored.prompts_to_expose ?? null, "a new client stores no list, which serves everything").toBeNull();
      expect(stored.resources_to_expose ?? null).toBeNull();
    });

    test("should save a prompt turned off and leave the untouched list unset", async ({ mcpRegistryPage, request }) => {
      const name = await createServedClient(mcpRegistryPage, "served_prompt_off");
      await mcpRegistryPage.openPromptsAndResources(name);
      await mcpRegistryPage.exposeToggle("prompts", PROMPT).click();
      await expect(mcpRegistryPage.exposeStatus("prompts")).toHaveText("None served");
      await mcpRegistryPage.saveDetailSheet();

      const stored = await mcpRegistryPage.storedClientConfig(request, name);
      expect(stored.prompts_to_expose).toEqual([]);
      expect(stored.resources_to_expose ?? null, "an untouched list keeps serving everything").toBeNull();
    });

    test("should show the saved lists when the sheet is reopened", async ({ mcpRegistryPage }) => {
      const name = await createServedClient(mcpRegistryPage, "served_reopen");
      await mcpRegistryPage.openPromptsAndResources(name);
      await mcpRegistryPage.exposeToggle("resources", RESOURCE).click();
      await mcpRegistryPage.saveDetailSheet();

      await mcpRegistryPage.openPromptsAndResources(name);
      await expect(mcpRegistryPage.exposeToggle("resources", RESOURCE)).toHaveAttribute("aria-checked", "false");
      await expect(mcpRegistryPage.exposeStatus("resources")).toHaveText("None served");
      await expect(mcpRegistryPage.exposeToggle("prompts", PROMPT)).toHaveAttribute("aria-checked", "true");
      await mcpRegistryPage.closeDetailSheet();
    });

    test("should collapse back to serving everything when the last item is turned on again", async ({ mcpRegistryPage, request }) => {
      const name = await createServedClient(mcpRegistryPage, "served_back_on");
      await mcpRegistryPage.setExposeLists(request, name, { prompts_to_expose: [] });
      // The page cached the client list before this API write; reload to read it back.
      await mcpRegistryPage.goto();

      await mcpRegistryPage.openPromptsAndResources(name);
      await expect(mcpRegistryPage.exposeToggle("prompts", PROMPT)).toHaveAttribute("aria-checked", "false");
      await mcpRegistryPage.exposeToggle("prompts", PROMPT).click();
      await expect(mcpRegistryPage.exposeStatus("prompts")).toHaveText("All served");
      await mcpRegistryPage.saveDetailSheet();

      expect((await mcpRegistryPage.storedClientConfig(request, name)).prompts_to_expose).toEqual(["*"]);
    });

    test("should clear and restore a whole list with its serve-all box", async ({ mcpRegistryPage, request }) => {
      const name = await createServedClient(mcpRegistryPage, "served_all_box");
      await mcpRegistryPage.openPromptsAndResources(name);

      await mcpRegistryPage.exposeAll("resources").click();
      await expect(mcpRegistryPage.exposeAll("resources")).toHaveAttribute("aria-checked", "false");
      await expect(mcpRegistryPage.exposeToggle("resources", RESOURCE)).toHaveAttribute("aria-checked", "false");
      await mcpRegistryPage.saveDetailSheet();
      expect((await mcpRegistryPage.storedClientConfig(request, name)).resources_to_expose).toEqual([]);

      await mcpRegistryPage.openPromptsAndResources(name);
      await mcpRegistryPage.exposeAll("resources").click();
      await expect(mcpRegistryPage.exposeAll("resources")).toHaveAttribute("aria-checked", "true");
      await mcpRegistryPage.saveDetailSheet();
      expect((await mcpRegistryPage.storedClientConfig(request, name)).resources_to_expose).toEqual(["*"]);
    });

    test("should not send the lists when another setting is edited", async ({ mcpRegistryPage, request }) => {
      const name = await createServedClient(mcpRegistryPage, "served_other_edit");
      await mcpRegistryPage.setExposeLists(request, name, { resources_to_expose: [RESOURCE] });

      await mcpRegistryPage.editClient(name, { maxProtocolVersion: "2025-06-18" });

      const stored = await mcpRegistryPage.storedClientConfig(request, name);
      expect(stored.max_protocol_version).toBe("2025-06-18");
      expect(stored.prompts_to_expose ?? null, "an edit elsewhere does not pin the prompts list").toBeNull();
      expect(stored.resources_to_expose).toEqual([RESOURCE]);
    });

    test("should keep pattern rules and undiscovered entries when a switch is toggled", async ({ mcpRegistryPage, request }) => {
      const name = await createServedClient(mcpRegistryPage, "served_rules");
      const GONE = "file:///fixture/gone.md";
      await mcpRegistryPage.setExposeLists(request, name, { prompts_to_expose: ["regex:rev.*"], resources_to_expose: [GONE] });
      // The page cached the client list before this API write; reload to read it back.
      await mcpRegistryPage.goto();

      await mcpRegistryPage.openPromptsAndResources(name);
      // A prompt served by a pattern rule reads as served and cannot be switched off on its own.
      await expect(mcpRegistryPage.exposeToggle("prompts", PROMPT)).toHaveAttribute("aria-checked", "true");
      await expect(mcpRegistryPage.exposeToggle("prompts", PROMPT)).toBeDisabled();
      await expect(mcpRegistryPage.exposeToggle("resources", RESOURCE)).toHaveAttribute("aria-checked", "false");
      await mcpRegistryPage.exposeToggle("resources", RESOURCE).click();
      await mcpRegistryPage.saveDetailSheet();

      const stored = await mcpRegistryPage.storedClientConfig(request, name);
      expect(stored.prompts_to_expose).toEqual(["regex:rev.*"]);
      expect(stored.resources_to_expose, "the undiscovered entry stays and the list does not widen to *").toEqual([GONE, RESOURCE]);
    });

    test("should discard a toggle when the sheet is closed without saving", async ({ mcpRegistryPage, request }) => {
      const name = await createServedClient(mcpRegistryPage, "served_discard");
      await mcpRegistryPage.openPromptsAndResources(name);
      await mcpRegistryPage.exposeToggle("prompts", PROMPT).click();
      await mcpRegistryPage.closeDetailSheet();

      expect((await mcpRegistryPage.storedClientConfig(request, name)).prompts_to_expose ?? null).toBeNull();
      await mcpRegistryPage.openPromptsAndResources(name);
      await expect(mcpRegistryPage.exposeToggle("prompts", PROMPT)).toHaveAttribute("aria-checked", "true");
      await mcpRegistryPage.closeDetailSheet();
    });

    test("should stop serving hidden prompts and resources through the MCP gateway", async ({ mcpRegistryPage, request }) => {
      const name = await createServedClient(mcpRegistryPage, "served_gateway");
      const prompt = `${name}-${PROMPT}`;

      const listed = await mcpRegistryPage.mcpGatewayCall(request, name, "prompts/list");
      expect(listed.result?.prompts.map((p: { name: string }) => p.name)).toContain(prompt);
      const read = await mcpRegistryPage.mcpGatewayCall(request, name, "resources/read", { uri: RESOURCE });
      expect(read.error, JSON.stringify(read)).toBeUndefined();
      expect(read.result?.contents[0].text).toBe("# Handbook");

      await mcpRegistryPage.openPromptsAndResources(name);
      await mcpRegistryPage.exposeToggle("prompts", PROMPT).click();
      await mcpRegistryPage.exposeToggle("resources", RESOURCE).click();
      await mcpRegistryPage.saveDetailSheet();

      const hidden = await mcpRegistryPage.mcpGatewayCall(request, name, "prompts/list");
      expect(hidden.result?.prompts.map((p: { name: string }) => p.name)).not.toContain(prompt);
      const fetched = await mcpRegistryPage.mcpGatewayCall(request, name, "prompts/get", { name: prompt });
      expect(fetched.error, "a hidden prompt cannot be fetched").toBeDefined();
      const resources = await mcpRegistryPage.mcpGatewayCall(request, name, "resources/list");
      expect(resources.result?.resources.map((r: { uri: string }) => r.uri)).not.toContain(RESOURCE);
      const refused = await mcpRegistryPage.mcpGatewayCall(request, name, "resources/read", { uri: RESOURCE });
      expect(refused.error, "a hidden resource cannot be read").toBeDefined();
    });

    test("should record a prompt read through the gateway in MCP logs as a prompt", async ({ mcpRegistryPage, mcpLogsPage, request, page }) => {
      const name = await createServedClient(mcpRegistryPage, "served_logged");
      const got = await mcpRegistryPage.mcpGatewayCall(request, name, "prompts/get", { name: `${name}-${PROMPT}` });
      expect(got.error, JSON.stringify(got)).toBeUndefined();

      // The log writer flushes in batches, so wait for the row to land.
      await expect
        .poll(
          async () => {
            const res = await request.get(`/api/mcp-logs?tool_names=${PROMPT}&server_labels=${encodeURIComponent(name)}`);
            const rows = res.ok() ? ((await res.json()).logs ?? []) : [];
            return rows.map((row: { method?: string; status: string }) => `${row.method}:${row.status}`);
          },
          { timeout: 20_000 },
        )
        .toContain("prompts/get:success");

      await page.goto(`/workspace/mcp-logs?server_labels=${encodeURIComponent(name)}`);
      await expect(mcpLogsPage.logsTable).toBeVisible();
      await expect(page.getByTestId("mcp-log-read-kind").first()).toHaveText("Prompt");
      await mcpLogsPage.viewLogDetails(0);
      await expect(mcpLogsPage.logDetailSheet.getByTestId("mcplogdetails-tool-label")).toHaveText("Prompt");
    });

    test("should show empty states for a server that lists no prompts or resources", async ({ mcpRegistryPage }) => {
      const clientData = createSTDIOClientData({ name: `served_none_${Date.now()}` });
      expect(await mcpRegistryPage.createClient(clientData)).toBe(true);
      createdClients.push(clientData.name);

      await mcpRegistryPage.openPromptsAndResources(clientData.name);
      await expect(mcpRegistryPage.detailSheet.getByTestId("mcp-expose-prompts-empty")).toBeVisible();
      await expect(mcpRegistryPage.detailSheet.getByTestId("mcp-expose-resources-empty")).toBeVisible();
      await expect(mcpRegistryPage.exposeAll("prompts")).toHaveCount(0);
      await mcpRegistryPage.closeDetailSheet();
    });
  });

  test.describe("MCP Client Management", () => {
    test("should delete MCP client", async ({ mcpRegistryPage }) => {
      // Create a client first using HTTP (most reliable)
      const clientData = createHTTPClientData({
        name: `delete_test_${Date.now()}`,
      });

      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true); // Client creation must succeed for this test

      // Verify it exists
      let exists = await mcpRegistryPage.clientExists(clientData.name);
      expect(exists).toBe(true);

      // Delete it
      await mcpRegistryPage.deleteClient(clientData.name);

      // Verify it's gone
      exists = await mcpRegistryPage.clientExists(clientData.name);
      expect(exists).toBe(false);
    });

    test("should view client details", async ({ mcpRegistryPage }) => {
      // Create a client first
      const clientData = createHTTPClientData({
        name: `view_test_${Date.now()}`,
      });

      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true); // Client creation must succeed for this test
      createdClients.push(clientData.name);

      // View details
      await mcpRegistryPage.viewClientDetails(clientData.name);

      // Detail sheet should be visible
      await expect(mcpRegistryPage.detailSheet).toBeVisible();

      // Close the sheet
      await mcpRegistryPage.closeDetailSheet();
    });

    test("should close client details sheet", async ({ mcpRegistryPage }) => {
      // Create a client first
      const clientData = createHTTPClientData({
        name: `close_sheet_test_${Date.now()}`,
      });

      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true); // Client creation must succeed for this test
      createdClients.push(clientData.name);

      // Open details
      await mcpRegistryPage.viewClientDetails(clientData.name);
      await expect(mcpRegistryPage.detailSheet).toBeVisible();

      // Close it
      await mcpRegistryPage.closeDetailSheet();

      // Should be closed
      await expect(mcpRegistryPage.detailSheet).not.toBeVisible();
    });

    test("should reconnect MCP client", async ({ mcpRegistryPage }) => {
      // Reconnect only applies to clients holding a persistent connection; a
      // default HTTP client dials per call, so use STDIO.
      const clientData = createSTDIOClientData({
        name: `reconnect_test_${Date.now()}`,
      });

      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true); // Client creation must succeed for this test
      createdClients.push(clientData.name);

      // Reconnect - method waits for success toast
      await mcpRegistryPage.reconnectClient(clientData.name);

      // Verify client still exists and has a status (reconnect completed)
      const exists = await mcpRegistryPage.clientExists(clientData.name);
      expect(exists).toBe(true);
      const status = await mcpRegistryPage.getClientStatus(clientData.name);
      expect(status).toBeTruthy();
      expect([
        "healthy",
        "unstable",
        "pending_verification",
        "needs_reauth",
        "disabled",
      ]).toContain(status.toLowerCase());
    });
  });

  test.describe("Client Status Display", () => {
    test("should display client connection status", async ({
      mcpRegistryPage,
    }) => {
      // Create a client first
      const clientData = createHTTPClientData({
        name: `status_test_${Date.now()}`,
      });

      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true); // Client creation must succeed for this test
      createdClients.push(clientData.name);

      // Get status
      const status = await mcpRegistryPage.getClientStatus(clientData.name);

      // Status should be one of the expected values
      expect(status).toBeTruthy();
      expect([
        "healthy",
        "unstable",
        "pending_verification",
        "needs_reauth",
        "disabled",
      ]).toContain(status?.toLowerCase());
    });
  });

  test.describe("Form Validation", () => {
    test("should validate name format", async ({ mcpRegistryPage }) => {
      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();

      // Try invalid name with hyphens (not allowed)
      await mcpRegistryPage.nameInput.fill("invalid-name-with-hyphens");

      // Fill connection URL to satisfy other validation
      await mcpRegistryPage.connectionUrlInput.fill("http://localhost:3001");

      // Complex forms keep the action enabled and show exact inline errors on submit.
      await mcpRegistryPage.saveBtn.click();
      await expect(
        mcpRegistryPage.page.getByText(
          "Server name can only contain letters, numbers, and underscores",
        ),
      ).toBeVisible();

      await mcpRegistryPage.cancelCreation();
    });

    test("should require connection URL for HTTP clients", async ({
      mcpRegistryPage,
    }) => {
      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();

      // Fill valid name
      await mcpRegistryPage.nameInput.fill(`valid_name_${Date.now()}`);

      // Leave connection URL empty, submit, and assert the highlighted requirement.
      await mcpRegistryPage.saveBtn.click();
      await expect(
        mcpRegistryPage.page.getByText("Connection URL is required"),
      ).toBeVisible();

      await mcpRegistryPage.cancelCreation();
    });
  });
});