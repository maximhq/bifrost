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

  test.describe("AWS SageMaker keys", () => {
    test("should create, reopen and edit a key with explicit AWS credentials", async ({ providersPage, page }) => {
      const keyName = `SageMaker-Key-${Date.now()}`;
      const createdProvider = !(await providersPage.providerExists("sagemaker"));
      try {
        if (createdProvider) {
          await providersPage.addKnownProviderFromDropdown("sagemaker");
        } else {
          await providersPage.selectProvider("sagemaker");
        }

        await providersPage.addSageMakerKey({
          name: keyName,
          accessKey: "AKIAE2ESAGEMAKER0001",
          secretKey: "e2e-sagemaker-secret-key-0001",
          region: "us-east-1",
        });
        expect(await providersPage.keyExists(keyName)).toBe(true);

        // Reopening must detect the explicit-credentials tab from the stored config and show the
        // non-secret fields verbatim; the secret key comes back masked.
        await providersPage.openKeyEditor(keyName);
        await expect(page.getByTestId("apikey-sagemaker-explicit-credentials-tab")).toHaveAttribute("data-state", "active");
        await expect(page.getByTestId("apikey-sagemaker-region-input")).toHaveValue("us-east-1");
        await expect(page.getByTestId("apikey-sagemaker-secret-key-input")).toHaveValue(/.+/);
        await expect(page.getByTestId("apikey-sagemaker-secret-key-input")).not.toHaveValue("e2e-sagemaker-secret-key-0001");

        // Saving with the masked secrets untouched must keep the stored credentials.
        await page.getByTestId("apikey-sagemaker-region-input").fill("us-west-2");
        await providersPage.keySaveBtn.click();
        await providersPage.waitForSuccessToast();
        await expect(providersPage.keyForm).not.toBeVisible({ timeout: 5000 });

        await providersPage.openKeyEditor(keyName);
        await expect(page.getByTestId("apikey-sagemaker-region-input")).toHaveValue("us-west-2");
        // Both stored credentials must survive the save, still masked.
        await expect(page.getByTestId("apikey-sagemaker-access-key-input")).toHaveValue(/.+/);
        await expect(page.getByTestId("apikey-sagemaker-secret-key-input")).toHaveValue(/.+/);
        await expect(page.getByTestId("apikey-sagemaker-secret-key-input")).not.toHaveValue("e2e-sagemaker-secret-key-0001");
        await providersPage.keyCancelBtn.click();
        await expect(providersPage.keyForm).not.toBeVisible({ timeout: 5000 });
      } finally {
        if (await providersPage.keyExists(keyName, 2000)) {
          await providersPage.deleteKey(keyName);
        }
        if (createdProvider && (await providersPage.providerExists("sagemaker"))) {
          await providersPage.deleteProvider("sagemaker", { skipToastWait: true });
          await expect(providersPage.getProviderItem("sagemaker")).toHaveCount(0, { timeout: 5000 });
        }
      }
    });
  });

  test.describe("GitHub Copilot sign in with GitHub", () => {
    let addedCopilotProvider = false;

    test.beforeEach(async ({ providersPage }) => {
      if (!(await providersPage.providerExists("github-copilot"))) {
        await providersPage.addKnownProviderFromDropdown("github-copilot");
        addedCopilotProvider = true;
      }
      await providersPage.selectProvider("github-copilot");
    });

    test.afterAll(async ({ request }) => {
      if (addedCopilotProvider) {
        await request.delete("/api/providers/github-copilot");
      }
    });

    // GitHub is mocked at the gateway's sign-in endpoints: the device flow itself needs a person
    // to approve on github.com. This pins the form side: the code is shown, polling finishes, the
    // returned token lands in the key's value, and the key saves with it.
    test("should sign in with the device flow and save the returned token as the key's value", async ({
      providersPage,
      request,
    }) => {
      const page = providersPage.page;

      await page.route("**/api/providers/github-copilot/sign-in/poll", async (route) => {
        await route.fulfill({ json: { status: "complete", token: "ghu_e2e_signed_in", login: "octocat" } });
      });
      await page.route("**/api/providers/github-copilot/sign-in", async (route) => {
        await route.fulfill({
          json: {
            device_code: "dc_e2e",
            user_code: "WDJB-MJHT",
            verification_uri: "https://github.com/login/device",
            expires_in: 900,
            interval: 1,
          },
        });
      });

      const keyName = `Copilot-Sign-In-${Date.now()}`;
      managementKeys.push(keyName);
      await providersPage.startCopilotSignIn({ name: keyName });

      await expect(page.getByTestId("copilot-sign-in-user-code")).toHaveText("WDJB-MJHT");
      await expect(page.getByTestId("copilot-sign-in-verification-link")).toHaveAttribute(
        "href",
        "https://github.com/login/device",
      );
      await expect(page.getByTestId("copilot-sign-in-status")).toContainText("Signed in as @octocat", { timeout: 10000 });
      await expect(page.getByTestId("key-input-copilot-token")).toHaveValue("ghu_e2e_signed_in");

      await providersPage.keySaveBtn.click();
      await providersPage.waitForSuccessToast();
      await expect(providersPage.keyForm).not.toBeVisible({ timeout: 5000 });

      const response = await request.get("/api/providers/github-copilot/keys");
      expect(response.ok(), await response.text()).toBeTruthy();
      const body = await response.json();
      const saved = (body.keys as Array<{ name: string; value?: { value?: string } }>).find((k) => k.name === keyName);
      expect(saved?.value?.value, "the signed-in token was not saved as the key's value").toBeTruthy();
    });
  });
});
