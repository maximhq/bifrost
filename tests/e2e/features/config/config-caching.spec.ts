import { expect, test } from "../../core/fixtures/base.fixture";
import { ConfigSettingsState } from "./pages/config-settings.page";

test.describe("Caching Settings", () => {
  test.describe.configure({ mode: "serial" });

  let originalState: ConfigSettingsState;

  test.beforeEach(async ({ configSettingsPage }) => {
    await configSettingsPage.goto("caching");
    originalState = await configSettingsPage.getCurrentSettings("caching");
  });

  test.afterEach(async ({ configSettingsPage }) => {
    if (originalState) {
      await configSettingsPage.restoreSettings(originalState);
    }
  });

  test("should display caching settings", async ({ configSettingsPage }) => {
    await expect(
      configSettingsPage.page.getByRole("heading", { name: /Local Cache/i }),
    ).toBeVisible();
  });
});

// The form only accepts edits while the cache plugin is enabled on a connected vector store, which
// the shared e2e server may not have. These routes report both, and capture the save instead of
// forwarding it, so the switch's wire contract is checked without changing the server's plugins.
test.describe("Caching identity scoping", () => {
  test("Scope Cache by Identity saves as disable_identity_scoping", async ({
    configSettingsPage,
  }) => {
    const page = configSettingsPage.page;
    const savedBodies: { config: Record<string, unknown> }[] = [];
    let savedConfig: Record<string, unknown> = {
      dimension: 1,
      ttl: 300,
      threshold: 0.8,
      cache_by_model: true,
      cache_by_provider: true,
    };
    const pluginRow = () => ({
      name: "semantic_cache",
      actualName: "semantic_cache",
      enabled: true,
      config: savedConfig,
      isCustom: false,
      path: null,
      status: { name: "semantic_cache", status: "active", logs: [], types: ["llm"] },
    });

    await page.route(
      (url) => url.pathname === "/api/config",
      async (route) => {
        if (route.request().method() !== "GET") return route.continue();
        const response = await route.fetch();
        const body = await response.json();
        await route.fulfill({ response, json: { ...body, is_cache_connected: true } });
      },
    );
    await page.route(
      (url) => url.pathname === "/api/plugins",
      async (route) => {
        if (route.request().method() !== "GET") return route.continue();
        const response = await route.fetch();
        const body = await response.json();
        const others = (body.plugins ?? []).filter(
          (p: { name: string }) => p.name !== "semantic_cache",
        );
        const plugins = [...others, pluginRow()];
        await route.fulfill({ response, json: { plugins, count: plugins.length } });
      },
    );
    await page.route(
      (url) => url.pathname === "/api/plugins/semantic_cache",
      async (route) => {
        if (route.request().method() !== "PUT") return route.continue();
        const body = route.request().postDataJSON() as { config: Record<string, unknown> };
        savedBodies.push(body);
        savedConfig = body.config;
        await route.fulfill({ json: { message: "Plugin updated successfully", plugin: pluginRow() } });
      },
    );

    await configSettingsPage.goto("caching");
    const identitySwitch = page.getByTestId("caching-identity-scoping-switch");
    const saveButton = page.getByTestId("caching-save-button");

    // A saved config without the field means scoping is on.
    await expect(identitySwitch).toHaveAttribute("data-state", "checked");

    await identitySwitch.click();
    await expect(identitySwitch).toHaveAttribute("data-state", "unchecked");
    await saveButton.click();
    await expect.poll(() => savedBodies.length).toBe(1);
    expect(savedBodies[0].config.disable_identity_scoping).toBe(true);
    await expect(identitySwitch).toHaveAttribute("data-state", "unchecked");

    await identitySwitch.click();
    await expect(identitySwitch).toHaveAttribute("data-state", "checked");
    await saveButton.click();
    await expect.poll(() => savedBodies.length).toBe(2);
    expect(savedBodies[1].config.disable_identity_scoping).toBe(false);
  });
});