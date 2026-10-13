import { expect, test } from "../../core/fixtures/base.fixture";
import { createProviderKeyData } from "./providers.data";

test.describe("Provider Key Management", () => {
  test.describe.configure({ mode: "serial" });

  // Track keys for cleanup in this test suite
  const managementKeys: string[] = [];

  test.beforeEach(async ({ providersPage }) => {
    await providersPage.goto();
    await providersPage.selectProvider("openai");
  });

  test.afterEach(async ({ providersPage }) => {
    // Clean up any keys created during tests
    for (const keyName of [...managementKeys]) {
      try {
        const exists = await providersPage.keyExists(keyName, 2000);
        if (exists) {
          await providersPage.deleteKey(keyName);
        }
      } catch (error) {
        const errorMsg = error instanceof Error ? error.message : String(error);
        console.error(
          `[CLEANUP ERROR] Failed to delete provider key ${keyName}: ${errorMsg}`,
        );
      }
    }
    managementKeys.length = 0;
  });

  test("should edit an existing key", async ({ providersPage }) => {
    // First add a key
    const keyData = createProviderKeyData({
      name: `Edit-Test-Key-${Date.now()}`,
      value: "sk-test-edit-key",
    });

    // Track for cleanup
    managementKeys.push(keyData.name);

    await providersPage.addKey(keyData);

    // Now edit it - set weight to 0.7
    await providersPage.editKey(keyData.name, {
      weight: 0.7,
    });

    // Verify weight was saved and displayed (wait for table to refresh after save)
    const keyRow = providersPage.getKeyRow(keyData.name);
    await expect(keyRow.getByTestId("key-weight-value")).toContainText("0.7", {
      timeout: 10000,
    });
  });

  test("should delete a key", async ({ providersPage }) => {
    // First add a key
    const keyData = createProviderKeyData({
      name: `Delete-Test-Key-${Date.now()}`,
      value: "sk-test-delete-key",
    });

    // Don't track for cleanup - we're testing delete

    await providersPage.addKey(keyData);

    // Verify it exists
    let keyExists = await providersPage.keyExists(keyData.name);
    expect(keyExists).toBe(true);

    // Delete it
    await providersPage.deleteKey(keyData.name);

    // Verify it's gone (use short timeout since we expect it to be gone)
    keyExists = await providersPage.keyExists(keyData.name, 1000);
    expect(keyExists).toBe(false);
  });

  test("should toggle key enabled state", async ({ providersPage }) => {
    // First add a key
    const keyData = createProviderKeyData({
      name: `Toggle-Test-Key-${Date.now()}`,
      value: "sk-test-toggle-key",
    });

    // Track for cleanup
    managementKeys.push(keyData.name);

    await providersPage.addKey(keyData);

    // Key starts enabled
    let isEnabled = await providersPage.getKeyEnabledState(keyData.name);
    expect(isEnabled).toBe(true);

    // Toggle to disabled
    await providersPage.toggleKeyEnabled(keyData.name);
    await expect
        .poll(async () => providersPage.getKeyEnabledState(keyData.name), {
            timeout: 10000,
        })
      .toBe(false);
    isEnabled = await providersPage.getKeyEnabledState(keyData.name);
    expect(isEnabled).toBe(false);
  });

  test("should display Allowed Models field when adding a key", async ({
    providersPage,
  }) => {
    await providersPage.addKeyBtn.click();
    await expect(providersPage.keyForm).toBeVisible();

    const allowedModels = providersPage.page.getByTestId(
      "api-keys-models-multiselect",
    );
    await expect(allowedModels).toBeVisible();

    // "All Models" should be selected by default
    await expect(allowedModels.getByText("All Models")).toBeVisible();

    await providersPage.keyCancelBtn.click();
  });

  test("should display Blocked Models field when adding a key", async ({
    providersPage,
  }) => {
    await providersPage.addKeyBtn.click();
    await expect(providersPage.keyForm).toBeVisible();

    const blockedModelsField = providersPage.page.getByTestId(
      "apikey-blacklisted-models-field",
    );
    await expect(blockedModelsField).toBeVisible();

    const blockedModelsMultiselect = providersPage.page.getByTestId(
      "api-keys-blocked-models-multiselect",
    );
    await expect(blockedModelsMultiselect).toBeVisible();

    await providersPage.keyCancelBtn.click();
  });
});
