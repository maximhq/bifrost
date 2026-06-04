import { Locator, Page, expect } from "@playwright/test";
import { BasePage } from "../../../core/pages/base.page";
import { fillSelect, waitForNetworkIdle } from "../../../core/utils/test-helpers";

/**
 * Provider display names mapping - matches the UI's ProviderLabels
 * Used for exact matching when selecting providers in dropdowns
 */
const PROVIDER_DISPLAY_NAMES: Record<string, string> = {
  openai: "OpenAI",
  anthropic: "Anthropic",
  azure: "Azure",
  bedrock: "AWS Bedrock",
  cohere: "Cohere",
  vertex: "Vertex AI",
  mistral: "Mistral AI",
  ollama: "Ollama",
  groq: "Groq",
  gemini: "Gemini",
  openrouter: "OpenRouter",
  huggingface: "HuggingFace",
  cerebras: "Cerebras",
  perplexity: "Perplexity",
  elevenlabs: "Elevenlabs",
  parasail: "Parasail",
  sgl: "SGLang",
  nebius: "Nebius Token Factory",
  xai: "xAI",
};

/**
 * Escape regex special characters in a string
 */
function escapeRegExp(string: string): string {
  return string.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * Budget configuration
 */
export interface BudgetConfig {
  maxLimit: number;
  resetDuration?: string; // e.g. "1d", "1m", "1M" — see resetDurationOptions
}

/**
 * Rate limit configuration
 */
export interface RateLimitConfig {
  tokenMaxLimit?: number;
  tokenResetDuration?: string;
  requestMaxLimit?: number;
  requestResetDuration?: string;
}

/**
 * Provider configuration for virtual key
 */
export interface ProviderConfig {
  provider: string;
  weight?: number;
  allowedModels?: string[];
  keyIds?: string[];
  budget?: BudgetConfig;
  rateLimit?: RateLimitConfig;
}

/**
 * Virtual key configuration
 */
export interface VirtualKeyConfig {
  name: string;
  description?: string;
  isActive?: boolean;
  providerConfigs?: ProviderConfig[];
  budgets?: BudgetConfig[];
  rateLimit?: RateLimitConfig;
  entityType?: "none" | "team" | "customer";
  teamId?: string;
  customerId?: string;
}

/**
 * Page object for the Virtual Keys page
 */
export class VirtualKeysPage extends BasePage {
  // Main page elements
  readonly createBtn: Locator;
  readonly table: Locator;
  readonly emptyState: Locator;

  // Virtual key sheet elements
  readonly sheet: Locator;
  readonly nameInput: Locator;
  readonly descriptionInput: Locator;
  readonly isActiveToggle: Locator;
  readonly providerSelect: Locator;
  readonly saveBtn: Locator;
  readonly cancelBtn: Locator;

  constructor(page: Page) {
    super(page);

    // Main page elements
    this.createBtn = page.getByTestId("create-vk-btn");
    this.table = page.getByTestId("vk-table");
    this.emptyState = page.getByTestId("virtual-keys-empty-state");

    // Virtual key sheet elements
    this.sheet = page.getByTestId("vk-sheet-content");
    this.nameInput = page.getByTestId("vk-name-input");
    this.descriptionInput = page.getByTestId("vk-description-input");
    this.isActiveToggle = page.getByTestId("vk-is-active-toggle");
    this.providerSelect = page.getByTestId("vk-provider-select");
    this.saveBtn = page.getByTestId("vk-save-btn");
    this.cancelBtn = page.getByTestId("vk-cancel-btn");
  }

  /**
   * Navigate to the virtual keys page
   */
  async goto(): Promise<void> {
    await this.page.goto("/workspace/governance/virtual-keys");
    await waitForNetworkIdle(this.page);
  }

  /**
   * Search the virtual keys table by name.
   */
  async searchVirtualKeys(name: string): Promise<void> {
    const searchInput = this.page.getByTestId("vk-search-input");

    for (let attempt = 0; attempt < 3; attempt++) {
      await searchInput.fill("", { force: true });
      await expect(searchInput)
        .toHaveValue("", { timeout: 2000 })
        .catch(() => {});

      await searchInput.fill(name, { force: true });
      const matched = await expect(searchInput)
        .toHaveValue(name, { timeout: 2000 })
        .then(() => true)
        .catch(() => false);
      if (matched) break;

      await this.page.waitForTimeout(500);
    }

    await expect(searchInput)
      .toHaveValue(name, { timeout: 10000 })
      .catch(() => {});
    await this.page.waitForTimeout(350);
  }

  /**
   * Clear table filters that can hide newly created or cleanup-target rows.
   */
  async clearTableFilters(): Promise<void> {
    const searchInput = this.page.getByTestId("vk-search-input");
    if (await searchInput.isVisible().catch(() => false)) {
      await searchInput.fill("");
    }
    await this.page.waitForTimeout(350);
  }

  /**
   * Get virtual key row locator by name
   */
  getVirtualKeyRow(name: string): Locator {
    return this.page.getByTestId(`vk-row-${name}`);
  }

  /**
   * Open the row actions dropdown and click Edit.
   */
  private async openVirtualKeyEditor(name: string): Promise<void> {
    await this.searchVirtualKeys(name);

    const row = this.getVirtualKeyRow(name);
    await expect(row).toBeVisible({ timeout: 10000 });
    await row.scrollIntoViewIfNeeded();

    const actionsBtn = this.page.getByTestId(`vk-actions-btn-${name}`);
    await actionsBtn.waitFor({ state: "visible", timeout: 10000 });
    await actionsBtn.scrollIntoViewIfNeeded();
    await actionsBtn.click();

    const editBtn = this.page.getByTestId(`vk-edit-btn-${name}`);
    await editBtn.waitFor({ state: "visible", timeout: 10000 });
    await editBtn.click();

    await expect(this.sheet).toBeVisible({ timeout: 10000 });
    await this.waitForSheetAnimation();
    await expect(this.nameInput).toHaveValue(name, { timeout: 10000 });
  }

  /**
   * Wait for the sheet to close after a save. If it stays open, close it without
   * racing a button that may already be detaching during the save animation.
   */
  private async waitForSheetClosedAfterSave(): Promise<void> {
    const closed = await expect(this.sheet)
      .not.toBeVisible({ timeout: 5000 })
      .then(() => true)
      .catch(() => false);
    if (!closed) {
      await this.page.keyboard.press("Escape");
      await expect(this.sheet).not.toBeVisible({ timeout: 5000 });
    }

    await expect(this.page.locator("html"))
      .not.toHaveClass(/bprogress-busy/, { timeout: 10000 })
      .catch(() => {});
  }

  private async preserveBudgetUsageIfPrompted(): Promise<void> {
    const dialog = this.page.getByTestId("vk-budget-reset-dialog");
    const isVisible = await dialog.waitFor({ state: 'visible', timeout: 1000 }).then(() => true).catch(() => false);
    if (!isVisible) return;
    await this.page.getByTestId("vk-budget-reset-preserve-btn").click();
    await dialog.waitFor({ state: 'hidden', timeout: 3000 })
  }

  /**
   * Check if a virtual key exists in the table
   */
  async virtualKeyExists(name: string): Promise<boolean> {
    await this.searchVirtualKeys(name).catch(() => {});
    const row = this.getVirtualKeyRow(name);
    return await row.isVisible({ timeout: 5000 }).catch(() => false);
  }

  /**
   * Check if the key value is revealed (visible) or masked in the table.
   * When masked, the display shows bullets (•); when revealed, it shows the full key.
   */
  async isKeyRevealed(name: string): Promise<boolean> {
    const row = this.getVirtualKeyRow(name);
    const keyCell = row.getByTestId("vk-key-value");
    await keyCell.waitFor({ state: "visible", timeout: 5000 });
    const text = (await keyCell.textContent())?.trim() ?? "";
    // Masked keys contain bullet character; revealed keys do not
    return text.length > 0 && !text.includes("•");
  }

  /**
   * Create a new virtual key
   */
  async createVirtualKey(config: VirtualKeyConfig): Promise<void> {
    // Click create button
    await this.createBtn.click();

    // Wait for sheet to appear and animation to complete
    await expect(this.sheet).toBeVisible();
    await this.waitForSheetAnimation();

    // Fill basic information using keyboard navigation
    await this.nameInput.focus();
    await this.page.keyboard.type(config.name);

    if (config.description) {
      await this.page.keyboard.press("Tab"); // Move to description
      await this.page.keyboard.type(config.description);
    }

    // Set active state if specified (default is true, so only toggle if we want inactive)
    if (config.isActive === false) {
      await this.isActiveToggle.focus();
      await this.page.keyboard.press("Space"); // Toggle the switch
    }

    // Add provider configurations
    if (config.providerConfigs && config.providerConfigs.length > 0) {
      for (const providerConfig of config.providerConfigs) {
        await this.addProviderConfig(providerConfig);
      }
    }

    // Set budget if specified
    if (config.budgets && config.budgets.length > 0) {
      await this.setBudgets(config.budgets);
    }

    // Set rate limits if specified
    if (config.rateLimit) {
      await this.setRateLimit(config.rateLimit);
    }

    // Set entity assignment if specified
    if (config.entityType && config.entityType !== "none") {
      await this.setEntityAssignment(config.entityType, config.teamId, config.customerId);
    }

    await expect(this.saveBtn).toBeEnabled({ timeout: 10000 });

    // Save the virtual key by clicking the save button
    await this.saveBtn.click();

    // Wait for the new row to appear in the table. This is a stronger success
    // signal than sonner toasts, which can overlap between tests and animations.
    await this.searchVirtualKeys(config.name);
    const row = this.getVirtualKeyRow(config.name);
    await row.waitFor({ state: "visible", timeout: 15000 });
    await row.scrollIntoViewIfNeeded();

    await expect(this.sheet)
      .not.toBeVisible({ timeout: 5000 })
      .catch(() => {});
  }

  /**
   * Add a provider configuration to the virtual key form
   */
  private async addProviderConfig(config: ProviderConfig): Promise<void> {
    // Click the provider select dropdown
    await this.providerSelect.click();

    // Wait for dropdown content
    await this.page.waitForSelector('[role="listbox"]', { timeout: 5000 });

    // Get display name - use mapping for known providers, otherwise use exact name
    const displayName = PROVIDER_DISPLAY_NAMES[config.provider.toLowerCase()] || config.provider;

    // First try exact match for base providers (e.g., "OpenAI", "Anthropic")
    let option = this.page.getByRole("option", { name: displayName, exact: true });

    if ((await option.count()) === 0) {
      // Fallback: try partial match for custom providers (contains provider name)
      // This handles custom providers like "test-anthropic-1234567890"
      option = this.page
        .getByRole("option")
        .filter({
          hasText: new RegExp(escapeRegExp(config.provider), "i"),
        })
        .first();
    }

    // Verify we found a matching option
    const optionCount = await option.count();
    if (optionCount === 0) {
      throw new Error(
        `No provider option found matching "${config.provider}" (display name: "${displayName}")`,
      );
    }

    await option.click();

    // Wait for dropdown to close after selection
    await this.page.waitForSelector('[role="listbox"]', { state: "hidden", timeout: 5000 });
  }

  /**
   * Set budget lines in the MultiBudgetLines component.
   * Clicks "Add Budget" for each entry, fills the amount input,
   * and selects the reset period.
   */
  private async setBudgets(budgets: BudgetConfig[]): Promise<void> {
    for (let i = 0; i < budgets.length; i++) {
      const budget = budgets[i];
      // Click "Add Budget" button to add a new budget line
      await this.page.getByTestId("vk-budget-lines-add-btn").click();
      const amountInput = this.page.getByTestId(`vk-budget-lines-amount-${i}`);
      await amountInput.fill(String(budget.maxLimit));
      // Select reset period if specified
      if (budget.resetDuration) {
        await this.page.getByTestId(`vk-budget-lines-line-${i}`).getByRole("combobox").click();
        await this.page
          .getByRole("option", { name: this.resetDurationLabel(budget.resetDuration), exact: true })
          .click();
      }
    }
  }

  private resetDurationLabel(value: string): string {
    const labels: Record<string, string> = {
      "1m": "Every Minute",
      "5m": "Every 5 Minutes",
      "15m": "Every 15 Minutes",
      "30m": "Every 30 Minutes",
      "1h": "Hourly",
      "6h": "Every 6 Hours",
      "1d": "Daily",
      "1w": "Weekly",
      "1M": "Monthly",
    };
    return labels[value] ?? value;
  }

  /**
   * Set rate limit configuration in the form
   */
  private async setRateLimit(rateLimit: RateLimitConfig): Promise<void> {
    // Set token limits (fill() clears and sets atomically)
    if (rateLimit.tokenMaxLimit !== undefined) {
      const tokenInput = this.page.locator("#tokenMaxLimit");
      await tokenInput.fill(String(rateLimit.tokenMaxLimit));
    }

    // Set request limits (fill() clears and sets atomically)
    if (rateLimit.requestMaxLimit !== undefined) {
      const requestInput = this.page.locator("#requestMaxLimit");
      await requestInput.fill(String(rateLimit.requestMaxLimit));
    }
  }

  /**
   * Set entity assignment (team or customer)
   */
  private async setEntityAssignment(
    entityType: "team" | "customer",
    teamId?: string,
    customerId?: string,
  ): Promise<void> {
    // Find and click entity type select
    const entityTypeSelect = this.page.locator('[data-testid="vk-entity-type-select"]');
    if (await entityTypeSelect.isVisible()) {
      await fillSelect(
        this.page,
        '[data-testid="vk-entity-type-select"]',
        entityType === "team" ? "Assign to Team" : "Assign to Customer",
      );

      // Select team or customer
      if (entityType === "team" && teamId) {
        const teamSelect = this.page.locator('[data-testid="vk-team-select"]');
        if (await teamSelect.isVisible()) {
          await fillSelect(this.page, '[data-testid="vk-team-select"]', teamId);
        }
      } else if (entityType === "customer" && customerId) {
        const customerSelect = this.page.locator('[data-testid="vk-customer-select"]');
        if (await customerSelect.isVisible()) {
          await fillSelect(this.page, '[data-testid="vk-customer-select"]', customerId);
        }
      }
    }
  }

  /**
   * Edit an existing virtual key
   */
  async editVirtualKey(name: string, updates: Partial<VirtualKeyConfig>): Promise<void> {
    // Wait for any existing toasts to disappear
    await this.forceCloseToasts();
    await this.openVirtualKeyEditor(name);

    // Update name using clear() and fill() for cross-platform compatibility
    if (updates.name) {
      await this.nameInput.clear();
      await this.nameInput.fill(updates.name);
    }

    // Update description using clear() and fill() for cross-platform compatibility
    if (updates.description !== undefined) {
      await this.descriptionInput.clear();
      if (updates.description) {
        await this.descriptionInput.fill(updates.description);
      }
    }

    // Update toggle using click() and data-state attribute for reliability
    if (updates.isActive !== undefined) {
      // Check current state using data-state attribute (Radix Switch)
      const isCurrentlyChecked =
        (await this.isActiveToggle.getAttribute("data-state")) === "checked";
      if (isCurrentlyChecked !== updates.isActive) {
        await this.isActiveToggle.click();
      }
    }

    if (updates.budgets && updates.budgets.length > 0) {
      await this.setBudgets(updates.budgets);
    }

    if (updates.rateLimit) {
      await this.setRateLimit(updates.rateLimit);
    }

    await expect(this.saveBtn).toBeEnabled({ timeout: 10000 });

    // Save changes by clicking the save button
    await this.saveBtn.click();
    await this.preserveBudgetUsageIfPrompted();

    await this.waitForSheetClosedAfterSave();

    const targetName = updates.name ?? name;
    if (updates.name) {
      await this.goto();
    }
    await this.searchVirtualKeys(targetName);
    await expect(this.getVirtualKeyRow(targetName)).toBeVisible({ timeout: 10000 });
  }

  /**
   * Poll until the virtual key row disappears from the table (e.g. after delete or refetch).
   * Polls so we don't rely on a stale locator.
   */
  async waitForVirtualKeyGone(name: string, timeoutMs: number): Promise<boolean> {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
      if ((await this.getVirtualKeyRow(name).count()) === 0) return true;
      await this.page.waitForTimeout(500);
    }
    return false;
  }

  async deleteVirtualKey(name: string, options?: { requireToast?: boolean }): Promise<void> {
    // Check if virtual key exists first
    const exists = await this.virtualKeyExists(name);
    if (!exists) {
      // Already deleted or doesn't exist, nothing to do
      return;
    }

    // Wait for any existing toasts to disappear
    await this.forceCloseToasts();

    // Find the delete button using data-testid (scroll row into view in case table just loaded)
    const row = this.getVirtualKeyRow(name);
    await row.scrollIntoViewIfNeeded().catch(() => {});
    await this.page.waitForTimeout(300);

    await this.searchVirtualKeys(name).catch(() => {});
    await expect(row).toBeVisible({ timeout: 10000 });

    const actionsBtn = this.page.getByTestId(`vk-actions-btn-${name}`);
    await actionsBtn.waitFor({ state: "visible", timeout: 10000 });
    await actionsBtn.scrollIntoViewIfNeeded();
    await actionsBtn.click();

    const deleteBtn = this.page.getByTestId(`vk-delete-btn-${name}`);

    // Check if button exists; if not, give table a moment and re-check once
    let btnCount = await deleteBtn.count();
    if (btnCount === 0) {
      await this.page.waitForTimeout(800);
      btnCount = await deleteBtn.count();
    }
    if (btnCount === 0) {
      const stillExists = await this.virtualKeyExists(name);
      if (!stillExists) return;
      throw new Error(`Delete button not found for virtual key: ${name}`);
    }

    // Check if button is disabled
    const isDisabled = await deleteBtn.isDisabled().catch(() => false);
    if (isDisabled) {
      throw new Error(
        `Delete button is disabled for virtual key: ${name} (likely due to RBAC permissions)`,
      );
    }

    await deleteBtn.waitFor({ state: "visible", timeout: 10000 });
    await deleteBtn.scrollIntoViewIfNeeded();
    await deleteBtn.click();

    // Wait for confirmation dialog and confirm deletion (match "Delete" or "Deleting...")
    const confirmDialog = this.page.locator('[role="alertdialog"]');
    await confirmDialog.waitFor({ state: "visible", timeout: 5000 });
    const confirmBtn = confirmDialog.getByRole("button", { name: /Delete/i });
    await confirmBtn.waitFor({ state: "visible", timeout: 2000 });

    await confirmBtn.click();

    // Wait for table to refetch and row to disappear (poll fresh locator; avoid stale row reference)
    const gone = await this.waitForVirtualKeyGone(name, 20000);
    if (!gone) {
      throw new Error(`Virtual key "${name}" still visible after delete`);
    }

    // Optionally wait for success toast (skip in cleanup to avoid false failures)
    if (options?.requireToast !== false) {
      await this.getToast()
        .waitFor({ state: "visible", timeout: 5000 })
        .catch(() => {});
    }

    await this.dismissToasts().catch(() => {});
  }

  /**
   * Click on a virtual key to view/edit details (opens via edit button)
   */
  async viewVirtualKey(name: string): Promise<void> {
    // Wait for any existing toasts to disappear
    await this.forceCloseToasts();
    await this.openVirtualKeyEditor(name);
  }

  /**
   * Get the count of virtual keys in the table
   */
  async getVirtualKeyCount(): Promise<number> {
    const rows = this.table.locator("tbody tr");
    const count = await rows.count();

    if (count === 0) {
      return 0;
    }

    // Check if it's the empty state row
    const firstRowText = await rows.first().textContent();
    if (firstRowText?.includes("No virtual keys found")) {
      return 0;
    }

    return count;
  }

  /**
   * Copy virtual key value to clipboard
   */
  async copyVirtualKeyValue(name: string): Promise<void> {
    // Find and click the copy button using data-testid
    const copyBtn = this.page.getByTestId(`vk-copy-btn-${name}`);
    await copyBtn.waitFor({ state: "attached", timeout: 10000 });
    await copyBtn.scrollIntoViewIfNeeded();
    await copyBtn.click();

    await this.waitForSuccessToast("Copied");
  }

  /**
   * Toggle key visibility (show/hide)
   */
  async toggleKeyVisibility(name: string): Promise<void> {
    // Find and click the visibility toggle button using data-testid
    const toggleBtn = this.page.getByTestId(`vk-visibility-btn-${name}`);
    await toggleBtn.waitFor({ state: "attached", timeout: 10000 });
    await toggleBtn.scrollIntoViewIfNeeded();
    await toggleBtn.click();
  }

  /**
   * Reveal and read the displayed virtual key value from the table.
   */
  async getDisplayedVirtualKeyValue(name: string): Promise<string> {
    await this.searchVirtualKeys(name);
    const row = this.getVirtualKeyRow(name);
    await expect(row).toBeVisible({ timeout: 10000 });
    await row.scrollIntoViewIfNeeded();

    if (!(await this.isKeyRevealed(name))) {
      await this.toggleKeyVisibility(name);
      await expect(row.getByTestId("vk-key-value")).not.toContainText("•", { timeout: 5000 });
    }

    return ((await row.getByTestId("vk-key-value").textContent()) ?? "").trim();
  }

  async waitForVirtualKeyValueToChange(name: string, oldValue: string): Promise<string> {
    const deadline = Date.now() + 20000;
    let currentValue = oldValue;

    while (Date.now() < deadline) {
      await this.page.waitForTimeout(500);
      currentValue = await this.getDisplayedVirtualKeyValue(name);
      if (currentValue && currentValue !== oldValue) {
        return currentValue;
      }
    }

    throw new Error(`Virtual key "${name}" value did not change after rotation`);
  }

  async rotateVirtualKey(name: string): Promise<void> {
    await this.forceCloseToasts();
    await this.openVirtualKeyEditor(name);

    const rotateBtn = this.page.getByTestId("vk-rotate-btn");
    await rotateBtn.waitFor({ state: "visible", timeout: 10000 });
    await rotateBtn.click();

    const confirmBtn = this.page.getByTestId("vk-rotate-confirm-btn");
    await confirmBtn.waitFor({ state: "visible", timeout: 5000 });
    await confirmBtn.click();

    await this.waitForSuccessToast("rotated");
    await this.closeSheet();
    await this.goto();
    await this.searchVirtualKeys(name);
  }

  async cancelRotateVirtualKey(name: string): Promise<void> {
    await this.forceCloseToasts();
    await this.openVirtualKeyEditor(name);

    const rotateBtn = this.page.getByTestId("vk-rotate-btn");
    await rotateBtn.waitFor({ state: "visible", timeout: 10000 });
    await rotateBtn.click();

    const cancelBtn = this.page.getByTestId("vk-rotate-cancel-btn");
    await cancelBtn.waitFor({ state: "visible", timeout: 5000 });
    await cancelBtn.click();
    await expect(cancelBtn).not.toBeVisible({ timeout: 5000 }).catch(() => {});

    await this.closeSheet();
    await this.goto();
    await this.searchVirtualKeys(name);
  }

  async selectVirtualKey(name: string): Promise<void> {
    await this.searchVirtualKeys(name);
    const row = this.getVirtualKeyRow(name);
    await expect(row).toBeVisible({ timeout: 10000 });
    const checkbox = this.page.getByTestId(`vk-select-checkbox-${name}`);
    await checkbox.scrollIntoViewIfNeeded();
    if ((await checkbox.getAttribute("data-state")) !== "checked") {
      await checkbox.click();
    }
  }

  async bulkRotateVirtualKeys(names: string[]): Promise<void> {
    await this.forceCloseToasts();
    for (const name of names) {
      await this.selectVirtualKey(name);
    }

    const rotateBtn = this.page.getByTestId("vk-bulk-rotate-btn");
    await rotateBtn.waitFor({ state: "visible", timeout: 10000 });
    await rotateBtn.click();

    const confirmBtn = this.page.getByTestId("vk-bulk-rotate-confirm-btn");
    await confirmBtn.waitFor({ state: "visible", timeout: 5000 });
    await confirmBtn.click();

    await this.waitForSuccessToast("Rotated");
    await this.goto();
  }

  /**
   * Close any open sheet/dialog
   */
  async closeSheet(): Promise<void> {
    const isSheetVisible = await this.sheet.isVisible().catch(() => false);
    if (isSheetVisible) {
      // We have to click on the close button to close the sheet
      const closeBtn = this.sheet
        .locator('button[aria-label*="close"], button:has(svg.lucide-x)')
        .first();
      if (await closeBtn.isVisible().catch(() => false)) {
        await closeBtn.click();
      } else {
        await this.page.keyboard.press("Escape");
      }
      await expect(this.sheet)
        .not.toBeVisible({ timeout: 5000 })
        .catch(() => {});
    }
  }

  /**
   * Get all virtual key names from the table
   */
  async getAllVirtualKeyNames(): Promise<string[]> {
    const names: string[] = [];
    const count = await this.getVirtualKeyCount();

    if (count === 0) return names;

    // Find all delete buttons which have the VK name in their test-id
    const deleteButtons = this.page.locator('[data-testid^="vk-delete-btn-"]');
    const buttonCount = await deleteButtons.count();

    for (let i = 0; i < buttonCount; i++) {
      const testId = await deleteButtons.nth(i).getAttribute("data-testid");
      if (testId) {
        // Extract name from test-id: "vk-delete-btn-{name}"
        const name = testId.replace("vk-delete-btn-", "");
        names.push(name);
      }
    }

    return names;
  }

  /**
   * Clean up all virtual keys (delete all)
   */
  async cleanupAllVirtualKeys(): Promise<void> {
    // First close any open sheet
    await this.closeSheet();

    // Wait for any toasts to clear
    await this.dismissToasts();

    // Keep trying until no more VKs exist
    let attempts = 0;
    const maxAttempts = 10; // Prevent infinite loops

    while (attempts < maxAttempts) {
      // Get current VK names (refresh the list each iteration)
      const names = await this.getAllVirtualKeyNames();

      if (names.length === 0) {
        // No more VKs to delete
        break;
      }

      // Delete each one
      for (const name of names) {
        try {
          // Check if VK still exists before trying to delete
          const exists = await this.virtualKeyExists(name);
          if (!exists) {
            // Already deleted, skip
            continue;
          }

          // Make sure sheet is closed before each delete
          await this.closeSheet();
          await this.deleteVirtualKey(name);

          // Wait a bit for table to refresh
          await this.page.waitForTimeout(500);
        } catch (error) {
          // If delete fails, try to close sheet and continue
          await this.closeSheet();
          const errorMsg = error instanceof Error ? error.message : String(error);
          console.log(`Failed to delete virtual key: ${name} - ${errorMsg}`);
          // Continue with next VK
        }
      }

      attempts++;

      // Wait a bit before next iteration to allow table to refresh
      await this.page.waitForTimeout(1000);
    }

    if (attempts >= maxAttempts) {
      const remainingNames = await this.getAllVirtualKeyNames();
      if (remainingNames.length > 0) {
        console.log(
          `Warning: Could not delete all virtual keys after ${maxAttempts} attempts. Remaining: ${remainingNames.join(", ")}`,
        );
      }
    }
  }

  /**
   * Clean up specific virtual keys by name
   */
  async cleanupVirtualKeys(names: string[]): Promise<void> {
    if (names.length === 0) return;

    // Ensure we're on the virtual keys list with a fresh load so table is ready
    await this.goto();
    await this.closeSheet();
    await this.dismissToasts().catch(() => {});
    await this.table.waitFor({ state: "visible", timeout: 10000 }).catch(() => {});

    for (const name of names) {
      const tryDelete = async (): Promise<void> => {
        const exists = await this.virtualKeyExists(name);
        if (!exists) return;
        await this.closeSheet();
        await this.deleteVirtualKey(name, { requireToast: false });
      };

      try {
        await tryDelete();
      } catch (error) {
        const errorMsg = error instanceof Error ? error.message : String(error);
        console.error(`[CLEANUP ERROR] Failed to delete virtual key: ${name} - ${errorMsg}`);
        await this.closeSheet();
        await this.page.waitForTimeout(1000);
        try {
          await tryDelete();
        } catch (retryError) {
          const retryMsg = retryError instanceof Error ? retryError.message : String(retryError);
          console.error(`[CLEANUP ERROR] Retry failed for virtual key: ${name} - ${retryMsg}`);
        }
      }
    }
  }
}