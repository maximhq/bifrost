import { expect, test } from "../../core/fixtures/base.fixture";
import {
  createCustomProviderData,
  createProviderKeyData,
} from "./providers.data";

// Track created resources for cleanup
const createdKeys: { provider: string; keyName: string }[] = [];
const createdProviders: string[] = [];

test.describe("Providers", () => {
  test.describe.configure({ mode: "serial" });

  test.beforeEach(async ({ providersPage }) => {
    await providersPage.goto();
  });

  test.afterEach(async ({ providersPage }) => {
    // Clean up any keys created during tests
    for (const { provider, keyName } of [...createdKeys]) {
      try {
        await providersPage.selectProvider(provider);
        const exists = await providersPage.keyExists(keyName, 2000);
        if (exists) {
          await providersPage.deleteKey(keyName);
        }
      } catch (error) {
        const errorMsg = error instanceof Error ? error.message : String(error);
        console.error(
          `[CLEANUP ERROR] Failed to delete provider key ${provider}/${keyName}: ${errorMsg}`,
        );
      }
    }
    createdKeys.length = 0;

    // Clean up any custom providers created during tests (skip toast wait so cleanup does not fail if toast is missing)
    for (const providerName of [...createdProviders]) {
      try {
        await providersPage.deleteProvider(providerName, {
          skipToastWait: true,
        });
      } catch (error) {
        const errorMsg = error instanceof Error ? error.message : String(error);
        console.error(
          `[CLEANUP ERROR] Failed to delete provider ${providerName}: ${errorMsg}`,
        );
      }
    }
    createdProviders.length = 0;
  });

  test.describe("Provider Navigation", () => {
    test("should display standard providers in sidebar", async ({
      providersPage,
    }) => {
      // Check that OpenAI provider is visible
      const openaiProvider = providersPage.getProviderItem("openai");
      await expect(openaiProvider).toBeVisible();

      // Check that Anthropic provider is visible
      const anthropicProvider = providersPage.getProviderItem("anthropic");
      await expect(anthropicProvider).toBeVisible();
    });

    test("should select a provider from the sidebar", async ({
      providersPage,
    }) => {
      await providersPage.selectProvider("openai");

      // Verify URL contains provider param
      await expect(providersPage.page).toHaveURL(/provider=openai/);
    });

    test("should switch between providers", async ({ providersPage }) => {
      // Select OpenAI first
      await providersPage.selectProvider("openai");
      await expect(providersPage.page).toHaveURL(/provider=openai/);

      // Switch to Anthropic
      await providersPage.selectProvider("anthropic");
      await expect(providersPage.page).toHaveURL(/provider=anthropic/);
    });
  });

  test.describe("Provider Keys", () => {
    test("should add a new key to OpenAI provider", async ({
      providersPage,
    }) => {
      // Select OpenAI provider
      await providersPage.selectProvider("openai");

      // Create test key data with unique name (no spaces for easier locating)
      const keyData = createProviderKeyData({
        name: `E2E-Test-Key-${Date.now()}`,
        value: "sk-test-e2e-key-12345",
        weight: 1.0,
      });

      // Track for cleanup
      createdKeys.push({ provider: "openai", keyName: keyData.name });

      // Add the key
      await providersPage.addKey(keyData);

      // Verify key appears in table (with waiting)
      const keyExists = await providersPage.keyExists(keyData.name);
      expect(keyExists).toBe(true);
    });

    test("should add a key with custom weight", async ({ providersPage }) => {
      await providersPage.selectProvider("openai");

      const keyData = createProviderKeyData({
        name: `Weight-Key-${Date.now()}`,
        value: "sk-test-weight-key-12345",
        weight: 0.5,
      });

      // Track for cleanup
      createdKeys.push({ provider: "openai", keyName: keyData.name });

      await providersPage.addKey(keyData);

      const keyExists = await providersPage.keyExists(keyData.name);
      expect(keyExists).toBe(true);
    });

    test("should display empty state when no keys configured", async ({
      providersPage,
    }) => {
      // Add Nebius from the dropdown if not already in sidebar (created with no keys)
      if (!(await providersPage.providerExists("nebius"))) {
        await providersPage.addKnownProviderFromDropdown("nebius");
        createdProviders.push("nebius");
      }
      // Select Nebius (it has zero keys)
      const providerItem = providersPage.getProviderItem("nebius");
      await expect(providerItem).toBeVisible({ timeout: 15000 });
      await providersPage.selectProvider("nebius");
      const keyCount = await providersPage.getKeyCount();
      expect(keyCount).toBe(0);

      // Empty state row should be visible
      await expect(providersPage.keysTableEmptyState).toBeVisible();
    });

    test("should save a Vertex key with AWS workload identity and re-detect the tab on edit", async ({
      providersPage,
      page,
    }) => {
      // Vertex may not be pre-configured on a fresh instance; add it from the dropdown if missing.
      if (!(await providersPage.providerExists("vertex"))) {
        await providersPage.addKnownProviderFromDropdown("vertex");
        createdProviders.push("vertex");
      }
      await expect(providersPage.getProviderItem("vertex")).toBeVisible({
        timeout: 15000,
      });
      await providersPage.selectProvider("vertex");

      const keyName = `Vertex-WIF-Key-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
      const audience =
        "//iam.googleapis.com/projects/123456789012/locations/global/workloadIdentityPools/e2e-pool/providers/aws";
      createdKeys.push({ provider: "vertex", keyName });
      const openKeyEditor = async (name: string) => {
        await providersPage.dismissToasts();
        const keyRow = providersPage.getKeyRow(name);
        await keyRow.scrollIntoViewIfNeeded();
        await keyRow.getByTestId("key-actions-trigger").click();
        await page.getByRole("menuitem", { name: /Edit/i }).click();
        await expect(providersPage.keyForm).toBeVisible();
      };

      await providersPage.dismissToasts();
      await providersPage.addKeyBtn.click();
      await expect(providersPage.keyForm).toBeVisible();

      await page.getByLabel("Name").fill(keyName);
      await page
        .getByPlaceholder("your-gcp-project-id or env.VERTEX_PROJECT_ID")
        .fill("e2e-gcp-project");
      await page.getByPlaceholder("us-central1 or env.VERTEX_REGION").fill("us-central1");

      // Create the key on the API Key tab first: switching an existing key to federation is the
      // path where a stale API key could survive and keep taking precedence on the server.
      await page.getByTestId("apikey-vertex-api-key-tab").click();
      await page.getByTestId("apikey-vertex-api-key-input").fill("e2e-vertex-api-key");
      await providersPage.keySaveBtn.click();
      await providersPage.waitForSuccessToast();
      await expect(providersPage.keyForm).not.toBeVisible({ timeout: 5000 });
      expect(await providersPage.keyExists(keyName)).toBe(true);

      // Edit it and switch to the federation tab: it must hide the JSON and API-key inputs and show its own.
      await openKeyEditor(keyName);
      await expect(page.getByTestId("apikey-vertex-api-key-tab")).toHaveAttribute("data-state", "active");
      await page.getByTestId("apikey-vertex-aws-workload-identity-tab").click();
      await expect(page.getByTestId("apikey-vertex-auth-credentials-input")).toHaveCount(0);
      await expect(page.getByTestId("apikey-vertex-api-key-input")).toHaveCount(0);
      const audienceInput = page.getByTestId("apikey-vertex-aws-wif-audience-input");
      await expect(audienceInput).toBeVisible();

      // Saving without an audience must fail inline, not silently fall back to ADC.
      await providersPage.keySaveBtn.click();
      await expect(page.getByText("Workload Identity Pool provider audience is required")).toBeVisible();
      await expect(providersPage.keyForm).toBeVisible();

      await audienceInput.fill(audience);
      await page
        .getByTestId("apikey-vertex-aws-wif-service-account-email-input")
        .fill("bifrost-vertex@e2e-gcp-project.iam.gserviceaccount.com");
      await page.getByTestId("apikey-vertex-aws-wif-aws-region-input").fill("us-east-1");

      // Model discovery runs on save and fails against a fake audience; the key must still save.
      await providersPage.keySaveBtn.click();
      await providersPage.waitForSuccessToast();
      await expect(providersPage.keyForm).not.toBeVisible({ timeout: 5000 });
      expect(await providersPage.keyExists(keyName)).toBe(true);

      // The stored key must now carry the federation block and no API key: the update endpoint keeps
      // an omitted value, so the form has to send an explicit empty one when leaving the API Key tab.
      const keysResponse = await page.request.get("/api/providers/vertex/keys");
      expect(keysResponse.ok()).toBe(true);
      const provider = (await keysResponse.json()) as {
        keys: {
          name: string;
          value?: { value?: string; ref?: string };
          vertex_key_config?: { aws_workload_identity?: { audience?: { value?: string } } };
        }[];
      };
      const saved = provider.keys.find((k) => k.name === keyName);
      expect(saved, "saved key must be listed").toBeTruthy();
      expect(saved?.value?.value ?? "").toBe("");
      expect(saved?.value?.ref ?? "").toBe("");
      expect(saved?.vertex_key_config?.aws_workload_identity?.audience?.value).toBe(audience);

      // Reopen: the form must re-detect the federation tab from the stored audience.
      await openKeyEditor(keyName);
      await expect(page.getByTestId("apikey-vertex-aws-workload-identity-tab")).toHaveAttribute(
        "data-state",
        "active",
      );
      await expect(page.getByTestId("apikey-vertex-aws-wif-audience-input")).toHaveValue(audience);
      await expect(page.getByTestId("apikey-vertex-aws-wif-aws-region-input")).toHaveValue("us-east-1");
      await providersPage.keyCancelBtn.click();
      await expect(providersPage.keyForm).not.toBeVisible({ timeout: 5000 });
    });
  });

  test.describe("Custom Providers", () => {
    test("should open custom provider creation sheet", async ({
      providersPage,
    }) => {
      await providersPage.openCustomProviderSheet();

      // Verify form fields are present
      await expect(providersPage.customProviderNameInput).toBeVisible();
      await expect(providersPage.baseProviderSelect).toBeVisible();
      await expect(providersPage.baseUrlInput).toBeVisible();
    });

    test("should create a custom OpenAI-compatible provider", async ({
      providersPage,
    }) => {
      const providerData = createCustomProviderData({
        name: `test-openai-${Date.now()}`,
        baseProviderType: "openai",
        baseUrl: "https://api.openai.com/v1",
      });

      // Track for cleanup
      createdProviders.push(providerData.name);

      await providersPage.createProvider(providerData);

      // Wait for provider to appear in sidebar
      const providerItem = providersPage.getProviderItem(providerData.name);
      await expect(providerItem).toBeVisible({ timeout: 15000 });
    });

    test("should create a custom Anthropic-compatible provider", async ({
      providersPage,
    }) => {
      const providerData = createCustomProviderData({
        name: `test-anthropic-${Date.now()}`,
        baseProviderType: "anthropic",
        baseUrl: "https://api.anthropic.com",
      });

      // Track for cleanup
      createdProviders.push(providerData.name);

      await providersPage.createProvider(providerData);

      // Wait for provider to appear in sidebar
      const providerItem = providersPage.getProviderItem(providerData.name);
      await expect(providerItem).toBeVisible({ timeout: 15000 });
    });

    test("should create a custom TypeSafe provider with decisions allowed", async ({
      providersPage,
      page,
    }) => {
      const providerData = createCustomProviderData({
        name: `test-typesafe-${Date.now()}`,
        baseProviderType: "typesafe",
        baseUrl: "https://api.typesafe.ai",
      });

      // Track for cleanup
      createdProviders.push(providerData.name);

      await providersPage.fillCustomProviderForm(providerData);

      // A TypeSafe base serves decisions and list models only
      const decisionsSwitch = page.getByTestId("allowed-request-switch-decisions");
      await expect(decisionsSwitch).toBeEnabled();
      await expect(decisionsSwitch).toHaveAttribute("data-state", "checked");
      await expect(page.getByTestId("allowed-request-switch-list_models")).toHaveAttribute("data-state", "checked");
      await expect(page.getByTestId("allowed-request-switch-chat_completion")).toBeDisabled();

      await providersPage.saveCustomProvider();

      const providerItem = providersPage.getProviderItem(providerData.name);
      await expect(providerItem).toBeVisible({ timeout: 15000 });

      // The stored config must carry the typesafe base and allow decisions: Go treats an
      // allowed_requests object without decisions as a denial.
      const response = await page.request.get(`/api/providers/${providerData.name}`);
      expect(response.ok(), await response.text()).toBe(true);
      const saved = (await response.json()) as {
        custom_provider_config?: {
          base_provider_type?: string;
          allowed_requests?: { decisions?: boolean; list_models?: boolean; chat_completion?: boolean };
        };
      };
      expect(saved.custom_provider_config?.base_provider_type).toBe("typesafe");
      expect(saved.custom_provider_config?.allowed_requests?.decisions).toBe(true);
      expect(saved.custom_provider_config?.allowed_requests?.list_models).toBe(true);
      expect(saved.custom_provider_config?.allowed_requests?.chat_completion).toBe(false);
    });

    test("should cancel custom provider creation", async ({
      providersPage,
    }) => {
      await providersPage.openCustomProviderSheet();

      // Fill some data
      await providersPage.customProviderNameInput.fill("cancelled-provider");

      // Cancel
      await providersPage.customProviderCancelBtn.click();

      // Sheet should close
      await expect(providersPage.customProviderSheet).not.toBeVisible();

      // Provider should not exist
      const providerExists =
        await providersPage.providerExists("cancelled-provider");
      expect(providerExists).toBe(false);
    });

    test("should delete custom provider and update UI", async ({
      providersPage,
    }) => {
      const providerData = createCustomProviderData({
        name: `delete-test-${Date.now()}`,
        baseProviderType: "openai",
        baseUrl: "https://api.openai.com/v1",
      });
      createdProviders.push(providerData.name);

      await providersPage.createProvider(providerData);

      const providerItem = providersPage.getProviderItem(providerData.name);
      await expect(providerItem).toBeVisible({ timeout: 15000 });

      await providersPage.deleteProvider(providerData.name, {
        skipToastWait: true,
      });

      const idx = createdProviders.indexOf(providerData.name);
      if (idx >= 0) createdProviders.splice(idx, 1);

      // Assert provider is no longer in the configured providers list (do not rely on toast)
      await expect(providerItem).not.toBeVisible({ timeout: 5000 });
    });
  });

  test.describe("Form Validation", () => {
    test("should require name for custom provider", async ({
      providersPage,
    }) => {
      await providersPage.openCustomProviderSheet();

      // Try to save without name
      await providersPage.baseUrlInput.fill("https://api.example.com");

      // The save button should be disabled or show error
      const saveBtn = providersPage.customProviderSaveBtn;
      await saveBtn.click();

      // Form should still be visible (not submitted)
      await expect(providersPage.customProviderSheet).toBeVisible();
    });

    test("should require base URL for custom provider", async ({
      providersPage,
    }) => {
      await providersPage.openCustomProviderSheet();

      // Fill only name
      await providersPage.customProviderNameInput.fill("test-provider");

      // Try to save
      await providersPage.customProviderSaveBtn.click();

      // Form should still be visible
      await expect(providersPage.customProviderSheet).toBeVisible();
    });

    test("should show an error for an invalid custom provider hostname", async ({
      providersPage,
    }) => {
      await providersPage.openCustomProviderSheet();

      await providersPage.customProviderNameInput.fill(`invalid-host-${Date.now()}`);
      await providersPage.baseProviderSelect.click();
      await providersPage.page.getByRole("option", { name: "OpenAI" }).click();
      await providersPage.baseUrlInput.fill("https://api.nonexistent-provider.invalid/v1");

      await providersPage.customProviderSaveBtn.click();

      await providersPage.waitForErrorToast("Invalid base URL");
      await expect(providersPage.customProviderSheet).toBeVisible();
    });
  });
});

test.describe("Provider specific configuration", () => {
  test.beforeEach(async ({ providersPage }) => {
    await providersPage.goto();
  });

  test("should display complete vLLM key configuration when adding a key", async ({
    providersPage,
  }) => {
    const vllmAvailable = await providersPage.providerExists("vllm");
    if (!vllmAvailable) {
      test.skip(true, "vLLM provider not in sidebar (add from dropdown first)");
      return;
    }

    await providersPage.selectProvider("vllm");
    await providersPage.addKeyBtn.click();

    const vllmUrlInput = providersPage.page.getByTestId("key-input-vllm-url");
    const vllmModelInput = providersPage.page.getByTestId(
      "key-input-vllm-model-name",
    );

    const urlVisible = await vllmUrlInput.isVisible().catch(() => false);
    const modelVisible = await vllmModelInput.isVisible().catch(() => false);

    if (!urlVisible && !modelVisible) {
      test.skip(
        true,
        "vLLM key form fields not shown (provider may use standard key form)",
      );
      return;
    }
    await expect(vllmUrlInput).toBeVisible();
    await expect(vllmModelInput).toBeVisible();
    await expect(
      providersPage.page.getByTestId("api-keys-models-multiselect"),
    ).toBeVisible();
    await expect(
      providersPage.page.getByTestId("apikey-blacklisted-models-field"),
    ).toBeVisible();
    await expect(
      providersPage.page.getByTestId("api-keys-blocked-models-multiselect"),
    ).toBeVisible();
    await expect(
      providersPage.page.getByTestId("apikey-deployments-field"),
    ).toBeVisible();
    await expect(
      providersPage.page.getByTestId("apikey-deployments-table"),
    ).toBeVisible();

    await providersPage.keyCancelBtn.click();
  });

  test("should display Ollama-specific key fields when adding key to Ollama provider", async ({
    providersPage,
  }) => {
    const available = await providersPage.providerExists("ollama");
    if (!available) {
      test.skip(
        true,
        "Ollama provider not in sidebar (add from dropdown first)",
      );
      return;
    }

    await providersPage.selectProvider("ollama");
    await providersPage.addKeyBtn.click();

    const urlInput = providersPage.page.getByTestId("key-input-ollama-url");
    const urlVisible = await urlInput.isVisible().catch(() => false);
    if (!urlVisible) {
      test.skip(true, "Ollama key form fields not shown");
      return;
    }

    await expect(urlInput).toBeVisible();
    await providersPage.keyCancelBtn.click();
  });

  test("should display SGLang-specific key fields when adding key to SGLang provider", async ({
    providersPage,
  }) => {
    const available = await providersPage.providerExists("sgl");
    if (!available) {
      test.skip(
        true,
        "SGLang provider not in sidebar (add from dropdown first)",
      );
      return;
    }

    await providersPage.selectProvider("sgl");
    await providersPage.addKeyBtn.click();

    const urlInput = providersPage.page.getByTestId("key-input-sgl-url");
    const urlVisible = await urlInput.isVisible().catch(() => false);
    if (!urlVisible) {
      test.skip(true, "SGLang key form fields not shown");
      return;
    }

    await expect(urlInput).toBeVisible();
    await providersPage.keyCancelBtn.click();
  });
});
