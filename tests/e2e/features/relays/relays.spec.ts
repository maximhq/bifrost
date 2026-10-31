import type { APIRequestContext } from "@playwright/test";
import { createServer } from "http";
import type { AddressInfo } from "net";
import { expect, test } from "../../core/fixtures/base.fixture";

const API = "/api/relays";
const VK_API = "/api/governance/virtual-keys";
const created: string[] = [];
const createdVkIds: string[] = [];

/** Reads a relay back from the API; secrets come back redacted. */
async function getRelay(request: APIRequestContext, name: string) {
  const res = await request.get(`${API}/${name}`);
  expect(res.status(), await res.text()).toBe(200);
  return res.json();
}

test.describe("Relays", () => {
  test.afterEach(async ({ relaysPage: page, request }) => {
    await page.closeSheets().catch(() => undefined);
    for (const id of createdVkIds.splice(0)) {
      await request.delete(`${VK_API}/${id}`).catch(() => undefined);
    }
    for (const name of created.splice(0)) {
      await request.delete(`${API}/${name}`).catch(() => undefined);
    }
  });

  test("creates an endpoint, prices a route override and previews its cost", async ({
    relaysPage: page,
  }) => {
    const name = `e2e-pte-${Date.now()}`;
    created.push(name);
    await page.goto();
    await page.createEndpoint(name, "https://api.example.com", "sk-e2e-secret");
    await page.select(name);
    await expect(page.editor).toBeVisible();

    // Endpoint-level pricing lives on the General tab of the config sheet.
    await page.openConfigTab("general");
    await page.page.getByTestId("relay-usage-cost-per-request").fill("0.001");
    await page.page.getByTestId("relay-save-general").click();
    await expect(page.getToast("success")).toBeVisible();
    await page.page.keyboard.press("Escape");

    // A route whose override adds a per-unit charge read from the response.
    await page.page.getByTestId("relay-routes-add").click();
    await page.page.getByTestId("relay-route-path").fill("/search");
    // The override is collapsed until opened.
    await expect(page.page.getByTestId("relay-route-usage-units-path")).toBeHidden();
    await page.page.getByTestId("relay-route-pricing-toggle").click();
    await page.page.getByTestId("relay-route-usage-units-path").fill("usage.credits");
    await page.page.getByTestId("relay-route-usage-cost-per-unit").fill("0.008");
    await page.page.getByTestId("relay-route-save").click();
    await expect(page.page.getByTestId("relay-route-row-0")).toContainText("/search");
    await expect(page.page.getByTestId("relay-route-row-0")).toContainText("override");
    await expect(page.page.getByTestId("relay-route-enabled-switch-0")).toBeChecked();

    // Test a call against the saved mapping: 0.001 + 4 * 0.008.
    await page.page.getByTestId("relay-preview-toggle").click();
    await expect(page.page.getByTestId("relay-preview-sheet")).toBeVisible();
    // The status takes three digits; letters and a fourth digit are refused as typed.
    const status = page.page.getByTestId("relay-preview-status");
    await status.fill("");
    await status.pressSequentially("2001sas");
    await expect(status).toHaveValue("200");
    await page.page.getByTestId("relay-preview-path").fill("/search");
    await page.page.getByTestId("relay-preview-response-body").fill('{"usage":{"credits":4}}');
    await page.page.getByTestId("relay-preview-run").click();
    await expect(page.page.getByTestId("relay-preview-cost")).toHaveText("$0.033");
    await expect(page.page.getByTestId("relay-preview-formula")).toContainText("4 × $0.008");
  });

  test("warns when the endpoint relays any path", async ({ relaysPage: page }) => {
    const name = `e2e-pte-open-${Date.now()}`;
    created.push(name);
    await page.goto();
    await page.createEndpoint(name, "https://api.example.com", "sk-e2e-secret");
    await page.select(name);
    await page.openConfigTab("general");
    await page.page.getByTestId("relay-skip-route-check").click();
    await expect(page.page.getByTestId("relay-all-routes-warning")).toBeVisible();
  });

  test("deletes an endpoint", async ({ relaysPage: page, request }) => {
    const name = `e2e-pte-del-${Date.now()}`;
    created.push(name);
    await page.goto();
    await page.createEndpoint(name, "https://api.example.com", "sk-e2e-secret");
    await page.select(name);
    await page.deleteSelected();
    await expect(page.getToast("success")).toBeVisible();
    const res = await request.get(`${API}/${name}`);
    expect(res.status()).toBe(404);
  });

  test("virtual key sheet offers relay access", async ({ page }) => {
    await page.goto("/workspace/virtual-keys");
    await page
      .getByTestId("create-vk-btn")
      .or(page.getByRole("button", { name: /add virtual key|create virtual key/i }))
      .first()
      .click();
    await expect(page.getByTestId("vk-relays-editor")).toBeVisible();
    // A key is grant rows only, like its agent and MCP grants: there is no allow-all switch.
    await expect(page.getByTestId("vk-relays-allow-all")).toHaveCount(0);
  });

  test("relay logs page loads", async ({ page }) => {
    await page.goto("/workspace/relay-logs");
    await expect(
      page.getByTestId("relay-logs-page").or(page.getByTestId("relay-logs-empty")),
    ).toBeVisible();
  });

  // ---------------------------------------------------------------------------------------------
  // Authentication types
  // ---------------------------------------------------------------------------------------------

  test("creates relays with header, query and basic authentication", async ({ relaysPage: page, request }) => {
    const stamp = Date.now();
    await page.goto();

    // Custom header: the header name and an optional literal prefix before the key.
    const header = `e2e-pte-auth-header-${stamp}`;
    created.push(header);
    await page.openCreate();
    await page.page.getByTestId("relay-name").fill(header);
    await page.page.getByTestId("relay-new-base-url").fill("https://api.example.com");
    await expect(page.authTypeTrigger).toContainText("Bearer token");
    await page.selectAuthType("Custom header");
    await page.page.getByTestId("relay-header-name").fill("X-API-Key");
    await page.addSheet.getByPlaceholder("Token ").fill("Token");
    await page.page.getByTestId("relay-credential").fill("sk-header-secret");
    await page.submitCreate(header);
    const headerRelay = await getRelay(request, header);
    expect(headerRelay.auth.type).toBe("header");
    expect(headerRelay.auth.header_name).toBe("X-API-Key");
    expect(headerRelay.auth.value_prefix).toBe("Token");
    // The credential is stored, but never echoed back in the clear.
    expect(headerRelay.credential?.value?.value ?? "").not.toBe("sk-header-secret");

    // Query parameter.
    const query = `e2e-pte-auth-query-${stamp}`;
    created.push(query);
    await page.openCreate();
    await page.page.getByTestId("relay-name").fill(query);
    await page.page.getByTestId("relay-new-base-url").fill("https://api.example.com");
    await page.selectAuthType("Query parameter");
    await expect(page.page.getByTestId("relay-header-name")).toHaveCount(0);
    await page.page.getByTestId("relay-query-param").fill("api_key");
    await page.page.getByTestId("relay-credential").fill("sk-query-secret");
    await page.submitCreate(query);
    const queryRelay = await getRelay(request, query);
    expect(queryRelay.auth.type).toBe("query");
    expect(queryRelay.auth.query_param).toBe("api_key");

    // Basic auth takes user:password in the one credential field.
    const basic = `e2e-pte-auth-basic-${stamp}`;
    created.push(basic);
    await page.openCreate();
    await page.page.getByTestId("relay-name").fill(basic);
    await page.page.getByTestId("relay-new-base-url").fill("https://api.example.com");
    await page.selectAuthType("Basic auth");
    await expect(page.page.getByTestId("relay-query-param")).toHaveCount(0);
    await expect(page.addSheet).toContainText("Credentials (Required)");
    await page.page.getByTestId("relay-credential").fill("user:password");
    await page.submitCreate(basic);
    const basicRelay = await getRelay(request, basic);
    expect(basicRelay.auth.type).toBe("basic");
  });

  test("creates relays with AWS SigV4 explicit credentials and with no authentication", async ({
    relaysPage: page,
    request,
  }) => {
    const stamp = Date.now();
    await page.goto();

    const aws = `e2e-pte-auth-aws-${stamp}`;
    created.push(aws);
    await page.openCreate();
    await page.page.getByTestId("relay-name").fill(aws);
    await page.page.getByTestId("relay-new-base-url").fill("https://textract.us-east-1.amazonaws.com");
    await page.selectAuthType("AWS SigV4");
    // SigV4 has no API key field; it signs with AWS credentials.
    await expect(page.page.getByTestId("relay-credential")).toHaveCount(0);
    await page.page.getByTestId("relay-aws-service").fill("textract");
    await expect(page.page.getByTestId("relay-aws-iam-role-tab")).toHaveAttribute("data-state", "active");
    await expect(page.page.getByTestId("relay-aws-access_key")).toHaveCount(0);
    await page.page.getByTestId("relay-aws-explicit-tab").click();
    await page.page.getByTestId("relay-aws-access_key").fill("AKIAE2EEXAMPLE");
    await page.page.getByTestId("relay-aws-secret_key").fill("e2e-secret-key");
    await page.page.getByTestId("relay-aws-region").fill("us-east-1");
    await page.submitCreate(aws);
    const awsRelay = await getRelay(request, aws);
    expect(awsRelay.auth.type).toBe("aws_sigv4");
    expect(awsRelay.auth.aws?.service).toBe("textract");
    expect(awsRelay.credential?.aws?.region?.value).toBe("us-east-1");
    expect(awsRelay.credential?.aws?.secret_key?.value ?? "").not.toBe("e2e-secret-key");

    const none = `e2e-pte-auth-none-${stamp}`;
    created.push(none);
    await page.openCreate();
    await page.page.getByTestId("relay-name").fill(none);
    await page.page.getByTestId("relay-new-base-url").fill("https://api.example.com");
    await page.selectAuthType("No authentication");
    await expect(page.page.getByTestId("relay-credential")).toHaveCount(0);
    await expect(page.page.getByTestId("relay-aws-service")).toHaveCount(0);
    await page.submitCreate(none);
    const noneRelay = await getRelay(request, none);
    expect(noneRelay.auth.type).toBe("none");
  });

  test("changes the authentication of an existing relay from the config sheet", async ({
    relaysPage: page,
    request,
  }) => {
    const name = `e2e-pte-auth-edit-${Date.now()}`;
    created.push(name);
    await page.goto();
    await page.createEndpoint(name, "https://api.example.com", "sk-e2e-secret");
    await page.select(name);

    await page.openConfigTab("authentication");
    await expect(page.authTypeTrigger).toContainText("Bearer token");
    await expect(page.saveButton("authentication")).toBeDisabled();
    await page.selectAuthType("Custom header");
    await page.page.getByTestId("relay-header-name").fill("X-Edited-Key");
    await page.page.getByTestId("relay-credential").fill("sk-edited-secret");
    await page.saveConfigTab("authentication");

    const relay = await getRelay(request, name);
    expect(relay.auth.type).toBe("header");
    expect(relay.auth.header_name).toBe("X-Edited-Key");

    // Reopening shows the saved type.
    await page.closeSheets();
    await page.openConfigTab("authentication");
    await expect(page.authTypeTrigger).toContainText("Custom header");
    await expect(page.page.getByTestId("relay-header-name")).toHaveValue("X-Edited-Key");
  });

  // ---------------------------------------------------------------------------------------------
  // Routes
  // ---------------------------------------------------------------------------------------------

  test("adds, edits, disables and removes routes with header matchers", async ({ relaysPage: page, request }) => {
    const name = `e2e-pte-routes-${Date.now()}`;
    created.push(name);
    await page.goto();
    await page.createEndpoint(name, "https://api.example.com", "sk-e2e-secret");
    // Give the relay a fee, so a billable route shows a price while a non-billable one stays free.
    const priced = await request.put(`${API}/${name}`, {
      data: {
        name,
        base_url: "https://api.example.com",
        auth: { type: "bearer" },
        credential: { value: { value: "sk-e2e-secret" } },
        usage: { cost_per_request: 0.001 },
      },
    });
    expect(priced.status(), await priced.text()).toBe(200);
    await page.goto();
    await page.select(name);
    await expect(page.routesEmpty).toBeVisible();

    // A GET route with a header matcher; GETs are free by default.
    await page.addRoute({
      method: "GET",
      path: "/v1/status",
      headers: [{ name: "X-Amz-Target", value: "Textract.Analyze*" }],
    });
    await expect(page.routesEmpty).toHaveCount(0);
    await expect(page.routeRow(0)).toContainText("GET");
    await expect(page.routeRow(0)).toContainText("/v1/status");
    await expect(page.routeRow(0)).toContainText("X-Amz-Target: Textract.Analyze*");
    await expect(page.routeRow(0)).toContainText("Free");
    await expect(page.routeEnabledSwitch(0)).toBeChecked();

    // Clearing a matcher's name keeps the row and its value, so saving is refused rather than
    // silently dropping the matcher and widening the route.
    await page.openEditRoute(0);
    await page.page.getByTestId("relay-route-header-name-0").fill("");
    await expect(page.page.getByTestId("relay-route-header-value-0")).toHaveValue("Textract.Analyze*");
    await page.page.getByTestId("relay-route-save").click();
    await expect(page.routeSheet).toContainText("A header matcher needs a header name");
    await page.page.getByTestId("relay-route-header-name-0").fill("X-Amz-Target");
    await page.saveRoute();
    await expect(page.routeRow(0)).toContainText("X-Amz-Target: Textract.Analyze*");

    // Renaming a second matcher to the first one's name is refused too, rather than saving its old name.
    await page.openEditRoute(0);
    await page.page.getByTestId("relay-route-header-name-1").fill("X-Kind");
    await page.page.getByTestId("relay-route-header-value-1").fill("poll");
    await page.page.getByTestId("relay-route-header-name-1").fill("X-Amz-Target");
    await page.page.getByTestId("relay-route-save").click();
    await expect(page.routeSheet).toContainText("This header is already matched on this route");
    await page.closeSheets();
    expect((await getRelay(request, name)).routes[0].headers).toEqual({ "X-Amz-Target": "Textract.Analyze*" });

    // A POST route (the default method) with no matcher. Routes are saved in match order, literal
    // paths A to Z, so /v1/search lands ahead of /v1/status.
    await page.addRoute({ path: "/v1/search" });
    await expect(page.routeRow(0)).toContainText("POST");
    await expect(page.routeRow(0)).toContainText("/v1/search");
    await expect(page.routeRow(0)).not.toContainText("Free");
    await expect(page.routeRow(1)).toContainText("/v1/status");

    let relay = await getRelay(request, name);
    expect(relay.routes).toHaveLength(2);
    expect(relay.routes[0]).toMatchObject({ method: "POST", path: "/v1/search" });
    expect(relay.routes[1].method).toBe("GET");
    expect(relay.routes[1].headers).toEqual({ "X-Amz-Target": "Textract.Analyze*" });

    // Edit: widen /v1/search into a catch-all, which is saved after the literal route it would otherwise shadow.
    await page.openEditRoute(0);
    await expect(page.page.getByTestId("relay-route-path")).toHaveValue("/v1/search");
    await page.fillRoute({ path: "/v1/search/**" });
    await page.saveRoute();
    await expect(page.routeRow(0)).toContainText("/v1/status");
    await expect(page.routeRow(1)).toContainText("/v1/search/**");

    // Disable from the row switch.
    await page.routeEnabledSwitch(1).click();
    await page.waitForSuccessToast("Route disabled");
    await expect(page.routeEnabledSwitch(1)).not.toBeChecked();
    relay = await getRelay(request, name);
    expect(relay.routes[1].path).toBe("/v1/search/**");
    expect(relay.routes[1].enabled).toBe(false);

    // Re-enable it.
    await page.routeEnabledSwitch(1).click();
    await page.waitForSuccessToast("Route enabled");
    await expect(page.routeEnabledSwitch(1)).toBeChecked();

    // Remove the first; the second moves up.
    await page.removeRoute(0);
    await expect(page.routeRow(1)).toHaveCount(0);
    await expect(page.routeRow(0)).toContainText("/v1/search/**");
    relay = await getRelay(request, name);
    expect(relay.routes).toHaveLength(1);
    expect(relay.routes[0].path).toBe("/v1/search/**");

    await page.removeRoute(0);
    await expect(page.routesEmpty).toBeVisible();
    relay = await getRelay(request, name);
    expect(relay.routes ?? []).toHaveLength(0);
  });

  test("refuses letters in a route price and clears a price back to the relay's", async ({ relaysPage: page, request }) => {
    const name = `e2e-pte-price-${Date.now()}`;
    created.push(name);
    await page.goto();
    await page.createEndpoint(name, "https://api.example.com", "sk-e2e-secret");
    await page.select(name);

    await page.openAddRoute();
    await page.fillRoute({ path: "/search" });
    await page.page.getByTestId("relay-route-pricing-toggle").click();
    const fee = page.page.getByTestId("relay-route-usage-cost-per-request");
    // Letters and signs are refused as typed, rather than shown while the form holds nothing.
    await fee.pressSequentially("e-1");
    await expect(fee).toHaveValue("1");
    await fee.fill("0.5");
    await page.saveRoute();
    let relay = await getRelay(request, name);
    expect(relay.routes[0].usage).toEqual({ cost_per_request: 0.5 });

    // Emptying the field stays empty and saves the route with no override.
    await page.openEditRoute(0);
    await page.page.getByTestId("relay-route-pricing-toggle").click();
    await expect(fee).toHaveValue("0.5");
    await fee.fill("");
    await expect(fee).toHaveValue("");
    await page.saveRoute();
    relay = await getRelay(request, name);
    expect(relay.routes[0].usage).toBeUndefined();
  });

  test("imports routes from an OpenAPI document fetched by URL", async ({ relaysPage: page, request }) => {
    // The gateway fetches the document (the browser could not, across origins); a relative server
    // resolves against the URL the document was read from.
    const spec = JSON.stringify({
      openapi: "3.0.0",
      info: { title: "Search API", version: "2" },
      servers: [{ url: "/v1" }],
      paths: { "/search": { post: { responses: { "200": { description: "ok" } } } }, "/status": { get: { responses: { "200": { description: "ok" } } } } },
    });
    const specServer = createServer((req, res) => {
      if (req.url === "/slow.json") {
        // Answers well after the dialog is cancelled; its document must not reach the reopened one.
        setTimeout(() => {
          res.writeHead(200, { "Content-Type": "application/json" });
          res.end(spec);
        }, 3000);
        return;
      }
      if (req.url === "/openapi.json") {
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(spec);
        return;
      }
      res.writeHead(404).end();
    });
    await new Promise<void>((resolve) => specServer.listen(0, "127.0.0.1", () => resolve()));
    const { port } = specServer.address() as AddressInfo;
    try {
      const name = `e2e-pte-import-${Date.now()}`;
      created.push(name);
      await page.goto();
      await page.createEndpoint(name, "https://api.example.com", "sk-e2e-secret");
      await page.select(name);

      await page.page.getByTestId("relay-routes-import").click();
      await page.page.getByTestId("openapi-spec-source-tab-url").click();
      const url = page.page.getByTestId("openapi-spec-url-input");

      // Cancelling during a fetch leaves nothing behind: the reopened dialog can fetch at once, and
      // the cancelled request's document never shows up in it.
      await url.fill(`http://127.0.0.1:${port}/slow.json`);
      await page.page.getByTestId("openapi-parse-btn").click();
      await expect(page.page.getByTestId("openapi-parse-btn")).toBeDisabled();
      await page.page.keyboard.press("Escape");
      await page.page.getByTestId("relay-routes-import").click();
      await page.page.getByTestId("openapi-spec-source-tab-url").click();
      await url.fill("https://example.invalid/spec.json");
      await expect(page.page.getByTestId("openapi-parse-btn")).toBeEnabled();
      await page.page.waitForTimeout(3500);
      await expect(page.page.getByTestId("relay-openapi-preview")).toHaveCount(0);

      // A relative address is refused before anything is fetched.
      await url.fill("/openapi.json");
      await page.page.getByTestId("openapi-parse-btn").click();
      await expect(page.page.getByTestId("openapi-spec-url-error")).toContainText("absolute http(s) URL");

      // A missing document reports the gateway's error.
      await url.fill(`http://127.0.0.1:${port}/missing.json`);
      await page.page.getByTestId("openapi-parse-btn").click();
      await expect(page.page.getByTestId("openapi-parse-error")).toContainText(/could not load spec_url/i);

      await url.fill(`http://127.0.0.1:${port}/openapi.json`);
      await page.page.getByTestId("openapi-parse-btn").click();
      const preview = page.page.getByTestId("relay-openapi-preview");
      await expect(preview).toContainText("Search API");
      await expect(preview).toContainText("2 routes");
      await expect(preview).toContainText(`http://127.0.0.1:${port}/v1`);
      await page.page.getByTestId("relay-openapi-import").click();

      // The import is staged for review before it replaces the routes.
      await page.page.getByRole("button", { name: "Apply import" }).click();
      await page.waitForSuccessToast();
      const relay = await getRelay(request, name);
      expect(relay.routes.map((r: { method: string; path: string }) => `${r.method} ${r.path}`)).toEqual(["POST /search", "GET /status"]);
    } finally {
      await new Promise<void>((resolve) => specServer.close(() => resolve()));
    }
  });

  // ---------------------------------------------------------------------------------------------
  // Advanced, Network and Proxy tabs
  // ---------------------------------------------------------------------------------------------

  test("saves the Advanced, Network and Proxy tabs of the config sheet", async ({ relaysPage: page, request }) => {
    const name = `e2e-pte-tabs-${Date.now()}`;
    created.push(name);
    await page.goto();
    await page.createEndpoint(name, "https://api.example.com", "sk-e2e-secret");
    await page.select(name);

    // Advanced: always-stream paths, an extra header and the reported-cost switch.
    await page.openConfigTab("advanced");
    await expect(page.saveButton("advanced")).toBeDisabled();
    await page.page.getByTestId("relay-streaming-paths").fill("/v1/stream/**");
    await page.page.getByTestId("relay-extra-header-name-0").fill("X-Tenant");
    await page.page.getByTestId("relay-extra-header-value-0").fill("acme");
    await page.page.getByTestId("relay-ignore-upstream-cost").click();
    await expect(page.page.getByTestId("relay-ignore-upstream-cost")).toHaveAttribute("data-state", "checked");
    await page.saveConfigTab("advanced");
    let relay = await getRelay(request, name);
    expect(relay.streaming?.paths).toEqual(["/v1/stream/**"]);
    expect(Object.keys(relay.extra_headers ?? {})).toEqual(["X-Tenant"]);
    expect(relay.ignore_upstream_cost).toBe(true);

    // Network: the timeout and TLS verification.
    await page.openConfigTab("network");
    await expect(page.saveButton("network")).toBeDisabled();
    await expect(page.configSheet).toContainText("Timeout (seconds)");
    await page.configSheet.getByPlaceholder("60").first().fill("30");
    await page.page.getByTestId("relay-insecure-skip-verify").click();
    await expect(page.page.getByTestId("relay-insecure-skip-verify")).toHaveAttribute("data-state", "checked");
    await page.saveConfigTab("network");
    relay = await getRelay(request, name);
    expect(relay.network?.timeout_seconds).toBe(30);
    expect(relay.network?.insecure_skip_verify).toBe(true);

    // Proxy: an HTTP proxy with its URL; the CA field accepts a PEM and is cleared again so the
    // save does not depend on a real certificate.
    await page.openConfigTab("proxy");
    await expect(page.saveButton("proxy")).toBeDisabled();
    await expect(page.page.getByTestId("relay-proxy-url")).toBeHidden();
    await page.page.getByTestId("relay-proxy-type").click();
    await page.page.getByRole("option", { name: "HTTP", exact: true }).click();
    await expect(page.page.getByTestId("relay-proxy-url")).toBeVisible();
    await page.page.getByTestId("relay-proxy-url").fill("http://proxy.example.com:8080");
    const caCert = page.page.getByTestId("relay-proxy-ca-cert-pem");
    await expect(caCert).toBeVisible();
    await caCert.fill("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----");
    await expect(caCert).toHaveValue(/BEGIN CERTIFICATE/);
    await caCert.fill("");
    await page.saveConfigTab("proxy");
    relay = await getRelay(request, name);
    expect(relay.proxy_config?.type).toBe("http");
    // The proxy URL can carry credentials, so it is read back redacted; it must still be set.
    expect(relay.proxy_config?.url?.value ?? "").not.toBe("");

    // The tabs keep their saved values on reopen.
    await page.closeSheets();
    await page.openConfigTab("proxy");
    await expect(page.page.getByTestId("relay-proxy-type")).toContainText("HTTP");
    await page.openConfigTab("advanced");
    await expect(page.page.getByTestId("relay-streaming-paths")).toHaveValue("/v1/stream/**");
    await expect(page.page.getByTestId("relay-extra-header-name-0")).toHaveValue("X-Tenant");
  });

  // ---------------------------------------------------------------------------------------------
  // Virtual key details: relay access
  // ---------------------------------------------------------------------------------------------

  test("virtual key details list the granted relays", async ({ virtualKeysPage, request }) => {
    const stamp = Date.now();
    const relayName = `e2e-pte-vk-grant-${stamp}`;
    created.push(relayName);
    const relayRes = await request.post(API, {
      data: { name: relayName, base_url: "https://api.example.com", auth: { type: "bearer" }, credential: { value: { value: "sk-e2e" } } },
    });
    expect(relayRes.status(), await relayRes.text()).toBe(201);

    const vkName = `e2e-pte-vk-granted-${stamp}`;
    const vkRes = await request.post(VK_API, {
      data: { name: vkName, is_active: true, relay_grants: [{ relay_name: relayName }] },
    });
    expect(vkRes.ok(), await vkRes.text()).toBeTruthy();
    createdVkIds.push((await vkRes.json()).virtual_key.id);

    await virtualKeysPage.goto();
    await virtualKeysPage.searchVirtualKeys(vkName);
    const row = virtualKeysPage.getVirtualKeyRow(vkName);
    await expect(row).toBeVisible({ timeout: 10000 });
    await row.getByText(vkName, { exact: true }).click();

    const access = virtualKeysPage.page.getByTestId("vk-details-relay-access");
    await expect(access).toBeVisible({ timeout: 10000 });
    await expect(access).toContainText("Relay access");
    await expect(virtualKeysPage.page.getByTestId(`vk-details-relay-grant-${relayName}`)).toBeVisible();
    await expect(virtualKeysPage.page.getByTestId(`vk-details-relay-grant-${relayName}`)).toHaveText(relayName);
    await expect(virtualKeysPage.page.getByTestId("vk-details-relay-none")).toHaveCount(0);
  });

  test("virtual key details show no relays, then the relay granted from the edit sheet", async ({
    virtualKeysPage,
    request,
  }) => {
    const stamp = Date.now();
    const relayName = `e2e-pte-vk-pick-${stamp}`;
    created.push(relayName);
    const relayRes = await request.post(API, {
      data: { name: relayName, base_url: "https://api.example.com", auth: { type: "bearer" }, credential: { value: { value: "sk-e2e" } } },
    });
    expect(relayRes.status(), await relayRes.text()).toBe(201);

    const vkName = `e2e-pte-vk-pick-${stamp}`;
    const vkRes = await request.post(VK_API, { data: { name: vkName, is_active: true } });
    expect(vkRes.ok(), await vkRes.text()).toBeTruthy();
    const vkId = (await vkRes.json()).virtual_key.id as string;
    createdVkIds.push(vkId);

    await virtualKeysPage.goto();
    await virtualKeysPage.searchVirtualKeys(vkName);
    const row = virtualKeysPage.getVirtualKeyRow(vkName);
    await expect(row).toBeVisible({ timeout: 10000 });
    await row.getByText(vkName, { exact: true }).click();
    await expect(virtualKeysPage.page.getByTestId("vk-details-relay-none")).toBeVisible({ timeout: 10000 });
    await expect(virtualKeysPage.page.getByTestId("vk-details-relay-none")).toHaveText("No relays granted");
    await virtualKeysPage.page.keyboard.press("Escape");
    await expect(virtualKeysPage.page.getByTestId("vk-details-relay-access")).toBeHidden();

    // Grant the relay from the edit sheet's picker and save. A key is grant rows only: there is
    // no allow-all switch, exactly as for its agent and MCP grants.
    await virtualKeysPage.viewVirtualKey(vkName);
    const editor = virtualKeysPage.page.getByTestId("vk-relays-editor");
    await expect(editor).toBeVisible();
    await expect(virtualKeysPage.page.getByTestId("vk-relays-allow-all")).toHaveCount(0);
    await editor.getByRole("combobox").click();
    await virtualKeysPage.page.getByPlaceholder("Search relays...").fill(relayName);
    await virtualKeysPage.page.getByRole("option", { name: relayName, exact: true }).click();
    await expect(virtualKeysPage.page.getByTestId(`vk-relays-grant-${relayName}`)).toBeVisible();
    await expect(virtualKeysPage.saveBtn).toBeEnabled();
    await virtualKeysPage.saveBtn.click();
    await virtualKeysPage.waitForSuccessToast();
    await expect(virtualKeysPage.sheet).toBeHidden({ timeout: 10000 });

    const saved = await request.get(`${VK_API}/${vkId}`);
    expect(saved.status(), await saved.text()).toBe(200);
    const grants = (await saved.json()).virtual_key.relay_grants as { relay_name: string }[];
    expect(grants.map((grant) => grant.relay_name)).toEqual([relayName]);

    await virtualKeysPage.searchVirtualKeys(vkName);
    await expect(virtualKeysPage.getVirtualKeyRow(vkName)).toBeVisible({ timeout: 10000 });
    await virtualKeysPage.getVirtualKeyRow(vkName).getByText(vkName, { exact: true }).click();
    await expect(virtualKeysPage.page.getByTestId(`vk-details-relay-grant-${relayName}`)).toBeVisible({ timeout: 10000 });
    await expect(virtualKeysPage.page.getByTestId(`vk-details-relay-grant-${relayName}`)).toHaveText(relayName);
    await expect(virtualKeysPage.page.getByTestId("vk-details-relay-none")).toHaveCount(0);
  });
});
