import { expect, test } from "../../core/fixtures/base.fixture";

// All describes below mutate config fields on the same shared singleton
// "openai" provider row (concurrency, buffer size, timeouts, proxy/network
// settings, governance budgets, debugging toggles). They must stay serialized
// relative to each other regardless of which Playwright project/worker runs
// this file, hence the explicit serial mode here instead of relying on
// file placement.
test.describe("Provider Settings", () => {
  test.describe.configure({ mode: "serial" });

  test.describe("Provider Configuration", () => {
    test.beforeEach(async ({ providersPage }) => {
      await providersPage.goto();
    });

    test("should view provider configuration", async ({ providersPage }) => {
      // Select OpenAI provider
      await providersPage.selectProvider("openai");

      // Should see the provider's key table
      await expect(providersPage.keysTable).toBeVisible();

      // Should see the add key button
      await expect(providersPage.addKeyBtn).toBeVisible();
    });

    test("should show provider models list", async ({ providersPage }) => {
      // Select OpenAI provider
      await providersPage.selectProvider("openai");

      // Models section should be visible for selected provider
      const modelsSection = providersPage.page.getByText(/Models/i).first();
      await expect(modelsSection).toBeVisible();
    });
  });

  test.describe("Performance Tuning", () => {
    test.beforeEach(async ({ providersPage }) => {
      await providersPage.goto();
      await providersPage.selectProvider("openai");
    });

    test("should display performance tuning tab", async ({ providerSettingsPage }) => {
      await providerSettingsPage.selectConfigTab("performance");

      // Should see concurrency and buffer size inputs
      await expect(providerSettingsPage.getConcurrencyInput()).toBeVisible();
      await expect(providerSettingsPage.getBufferSizeInput()).toBeVisible();
    });

    test("should display raw request/response toggles", async ({
      providerSettingsPage,
    }) => {
      await providerSettingsPage.selectConfigTab("debugging");

      // Should see raw request and response toggles (Debugging tab labels)
      const rawRequestLabel = providerSettingsPage.page.getByText(
        "Send Back Raw Request",
      );
      const rawResponseLabel = providerSettingsPage.page.getByText(
        "Send Back Raw Response",
      );

      await expect(rawRequestLabel).toBeVisible();
      await expect(rawResponseLabel).toBeVisible();
    });

    test("should update concurrency value", async ({ providerSettingsPage }) => {
      await providerSettingsPage.selectConfigTab("performance");

      const concurrencyInput = providerSettingsPage.getConcurrencyInput();
      const originalValue = await concurrencyInput.inputValue();

      // Use a small value that is always <= buffer size
      const newValue = "5";

      await providerSettingsPage.fillNumberInput(concurrencyInput, newValue);

      // Verify value changed
      const currentValue = await concurrencyInput.inputValue();
      expect(currentValue).toBe(newValue);
      // Blur the input
      await concurrencyInput.blur();
      // No validation error should appear
      await expect(
        providerSettingsPage.page.getByText("Concurrency must be a number"),
      ).not.toBeVisible();
      await expect(
        providerSettingsPage.page.getByText("Concurrency must be greater than 0"),
      ).not.toBeVisible();
      await expect(
        providerSettingsPage.page.getByText(
          "Concurrency must be less than or equal to buffer size",
        ),
      ).not.toBeVisible();

      // Save and verify success
      const saveBtn = providerSettingsPage.getConfigSaveBtn("performance");
      await expect(saveBtn).toBeEnabled();
      await providerSettingsPage.savePerformanceConfig();

      // Verify value persisted after save (reload would be ideal but we restore instead)
      const afterSaveValue = await concurrencyInput.inputValue();
      expect(afterSaveValue).toBe(newValue);

      // Restore original value
      await providerSettingsPage.fillNumberInput(concurrencyInput, originalValue);
      // Blur the input
      await concurrencyInput.blur();
      await providerSettingsPage.savePerformanceConfig();
    });

    test("should update buffer size value", async ({ providerSettingsPage }) => {
      await providerSettingsPage.selectConfigTab("performance");

      const bufferSizeInput = providerSettingsPage.getBufferSizeInput();
      const originalValue = await bufferSizeInput.inputValue();

      // Use a large value that is always >= concurrency
      const newValue = "6000";

      await providerSettingsPage.fillNumberInput(bufferSizeInput, newValue);

      // Verify value changed
      const currentValue = await bufferSizeInput.inputValue();
      expect(currentValue).toBe(newValue);

      // Blur the input
      await bufferSizeInput.blur();

      // No validation error should appear
      await expect(
        providerSettingsPage.page.getByText("Buffer size must be a number"),
      ).not.toBeVisible();
      await expect(
        providerSettingsPage.page.getByText("Buffer size must be greater than 0"),
      ).not.toBeVisible();
      await expect(
        providerSettingsPage.page.getByText(
          "Concurrency must be less than or equal to buffer size",
        ),
      ).not.toBeVisible();

      // Save and verify success
      const saveBtn = providerSettingsPage.getConfigSaveBtn("performance");
      await expect(saveBtn).toBeEnabled();
      await providerSettingsPage.savePerformanceConfig();

      // Restore original value
      await providerSettingsPage.fillNumberInput(bufferSizeInput, originalValue);
      // Blur the input
      await bufferSizeInput.blur();
      await providerSettingsPage.savePerformanceConfig();
    });

    test("should toggle and save raw request/response", async ({
      providerSettingsPage,
    }) => {
      await providerSettingsPage.selectConfigTab("debugging");

      const rawRequestSwitch = providerSettingsPage.getRawRequestSwitch();
      const rawResponseSwitch = providerSettingsPage.getRawResponseSwitch();

      // Capture original states
      const originalRawRequest =
        (await rawRequestSwitch.getAttribute("data-state")) === "checked";
      const originalRawResponse =
        (await rawResponseSwitch.getAttribute("data-state")) === "checked";

      // Toggle both switches
      await rawRequestSwitch.click();
      await rawResponseSwitch.click();

      // Save and verify success
      const saveBtn = providerSettingsPage.getConfigSaveBtn("debugging");
      await expect(saveBtn).toBeEnabled();
      await providerSettingsPage.saveDebuggingConfig();

      // Restore original states
      const currentRawRequest =
        (await rawRequestSwitch.getAttribute("data-state")) === "checked";
      const currentRawResponse =
        (await rawResponseSwitch.getAttribute("data-state")) === "checked";

      if (currentRawRequest !== originalRawRequest) {
        await rawRequestSwitch.click();
      }
      if (currentRawResponse !== originalRawResponse) {
        await rawResponseSwitch.click();
      }

      await providerSettingsPage.saveDebuggingConfig();
    });
  });

  test.describe("Proxy Configuration", () => {
    test.beforeEach(async ({ providersPage }) => {
      await providersPage.goto();
      await providersPage.selectProvider("openai");
    });

    test("should display proxy config tab", async ({ providerSettingsPage }) => {
      await providerSettingsPage.selectConfigTab("proxy");

      // Should see proxy type selector
      const proxyTypeLabel = providerSettingsPage.page.getByText("Proxy Type");
      await expect(proxyTypeLabel).toBeVisible();
    });

    test("should show proxy type options", async ({ providerSettingsPage }) => {
      await providerSettingsPage.selectConfigTab("proxy");

      // Open the proxy type dropdown
      const proxySelect = providerSettingsPage.getProxyTypeSelect();
      await proxySelect.click();

      // Should see HTTP, SOCKS5, Environment options
      await expect(
        providerSettingsPage.page.getByRole("option", { name: /HTTP/i }),
      ).toBeVisible();
      await expect(
        providerSettingsPage.page.getByRole("option", { name: /SOCKS5/i }),
      ).toBeVisible();
      await expect(
        providerSettingsPage.page.getByRole("option", { name: /Environment/i }),
      ).toBeVisible();

      // Close dropdown
      await providerSettingsPage.page.keyboard.press("Escape");
    });

    test("should show URL fields when HTTP proxy selected", async ({
      providerSettingsPage,
    }) => {
      await providerSettingsPage.selectConfigTab("proxy");

      // Select HTTP proxy type
      const proxySelect = providerSettingsPage.getProxyTypeSelect();
      await proxySelect.click();
      await providerSettingsPage.page.getByRole("option", { name: /HTTP/i }).click();

      // Should show URL, username, password fields
      await expect(providerSettingsPage.page.getByLabel("Proxy URL")).toBeVisible();
      await expect(providerSettingsPage.page.getByLabel("Username")).toBeVisible();
      await expect(providerSettingsPage.page.getByLabel("Password")).toBeVisible();
    });
  });

  test.describe("Network Configuration", () => {
    test.beforeEach(async ({ providersPage }) => {
      await providersPage.goto();
      await providersPage.selectProvider("openai");
    });

    test("should display network config tab", async ({ providerSettingsPage }) => {
      await providerSettingsPage.selectConfigTab("network");

      // Should see timeout and retry settings
      await expect(
        providerSettingsPage.page.getByLabel("Timeout (seconds)", { exact: true }),
      ).toBeVisible();
      await expect(providerSettingsPage.page.getByLabel(/Max Retries/i)).toBeVisible();
    });

    test("should display backoff settings", async ({ providerSettingsPage }) => {
      await providerSettingsPage.selectConfigTab("network");

      // Should see backoff configuration
      await expect(
        providerSettingsPage.page.getByLabel(/Initial Backoff/i),
      ).toBeVisible();
      await expect(providerSettingsPage.page.getByLabel(/Max Backoff/i)).toBeVisible();
    });

    test("should update timeout value", async ({ providerSettingsPage }) => {
      await providerSettingsPage.selectConfigTab("network");

      // Ensure backoff fields are valid (minimum 100ms) so form validation passes
      const initialBackoff = providerSettingsPage.page.getByLabel(/Initial Backoff/i);
      const maxBackoff = providerSettingsPage.page.getByLabel(/Max Backoff/i);
      const ibVal = await initialBackoff.inputValue();
      const mbVal = await maxBackoff.inputValue();
      if (Number(ibVal) < 100) {
        await providerSettingsPage.fillNumberInput(initialBackoff, "500");
      }
      if (Number(mbVal) < 100) {
        await providerSettingsPage.fillNumberInput(maxBackoff, "10000");
      }

      const timeoutInput = providerSettingsPage.page.getByLabel("Timeout (seconds)", {
        exact: true,
      });
      const originalValue = await timeoutInput.inputValue();
      const newValue = originalValue === "30" ? "60" : "30";

      await providerSettingsPage.fillNumberInput(timeoutInput, newValue);

      // Verify value changed
      const currentValue = await timeoutInput.inputValue();
      expect(currentValue).toBe(newValue);

      // Save button should be enabled
      const saveBtn = providerSettingsPage.getConfigSaveBtn("network");
      await expect(saveBtn).toBeEnabled();
      await providerSettingsPage.saveNetworkConfig();

      // Restore original value to avoid leaving form dirty
      await providerSettingsPage.fillNumberInput(timeoutInput, originalValue);
      await providerSettingsPage.saveNetworkConfig();
    });

    test("should update max retries value", async ({ providerSettingsPage }) => {
      await providerSettingsPage.selectConfigTab("network");

      // Ensure backoff fields are valid (minimum 100ms) so form validation passes
      const initialBackoff = providerSettingsPage.page.getByLabel(/Initial Backoff/i);
      const maxBackoff = providerSettingsPage.page.getByLabel(/Max Backoff/i);
      const ibVal = await initialBackoff.inputValue();
      const mbVal = await maxBackoff.inputValue();
      if (Number(ibVal) < 100) {
        await providerSettingsPage.fillNumberInput(initialBackoff, "500");
      }
      if (Number(mbVal) < 100) {
        await providerSettingsPage.fillNumberInput(maxBackoff, "10000");
      }

      const retriesInput = providerSettingsPage.page.getByLabel(/Max Retries/i);
      const originalValue = await retriesInput.inputValue();
      const newValue = originalValue === "0" ? "3" : "0";

      await providerSettingsPage.fillNumberInput(retriesInput, newValue);

      // Verify value changed
      const currentValue = await retriesInput.inputValue();
      expect(currentValue).toBe(newValue);

      // Save button should be enabled
      const saveBtn = providerSettingsPage.getConfigSaveBtn("network");
      await expect(saveBtn).toBeEnabled();
      await providerSettingsPage.saveNetworkConfig();

      // Restore original value to avoid leaving form dirty
      await providerSettingsPage.fillNumberInput(retriesInput, originalValue);
      await providerSettingsPage.saveNetworkConfig();
    });
  });

  test.describe("Governance (Budget & Rate Limits)", () => {
    test.beforeEach(async ({ providersPage }) => {
      await providersPage.goto();
      await providersPage.selectProvider("openai");
    });

    test("should display governance tab", async ({ providerSettingsPage }) => {
      const isVisible = await providerSettingsPage.isGovernanceTabVisible();

      if (isVisible) {
        await providerSettingsPage.selectConfigTab("governance");

        // Should see budget configuration section
        await expect(
          providerSettingsPage.page.getByText("Budget Configuration"),
        ).toBeVisible();
      }
    });

    test("should display budget configuration", async ({ providerSettingsPage }) => {
      const isVisible = await providerSettingsPage.isGovernanceTabVisible();

      if (isVisible) {
        await providerSettingsPage.setGovernanceConfig({
          budgets: [{ amount: 100, resetPeriod: "1h" }],
        });

        // Should see budget limit input
        const budgetInput = providerSettingsPage.page.getByTestId(
          "provider-governance-budgets-amount-0",
        );
        await expect(budgetInput).toBeVisible();
      }
    });

    test("should display rate limiting configuration", async ({
      providerSettingsPage,
    }) => {
      const isVisible = await providerSettingsPage.isGovernanceTabVisible();

      if (isVisible) {
        await providerSettingsPage.selectConfigTab("governance");

        // Should see rate limiting section
        await expect(
          providerSettingsPage.page.getByText("Rate Limiting Configuration"),
        ).toBeVisible();

        // Should see token and request limit inputs
        const tokenInput = providerSettingsPage.page.locator("#providerTokenMaxLimit");
        const requestInput = providerSettingsPage.page.locator(
          "#providerRequestMaxLimit",
        );

        await expect(tokenInput).toBeVisible();
        await expect(requestInput).toBeVisible();
      }
    });

    test("should set budget limit", async ({ providerSettingsPage }) => {
      const isVisible = await providerSettingsPage.isGovernanceTabVisible();

      if (isVisible) {
        await providerSettingsPage.setGovernanceConfig({
          budgets: [{ amount: 100, resetPeriod: "1h" }],
        });

        const budgetInput = providerSettingsPage.page.getByTestId(
          "provider-governance-budgets-amount-0",
        );

        // Verify value
        const value = await budgetInput.inputValue();
        expect(value).toBe("100");

        // Form should now be dirty - save button should be enabled
        const saveBtn = providerSettingsPage.getConfigSaveBtn("governance");
        // Give React time to update the form state
        await providerSettingsPage.page.waitForTimeout(500);
        await expect(saveBtn).toBeEnabled({ timeout: 5000 });
      }
    });

    test("should set rate limits", async ({ providerSettingsPage }) => {
      const isVisible = await providerSettingsPage.isGovernanceTabVisible();

      if (isVisible) {
        await providerSettingsPage.selectConfigTab("governance");

        // Set token limit - use pressSequentially for proper React onChange
        const tokenInput = providerSettingsPage.page.locator("#providerTokenMaxLimit");
        await tokenInput.click();
        await tokenInput.fill("");
        await tokenInput.pressSequentially("100000");

        // Set request limit
        const requestInput = providerSettingsPage.page.locator(
          "#providerRequestMaxLimit",
        );
        await requestInput.click();
        await requestInput.fill("");
        await requestInput.pressSequentially("1000");

        // Verify values
        expect(await tokenInput.inputValue()).toBe("100000");
        expect(await requestInput.inputValue()).toBe("1000");
      }
    });
  });

  test.describe("Debugging Tab", () => {
    test.beforeEach(async ({ providersPage }) => {
      await providersPage.goto();
      await providersPage.selectProvider("openai");
    });

    test("should display debugging tab", async ({ providerSettingsPage }) => {
      await providerSettingsPage.openConfigSheet();
      const page = providerSettingsPage.page;
      const debuggingTab = page.getByTestId("provider-tab-debugging");
      const overflow = page.getByTestId("tabs-overflow-trigger");
      await expect(debuggingTab.or(overflow).first()).toBeVisible();
      // On narrow sheets the tab collapses into the overflow menu.
      if (!(await debuggingTab.isVisible())) {
        await overflow.click();
        await expect(page.getByRole("menuitem", { name: /Debugging/i })).toBeVisible();
      }
    });

    test("should navigate to debugging tab", async ({ providerSettingsPage }) => {
      await providerSettingsPage.selectConfigTab("debugging");

      const debuggingTab = providerSettingsPage.page.getByTestId(
        "provider-tab-debugging",
      );
      await expect(debuggingTab).toHaveAttribute("data-state", "active");
      const debuggingContent = providerSettingsPage.page.getByTestId(
        "provider-config-debugging-content",
      );
      await expect(debuggingContent).toBeVisible();
    });
  });

  test.describe("Web Search Tab", () => {
    test.beforeEach(async ({ providersPage }) => {
      await providersPage.goto();
      await providersPage.selectProvider("openai");
    });

    test("should navigate to the web search tab", async ({ providerSettingsPage }) => {
      await providerSettingsPage.selectConfigTab("web-search");

      const page = providerSettingsPage.page;
      await expect(page.getByTestId("provider-tab-web-search")).toHaveAttribute("data-state", "active");
      await expect(page.getByTestId("provider-config-web-search-content")).toBeVisible();
      await expect(page.getByTestId("provider-web-search-client-select")).toBeVisible();
    });

    test("should require an MCP server before a tool can be picked", async ({ providerSettingsPage }) => {
      await providerSettingsPage.selectConfigTab("web-search");

      const page = providerSettingsPage.page;
      await expect(page.getByTestId("provider-web-search-tool-select")).toBeDisabled();
      await expect(page.getByTestId("provider-web-search-save-btn")).toBeDisabled();
    });
  });
});
