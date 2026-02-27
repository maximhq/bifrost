import { Page, expect } from '@playwright/test';

/**
 * Wait for network to be idle
 */
export async function waitForNetworkIdle(page: Page, timeout = 5000): Promise<void> {
  await page.waitForLoadState('networkidle', { timeout })
}

/**
 * Wait for a specific number of milliseconds
 */
export async function wait(ms: number): Promise<void> {
  return new Promise(resolve => setTimeout(resolve, ms))
}

/**
 * Retry a function until it succeeds or times out
 */
export async function retry<T>(
  fn: () => Promise<T>,
  options: { retries?: number; delay?: number } = {}
): Promise<T> {
  const { retries = 3, delay = 1000 } = options
  let lastError: Error | undefined

  for (let i = 0; i < retries; i++) {
    try {
      return await fn()
    } catch (error) {
      lastError = error as Error
      if (i < retries - 1) {
        await wait(delay)
      }
    }
  }

  throw lastError
}

/**
 * Generate a random string
 */
export function randomString(length = 8): string {
  return Math.random().toString(36).substring(2).padEnd(length, '0').substring(0, length)
}

/**
 * Generate a unique test name
 */
export function uniqueTestName(prefix: string): string {
  return `${prefix}-${Date.now()}-${randomString(4)}`
}

/**
 * Assert that a toast message appears
 */
export async function assertToast(
  page: Page,
  expectedText: string,
  type: 'success' | 'error' | 'info' = 'success'
): Promise<void> {
  const selector = `[data-sonner-toast][data-type="${type}"]:not([data-removed="true"])`
  const toast = page.locator(selector).first()
  await expect(toast).toBeVisible({ timeout: 10000 })
  await expect(toast).toContainText(expectedText)
}

/**
 * Assert that page URL matches expected pattern
 */
export async function assertUrl(page: Page, pattern: string | RegExp): Promise<void> {
  await expect(page).toHaveURL(pattern)
}

/**
 * Fill a Radix/Shadcn Select component
 */
export async function fillSelect(
  page: Page,
  triggerSelector: string,
  optionText: string
): Promise<void> {
  // Click the trigger to open the dropdown
  await page.locator(triggerSelector).click()

  // Wait for the dropdown content to appear
  await page.waitForSelector('[role="listbox"]', { timeout: 5000 })

  // Click the option
  await page.getByRole('option', { name: optionText }).click()
}

/**
 * Fill a multi-select component
 */
export async function fillMultiSelect(
  page: Page,
  inputSelector: string,
  values: string[]
): Promise<void> {
  const input = page.locator(inputSelector)

  for (const value of values) {
    await input.fill(value)
    await page.keyboard.press('Enter')
    await wait(100) // Small delay between entries
  }
}

/**
 * Clear and fill an input
 */
export async function clearAndFill(page: Page, selector: string, value: string): Promise<void> {
  const input = page.locator(selector)
  await input.clear()
  await input.fill(value)
}

/**
 * Get table row count
 */
export async function getTableRowCount(page: Page, tableSelector: string): Promise<number> {
  const rows = page.locator(`${tableSelector} tbody tr`)
  return await rows.count()
}

/**
 * Check if table contains a row with specific text
 */
export async function tableContainsRow(
  page: Page,
  tableSelector: string,
  text: string
): Promise<boolean> {
  const table = page.locator(tableSelector)
  const row = table.locator('tbody tr', { hasText: text })
  return await row.count() > 0
}

/**
 * Wait for table to load (no loading indicator)
 */
export async function waitForTableLoad(page: Page, tableSelector: string): Promise<void> {
  // Wait for table to be visible
  await page.locator(tableSelector).waitFor({ state: 'visible' })

  // Wait for any loading spinners to disappear
  const loadingIndicator = page.locator('[data-testid="loading-spinner"]')
  if (await loadingIndicator.count() > 0) {
    await loadingIndicator.waitFor({ state: 'hidden', timeout: 10000 })
  }
}

/**
 * Screenshot on failure helper
 */
export async function screenshotOnError(
  page: Page,
  testName: string,
  fn: () => Promise<void>
): Promise<void> {
  try {
    await fn()
  } catch (error) {
    await page.screenshot({ path: `./screenshots/error-${testName}-${Date.now()}.png` })
    throw error
  }
}
