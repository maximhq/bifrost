import { createServer, type Server } from "http";
import type { AddressInfo } from "net";
import { expect, test } from "../../core/fixtures/base.fixture";
import {
  createOpenAPIClientData,
  NOT_AN_OPENAPI_SPEC_TEXT,
  PETSTORE_OPENAPI_SPEC_TEXT,
  PETSTORE_OPENAPI_SPEC_YAML,
} from "./mcp-registry.data";

// Track created clients for cleanup
const createdClients: string[] = [];

test.describe("MCP Registry - OpenAPI servers", () => {
  // Parsing and creating round-trip the backend; give tests room to complete.
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

  test.describe("Creation from a pasted document", () => {
    test("parses the document, lets operations be picked, and creates the server", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createOpenAPIClientData();

      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();
      await mcpRegistryPage.nameInput.fill(clientData.name);
      await mcpRegistryPage.selectConnectionType("openapi");

      // The MCP connection URL does not apply to an OpenAPI server.
      await expect(
        mcpRegistryPage.sheet.getByTestId("connection-url-input"),
      ).toHaveCount(0);

      await mcpRegistryPage.fillOpenAPISource(clientData);
      await mcpRegistryPage.parseOpenAPISpec();

      await expect(
        mcpRegistryPage.sheet.getByTestId("openapi-preview-title"),
      ).toHaveText("E2E Petstore");
      await expect(
        mcpRegistryPage.sheet.getByTestId("openapi-preview-version"),
      ).toContainText("3.0.3");

      const names = await mcpRegistryPage.getOpenAPIPreviewToolNames();
      expect(names).toEqual(
        expect.arrayContaining([
          "listPets",
          "createPet",
          "getPetById",
          "uploadPetPhoto",
        ]),
      );
      expect(
        await mcpRegistryPage.isOpenAPIToolDisabled("uploadPetPhoto"),
      ).toBe(true);
      await expect(
        mcpRegistryPage.sheet.getByTestId(
          "openapi-tool-skip-reason-uploadPetPhoto",
        ),
      ).toBeVisible();
      expect(await mcpRegistryPage.getOpenAPISelectedCountText()).toBe(
        "3 of 3 selected",
      );

      // The security scheme declared by the document gets a credential input.
      await mcpRegistryPage.fillOpenAPICredential("ApiKeyAuth", "e2e-api-key");

      // Untick one operation.
      await mcpRegistryPage.toggleOpenAPITool("createPet");
      expect(await mcpRegistryPage.isOpenAPIToolChecked("createPet")).toBe(
        false,
      );
      expect(await mcpRegistryPage.getOpenAPISelectedCountText()).toBe(
        "2 of 3 selected",
      );

      // OAuth and token exchange are not offered for this type.
      await mcpRegistryPage.authTypeSelect.click();
      await expect(
        mcpRegistryPage.page.getByTestId("auth-type-oauth"),
      ).toHaveCount(0);
      await mcpRegistryPage.page.getByTestId("auth-type-none").click();

      const responsePromise = mcpRegistryPage.page.waitForResponse(
        (response) =>
          response.url().includes("/mcp/client") &&
          !response.url().endsWith("/mcp/clients") &&
          response.request().method() === "POST",
        { timeout: 60000 },
      );
      await mcpRegistryPage.saveBtn.click();
      const response = await responsePromise;
      expect(response.ok(), await response.text().catch(() => "")).toBe(true);
      createdClients.push(clientData.name);

      expect(
        await mcpRegistryPage.waitForClientInTable(clientData.name, 15000),
      ).toBe(true);
      expect(
        await mcpRegistryPage.getClientConnectionType(clientData.name),
      ).toBe("OpenAPI");
      expect(await mcpRegistryPage.getEnabledToolsCount(clientData.name)).toBe(
        "2/3",
      );
    });

    test("creates through the shared createClient helper and shows the stored document on the edit sheet", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createOpenAPIClientData({
        openapiDeselectTools: ["getPetById"],
      });

      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true);
      createdClients.push(clientData.name);

      await mcpRegistryPage.viewClientDetails(clientData.name);
      const summary = await mcpRegistryPage.getOpenAPISummaryText();
      expect(summary).toContain("E2E Petstore");
      expect(summary).toContain("inline spec");
      expect(summary).toContain("3 operations");

      const toolCount = await mcpRegistryPage.getToolsCount();
      expect(toolCount).toBe(3);
      await mcpRegistryPage.closeDetailSheet();
    });

    test("rejects a document that is not an OpenAPI description", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createOpenAPIClientData({
        openapiSpecText: NOT_AN_OPENAPI_SPEC_TEXT,
      });

      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();
      await mcpRegistryPage.nameInput.fill(clientData.name);
      await mcpRegistryPage.selectConnectionType("openapi");
      await mcpRegistryPage.fillOpenAPISource(clientData);

      const status = await mcpRegistryPage.parseOpenAPISpec({
        expectOk: false,
      });
      expect(status).toBe(400);
      expect(await mcpRegistryPage.getOpenAPIParseError()).toContain(
        "not an OpenAPI document",
      );
      await expect(
        mcpRegistryPage.sheet.getByTestId("openapi-preview-title"),
      ).toHaveCount(0);

      await mcpRegistryPage.cancelCreation();
    });

    test("does not create before the document was parsed", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createOpenAPIClientData();

      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();
      await mcpRegistryPage.nameInput.fill(clientData.name);
      await mcpRegistryPage.selectConnectionType("openapi");
      await mcpRegistryPage.fillOpenAPISource(clientData);

      let created = false;
      mcpRegistryPage.page.on("request", (request) => {
        if (
          request.method() === "POST" &&
          request.url().includes("/mcp/client") &&
          !request.url().endsWith("/mcp/clients")
        ) {
          created = true;
        }
      });
      await mcpRegistryPage.saveBtn.click();
      await expect(
        mcpRegistryPage.page.getByText(/Parse the spec first/i).first(),
      ).toBeVisible({ timeout: 5000 });
      expect(created).toBe(false);
      await expect(mcpRegistryPage.sheet).toBeVisible();

      await mcpRegistryPage.cancelCreation();
    });
  });

  test.describe("Other document sources", () => {
    let specServer: Server | null = null;
    let specUrl = "";

    test.beforeAll(async () => {
      specServer = createServer((req, res) => {
        if (req.url?.startsWith("/openapi.json")) {
          res.writeHead(200, { "Content-Type": "application/json" });
          res.end(PETSTORE_OPENAPI_SPEC_TEXT);
          return;
        }
        res.writeHead(404);
        res.end();
      });
      await new Promise<void>((resolve) =>
        specServer!.listen(0, "127.0.0.1", () => resolve()),
      );
      const { port } = specServer.address() as AddressInfo;
      specUrl = `http://127.0.0.1:${port}/openapi.json`;
    });

    test.afterAll(async () => {
      await new Promise<void>((resolve) =>
        specServer ? specServer.close(() => resolve()) : resolve(),
      );
    });

    test("fetches the document from a URL", async ({ mcpRegistryPage }) => {
      // The E2E profile runs with dashboard authentication, so an authenticated
      // admin may point spec_url at loopback (unauthenticated callers may not).
      const clientData = createOpenAPIClientData({
        openapiSourceMode: "url",
        openapiSpecUrl: specUrl,
        openapiSpecText: undefined,
      });

      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true);
      createdClients.push(clientData.name);

      expect(
        await mcpRegistryPage.getClientConnectionType(clientData.name),
      ).toBe("OpenAPI");
      expect(await mcpRegistryPage.getEnabledToolsCount(clientData.name)).toBe(
        "3/3",
      );

      await mcpRegistryPage.viewClientDetails(clientData.name);
      expect(await mcpRegistryPage.getOpenAPISummaryText()).toContain(specUrl);
      await mcpRegistryPage.closeDetailSheet();
    });

    test("accepts an uploaded YAML document", async ({ mcpRegistryPage }) => {
      const clientData = createOpenAPIClientData({
        openapiSourceMode: "upload",
        openapiSpecText: undefined,
        openapiSpecFile: {
          name: "petstore.yaml",
          mimeType: "application/yaml",
          buffer: Buffer.from(PETSTORE_OPENAPI_SPEC_YAML),
        },
      });

      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();
      await mcpRegistryPage.nameInput.fill(clientData.name);
      await mcpRegistryPage.selectConnectionType("openapi");
      await mcpRegistryPage.fillOpenAPISource(clientData);
      await mcpRegistryPage.parseOpenAPISpec();

      await expect(
        mcpRegistryPage.sheet.getByTestId("openapi-preview-title"),
      ).toHaveText("E2E Petstore YAML");
      expect(await mcpRegistryPage.getOpenAPIPreviewToolNames()).toEqual(
        expect.arrayContaining(["listPets", "getPetById", "deletePet"]),
      );
      await mcpRegistryPage.cancelCreation();
    });

    test("refuses an oversized upload before parsing", async ({
      mcpRegistryPage,
    }) => {
      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();
      await mcpRegistryPage.selectConnectionType("openapi");
      await mcpRegistryPage.selectOpenAPISourceMode("upload");
      await mcpRegistryPage.uploadOpenAPISpec({
        name: "huge.json",
        mimeType: "application/json",
        buffer: Buffer.alloc(5 * 1024 * 1024 + 1, 0x20),
      });
      await expect(
        mcpRegistryPage.sheet.getByTestId("openapi-spec-upload-error"),
      ).toBeVisible();
      await expect(
        mcpRegistryPage.sheet.getByTestId("openapi-parse-btn"),
      ).toBeDisabled();
      await mcpRegistryPage.cancelCreation();
    });
  });

  test.describe("Replacing the document", () => {
    test("keeps the selection of operations that survive and adds the new ones", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createOpenAPIClientData({
        openapiDeselectTools: ["createPet"],
      });
      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true);
      createdClients.push(clientData.name);
      expect(await mcpRegistryPage.getEnabledToolsCount(clientData.name)).toBe(
        "2/3",
      );

      await mcpRegistryPage.viewClientDetails(clientData.name);
      await mcpRegistryPage.openReplaceSpec();

      // The replacement drops createPet and adds deletePet.
      const panel = mcpRegistryPage.detailSheet.getByTestId(
        "openapi-replace-spec-panel",
      );
      await panel.getByTestId("openapi-spec-source-tab-paste").click();
      const editor = panel.getByTestId("openapi-spec-paste-editor");
      await editor
        .locator("textarea.inputarea")
        .waitFor({ state: "attached", timeout: 10000 });
      await editor.locator(".monaco-editor").first().click();
      await mcpRegistryPage.page.keyboard.press(
        process.platform === "darwin" ? "Meta+A" : "Control+A",
      );
      await mcpRegistryPage.page.keyboard.press("Backspace");
      await mcpRegistryPage.page.keyboard.insertText(
        PETSTORE_OPENAPI_SPEC_YAML,
      );

      const previewPromise = mcpRegistryPage.page.waitForResponse(
        (response) =>
          response.url().includes("/mcp/openapi/preview") &&
          response.request().method() === "POST",
        { timeout: 30000 },
      );
      await panel.getByTestId("openapi-parse-btn").click();
      expect((await previewPromise).ok()).toBe(true);
      await expect(panel.getByTestId("openapi-preview-title")).toHaveText(
        "E2E Petstore YAML",
      );

      // listPets and getPetById were selected before and stay selected; deletePet is new and unselected.
      expect(
        await panel
          .getByTestId("openapi-tool-checkbox-listPets")
          .getAttribute("data-state"),
      ).toBe("checked");
      expect(
        await panel
          .getByTestId("openapi-tool-checkbox-getPetById")
          .getAttribute("data-state"),
      ).toBe("checked");
      expect(
        await panel
          .getByTestId("openapi-tool-checkbox-deletePet")
          .getAttribute("data-state"),
      ).toBe("unchecked");
      await expect(panel.getByTestId("openapi-tool-row-createPet")).toHaveCount(
        0,
      );

      const updatePromise = mcpRegistryPage.page.waitForResponse(
        (response) =>
          response.url().includes("/mcp/client/") &&
          response.request().method() === "PUT",
        { timeout: 60000 },
      );
      await mcpRegistryPage.detailSheet
        .getByTestId("mcpclient-save-btn")
        .click();
      const update = await updatePromise;
      expect(update.ok(), await update.text().catch(() => "")).toBe(true);
      await mcpRegistryPage.closeDetailSheet();

      // The table now reflects the new document: 2 of its 3 operations enabled.
      await expect
        .poll(
          async () => mcpRegistryPage.getEnabledToolsCount(clientData.name),
          { timeout: 20000 },
        )
        .toBe("2/3");
      await mcpRegistryPage.viewClientDetails(clientData.name);
      expect(await mcpRegistryPage.getOpenAPISummaryText()).toContain(
        "E2E Petstore YAML",
      );
      await mcpRegistryPage.closeDetailSheet();
    });
  });

  test.describe("Filtering", () => {
    test("lists OpenAPI as a connection type facet", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createOpenAPIClientData();
      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true);
      createdClients.push(clientData.name);

      expect(
        await mcpRegistryPage.getClientConnectionType(clientData.name),
      ).toBe("OpenAPI");
    });
  });
});
