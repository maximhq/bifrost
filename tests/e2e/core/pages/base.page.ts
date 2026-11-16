import { Locator, Page, expect } from '@playwright/test'

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
   * Filters out toasts that are being removed to avoid matching stale toasts.
   * Optionally filters by toast type (success, error, loading, default).
   */
  getToast(type?: 'success' | 'error' | 'loading' | 'default'): Locator {
    const selector = type
      ? `[data-sonner-toast][data-type="${type}"]:not([data-removed="true"]):not([data-e2e-dismissed])`
      : '[data-sonner-toast]:not([data-removed="true"]):not([data-e2e-dismissed])'
    return this.page.locator(selector).first()
  }

  /**
   * Wait for a success toast to appear
   */
  async waitForSuccessToast(message?: string): Promise<void> {
    const toast = this.getToast('success')
    await expect(toast).toBeVisible({ timeout: 10000 })
    if (message) {
      await expect(toast).toContainText(message)
    }
    // Consumed: retire it so the next wait cannot match this stale toast.
    await this.waitForToastsToDisappear()
  }

  /**
   * Wait for an error toast to appear
   */
  async waitForErrorToast(message?: string): Promise<void> {
    const toast = this.getToast('error')
    await expect(toast).toBeVisible({ timeout: 10000 })
    if (message) {
      await expect(toast).toContainText(message)
    }
    // Consumed: retire it so the next wait cannot match this stale toast.
    await this.waitForToastsToDisappear()
  }

  /**
   * Retire every toast currently on screen: marks them so toast locators skip them
   * and the fixture's CSS hides them. Instant, unlike waiting ~5s for auto-dismiss.
   */
  async waitForToastsToDisappear(_timeout?: number): Promise<void> {
    await this.page
      .evaluate(() => document.querySelectorAll('[data-sonner-toast]').forEach((t) => t.setAttribute('data-e2e-dismissed', '')))
      .catch(() => {})
  }

  /**
   * Wait for a sheet/dialog to be fully visible (animation complete)
   */
  async waitForSheetAnimation(): Promise<void> {
    // Wait for any sheet transition to complete by checking for stable state
    await this.page.waitForFunction(() => {
      const sheet = document.querySelector('[role="dialog"]')
      if (!sheet) return true
      const style = window.getComputedStyle(sheet)
      return style.opacity === '1' && style.transform === 'none'
    }, { timeout: 2000 }).catch(() => {})
  }

  /**
   * Wait for element state to change (useful for toggles)
   */
  async waitForStateChange(locator: Locator, attribute: string, expectedValue: string, timeout = 5000): Promise<void> {
    await expect(locator).toHaveAttribute(attribute, expectedValue, { timeout })
  }

  /**
   * Wait for URL to contain a specific parameter
   */
  async waitForUrlParam(param: string, value: string, timeout = 5000): Promise<void> {
    await expect(this.page).toHaveURL(new RegExp(`${param}=${value}`), { timeout })
  }

  /**
   * Wait for charts/data to load after page navigation
   */
  async waitForChartsToLoad(): Promise<void> {
    // Wait for network to be idle (data fetching complete)
    await this.page.waitForLoadState('networkidle')
    // Wait for any loading skeletons to disappear
    const skeletons = this.page.locator('[data-testid="skeleton"], .skeleton, [data-loading="true"]')
    if (await skeletons.count() > 0) {
      await skeletons.first().waitFor({ state: 'hidden', timeout: 10000 }).catch(() => {})
    }
  }

  /**
   * Dismiss all visible toasts by waiting for them to disappear
   */
  async dismissToasts(): Promise<void> {
    // Just wait for toasts to auto-dismiss
    await this.waitForToastsToDisappear()
  }
  
  /**
   * Force dismiss all toasts by clicking away and waiting
   */
  async forceCloseToasts(): Promise<void> {
    await this.waitForToastsToDisappear()
  }

  /**
   * Close the Dev Profiler overlay if it is visible.
   * Clicks the dismiss (X) button on the profiler panel. Silently continues if not present.
   */
  async closeDevProfiler(): Promise<void> {
    const profilerHeader = this.page.locator('text=Dev Profiler')
    const isVisible = await profilerHeader.isVisible().catch(() => false)
    if (isVisible) {
      const dismissBtn = this.page.locator('button[title="Dismiss"]')
      if (await dismissBtn.isVisible().catch(() => false)) {
        await dismissBtn.click()
        await profilerHeader.waitFor({ state: 'hidden', timeout: 3000 }).catch(() => {})
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
