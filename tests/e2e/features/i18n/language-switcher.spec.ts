import { readFileSync } from 'fs'
import { resolve } from 'path'
import type { Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

const LOCALE_STORAGE_KEY = 'bifrost.locale'

// Expected strings come from the shipped catalogs, so a translation edit does not break the test.
function catalog(locale: string, namespace: string): Record<string, any> {
  return JSON.parse(readFileSync(resolve(__dirname, `../../../../ui/locales/${locale}/${namespace}.json`), 'utf8'))
}

function strings(locale: string) {
  return {
    language: catalog(locale, 'common').language as string,
    observability: catalog(locale, 'shell').nav.observability as string,
  }
}

async function selectLanguage(page: Page, locale: string) {
  await page.getByTestId('client-settings-language-select').click()
  await page.getByTestId(`client-settings-language-option-${locale}`).click()
}

async function expectLanguage(page: Page, locale: string) {
  const expected = strings(locale)
  await expect(page.locator('html')).toHaveAttribute('lang', locale)
  await expect(page.locator('label[for="client-settings-language"]')).toContainText(expected.language)
  await expect(page.getByRole('button', { name: expected.observability, exact: true })).toBeVisible()
}

test.describe('Language setting (Client Settings)', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/workspace/config/client-settings')
    await expectLanguage(page, 'en')
  })

  test('lives in Client Settings with a beta badge, not in the topbar', async ({ page }) => {
    await expect(page.getByTestId('language-switcher')).toHaveCount(0)
    const label = page.locator('label[for="client-settings-language"]')
    await expect(label).toContainText(catalog('en', 'common').badges.beta as string)
  })

  test('switches between languages and persists the choice across a reload', async ({ page }) => {
    await selectLanguage(page, 'zh-CN')
    await expectLanguage(page, 'zh-CN')

    // Non-English to non-English must replace the previous translation, not fall back to English.
    await selectLanguage(page, 'ja')
    await expectLanguage(page, 'ja')
    expect(await page.evaluate((key) => localStorage.getItem(key), LOCALE_STORAGE_KEY)).toBe('ja')

    await page.reload()
    await expectLanguage(page, 'ja')

    await selectLanguage(page, 'en')
    await expectLanguage(page, 'en')
    expect(await page.evaluate((key) => localStorage.getItem(key), LOCALE_STORAGE_KEY)).toBe('en')
  })

  test('marks only the active language in the menu', async ({ page }) => {
    await selectLanguage(page, 'ja')
    await expectLanguage(page, 'ja')

    await page.getByTestId('client-settings-language-select').click()
    await expect(page.getByTestId('client-settings-language-option-ja')).toHaveAttribute('data-state', 'checked')
    await expect(page.getByTestId('client-settings-language-option-en')).toHaveAttribute('data-state', 'unchecked')
    await page.keyboard.press('Escape')
  })

  test('is reachable from the account menu outside Client Settings', async ({ page }) => {
    // Client Settings is gated by Settings View; the account menu is not.
    await page.goto('/workspace/logs')
    await page.locator('[data-testid="topbar-user-pill"], [data-testid="topbar-menu-btn"]').first().click()
    await page.getByTestId('topbar-language-submenu').hover()
    await expect(page.getByTestId('topbar-language-option-en')).toHaveAttribute('data-state', 'checked')
    await page.getByTestId('topbar-language-option-ja').click()

    await expect(page.locator('html')).toHaveAttribute('lang', 'ja')
    await expect(page.getByRole('button', { name: strings('ja').observability, exact: true })).toBeVisible()
    expect(await page.evaluate((key) => localStorage.getItem(key), LOCALE_STORAGE_KEY)).toBe('ja')

    await page.goto('/workspace/config/client-settings')
    await expectLanguage(page, 'ja')
    await selectLanguage(page, 'en')
    await expectLanguage(page, 'en')
  })

  test('renders a saved non-English language on first load', async ({ page }) => {
    await page.addInitScript((key) => localStorage.setItem(key, 'ja'), LOCALE_STORAGE_KEY)
    await page.goto('/workspace/config/client-settings')
    await expectLanguage(page, 'ja')

    await selectLanguage(page, 'zh-CN')
    await expectLanguage(page, 'zh-CN')
  })
})
