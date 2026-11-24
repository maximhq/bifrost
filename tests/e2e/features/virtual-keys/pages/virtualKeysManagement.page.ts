import { expect } from "@playwright/test";
import { VirtualKeysPage } from "./virtual-keys.page";

/**
 * Page object for Virtual Key rotation flows (single + bulk).
 * Extends VirtualKeysPage so management specs get the full CRUD surface
 * (create/edit/delete/view) plus these rotation-only methods through one fixture.
 */
export class VirtualKeysManagementPage extends VirtualKeysPage {
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
}
