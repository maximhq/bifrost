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