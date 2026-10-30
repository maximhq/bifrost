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
      // OpenAPI is a server source of its own, marked beta, not a transport.
      await expect(
        mcpRegistryPage.page.getByTestId("mcp-server-source-openapi"),
      ).toContainText("BETA");
      await expect(
        mcpRegistryPage.page.getByTestId("connection-type-select"),
      ).toHaveCount(0);

      // The MCP connection URL does not apply to an OpenAPI server.
      await expect(
        mcpRegistryPage.sheet.getByTestId("connection-url-input"),
      ).toHaveCount(0);

      // A pasted document parses on its own; once it has, there is no parse button.
      await mcpRegistryPage.parseOpenAPISpec({
        supply: () => mcpRegistryPage.fillOpenAPISource(clientData),
      });

      await expect(
        mcpRegistryPage.sheet.getByTestId("openapi-preview-title"),
      ).toHaveText("E2E Petstore");
      await expect(
        mcpRegistryPage.sheet.getByTestId("openapi-parse-btn"),
      ).toHaveCount(0);
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
      // Tracked before the request: a create that fails after the row was
      // written must still be cleaned up (cleanup skips names that do not exist).
      createdClients.push(clientData.name);
      await mcpRegistryPage.saveBtn.click();
      const response = await responsePromise;
      expect(response.ok(), await response.text().catch(() => "")).toBe(true);

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

    test("per-user auth takes a header scheme from the caller's own header", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createOpenAPIClientData();

      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();
      await mcpRegistryPage.nameInput.fill(clientData.name);
      await mcpRegistryPage.selectConnectionType("openapi");
      await mcpRegistryPage.parseOpenAPISpec({
        supply: () => mcpRegistryPage.fillOpenAPISource(clientData),
      });

      await mcpRegistryPage.selectAuthType("headers");
      await mcpRegistryPage.selectAuthScope("per_user");

      // The header scheme names the caller's header (its own name by default)
      // instead of storing a value.
      const headerInput = mcpRegistryPage.sheet.getByTestId(
        "openapi-security-ApiKeyAuth-header-input",
      );
      await expect(headerInput).toHaveValue("X-API-Key");
      await expect(
        mcpRegistryPage.sheet.getByTestId("openapi-security-ApiKeyAuth-input"),
      ).toHaveCount(0);
      await headerInput.fill("X-Petstore-Key");

      const requestPromise = mcpRegistryPage.page.waitForRequest(
        (request) =>
          request.url().includes("/mcp/client") &&
          !request.url().endsWith("/mcp/clients") &&
          request.method() === "POST",
        { timeout: 60000 },
      );
      const responsePromise = mcpRegistryPage.page.waitForResponse(
        (response) =>
          response.url().includes("/mcp/client") &&
          !response.url().endsWith("/mcp/clients") &&
          response.request().method() === "POST",
        { timeout: 60000 },
      );
      createdClients.push(clientData.name);
      // Required Headers stays empty: the scheme's header alone satisfies it.
      // There is no MCP server to verify sample values against, so Create
      // posts directly instead of opening the per-user headers check.
      await mcpRegistryPage.saveBtn.click();
      const payload = (await requestPromise).postDataJSON();
      await expect(
        mcpRegistryPage.page.getByTestId("per-user-headers-confirm"),
      ).toHaveCount(0);
      const response = await responsePromise;
      expect(response.ok(), await response.text().catch(() => "")).toBe(true);

      expect(payload.auth_type).toBe("per_user_headers");
      expect(payload.per_user_header_keys).toEqual(["X-Petstore-Key"]);
      expect(
        payload.openapi_config.security_credentials?.ApiKeyAuth,
      ).toBeUndefined();
      expect(
        await mcpRegistryPage.waitForClientInTable(clientData.name, 15000),
      ).toBe(true);
    });

    test("creates through the shared createClient helper and shows the stored document on the edit sheet", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createOpenAPIClientData({
        openapiDeselectTools: ["getPetById"],
      });

      createdClients.push(clientData.name);
      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true);
      // The sheet closes before the table refresh lands; read the row once it is there.
      expect(
        await mcpRegistryPage.waitForClientInTable(clientData.name, 15000),
      ).toBe(true);

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
      const status = await mcpRegistryPage.parseOpenAPISpec({
        supply: () => mcpRegistryPage.fillOpenAPISource(clientData),
        expectOk: false,
      });
      expect(status).toBe(400);
      expect(await mcpRegistryPage.getOpenAPIParseError()).toContain(
        "not an OpenAPI document",
      );
      // A failed parse brings the button back to retry.
      await expect(
        mcpRegistryPage.sheet.getByTestId("openapi-parse-btn"),
      ).toHaveText("Parse again");
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
      // Hold the automatic parse so the document is still unparsed at save time.
      // Held requests are aborted in finally, so a failed assertion never
      // leaves one pending with the route still installed.
      const held: Array<{ abort: () => Promise<void> }> = [];
      await mcpRegistryPage.page.route("**/mcp/openapi/preview", (route) => {
        held.push(route);
      });
      try {
        await mcpRegistryPage.fillOpenAPISource(clientData);
        await expect(
          mcpRegistryPage.sheet.getByTestId("openapi-parse-pending"),
        ).toBeVisible({ timeout: 5000 });

        // Watched for a bounded window that outlasts the toast, so a create sent
        // just after the toast appears is still caught.
        const createSent = mcpRegistryPage.page
          .waitForRequest(
            (request) =>
              request.method() === "POST" &&
              request.url().includes("/mcp/client") &&
              !request.url().endsWith("/mcp/clients"),
            { timeout: 3000 },
          )
          .then(() => true)
          .catch(() => false);
        await mcpRegistryPage.saveBtn.click();
        await expect(
          mcpRegistryPage.page.getByText(/not parsed yet/i).first(),
        ).toBeVisible({ timeout: 5000 });
        expect(await createSent).toBe(false);
        await expect(mcpRegistryPage.sheet).toBeVisible();
      } finally {
        await Promise.all(held.map((route) => route.abort().catch(() => {})));
        await mcpRegistryPage.page.unroute("**/mcp/openapi/preview");
      }

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

      createdClients.push(clientData.name);
      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true);
      // The sheet closes before the table refresh lands; read the row once it is there.
      expect(
        await mcpRegistryPage.waitForClientInTable(clientData.name, 15000),
      ).toBe(true);

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

    test("parses the pasted document again after a URL was fetched", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createOpenAPIClientData();
      await mcpRegistryPage.createBtn.click();
      await expect(mcpRegistryPage.sheet).toBeVisible();
      await mcpRegistryPage.nameInput.fill(clientData.name);
      await mcpRegistryPage.selectConnectionType("openapi");

      await mcpRegistryPage.parseOpenAPISpec({
        supply: async () => {
          await mcpRegistryPage.selectOpenAPISourceMode("paste");
          await mcpRegistryPage.pasteOpenAPISpec(PETSTORE_OPENAPI_SPEC_YAML);
        },
      });
      const title = mcpRegistryPage.sheet.getByTestId("openapi-preview-title");
      await expect(title).toHaveText("E2E Petstore YAML");

      await mcpRegistryPage.parseOpenAPISpec({
        supply: async () => {
          await mcpRegistryPage.selectOpenAPISourceMode("url");
          await mcpRegistryPage.fillOpenAPISpecUrl(specUrl);
        },
      });
      await expect(title).toHaveText("E2E Petstore");

      // Back on Paste the preview on screen is the URL's, so the unchanged
      // pasted document is parsed again on its own.
      await mcpRegistryPage.parseOpenAPISpec({
        supply: () => mcpRegistryPage.selectOpenAPISourceMode("paste"),
      });
      await expect(title).toHaveText("E2E Petstore YAML");

      await mcpRegistryPage.cancelCreation();
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
      await mcpRegistryPage.parseOpenAPISpec({
        supply: () => mcpRegistryPage.fillOpenAPISource(clientData),
      });

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
      // Nothing is parsed, so there is no parse in flight and no button.
      await expect(
        mcpRegistryPage.sheet.getByTestId("openapi-parse-pending"),
      ).toHaveCount(0);
      await expect(
        mcpRegistryPage.sheet.getByTestId("openapi-parse-btn"),
      ).toHaveCount(0);
      await mcpRegistryPage.cancelCreation();
    });
  });

  test.describe("Replacing the document", () => {
    test("keeps the selection of operations that survive and adds the new ones", async ({
      mcpRegistryPage,
    }) => {
      // getPetById survives the replacement but starts unchecked: it must stay
      // unchecked, not be re-enabled by the re-parse.
      const clientData = createOpenAPIClientData({
        openapiDeselectTools: ["createPet", "getPetById"],
      });
      createdClients.push(clientData.name);
      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true);
      // The sheet closes before the table refresh lands; read the row once it is there.
      expect(
        await mcpRegistryPage.waitForClientInTable(clientData.name, 15000),
      ).toBe(true);
      expect(await mcpRegistryPage.getEnabledToolsCount(clientData.name)).toBe(
        "1/3",
      );

      await mcpRegistryPage.viewClientDetails(clientData.name);
      await mcpRegistryPage.openReplaceSpec();

      // The replacement drops createPet and adds deletePet.
      const panel = mcpRegistryPage.detailSheet.getByTestId(
        "openapi-replace-spec-panel",
      );
      await panel.getByTestId("openapi-spec-source-tab-paste").click();
      // The stored document fills the editor once it has loaded; replace it only then.
      await expect(
        panel.getByTestId("openapi-spec-paste-editor").locator(".view-lines"),
      ).toContainText("openapi", { timeout: 10000 });
      await mcpRegistryPage.pasteOpenAPISpec(PETSTORE_OPENAPI_SPEC_YAML, panel);

      // The pasted replacement parses on its own.
      await expect(panel.getByTestId("openapi-preview-title")).toHaveText(
        "E2E Petstore YAML",
        { timeout: 30000 },
      );

      // listPets was selected and stays selected; getPetById was unchecked and
      // stays unchecked; deletePet is new and unselected.
      expect(
        await panel
          .getByTestId("openapi-tool-checkbox-listPets")
          .getAttribute("data-state"),
      ).toBe("checked");
      expect(
        await panel
          .getByTestId("openapi-tool-checkbox-getPetById")
          .getAttribute("data-state"),
      ).toBe("unchecked");
      expect(
        await panel
          .getByTestId("openapi-tool-checkbox-deletePet")
          .getAttribute("data-state"),
      ).toBe("unchecked");
      await expect(panel.getByTestId("openapi-tool-row-createPet")).toHaveCount(
        0,
      );

      // While the parsed replacement decides the saved tools, the Tools tab is
      // read-only and says why.
      await mcpRegistryPage.detailSheet
        .getByTestId("mcpclient-tab-tools")
        .click();
      await expect(
        mcpRegistryPage.detailSheet.getByTestId(
          "mcp-client-tools-replacement-note",
        ),
      ).toBeVisible();
      await expect(
        mcpRegistryPage.detailSheet.getByRole("tabpanel"),
      ).toHaveAttribute("inert", "");
      await mcpRegistryPage.detailSheet
        .getByTestId("mcpclient-tab-general")
        .click();

      const updatePromise = mcpRegistryPage.page.waitForResponse(
        (response) =>
          response.url().includes("/mcp/client/") &&
          response.request().method() === "PUT",
        { timeout: 60000 },
      );
      // Only a list request sent after the PUT answered counts as the refresh:
      // a background poll or an earlier refetch would carry the old row.
      let putAnswered = false;
      const listRequestsAfterPut = new Set<unknown>();
      const page = mcpRegistryPage.page;
      const onResponse = (
        response: Awaited<ReturnType<typeof page.waitForResponse>>,
      ) => {
        if (
          response.url().includes("/mcp/client/") &&
          response.request().method() === "PUT"
        ) {
          putAnswered = true;
        }
      };
      const onRequest = (
        request: Awaited<ReturnType<typeof page.waitForRequest>>,
      ) => {
        if (
          putAnswered &&
          request.method() === "GET" &&
          request.url().includes("/mcp/clients")
        ) {
          listRequestsAfterPut.add(request);
        }
      };
      page.on("response", onResponse);
      page.on("request", onRequest);
      const refreshedList = mcpRegistryPage.page.waitForResponse(
        (response) =>
          listRequestsAfterPut.has(response.request()) && response.ok(),
        { timeout: 60000 },
      );
      try {
        await mcpRegistryPage.detailSheet
          .getByTestId("mcpclient-save-btn")
          .click();
        const update = await updatePromise;
        expect(update.ok(), await update.text().catch(() => "")).toBe(true);
        // The tool count reads 1/3 before and after the save, so it cannot tell a
        // refreshed row from a stale one: wait for the list refresh the update
        // triggers before reopening the sheet from that row.
        await refreshedList;
      } finally {
        page.off("response", onResponse);
        page.off("request", onRequest);
      }
      await mcpRegistryPage.closeDetailSheet();

      // The table now reflects the new document: only listPets of its 3 operations enabled.
      await expect
        .poll(
          async () => mcpRegistryPage.getEnabledToolsCount(clientData.name),
          { timeout: 20000 },
        )
        .toBe("1/3");
      await mcpRegistryPage.viewClientDetails(clientData.name);
      await expect
        .poll(async () => mcpRegistryPage.getOpenAPISummaryText(), {
          timeout: 20000,
        })
        .toContain("E2E Petstore YAML");
      // Reopening the replacement shows the saved selection, getPetById still unchecked.
      // The stored document seeds the replacement and parses on its own.
      await mcpRegistryPage.openReplaceSpec();
      await expect(
        panel.getByTestId("openapi-tool-checkbox-getPetById"),
      ).toHaveAttribute("data-state", "unchecked", { timeout: 30000 });
      await expect(
        panel.getByTestId("openapi-tool-checkbox-listPets"),
      ).toHaveAttribute("data-state", "checked");
      // deletePet was added unselected and is still offered, unselected.
      await expect(
        panel.getByTestId("openapi-tool-checkbox-deletePet"),
      ).toHaveAttribute("data-state", "unchecked");
      await mcpRegistryPage.closeDetailSheet();
    });
  });

  test.describe("Replacing the document: base URL", () => {
    test("shows an invalid custom base URL at the input and does not save", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createOpenAPIClientData();
      createdClients.push(clientData.name);
      expect(await mcpRegistryPage.createClient(clientData)).toBe(true);
      expect(
        await mcpRegistryPage.waitForClientInTable(clientData.name, 15000),
      ).toBe(true);

      await mcpRegistryPage.viewClientDetails(clientData.name);
      await mcpRegistryPage.openReplaceSpec();
      const panel = mcpRegistryPage.detailSheet.getByTestId(
        "openapi-replace-spec-panel",
      );
      // The stored document seeds the replacement and parses on its own.
      await expect(panel.getByTestId("openapi-preview-title")).toBeVisible({
        timeout: 30000,
      });
      await panel.getByTestId("openapi-base-url-select").click();
      await mcpRegistryPage.page
        .getByTestId("openapi-base-url-option-custom")
        .click();
      await panel.getByTestId("openapi-base-url-input").fill("not a url");

      const updateSent = mcpRegistryPage.page
        .waitForRequest(
          (request) =>
            request.method() === "PUT" &&
            request.url().includes("/mcp/client/"),
          { timeout: 3000 },
        )
        .then(() => true)
        .catch(() => false);
      await mcpRegistryPage.detailSheet
        .getByTestId("mcpclient-save-btn")
        .click();
      await expect(panel.getByTestId("openapi-base-url-error")).toContainText(
        "http",
        { timeout: 5000 },
      );
      expect(await updateSent).toBe(false);

      // Editing the value clears the error.
      await panel
        .getByTestId("openapi-base-url-input")
        .fill("https://petstore.example.com/v2");
      await expect(panel.getByTestId("openapi-base-url-error")).toHaveCount(0);
      await mcpRegistryPage.closeDetailSheet();
    });
  });

  test.describe("Filtering", () => {
    test("filters the table by the OpenAPI connection type", async ({
      mcpRegistryPage,
    }) => {
      const clientData = createOpenAPIClientData();
      createdClients.push(clientData.name);
      const created = await mcpRegistryPage.createClient(clientData);
      expect(created).toBe(true);
      // The sheet closes before the table refresh lands; read the row once it is there.
      expect(
        await mcpRegistryPage.waitForClientInTable(clientData.name, 15000),
      ).toBe(true);

      expect(
        await mcpRegistryPage.getClientConnectionType(clientData.name),
      ).toBe("OpenAPI");

      // The OpenAPI facet keeps the server and shows only OpenAPI servers.
      await mcpRegistryPage.setConnectionTypeFilter(["openapi"]);
      await expect(mcpRegistryPage.getClientRow(clientData.name)).toBeVisible();
      await expect
        .poll(async () => [...new Set(await mcpRegistryPage.getVisibleConnectionTypes())])
        .toEqual(["OpenAPI"]);

      // Another facet hides it.
      await mcpRegistryPage.setConnectionTypeFilter(["http"]);
      await expect(mcpRegistryPage.getClientRow(clientData.name)).toHaveCount(0);

      await mcpRegistryPage.setConnectionTypeFilter([]);
      await expect(mcpRegistryPage.getClientRow(clientData.name)).toBeVisible();
    });
  });
});
