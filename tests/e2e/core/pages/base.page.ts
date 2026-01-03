import { Page, Locator, expect } from '@playwright/test'

/**
 * Base page object with common methods shared across all pages
 */
export class BasePage {
  readonly page: Page

  constructor(page: Page) {
    this.page = page
  }

  /**
   * Wait for the page to finish loading
   */
  async waitForPageLoad(): Promise<void> {
    await this.page.waitForLoadState('networkidle')
  }

  /**
   * Get the toast notification element (first/most recent one)
   */
  getToast(): Locator {
    return this.page.locator('[data-sonner-toast]').first()
  }

  /**
   * Wait for a success toast to appear
   */
  async waitForSuccessToast(message?: string): Promise<void> {
    const toast = this.getToast()
    await expect(toast).toBeVisible({ timeout: 10000 })
    if (message) {
      await expect(toast).toContainText(message)
    }
  }

  /**
   * Wait for an error toast to appear
   */
  async waitForErrorToast(message?: string): Promise<void> {
    const toast = this.getToast()
    await expect(toast).toBeVisible({ timeout: 10000 })
    if (message) {
      await expect(toast).toContainText(message)
    }
  }

  /**
   * Dismiss all visible toasts
   */
  async dismissToasts(): Promise<void> {
    const toasts = this.page.locator('[data-sonner-toast]')
    const count = await toasts.count()
    for (let i = 0; i < count; i++) {
      try {
        await toasts.first().click()
        await this.page.waitForTimeout(100)
      } catch {
        // Toast may have already disappeared
      }
    }
  }

  /**
   * Fill a form field by label
   */
  async fillByLabel(label: string, value: string): Promise<void> {
    await this.page.getByLabel(label).fill(value)
  }

  /**
   * Fill a form field by placeholder
   */
  async fillByPlaceholder(placeholder: string, value: string): Promise<void> {
    await this.page.getByPlaceholder(placeholder).fill(value)
  }

  /**
   * Fill a form field by test id
   */
  async fillByTestId(testId: string, value: string): Promise<void> {
    await this.page.getByTestId(testId).fill(value)
  }

  /**
   * Click a button by text
   */
  async clickButton(text: string): Promise<void> {
    await this.page.getByRole('button', { name: text }).click()
  }

  /**
   * Click a button by test id
   */
  async clickByTestId(testId: string): Promise<void> {
    await this.page.getByTestId(testId).click()
  }

  /**
   * Select an option from a dropdown by label
   */
  async selectOption(label: string, value: string): Promise<void> {
    await this.page.getByLabel(label).selectOption(value)
  }

  /**
   * Check if an element is visible
   */
  async isVisible(selector: string): Promise<boolean> {
    return await this.page.locator(selector).isVisible()
  }

  /**
   * Wait for an element to be visible
   */
  async waitForSelector(selector: string, timeout = 10000): Promise<void> {
    await this.page.waitForSelector(selector, { timeout })
  }

  /**
   * Get text content of an element
   */
  async getTextContent(selector: string): Promise<string | null> {
    return await this.page.locator(selector).textContent()
  }

  /**
   * Take a screenshot
   */
  async screenshot(name: string): Promise<void> {
    await this.page.screenshot({ path: `./screenshots/${name}.png` })
  }
}
